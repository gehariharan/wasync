package api

import (
	"context"
	"errors"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/agentconnect"
	"github.com/gehariharan/wasync/internal/prefs"
	"github.com/gehariharan/wasync/internal/uninstall"
)

// removeSpy stands in for the phone link, the removal helper and stopping.
type removeSpy struct {
	mu        sync.Mutex
	unlinks   int
	unlinkErr error
	started   []uninstall.Request
	stopped   chan struct{}
	stopOnce  sync.Once
}

func (s *removeSpy) owner(o *Owner) {
	s.stopped = make(chan struct{})
	o.Paired = func() bool { return true }
	o.Unlink = func(context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.unlinks++
		return s.unlinkErr
	}
	o.Stop = func() { s.stopOnce.Do(func() { close(s.stopped) }) }
	o.Remover = Remover{
		Handoff: func() uninstall.Handoff {
			return uninstall.Handoff{Kind: uninstall.KindMacApp, Target: "/Applications/wasync.app"}
		},
		Start: func(req uninstall.Request) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.started = append(s.started, req)
			return nil
		},
	}
}

func (s *removeSpy) counts() (unlinks, started int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unlinks, len(s.started)
}

func (s *removeSpy) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case <-s.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("wasync did not stop after the removal started")
	}
}

func TestRemoveWasyncDisconnectsUnlinksAndHandsOverKeepingTheData(t *testing.T) {
	spy := &removeSpy{}
	f, c, csrf := newConnectFixture(t, true, spy.owner)
	f.stub.set(true)
	_, digest := preview(t, c, "codex")
	if _, _, hd := c.post("/ui/setup/connect", url.Values{"csrf": {csrf}, "agent": {"codex"}, "digest": {digest}}); !strings.Contains(hd.Get("Location"), "n=agentconnected") {
		t.Fatalf("connect: %v", hd)
	}

	code, page, _ := c.get(removePath)
	for _, want := range []string{"Disconnect ChatGPT &amp; Codex", "Unlink this computer from WhatsApp", "Quit and move wasync to the Bin.", `name="delete" value="yes">`, "Also delete my chats and data", `value="remove"`} {
		if code != 200 || !strings.Contains(page, want) {
			t.Fatalf("remove page lacks %q: %d\n%s", want, code, page)
		}
	}
	if strings.Contains(page, `value="yes" checked`) {
		t.Fatal("deleting the data is ticked by default")
	}
	_, settings, _ := c.get("/ui/settings")
	if !strings.Contains(settings, `href="/ui/settings/remove"`) {
		t.Fatalf("Settings does not lead to Remove wasync:\n%s", settings)
	}

	if code, _, _ := c.post(removePath, url.Values{"csrf": {"forged"}, "action": {"remove"}}); code != 403 {
		t.Fatalf("forged form: %d", code)
	}
	if unlinks, started := spy.counts(); unlinks != 0 || started != 0 {
		t.Fatal("a forged form removed something")
	}

	asked := len(f.stub.seen())
	code, page, _ = c.post(removePath, url.Values{"csrf": {csrf}, "action": {"remove"}})
	if code != 200 || !strings.Contains(page, "Removing wasync") || !strings.Contains(page, "Your chats and data are kept") {
		t.Fatalf("removing: %d\n%s", code, page)
	}
	if seen := f.stub.seen()[asked:]; len(seen) != 1 || !strings.Contains(seen[0], "revoking every agent's credential") || !strings.Contains(seen[0], "your data is kept") {
		t.Fatalf("removing asks once, up front: %q", seen)
	}
	spy.waitStopped(t)
	unlinks, started := spy.counts()
	if unlinks != 1 || started != 1 {
		t.Fatalf("unlinks %d, helper starts %d", unlinks, started)
	}
	req := spy.started[0]
	if req.DeleteData || req.DataDir != f.dir || req.AppPID != 0 || req.Handoff.Kind != uninstall.KindMacApp {
		t.Fatalf("request: %+v", req)
	}
	if conns, err := agentconnect.Connections(f.dir); err != nil || len(conns) != 0 {
		t.Fatalf("apps still connected: %+v %v", conns, err)
	}
	clients, err := agentauth.List(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, cl := range clients {
		if cl.Status(time.Now()) == "active" {
			t.Fatalf("agent %q still works", cl.Name)
		}
	}
	if code, _ := f.do(t, "GET", "/api/v1/chats", f.jit, nil); code != 401 {
		t.Fatalf("a revoked agent still reads: %d", code)
	}
	for _, want := range []string{"wasync-axi setup hooks --remove", "wasync-axi setup skill --remove"} {
		if !slices.Contains(*f.ran, want) {
			t.Fatalf("the removal did not run %q: %q", want, *f.ran)
		}
	}
}

func TestRemoveWasyncDeletesTheDataOnlyAfterTheComputerConfirms(t *testing.T) {
	spy := &removeSpy{}
	f, c, csrf := newConnectFixture(t, false, spy.owner)
	f.stub.set(false)
	_, _, hd := c.post(removePath, url.Values{"csrf": {csrf}, "action": {"remove"}, "delete": {"yes"}})
	if !strings.Contains(hd.Get("Location"), "n=declined") {
		t.Fatalf("declined: %v", hd)
	}
	if unlinks, started := spy.counts(); unlinks != 0 || started != 0 {
		t.Fatal("a declined confirmation still removed something")
	}
	if got, _ := agentauth.ActiveByName(f.dir, "jit"); got == nil {
		t.Fatal("a declined confirmation disconnected an agent")
	}
	if seen := f.stub.seen(); len(seen) != 1 || !strings.Contains(seen[0], "delete all its data, which cannot be undone") {
		t.Fatalf("prompt: %q", seen)
	}

	f.stub.set(true)
	code, page, _ := c.post(removePath, url.Values{"csrf": {csrf}, "action": {"remove"}, "delete": {"yes"}})
	if code != 200 || !strings.Contains(page, "Your chats and data are deleted") {
		t.Fatalf("removing: %d\n%s", code, page)
	}
	spy.waitStopped(t)
	if _, started := spy.counts(); started != 1 || !spy.started[0].DeleteData {
		t.Fatalf("request: %+v", spy.started)
	}
}

func TestRemoveWasyncDoesNothingWithoutTheUpFrontConfirmation(t *testing.T) {
	spy := &removeSpy{}
	f, c, csrf := newConnectFixture(t, false, spy.owner)
	f.stub.set(false)
	if _, _, hd := c.post(removePath, url.Values{"csrf": {csrf}, "action": {"remove"}}); !strings.Contains(hd.Get("Location"), "n=declined") {
		t.Fatalf("declined: %v", hd)
	}
	if unlinks, started := spy.counts(); unlinks != 0 || started != 0 {
		t.Fatal("a declined removal still removed something")
	}
	if got, _ := agentauth.ActiveByName(f.dir, "jit"); got == nil {
		t.Fatal("a declined removal revoked an agent")
	}

	sf, nc, ncsrf := newSetupFixture(t, false)
	_, page, _ := nc.get(removePath)
	if strings.Contains(page, `value="remove"`) || !strings.Contains(page, "cannot show a system confirmation") {
		t.Fatalf("remove page with no confirmation:\n%s", page)
	}
	if _, _, hd := nc.post(removePath, url.Values{"csrf": {ncsrf}, "action": {"remove"}}); !strings.Contains(hd.Get("Location"), "n=nogate") {
		t.Fatalf("remove with no confirmation: %v", hd)
	}
	if got, _ := agentauth.ActiveByName(sf.dir, "jit"); got == nil {
		t.Fatal("removing with no confirmation revoked an agent")
	}
}

func TestRemoveWasyncSaysWhatItCouldNotUndoAndWaits(t *testing.T) {
	spy := &removeSpy{unlinkErr: errors.New("offline")}
	f, c, csrf := newConnectFixture(t, false, spy.owner)
	f.stub.set(true)
	code, page, _ := c.post(removePath, url.Values{"csrf": {csrf}, "action": {"remove"}})
	if code != 200 || !strings.Contains(page, "could not unlink this computer from WhatsApp") || !strings.Contains(page, `value="finish"`) {
		t.Fatalf("problems page: %d\n%s", code, page)
	}
	if _, started := spy.counts(); started != 0 {
		t.Fatal("the helper started before the person saw what was left")
	}
	// Another browser cannot finish someone else's removal.
	other := newPageClient(t, f.accessFixture)
	other.get(linkPath(t, f.accessFixture))
	_, settings, _ := other.get("/ui/settings")
	if _, _, hd := other.post(removePath, url.Values{"csrf": {csrfOf(t, settings)}, "action": {"finish"}}); !strings.Contains(hd.Get("Location"), "n=removeexpired") {
		t.Fatalf("another session finished it: %v", hd)
	}
	code, page, _ = c.post(removePath, url.Values{"csrf": {csrf}, "action": {"finish"}})
	if code != 200 || !strings.Contains(page, "Removing wasync") {
		t.Fatalf("finish: %d\n%s", code, page)
	}
	spy.waitStopped(t)
	if _, started := spy.counts(); started != 1 {
		t.Fatal("finish did not start the helper")
	}
}

func TestRemoveWasyncOnlyOnTheComputerRunningIt(t *testing.T) {
	_, _, c := remoteOwner(t)
	_, page, _ := c.get(removePath)
	if !strings.Contains(page, "Remove wasync on the computer running it.") || strings.Contains(page, `value="remove"`) {
		t.Fatalf("remote remove page:\n%s", page)
	}
	_, settings, _ := c.get("/ui/settings")
	if strings.Contains(settings, `href="/ui/settings/remove"`) {
		t.Fatal("Settings offers Remove wasync on another device")
	}
	if _, _, hd := c.post(removePath, url.Values{"csrf": {csrfOf(t, settings)}, "action": {"remove"}}); !strings.Contains(hd.Get("Location"), "n=removehere") {
		t.Fatalf("remote remove: %v", hd)
	}
}

func TestHomeAndApprovalsListOnlyAgentsAskingToSend(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	if _, err := prefs.Open(sf.dir).Update(func(s *prefs.Settings) { s.Use = prefs.UseAgents }); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two", "three", "four"} {
		sf.askSend(t, otherChat, "synthetic "+text)
	}
	_, page, _ := c.get("/ui/home")
	for _, want := range []string{"Needs you · 4", "See all 4", "Not now to all", "<strong>jit</strong> wants to send a message to", "Connected agents", "Chats agents can use"} {
		if !strings.Contains(page, want) {
			t.Fatalf("home lacks %q:\n%s", want, page)
		}
	}
	if strings.Count(page, `value="approve"`) != homeRequests {
		t.Fatalf("home shows %d requests, want %d", strings.Count(page, `value="approve"`), homeRequests)
	}
	if strings.Contains(page, otherChat) {
		t.Fatal("home shows a raw chat address")
	}
	_, page, _ = c.get("/ui/approvals")
	if !strings.Contains(page, "Agents asking to send · 4") || strings.Count(page, `value="approve"`) != 4 {
		t.Fatalf("approvals:\n%s", page)
	}
	if _, _, hd := c.post("/ui/decide", url.Values{"csrf": {csrf}, "action": {"decline-all"}, "back": {"/ui/home"}}); !strings.HasPrefix(hd.Get("Location"), "/ui/home") {
		t.Fatalf("Not now to all from Home: %v", hd)
	}
	if _, page, _ = c.get("/ui/home"); strings.Contains(page, "Needs you") {
		t.Fatalf("requests left after Not now to all:\n%s", page)
	}
}

func TestSettingsSwitchesHowWasyncIsUsedAndMedia(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	_, page, _ := c.get("/ui/settings")
	for _, want := range []string{"Show my chats in wasync", "Also save photos, videos and files", "Start wasync when I log in", "Remove wasync", `href="https://gehariharan.com"`, "not made by"} {
		if !strings.Contains(page, want) {
			t.Fatalf("settings lacks %q:\n%s", want, page)
		}
	}
	if _, _, hd := c.post("/ui/settings", url.Values{"csrf": {csrf}, "action": {"chats"}, "on": {"false"}}); !strings.HasPrefix(hd.Get("Location"), "/ui/settings") {
		t.Fatalf("chats off: %v", hd)
	}
	if s, _ := prefs.Open(sf.dir).Get(); !s.AgentOnly() {
		t.Fatalf("chats off was not saved: %+v", s)
	}
	if _, _, hd := c.get("/ui/chats"); hd.Get("Location") != "/ui/home" {
		t.Fatalf("chats are still shown: %v", hd)
	}
	c.post("/ui/settings", url.Values{"csrf": {csrf}, "action": {"media"}, "on": {"false"}})
	if s, _ := prefs.Open(sf.dir).Get(); s.MediaOn() {
		t.Fatalf("media off was not saved: %+v", s)
	}
	if code, _, _ := c.post("/ui/settings", url.Values{"csrf": {csrf}, "action": {"chats"}, "on": {"maybe"}}); code != 400 {
		t.Fatalf("bad value: %d", code)
	}
}

func TestAgentsPageRemovesAnAgentOnlyAfterTheComputerConfirms(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	_, page, _ := c.get("/ui/agents")
	if !strings.Contains(page, `href="/ui/agents/connect"`) || !strings.Contains(page, `href="/ui/sent"`) || !strings.Contains(page, `name="agent" value="jit"`) {
		t.Fatalf("agents page:\n%s", page)
	}
	remove := url.Values{"csrf": {csrf}, "action": {"revoke"}, "agent": {"jit"}}
	if _, _, hd := c.post("/ui/agents", remove); !strings.Contains(hd.Get("Location"), "n=declined") {
		t.Fatalf("declined remove: %v", hd)
	}
	if got, _ := agentauth.ActiveByName(sf.dir, "jit"); got == nil {
		t.Fatal("a declined confirmation removed the agent")
	}
	if seen := sf.stub.seen(); len(seen) != 1 || !strings.Contains(seen[0], `revoke agent "jit"`) {
		t.Fatalf("prompt: %q", seen)
	}
	sf.stub.set(true)
	if _, _, hd := c.post("/ui/agents", remove); !strings.Contains(hd.Get("Location"), "n=agentremoved") {
		t.Fatalf("remove: %v", hd)
	}
	if got, _ := agentauth.ActiveByName(sf.dir, "jit"); got != nil {
		t.Fatal("the agent still works")
	}
}

func TestAgentsPageCannotRemoveAnAgentWithoutAConfirmation(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, false)
	if _, _, hd := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"revoke"}, "agent": {"jit"}}); !strings.Contains(hd.Get("Location"), "n=nogate") {
		t.Fatalf("remove with no confirmation: %v", hd)
	}
	if got, _ := agentauth.ActiveByName(sf.dir, "jit"); got == nil {
		t.Fatal("the agent was removed with no confirmation")
	}
}

// readOnly makes dir unwritable until the test ends, so writing agents.json
// fails while reading it still works.
func readOnly(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this user cannot write")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func TestRemoveWasyncWaitsWhenAnAgentCouldNotBeRevoked(t *testing.T) {
	spy := &removeSpy{}
	f, c, csrf := newConnectFixture(t, false, spy.owner)
	f.stub.set(true)
	readOnly(t, f.dir)
	code, page, _ := c.post(removePath, url.Values{"csrf": {csrf}, "action": {"remove"}})
	if code != 200 || !strings.Contains(page, `wasync could not revoke the agent &#34;jit&#34;`) || !strings.Contains(page, `value="finish"`) {
		t.Fatalf("a failed revoke was not reported: %d\n%s", code, page)
	}
	if _, started := spy.counts(); started != 0 {
		t.Fatal("the helper started although an agent still works")
	}
	if got, _ := agentauth.ActiveByName(f.dir, "jit"); got == nil {
		t.Fatal("the agent was revoked after all; the test did not make revoking fail")
	}
}

func TestDisconnectSaysSoWhenTheCredentialCouldNotBeRevoked(t *testing.T) {
	f, c, csrf := newConnectFixture(t, false)
	f.stub.set(true)
	_, digest := preview(t, c, "claude-code")
	c.post("/ui/setup/connect", url.Values{"csrf": {csrf}, "agent": {"claude-code"}, "digest": {digest}})
	conn, _ := agentconnect.Find(f.dir, agentconnect.ClaudeCode)
	if conn == nil {
		t.Fatal("not connected")
	}
	readOnly(t, f.dir)
	for path, form := range map[string]url.Values{
		"/ui/setup/disconnect": {"csrf": {csrf}, "agent": {"claude-code"}},
		"/ui/agents":           {"csrf": {csrf}, "action": {"revoke"}, "agent": {conn.Client}},
	} {
		if _, _, hd := c.post(path, form); !strings.Contains(hd.Get("Location"), "n=revokefailed") {
			t.Fatalf("%s with a failed revoke: %v", path, hd)
		}
	}
	if got, _ := agentauth.ActiveByName(f.dir, conn.Client); got == nil {
		t.Fatal("the credential was revoked after all")
	}
	if again, _ := agentconnect.Find(f.dir, agentconnect.ClaudeCode); again == nil {
		t.Fatal("the record was dropped, so Disconnect cannot be tried again")
	}
}

func TestRemoveWasyncListsAWasyncAxiCleanupThatFailed(t *testing.T) {
	spy := &removeSpy{}
	f, c, csrf := newConnectFixture(t, false, spy.owner, func(o *Owner) {
		env := *o.AgentConnect
		env.Run = func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
			if strings.Join(args, " ") == "setup hooks --remove" {
				return nil, errors.New("exit status 1")
			}
			return nil, nil
		}
		o.AgentConnect = &env
	})
	f.stub.set(true)
	code, page, _ := c.post(removePath, url.Values{"csrf": {csrf}, "action": {"remove"}})
	if code != 200 || !strings.Contains(page, "run `wasync-axi setup hooks --remove`") || !strings.Contains(page, `value="finish"`) {
		t.Fatalf("a failed hooks removal was not listed: %d\n%s", code, page)
	}
	if strings.Contains(page, "setup skill --remove") {
		t.Fatalf("a skill removal that worked was listed as left:\n%s", page)
	}
	if _, started := spy.counts(); started != 0 {
		t.Fatal("the helper started before the person saw what was left")
	}
}

func TestRemoveWasyncReportsAConnectedAppItCouldNotRevokeOnce(t *testing.T) {
	spy := &removeSpy{}
	f, c, csrf := newConnectFixture(t, false, spy.owner)
	f.stub.set(true)
	_, digest := preview(t, c, "claude-code")
	c.post("/ui/setup/connect", url.Values{"csrf": {csrf}, "agent": {"claude-code"}, "digest": {digest}})
	conn, _ := agentconnect.Find(f.dir, agentconnect.ClaudeCode)
	if conn == nil {
		t.Fatal("not connected")
	}
	readOnly(t, f.dir)
	_, page, _ := c.post(removePath, url.Values{"csrf": {csrf}, "action": {"remove"}})
	if n := strings.Count(page, "could not revoke the agent &#34;"+conn.Client+"&#34;"); n != 1 {
		t.Fatalf("the failed revoke is reported %d times:\n%s", n, page)
	}
	if !strings.Contains(page, "tool server from Claude Code") {
		t.Fatalf("the page does not say to take the entry out of the app's settings:\n%s", page)
	}
}

func TestAgentsPageRemovesANewAgentThatReusesAConnectedAppsName(t *testing.T) {
	f, c, csrf := newConnectFixture(t, false)
	f.stub.set(true)
	_, digest := preview(t, c, "claude-code")
	c.post("/ui/setup/connect", url.Values{"csrf": {csrf}, "agent": {"claude-code"}, "digest": {digest}})
	conn, _ := agentconnect.Find(f.dir, agentconnect.ClaudeCode)
	if conn == nil {
		t.Fatal("not connected")
	}
	// Revoked in a terminal, which keeps the connection record, then the
	// name is given to a new agent.
	if _, err := agentauth.Revoke(f.dir, conn.Client, time.Now()); err != nil {
		t.Fatal(err)
	}
	grantLegacyOnDemand(t, f.dir, conn.Client, agentauth.ScopeReadChats)
	_, page, _ := c.get("/ui/agents")
	card := `name="agent" value="` + conn.Client + `"><button name="action" value="revoke">`
	if !strings.Contains(page, card+"Remove</button>") {
		t.Fatalf("the new agent's card is not a plain Remove:\n%s", page)
	}
	if _, _, hd := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"revoke"}, "agent": {conn.Client}}); !strings.Contains(hd.Get("Location"), "n=agentremoved") {
		t.Fatalf("remove: %v", hd)
	}
	if got, _ := agentauth.ActiveByName(f.dir, conn.Client); got != nil {
		t.Fatal("the new agent still works after the page said it was removed")
	}
}
