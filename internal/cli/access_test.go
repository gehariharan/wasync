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

func TestAgentsGrantOnDemandAndList(t *testing.T) {
	dir := t.TempDir() + "/data"
	run := func(app *App, args ...string) error {
		return app.Run(context.Background(), append([]string{"--data-dir", dir, "agents"}, args...))
	}
	app, out, errs := agentsApp("jit\n", true)
	if err := run(app, "grant", "jit", "--scope", "read.chats,read.messages,send", "--on-demand"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs.String(), "only those you approve") || !strings.HasPrefix(out.String(), "wsa_") {
		t.Fatalf("grant output %q %q", out.String(), errs.String())
	}
	app, out, _ = agentsApp("", false)
	if err := run(app, "list"); err != nil || !strings.Contains(out.String(), "none yet (on demand)") {
		t.Fatalf("list: %v %q", err, out.String())
	}
	client, _ := agentauth.ActiveByName(dir, "jit")
	if _, err := agentauth.GrantChatAccess(dir, client.ID, "one@g.us", []string{agentauth.ScopeReadChats}, "r", time.Now()); err != nil {
		t.Fatal(err)
	}
	app, out, _ = agentsApp("", false)
	if err := run(app, "list", "--json"); err != nil || !strings.Contains(out.String(), `"granted_chats":[{"chat":"one@g.us","scopes":["read.chats"]`) || !strings.Contains(out.String(), `"on_demand":true`) {
		t.Fatalf("json: %v %q", err, out.String())
	}
}

func TestActionsApproveChatAccessChoosesChatAndHistory(t *testing.T) {
	app, output, store, account, dir := queryFixture(t)
	ctx := context.Background()
	queue := &approval.Queue{}
	session := tui.NewSession(ctx, store, nil)
	session.Approvals, session.DataDir = queue, dir
	server, err := api.StartControlled("127.0.0.1:0", dir, store, nil, queue, session.ApproveAction, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_, client, err := agentauth.Grant(dir, agentauth.GrantRequest{Name: "jit", Scopes: []string{"read.chats,read.messages,send"}, OnDemand: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ask := func(send bool) approval.Request {
		t.Helper()
		scopes := []string{"read.chats", "read.messages"}
		if send {
			scopes = append(scopes, "send")
		}
		r, err := queue.AddAccess(account, approval.Access{Agent: "jit", AgentID: client.ID, Send: send, Scopes: scopes, Query: "fir\x1b[2Jst",
			Candidates: []approval.Candidate{{Chat: "one@g.us", Name: "First"}, {Chat: "two@g.us", Name: "Second\x1b[2J"}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	flags := []string{"--data-dir", dir, "--api-addr", strings.TrimPrefix(server.URL, "http://")}
	approve := func(id string, answers ...string) (string, error) {
		errs := &bytes.Buffer{}
		app.Interactive, app.In, app.Err = true, &lineReader{answers}, errs
		output.Reset()
		err := app.Run(ctx, append(append([]string{}, flags...), "actions", "approve", id))
		return errs.String(), err
	}
	r := ask(true)
	code := "ALLOW " + r.ID[len(r.ID)-6:]
	for _, bad := range [][]string{{"3"}, {"x"}, {"2", "3"}, {"2", ""}, {"2", "2", "yes"}, {"2", "2", "DELETE " + r.ID[len(r.ID)-6:]}} {
		if _, err = approve(r.ID, bad...); err == nil {
			t.Fatalf("accepted answers %v", bad)
		}
	}
	groups, _ := store.Groups(ctx, account)
	for _, g := range groups {
		if g.ID == "two@g.us" && g.Enabled {
			t.Fatal("a refused approval enabled capture")
		}
	}
	if queue.Pending(account) == nil {
		t.Fatal("refused approvals consumed the request")
	}
	prompts, err := approve(r.ID, "2", "2", code)
	if err != nil {
		t.Fatalf("approve: %v\n%s", err, prompts)
	}
	for _, want := range []string{"jit", "READ and SEND", "1) First", "2) Second", "full history sync", "read and send access", "two@g.us", code} {
		if !strings.Contains(prompts, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompts)
		}
	}
	if strings.Contains(prompts, "\x1b") {
		t.Fatalf("terminal escape from a chat name reached the prompt: %q", prompts)
	}
	got := mustClient(t, dir)
	if len(got.Grants) != 1 || got.Grants[0].Chat != "two@g.us" || strings.Join(got.Grants[0].Scopes, ",") != "read.chats,read.messages,send" {
		t.Fatalf("grants %+v", got.Grants)
	}
	if g := groupByID(t, store, account, "two@g.us"); !g.Enabled || g.HistoryRequest != "queued" {
		t.Fatalf("two@g.us %+v", g)
	}
	// From now on only, read only, single candidate: no chat question.
	single, err := queue.AddAccess(account, approval.Access{Agent: "jit", AgentID: client.ID, Scopes: []string{"read.chats"}, Query: "x", Candidates: []approval.Candidate{{Chat: "one@g.us", Name: "First"}}})
	if err != nil {
		t.Fatal(err)
	}
	prompts, err = approve(single.ID, "1", "ALLOW "+single.ID[len(single.ID)-6:])
	if err != nil || strings.Contains(prompts, "Choose the chat") || !strings.Contains(prompts, "from now on only") {
		t.Fatalf("single: %v\n%s", err, prompts)
	}
	if g := groupByID(t, store, account, "one@g.us"); !g.Enabled {
		t.Fatalf("one@g.us %+v", g)
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

func TestServeAnnouncesAccessRequestsWithoutNamingChatsAndActionsListShowsThem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	output := channelOutput{make(chan string, 20)}
	app := &App{Out: output, Err: io.Discard, OpenClient: func(context.Context, string, *slog.Logger) (Client, error) {
		t.Error("offline server opened WhatsApp")
		return nil, errors.New("unexpected client")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	api := "127.0.0.1:" + freePort(t)
	go func() {
		done <- app.Run(ctx, []string{"--data-dir", dir, "--api-addr", api, "serve", "--offline"})
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
	r, err := app.Approvals.AddAccess(account, approval.Access{Agent: "jit", AgentID: "0123abcd", Send: true, Scopes: []string{"read.chats", "send"}, Query: "+15551234567",
		Candidates: []approval.Candidate{{Chat: "15551234567@s.whatsapp.net", Name: "Secret Person"}}})
	if err != nil {
		t.Fatal(err)
	}
	line := <-output.writes
	if !strings.Contains(line, "agent jit") || !strings.Contains(line, "wasync actions approve "+r.ID) || strings.Contains(line, "15551234567") || strings.Contains(line, "Secret") {
		t.Fatalf("console line %q", line)
	}
	// A repeated ask does not announce twice.
	if _, err = app.Approvals.AddAccess(account, approval.Access{Agent: "jit", AgentID: "0123abcd", Send: true, Scopes: []string{"read.chats", "send"}, Query: "+15551234567",
		Candidates: []approval.Candidate{{Chat: "15551234567@s.whatsapp.net", Name: "Secret Person"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case extra := <-output.writes:
		t.Fatalf("duplicate announcement %q", extra)
	case <-time.After(100 * time.Millisecond):
	}
	// The owner's terminal listing does name what is asked for; scripts get nothing.
	var out, errs bytes.Buffer
	lister := &App{Out: &out, Err: &errs}
	args := []string{"--data-dir", dir, "--api-addr", api, "actions", "list"}
	if err = lister.Run(ctx, args); err == nil || out.Len() != 0 {
		t.Fatalf("non-interactive list: %v %q", err, out.String())
	}
	lister.Interactive = true
	if err = lister.Run(ctx, args); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{r.ID, "agent jit: read+send", "Secret Person", "15551234567@s.whatsapp.net", "+15551234567"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list lacks %q:\n%s", want, out.String())
		}
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
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
