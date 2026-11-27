package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/tui"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// lineReader returns one line per Read, like a terminal, so each prompt in a
// multi-question flow consumes exactly one scripted answer.
type lineReader struct{ lines []string }

func (r *lineReader) Read(p []byte) (int, error) {
	if len(r.lines) == 0 {
		return 0, fmt.Errorf("unexpected extra prompt")
	}
	n := copy(p, r.lines[0]+"\n")
	r.lines = r.lines[1:]
	return n, nil
}

func TestAgentsGrantSyncedChatsAndList(t *testing.T) {
	_, _, store, account, dir := queryFixture(t) // one@g.us is synced
	// two@g.us is archived only for an agent's approved list, so it is not shared.
	if err := store.EnableListCapture(context.Background(), account, "two@g.us"); err != nil {
		t.Fatal(err)
	}
	run := func(app *App, args ...string) error {
		return app.Run(context.Background(), append([]string{"--data-dir", dir, "agents"}, args...))
	}
	app, out, errs := agentsApp(t, "codex\nn\n", true)
	if err := run(app, "grant", "codex", "--scope", "read.chats,read.messages,send,react", "--synced-chats"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs.String(), "every chat you sync in wasync (1 now), now and later") || !strings.HasPrefix(out.String(), "wsa_") {
		t.Fatalf("grant output %q %q", out.String(), errs.String())
	}
	if c, _ := agentauth.ActiveByName(dir, "codex"); c == nil || !c.SyncedChats || strings.Join(c.Chats, ",") != "one@g.us" {
		t.Fatalf("client %+v", c)
	}
	app, out, _ = agentsApp(t, "", false)
	if err := run(app, "list"); err != nil || !strings.Contains(out.String(), "every synced chat (1 when last checked)") {
		t.Fatalf("list: %v %q", err, out.String())
	}
	app, out, _ = agentsApp(t, "", false)
	if err := run(app, "list", "--json"); err != nil || !strings.Contains(out.String(), `"synced_chats":true`) {
		t.Fatalf("json: %v %q", err, out.String())
	}
	for _, args := range [][]string{
		{"grant", "old", "--scope", "read.chats,send", "--on-demand"},
		{"grant", "both", "--scope", "send", "--synced-chats", "--chat", "one@g.us"},
	} {
		app, _, _ = agentsApp(t, "x\n", true)
		if err := run(app, args...); err == nil {
			t.Fatalf("%v was accepted", args)
		} else if args[3] == "read.chats,send" && !strings.Contains(err.Error(), "--synced-chats") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestTheAccessModeCommandIsGone(t *testing.T) {
	_, _, run, _, _, _, _ := ownerFixture(t)
	if err := run("access", "mode"); err == nil || !strings.Contains(err.Error(), "use every chat you sync") {
		t.Fatalf("access mode: %v", err)
	}
}

// sendOwner is a running owner whose session sends through a recording fake.
func sendOwner(t *testing.T) (app *App, run func(args ...string) error, store *storage.Store, account int64, queue *approval.Queue, dir string, sent *[]whatsapp.SendInput) {
	t.Helper()
	app, output, store, account, dir := queryFixture(t)
	ctx := context.Background()
	queue = &approval.Queue{}
	session := tui.NewSession(ctx, store, nil)
	session.Approvals, session.DataDir = queue, dir
	sent = &[]whatsapp.SendInput{}
	session.Send = func(_ context.Context, in whatsapp.SendInput) (whatsapp.SendResult, error) {
		*sent = append(*sent, in)
		return whatsapp.SendResult{MessageID: "3EB0AA"}, nil
	}
	server, err := api.StartControlled("127.0.0.1:0", dir, store, nil, queue, session.ApproveAction, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	flags := []string{"--data-dir", dir, "--api-addr", strings.TrimPrefix(server.URL, "http://")}
	run = func(args ...string) error {
		output.Reset()
		return app.Run(ctx, append(append([]string{}, flags...), args...))
	}
	return
}

func TestActionsApproveAgentSendShowsTheMessageAndNeedsItsPhrase(t *testing.T) {
	app, run, _, account, queue, dir, sent := sendOwner(t)
	_, client, err := agentauth.Grant(dir, agentauth.ConnectRequest("codex", nil), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := queue.AddSend(account, "15551234567@s.whatsapp.net", approval.AgentSend{Agent: "co\x1b[2Jdex", AgentID: client.ID, Text: "Hello\x1b[2J there\nBring the keys", NewNumber: true})
	if err != nil {
		t.Fatal(err)
	}
	approve := func(answer string) (string, error) {
		errs := &bytes.Buffer{}
		app.Interactive, app.In, app.Err = true, &lineReader{[]string{answer}}, errs
		err := run("actions", "approve", r.ID)
		return errs.String(), err
	}
	for _, bad := range []string{"yes", "DELETE " + r.ID[len(r.ID)-6:], "ALLOW " + r.ID[len(r.ID)-6:]} {
		if _, err = approve(bad); err == nil || len(*sent) != 0 {
			t.Fatalf("accepted %q", bad)
		}
	}
	if queue.Pending(account) == nil {
		t.Fatal("refused approvals consumed the request")
	}
	code := "SEND " + r.ID[len(r.ID)-6:]
	prompts, err := approve(code)
	if err != nil {
		t.Fatalf("approve: %v\n%s", err, prompts)
	}
	for _, want := range []string{"co[2Jdex", "ONE message", "new number +15551234567", "Hello[2J there\nBring the keys", "exactly this message once", code} {
		if !strings.Contains(prompts, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompts)
		}
	}
	if strings.Contains(prompts, "\x1b") {
		t.Fatalf("control characters reached the terminal: %q", prompts)
	}
	if len(*sent) != 1 || (*sent)[0].Text != "Hello\x1b[2J there\nBring the keys" || (*sent)[0].To != "15551234567@s.whatsapp.net" {
		t.Fatalf("sent %+v", *sent)
	}
}

func TestServeAnnouncesSendRequestsWithoutNamingTheChatOrTextAndActionsListShowsThem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	output := channelOutput{make(chan string, 20)}
	app := &App{Out: output, Err: io.Discard, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) {
		t.Error("offline server opened WhatsApp")
		return nil, errors.New("unexpected client")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	addr := "127.0.0.1:" + freePort(t)
	go func() {
		done <- app.Run(ctx, []string{"--data-dir", dir, "--api-addr", addr, "serve", "--offline"})
	}()
	select {
	case text := <-output.writes:
		if !strings.Contains(text, "Local API") {
			t.Fatalf("unexpected first output %q", text)
		}
	case err := <-done:
		t.Fatalf("serve exited: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not start")
	}
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	account, err := store.UseAccount(ctx, "synthetic@lid")
	if err != nil {
		t.Fatal(err)
	}
	ask := approval.AgentSend{Agent: "jit", AgentID: "0123abcd", Text: "Secret text\nline two", NewNumber: true}
	r, _, err := app.Approvals.AddSend(account, "15551234567@s.whatsapp.net", ask)
	if err != nil {
		t.Fatal(err)
	}
	line := <-output.writes
	if !strings.Contains(line, "Agent jit") || !strings.Contains(line, "wasync actions approve "+r.ID) || strings.Contains(line, "15551234567") || strings.Contains(line, "Secret") {
		t.Fatalf("console line %q", line)
	}
	// A repeated ask does not announce twice.
	if _, _, err = app.Approvals.AddSend(account, "15551234567@s.whatsapp.net", ask); err != nil {
		t.Fatal(err)
	}
	select {
	case extra := <-output.writes:
		t.Fatalf("duplicate announcement %q", extra)
	case <-time.After(100 * time.Millisecond):
	}
	// The owner's terminal listing does show it; scripts get nothing.
	var out, errs bytes.Buffer
	lister := &App{Out: &out, Err: &errs}
	args := []string{"--data-dir", dir, "--api-addr", addr, "actions", "list"}
	if err = lister.Run(ctx, args); err == nil || out.Len() != 0 {
		t.Fatalf("non-interactive list: %v %q", err, out.String())
	}
	lister.Interactive = true
	if err = lister.Run(ctx, args); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{r.ID, "agent jit: send", "new number +15551234567", `"Secret text\nline two"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list lacks %q:\n%s", want, out.String())
		}
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func mustClient(t *testing.T, dir string) *agentauth.Client {
	t.Helper()
	c, err := agentauth.ActiveByName(dir, "jit")
	if err != nil || c == nil {
		t.Fatal(err)
	}
	return c
}

func groupByID(t *testing.T, store *storage.Store, account int64, id string) storage.Group {
	t.Helper()
	groups, err := store.GroupSummaries(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("%s missing", id)
	return storage.Group{}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func ownerFixture(t *testing.T) (app *App, output *bytes.Buffer, run func(args ...string) error, store *storage.Store, account int64, queue *approval.Queue, dir string) {
	t.Helper()
	app, output, store, account, dir = queryFixture(t)
	ctx := context.Background()
	queue = &approval.Queue{}
	session := tui.NewSession(ctx, store, nil)
	session.Approvals, session.DataDir = queue, dir
	server, err := api.StartControlled("127.0.0.1:0", dir, store, nil, queue, session.ApproveAction, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	flags := []string{"--data-dir", dir, "--api-addr", strings.TrimPrefix(server.URL, "http://")}
	run = func(args ...string) error {
		output.Reset()
		return app.Run(ctx, append(append([]string{}, flags...), args...))
	}
	return
}
