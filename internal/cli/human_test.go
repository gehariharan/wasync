package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/tui"
)

type humanStub struct {
	confirm bool
	asked   []string
}

func (h *humanStub) Name() string    { return "the test prompt" }
func (h *humanStub) Available() bool { return true }
func (h *humanStub) Verify(_ context.Context, reason string) error {
	h.asked = append(h.asked, reason)
	if h.confirm {
		return nil
	}
	return errors.New("declined")
}

// A script that gives `wasync actions approve` a pseudo-terminal and the typed
// phrase gets exactly as far as this test does: the typed words are all right,
// and the approval still waits for the person at the computer.
func TestTypedPhraseAloneDoesNotApproveWhenThePersonDeclines(t *testing.T) {
	app, output, store, account, dir := queryFixture(t)
	ctx := context.Background()
	stub := &humanStub{}
	queue := &approval.Queue{}
	session := tui.NewSession(ctx, store, nil)
	session.Approvals, session.DataDir = queue, dir
	server, err := api.StartOwner(api.Owner{Addr: "127.0.0.1:0", Dir: dir, Store: store, Approvals: queue, Approve: session.ApproveAction, Reject: session.RejectAction, Human: humancheck.NewGate(stub)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_, client, err := agentauth.Grant(dir, agentauth.GrantRequest{Name: "jit", Scopes: []string{"read.chats,read.messages"}, OnDemand: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r, err := queue.AddAccess(account, approval.Access{Agent: "jit", AgentID: client.ID, Scopes: []string{"read.chats"}, Query: "second",
		Candidates: []approval.Candidate{{Chat: "two@g.us", Name: "Second"}}})
	if err != nil {
		t.Fatal(err)
	}
	app.Human = humancheck.NewGate(stub)
	flags := []string{"--data-dir", dir, "--api-addr", strings.TrimPrefix(server.URL, "http://")}
	approve := func() (string, error) {
		errs := &bytes.Buffer{}
		app.Interactive, app.In, app.Err = true, &lineReader{[]string{"1", "ALLOW " + r.ID[len(r.ID)-6:]}}, errs
		output.Reset()
		err := app.Run(ctx, append(append([]string{}, flags...), "actions", "approve", r.ID))
		return errs.String(), err
	}
	said, err := approve()
	if err == nil {
		t.Fatal("approved without the person confirming")
	}
	if !strings.Contains(said, "confirm on this computer") {
		t.Fatalf("the terminal did not tell the person to look at the screen:\n%s", said)
	}
	if groupByID(t, store, account, "two@g.us").Enabled {
		t.Fatal("a declined prompt turned on capture")
	}
	if queue.Pending(account) == nil {
		t.Fatalf("a declined prompt used up the request; terminal said:\n%s\nerr: %v", said, err)
	}
	if len(stub.asked) == 0 || !strings.Contains(stub.asked[0], "Second") || !strings.Contains(stub.asked[0], "jit") {
		t.Fatalf("prompt text: %q", stub.asked)
	}
	stub.confirm = true
	if _, err = approve(); err != nil {
		t.Fatalf("approval after the person confirmed: %v", err)
	}
	if !groupByID(t, store, account, "two@g.us").Enabled {
		t.Fatal("confirmed approval did not apply")
	}
}

func TestApprovalsCommandGivesAOneTimeLinkAndOpensIt(t *testing.T) {
	app, output, run, _, _, _, _ := ownerFixture(t)
	var opened []string
	app.OpenURL = func(u string) error { opened = append(opened, u); return nil }
	app.Err = &bytes.Buffer{}
	for range 2 {
		if err := run("approvals"); err != nil {
			t.Fatal(err)
		}
	}
	if len(opened) != 2 || opened[0] == opened[1] {
		t.Fatalf("opened %q (each link must be fresh)", opened)
	}
	for _, link := range opened {
		if !strings.HasPrefix(link, "http://127.0.0.1:") || !strings.Contains(link, "/ui/enter?t=") {
			t.Fatalf("link %q", link)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		for i, want := range []int{http.StatusSeeOther, http.StatusUnauthorized} {
			res, err := client.Get(link)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != want {
				t.Fatalf("use %d of %q: %d, want %d", i+1, link, res.StatusCode, want)
			}
		}
	}
	if output.Len() != 0 || strings.Contains(app.Err.(*bytes.Buffer).String(), "/ui/enter") {
		t.Fatalf("the link was printed: %q %q", output.String(), app.Err)
	}
	app.OpenURL = func(string) error { return errors.New("no browser") }
	app.Err = &bytes.Buffer{}
	err := run("approvals")
	if err == nil || strings.Contains(err.Error(), "/ui/enter") || strings.Contains(app.Err.(*bytes.Buffer).String(), "/ui/enter") || output.Len() != 0 {
		t.Fatalf("no browser: err %v, out %q, err %q", err, output.String(), app.Err)
	}
	if err := run("approvals", "--print"); err == nil {
		t.Fatal("accepted --print")
	}
	if err := run("approvals", "extra"); err == nil {
		t.Fatal("accepted a stray argument")
	}
}

func TestStartupLineSaysWhatStandsBetweenAnAgentAndAnApproval(t *testing.T) {
	with := &App{Human: humancheck.NewGate(&humanStub{})}
	if got := with.approvalCheckLine(); !strings.Contains(got, "the test prompt") {
		t.Fatalf("%q", got)
	}
	without := &App{}
	if got := without.approvalCheckLine(); !strings.Contains(got, "typed phrase") {
		t.Fatalf("%q", got)
	}
}

// Typing the client's name in a pseudo-terminal is not enough to mint a
// credential or revoke one where the computer has a system confirmation.
func TestAgentGrantAndRevokeWaitForThePerson(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	run := func(app *App, args ...string) error {
		return app.Run(context.Background(), append([]string{"--data-dir", dir, "agents"}, args...))
	}
	stub := &humanStub{}

	app, out, errs := agentsApp(t, "axi\nn\n", true)
	app.Human = humancheck.NewGate(stub)
	err := run(app, "grant", "axi", "--on-demand", "--expires", "720h")
	if err == nil || !strings.Contains(err.Error(), "no credential created") {
		t.Fatalf("declined grant: %v", err)
	}
	if out.Len() != 0 || strings.Contains(errs.String(), "wsa_") {
		t.Fatalf("a credential was shown without the person: %q", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, agentauth.FileName)); !os.IsNotExist(statErr) {
		t.Fatal("registry written without the person")
	}
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], `"axi"`) || !strings.Contains(stub.asked[0], "only the chats you approve") || !strings.Contains(stub.asked[0], "720h") {
		t.Fatalf("prompt: %q", stub.asked)
	}

	stub.confirm = true
	app, out, _ = agentsApp(t, "axi\nn\n", true)
	app.Human = humancheck.NewGate(stub)
	if err = run(app, "grant", "axi", "--on-demand"); err != nil || strings.Count(out.String(), "wsa_") != 1 {
		t.Fatalf("confirmed grant: %v %q", err, out.String())
	}

	stub.confirm = false
	app, _, _ = agentsApp(t, "axi\n", true)
	app.Human = humancheck.NewGate(stub)
	if err = run(app, "revoke", "axi"); err == nil || !strings.Contains(err.Error(), "nothing revoked") {
		t.Fatalf("declined revoke: %v", err)
	}
	if c, _ := agentauth.ActiveByName(dir, "axi"); c == nil {
		t.Fatal("revoked without the person")
	}
	stub.confirm = true
	app, _, _ = agentsApp(t, "axi\n", true)
	app.Human = humancheck.NewGate(stub)
	if err = run(app, "revoke", "axi"); err != nil {
		t.Fatal(err)
	}
	if c, _ := agentauth.ActiveByName(dir, "axi"); c != nil {
		t.Fatal("confirmed revoke did not apply")
	}
}
