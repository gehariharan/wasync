package cli

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func TestEditReplacesOwnRecentTextOnceAndClosesSession(t *testing.T) {
	dir, client, app, out := writeFixture(t)
	err := app.Run(context.Background(), []string{"--data-dir", dir, "edit", "--chat", "one@g.us", "--message-id", "mine", "--text", "corrected"})
	if err != nil || len(client.edits) != 1 || !client.closed || !client.connect {
		t.Fatalf("edit lifecycle: %+v %v", client, err)
	}
	in := client.edits[0]
	if in.To != "one@g.us" || in.Target.MessageID != "mine" || in.Text != "corrected" || !strings.Contains(out.String(), `"target_message_id":"mine"`) {
		t.Fatalf("input %+v output %s", in, out)
	}
}

func TestRefusedEditNeverOpensDevice(t *testing.T) {
	dir, _, app, _ := writeFixture(t)
	app.OpenClient = func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("a refused edit opened a device")
		return nil, nil
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"someone else's message": {[]string{"--message-id", "theirs", "--text", "x"}, "only your own messages"},
		"outside the window":     {[]string{"--message-id", "stale", "--text", "x"}, "allows editing a message for"},
		"deleted":                {[]string{"--message-id", "gone", "--text", "x"}, "deleted"},
		"not archived":           {[]string{"--message-id", "unknown", "--text", "x"}, "not in the archive"},
		"no text":                {[]string{"--message-id", "mine"}, "--text"},
		"no message":             {[]string{"--text", "x"}, "--message-id"},
	} {
		err := app.Run(context.Background(), append([]string{"--data-dir", dir, "edit", "--chat", "one@g.us"}, tc.args...))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v (want %q)", name, err, tc.want)
		}
	}
	if err := app.Run(context.Background(), []string{"--data-dir", dir, "edit", "--chat", "one@g.us", "--message-id", "mine", "--text", "x", "stray"}); err == nil {
		t.Error("extra arguments accepted")
	}
}

func TestEditReusesOwnerWithoutCompetingSession(t *testing.T) {
	app, out, store, account, dir := queryFixture(t)
	if _, err := store.Apply(context.Background(), account, []storage.Message{
		{GroupID: "one@g.us", MessageID: "mine", SenderID: "me@lid", FromMe: true, Timestamp: time.Now().Add(-time.Minute), Type: "text", Text: "my words", Source: "live"},
	}); err != nil {
		t.Fatal(err)
	}
	var got []whatsapp.EditInput
	server, err := api.StartOperations("127.0.0.1:0", dir, store, nil, nil, nil, nil, nil, whatsapp.Operations{
		Edit: func(_ context.Context, in whatsapp.EditInput) (whatsapp.SendResult, error) {
			got = append(got, in)
			return whatsapp.SendResult{ChatID: in.To, Status: "server_acknowledged", MessageID: "fake"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	app.OpenClient = func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("opened a competing client")
		return nil, nil
	}
	flags := []string{"--data-dir", dir, "--api-addr", strings.TrimPrefix(server.URL, "http://")}
	if err = app.Run(context.Background(), append(flags, "edit", "--chat", "one@g.us", "--message-id", "mine", "--text", "fixed")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "fixed" || got[0].Target.MessageID != "mine" || !strings.Contains(out.String(), "server_acknowledged") {
		t.Fatalf("owner edit: %+v %s", got, out)
	}
	// The owner refuses what WhatsApp would refuse and the CLI reports it.
	if err = app.Run(context.Background(), append(flags, "edit", "--chat", "one@g.us", "--message-id", "1", "--text", "x")); err == nil || len(got) != 1 {
		t.Fatalf("someone else's message reached the owner's client: %v %d", err, len(got))
	}
}
