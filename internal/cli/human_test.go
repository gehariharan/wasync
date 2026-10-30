package cli

import (
	"bytes"
	"context"
	"errors"
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
	if err := run("approvals", "--print"); err != nil {
		t.Fatal(err)
	}
	link := strings.TrimSpace(output.String())
	if !strings.HasPrefix(link, "http://127.0.0.1:") || !strings.Contains(link, "/ui/enter?t=") {
		t.Fatalf("link %q", link)
	}
	var opened string
	app.OpenURL = func(u string) error { opened = u; return nil }
	app.Err = &bytes.Buffer{}
	if err := run("approvals"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(opened, "/ui/enter?t=") || opened == link {
		t.Fatalf("opened %q (each link must be fresh)", opened)
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
