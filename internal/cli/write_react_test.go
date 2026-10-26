package cli

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func TestReactSetsAndClearsOnceAndClosesSession(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		emoji string
	}{
		{[]string{"--emoji", "👍"}, "👍"},
		{[]string{"--remove"}, ""},
	} {
		dir, client, app, out := writeFixture(t)
		args := append([]string{"--data-dir", dir, "react", "--chat", "one@g.us", "--message-id", "theirs"}, tc.args...)
		if err := app.Run(context.Background(), args); err != nil || len(client.reactions) != 1 || !client.closed || !client.connect {
			t.Fatalf("react lifecycle: %+v %v", client, err)
		}
		in := client.reactions[0]
		if in.Emoji != tc.emoji || in.To != "one@g.us" || in.Target.MessageID != "theirs" || !strings.Contains(out.String(), `"target_message_id":"theirs"`) {
			t.Fatalf("input %+v output %s", in, out)
		}
	}
}

func TestInvalidReactDoesNotOpenDevice(t *testing.T) {
	dir, _, app, _ := writeFixture(t)
	app.OpenClient = func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("invalid reaction opened a device")
		return nil, nil
	}
	for name, args := range map[string][]string{
		"neither":      {"react", "--chat", "one@g.us", "--message-id", "theirs"},
		"both":         {"react", "--chat", "one@g.us", "--message-id", "theirs", "--emoji", "👍", "--remove"},
		"text":         {"react", "--chat", "one@g.us", "--message-id", "theirs", "--emoji", "yes"},
		"no message":   {"react", "--chat", "one@g.us", "--emoji", "👍"},
		"bad chat":     {"react", "--chat", "status@broadcast", "--message-id", "theirs", "--emoji", "👍"},
		"extra args":   {"react", "--chat", "one@g.us", "--message-id", "theirs", "--emoji", "👍", "stray"},
		"not archived": {"react", "--chat", "one@g.us", "--message-id", "unknown", "--emoji", "👍"},
		"deleted":      {"react", "--chat", "one@g.us", "--message-id", "gone", "--emoji", "👍"},
	} {
		if err := app.Run(context.Background(), append([]string{"--data-dir", dir}, args...)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestReactReusesOwnerWithoutCompetingSession(t *testing.T) {
	app, out, store, _, dir := queryFixture(t)
	var got []whatsapp.ReactInput
	server, err := api.StartOperations("127.0.0.1:0", dir, store, nil, nil, nil, nil, nil, whatsapp.Operations{
		React: func(_ context.Context, in whatsapp.ReactInput) (whatsapp.SendResult, error) {
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
	if err = app.Run(context.Background(), append(flags, "react", "--chat", "one@g.us", "--message-id", "1", "--emoji", "🙏")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Emoji != "🙏" || got[0].Target.MessageID != "1" || !strings.Contains(out.String(), "server_acknowledged") {
		t.Fatalf("owner reaction: %+v %s", got, out)
	}
	if err = app.Run(context.Background(), append(flags, "react", "--chat", "one@g.us", "--message-id", "nope", "--remove")); err == nil || len(got) != 1 {
		t.Fatalf("unarchived target reached the owner: %v %d", err, len(got))
	}
}
