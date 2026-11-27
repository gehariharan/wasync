package api

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/opensend"
	"github.com/gehariharan/wasync/internal/sentlog"
)

// agentsFixture is the access fixture behind a signed-in browser, with an
// optional system confirmation (nil: this computer cannot show one).
func agentsFixture(t *testing.T, stub *promptStub) (accessFixture, *pageClient, string) {
	t.Helper()
	var gate *humancheck.Gate
	if stub != nil {
		gate = humancheck.NewGate(stub)
	}
	f := setupAccessWith(t, gate)
	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	_, page, _ := c.get("/ui/chats/pick")
	return f, c, csrfOf(t, page)
}

func (f accessFixture) client(t *testing.T, name string) *agentauth.Client {
	t.Helper()
	clients, err := agentauth.List(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range clients {
		if c.Name == name {
			return &c
		}
	}
	t.Fatalf("no agent %s", name)
	return nil
}

func notice(hd map[string][]string) string {
	if len(hd["Location"]) == 0 {
		return ""
	}
	u, _ := url.Parse(hd["Location"][0])
	return u.Query().Get("n")
}

func TestAgentsPageNeedsASession(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	if code, _, _ := c.get("/ui/agents"); code != 401 {
		t.Fatalf("agents page without a session: %d", code)
	}
	if code, _, _ := c.post("/ui/agents", url.Values{"action": {"openoff"}}); code != 401 {
		t.Fatalf("agents action without a session: %d", code)
	}
}

func TestOpenSendingIsSwitchedOnOnlyAfterTheSystemConfirmation(t *testing.T) {
	stub := &promptStub{}
	f, c, csrf := agentsFixture(t, stub)
	post := func(action string) string {
		code, _, hd := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {action}})
		if code != 303 {
			t.Fatalf("%s: %d", action, code)
		}
		return notice(hd)
	}
	if got := post("openon"); got != "declined" {
		t.Fatalf("a declined prompt gave %q", got)
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); on {
		t.Fatal("open sending went on without a confirmation")
	}
	stub.set(true)
	if got := post("openon"); got != "openon" {
		t.Fatalf("a confirmed prompt gave %q", got)
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); !on {
		t.Fatal("open sending did not go on after confirmation")
	}
	prompts := stub.seen()
	if len(prompts) != 2 || !strings.Contains(prompts[1], "ANY chat or phone number") || !strings.Contains(prompts[1], "15 messages and 5 new numbers") {
		t.Fatalf("the prompt must say what is being allowed: %q", prompts)
	}
	_, page, _ := c.get("/ui/agents")
	if !strings.Contains(page, "On until") || !strings.Contains(page, "Turn off now") || !strings.Contains(page, "0 of 15 messages, 0 of 5 new numbers") {
		t.Fatalf("agents page while on:\n%s", page)
	}
	// Turning it off asks for nothing.
	stub.set(false)
	if got := post("openoff"); got != "openoff" {
		t.Fatalf("turn off gave %q", got)
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); on || len(stub.seen()) != 2 {
		t.Fatalf("turning off must work without a prompt: prompts=%d", len(stub.seen()))
	}
	if audit := auditText(t, f.dir); !strings.Contains(audit, `"event":"open_send_on"`) || !strings.Contains(audit, `"event":"open_send_off"`) {
		t.Fatalf("the switch was not audited: %s", audit)
	}
}

func TestAgentsPageIsReadOnlyWithoutASystemConfirmation(t *testing.T) {
	f, c, csrf := agentsFixture(t, nil)
	grantLegacyOnDemand(t, f.dir, "helper", agentauth.ScopeReadChats, agentauth.ScopeReadMessages)
	_, page, _ := c.get("/ui/agents")
	if !strings.Contains(page, "read-only") || strings.Contains(page, "Turn on for 1 hour") || strings.Contains(page, "Allow sending") {
		t.Fatalf("a computer with no confirmation was offered a way to widen access:\n%s", page)
	}
	for _, form := range []url.Values{{"action": {"openon"}}, {"action": {"allowsend"}, "agent": {"helper"}}} {
		form.Set("csrf", csrf)
		if _, _, hd := c.post("/ui/agents", form); notice(hd) != "nogate" {
			t.Fatalf("%v: %q", form, notice(hd))
		}
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); on || f.client(t, "helper").HasScope(agentauth.ScopeSend) {
		t.Fatal("access was widened with no system confirmation")
	}
}

func TestAllowingAndStoppingSendingForAnAgent(t *testing.T) {
	stub := &promptStub{}
	f, c, csrf := agentsFixture(t, stub)
	scopes := []string{agentauth.ScopeReadChats, agentauth.ScopeReadMessages, agentauth.ScopeSend}
	grantLegacyOnDemand(t, f.dir, "helper", scopes[:2]...)
	if _, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "everything", Scopes: scopes[:2]}, time.Now()); err != nil {
		t.Fatal(err)
	}
	post := func(action, agent string) string {
		_, _, hd := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {action}, "agent": {agent}})
		return notice(hd)
	}
	if got := post("allowsend", "helper"); got != "declined" || f.client(t, "helper").HasScope(agentauth.ScopeSend) {
		t.Fatalf("declined prompt: %q", got)
	}
	stub.set(true)
	if got := post("allowsend", "everything"); got != "agentall" || f.client(t, "everything").HasScope(agentauth.ScopeSend) {
		t.Fatalf("an agent that reads every chat was given sending: %q", got)
	}
	if got := post("allowsend", "helper"); got != "sendon" || !f.client(t, "helper").HasScope(agentauth.ScopeSend) {
		t.Fatalf("confirmed prompt: %q", got)
	}
	if p := stub.seen(); !strings.Contains(p[len(p)-1], `"helper"`) || !strings.Contains(p[len(p)-1], "send messages") {
		t.Fatalf("the prompt must name the agent and the change: %q", p)
	}
	if got := post("allowsend", "no-such-agent"); got != "failed" {
		t.Fatalf("unknown agent: %q", got)
	}
	if got := post("allowsend", "Bad Name!"); got != "invalid" {
		t.Fatalf("bad name: %q", got)
	}
	before := len(stub.seen())
	stub.set(false)
	if got := post("stopsend", "helper"); got != "sendoff" || f.client(t, "helper").HasScope(agentauth.ScopeSend) || len(stub.seen()) != before {
		t.Fatalf("stopping sending must work without a prompt: %q", got)
	}
	if code, _, _ := c.post("/ui/agents", url.Values{"csrf": {"forged"}, "action": {"stopsend"}, "agent": {"helper"}}); code != 403 {
		t.Fatalf("forged form: %d", code)
	}
	if code, _, _ := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"nonsense"}}); code != 400 {
		t.Fatalf("unknown action: %d", code)
	}
}

func TestOpenSendsShowOnTheAgentsAndSentPages(t *testing.T) {
	stub := &promptStub{confirm: true}
	f, c, csrf := agentsFixture(t, stub)
	c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"openon"}})
	log := sentlog.New(f.dir)
	now := time.Now()
	for _, e := range []sentlog.Entry{
		{Time: now.Add(-2 * time.Minute), Actor: "helper", Kind: "send", Chat: "15551230000@s.whatsapp.net", Outcome: sentlog.OutcomeSent, Open: true, NewNumber: true},
		{Time: now.Add(-time.Minute), Actor: "helper", Kind: "send", Chat: familyChat, Outcome: sentlog.OutcomeSent},
	} {
		if err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	_, page, _ := c.get("/ui/agents")
	if !strings.Contains(page, "1 of 15 messages, 1 of 5 new numbers") || !strings.Contains(page, "Recent open sends") || strings.Contains(page, "Family Group") {
		t.Fatalf("only open sends belong in the open sending summary:\n%s", page)
	}
	_, page, _ = c.get("/ui/sent")
	if !strings.Contains(page, "open sending, new number") {
		t.Fatalf("the sent page does not mark open sends:\n%s", page)
	}
}

func auditText(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, agentauth.AuditFileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
