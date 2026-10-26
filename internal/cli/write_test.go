package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// fakeWritingClient records the writes a CLI command asks its connected client for.
type fakeWritingClient struct {
	fakeClient
	replies   []whatsapp.ReplyInput
	reactions []whatsapp.ReactInput
}

func (f *fakeWritingClient) Operations() whatsapp.Operations {
	return whatsapp.Operations{
		React: func(_ context.Context, in whatsapp.ReactInput) (whatsapp.SendResult, error) {
			f.reactions = append(f.reactions, in)
			return whatsapp.SendResult{ChatID: in.To, MessageID: "synthetic-react", Status: "server_acknowledged", TargetMessageID: in.Target.MessageID}, nil
		},
		Reply: func(_ context.Context, in whatsapp.ReplyInput) (whatsapp.SendResult, error) {
			f.replies = append(f.replies, in)
			return whatsapp.SendResult{ChatID: in.To, MessageID: "synthetic-reply", Status: "server_acknowledged", TargetMessageID: in.Target.MessageID}, nil
		},
	}
}

// writeFixture is an archive with no running owner, so commands connect themselves.
func writeFixture(t *testing.T) (dir string, client *fakeWritingClient, app *App, out *bytes.Buffer) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.UseAccount(ctx, "test@lid")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveGroups(ctx, account, []storage.Group{{ID: "one@g.us", Name: "First"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetEnabled(ctx, account, []string{"one@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Minute).UTC()
	if _, err = store.Apply(ctx, account, []storage.Message{
		{GroupID: "one@g.us", MessageID: "theirs", SenderID: "alice@lid", Timestamp: stamp, Type: "text", Text: "hello archive", Source: "live"},
		{GroupID: "one@g.us", MessageID: "mine", SenderID: "me@lid", FromMe: true, Timestamp: stamp, Type: "text", Text: "my words", Source: "live"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Apply(ctx, account, []storage.Message{{GroupID: "one@g.us", MessageID: "gone", SenderID: "alice@lid", Timestamp: stamp, Type: "text", Text: "bye", Source: "live"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Apply(ctx, account, []storage.Message{{GroupID: "one@g.us", MessageID: "gone", Kind: "delete", Type: "deleted", MutationAt: stamp, Timestamp: stamp, Source: "live"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	client = &fakeWritingClient{}
	out = &bytes.Buffer{}
	app = &App{Out: out, Err: &bytes.Buffer{}, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) { return client, nil }}
	return dir, client, app, out
}

func TestReplyQuotesArchivedMessageOnceAndClosesSession(t *testing.T) {
	dir, client, app, out := writeFixture(t)
	err := app.Run(context.Background(), []string{"--data-dir", dir, "reply", "--chat", "one@g.us", "--message-id", "theirs", "--text", "hi @12345", "--mention", "12345@s.whatsapp.net"})
	if err != nil || len(client.replies) != 1 || !client.closed || !client.connect {
		t.Fatalf("reply lifecycle: %+v %v", client, err)
	}
	in := client.replies[0]
	if in.To != "one@g.us" || in.Target.MessageID != "theirs" || in.Target.Text != "hello archive" || in.Text != "hi @12345" || len(in.Mentions) != 1 {
		t.Fatalf("input %+v", in)
	}
	var printed struct {
		Result whatsapp.SendResult `json:"result"`
		Error  string              `json:"error"`
	}
	if json.Unmarshal(out.Bytes(), &printed) != nil || printed.Result.Status != "server_acknowledged" || printed.Result.TargetMessageID != "theirs" || printed.Error != "" {
		t.Fatalf("output %s", out)
	}
}

func TestInvalidReplyDoesNotOpenDevice(t *testing.T) {
	dir, _, app, _ := writeFixture(t)
	app.OpenClient = func(context.Context, string, *slog.Logger) (Client, error) {
		t.Fatal("invalid reply opened a device")
		return nil, nil
	}
	for name, args := range map[string][]string{
		"no text":        {"reply", "--chat", "one@g.us", "--message-id", "theirs"},
		"no message":     {"reply", "--chat", "one@g.us", "--text", "hi"},
		"bad chat":       {"reply", "--chat", "status@broadcast", "--message-id", "theirs", "--text", "hi"},
		"hidden mention": {"reply", "--chat", "one@g.us", "--message-id", "theirs", "--text", "hi", "--mention", "12345@s.whatsapp.net"},
		"extra args":     {"reply", "--chat", "one@g.us", "--message-id", "theirs", "--text", "hi", "stray"},
		"not archived":   {"reply", "--chat", "one@g.us", "--message-id", "unknown", "--text", "hi"},
		"deleted target": {"reply", "--chat", "one@g.us", "--message-id", "gone", "--text", "hi"},
		"other chat":     {"reply", "--chat", "two@g.us", "--message-id", "theirs", "--text", "hi"},
	} {
		if err := app.Run(context.Background(), append([]string{"--data-dir", dir}, args...)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestReplyReusesOwnerWithoutCompetingSession(t *testing.T) {
	app, out, store, _, dir := queryFixture(t)
	var got []whatsapp.ReplyInput
	server, err := api.StartOperations("127.0.0.1:0", dir, store, nil, nil, nil, nil, nil, whatsapp.Operations{
		Reply: func(_ context.Context, in whatsapp.ReplyInput) (whatsapp.SendResult, error) {
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
	if err = app.Run(context.Background(), append(flags, "reply", "--chat", "one@g.us", "--message-id", "1", "--text", "synthetic")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Target.MessageID != "1" || !strings.Contains(out.String(), "server_acknowledged") {
		t.Fatalf("owner reply: %+v %s", got, out)
	}
	if err = app.Run(context.Background(), append(flags, "reply", "--chat", "one@g.us", "--message-id", "nope", "--text", "x")); err == nil || len(got) != 1 {
		t.Fatalf("unarchived target reached the owner: %v %d", err, len(got))
	}
}
