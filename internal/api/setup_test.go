package api

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/axicred"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/prefs"
	"github.com/gehariharan/wasync/internal/service"
	linking "github.com/gehariharan/wasync/internal/setup"
	"github.com/gehariharan/wasync/internal/storage"
)

type fakeLoginItem struct {
	mu        sync.Mutex
	installed bool
	spec      service.Spec
}

func (f *fakeLoginItem) Where() string { return "a test login item" }
func (f *fakeLoginItem) Install(s service.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed, f.spec = true, s
	return nil
}
func (f *fakeLoginItem) Uninstall() error {
	f.mu.Lock()
	f.installed = false
	f.mu.Unlock()
	return nil
}
func (f *fakeLoginItem) Installed() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed, nil
}

type setupFixture struct {
	accessFixture
	axiFile string
	item    *fakeLoginItem
	state   *linking.State
	stub    *promptStub
}

func newSetupFixture(t *testing.T, withPrompt bool) (setupFixture, *pageClient, string) {
	t.Helper()
	sf := setupFixture{axiFile: filepath.Join(t.TempDir(), "axi", "credential"), item: &fakeLoginItem{}, state: &linking.State{}, stub: &promptStub{}}
	var gate *humancheck.Gate
	if withPrompt {
		gate = humancheck.NewGate(sf.stub)
	}
	sf.accessFixture = setupAccessOwner(t, gate, func(o *Owner) {
		o.Setup, o.Service, o.ServiceSpec = sf.state, sf.item, service.Spec{Exe: "/opt/wasync", DataDir: "/data/wasync"}
		o.AxiPath = func() (string, error) { return sf.axiFile, nil }
	})
	sf.state.SetPaired()
	c := newPageClient(t, sf.accessFixture)
	c.get(linkPath(t, sf.accessFixture))
	_, page, _ := c.get("/ui/settings")
	return sf, c, csrfOf(t, page)
}

func TestSetupLinkOpensTheSetupPageAndNothingElse(t *testing.T) {
	f := setupAccess(t)
	code, out := f.signed(t, "POST", uiLinkPath, f.approver, map[string]string{"page": "setup"})
	if code != 200 || !strings.Contains(string(out), "next=setup") {
		t.Fatalf("setup link: %d %s", code, out)
	}
	if code, _ = f.signed(t, "POST", uiLinkPath, f.approver, map[string]string{"page": "../../etc"}); code != 400 {
		t.Fatalf("an arbitrary page name was accepted: %d", code)
	}
	c := newPageClient(t, f)
	var got struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	link := got.URL[strings.Index(got.URL, "/ui/enter"):]
	if code, _, hd := c.get(link); code != 303 || hd.Get("Location") != "/ui/setup" {
		t.Fatalf("enter: %d %v", code, hd)
	}
}

// qrImage marks the QR code drawing; the page has other drawings (its icons).
const qrImage = `aria-label="QR code to scan`

func TestSetupPageShowsTheQRCodeAsAnImageUntilTheScan(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	state := &linking.State{}
	server, err := StartOwner(Owner{Addr: "127.0.0.1:0", Dir: dir, Store: store, Setup: state, Human: humancheck.NewGate(&promptStub{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	c := &pageClient{t: t, base: server.URL, http: newPageClient(t, accessFixture{server: server}).http}
	c.get(strings.TrimPrefix(server.NewLink("setup"), server.URL))

	// A new computer opens on the First open screen, once.
	code, _, hd := c.get("/ui/setup")
	if code != 303 || hd.Get("Location") != welcomePath {
		t.Fatalf("first open: %d %q", code, hd.Get("Location"))
	}
	_, welcome, _ := c.get(welcomePath)
	for _, want := range []string{"Welcome to wasync", `href="https://gehariharan.com"`, "@gehariharan", "not made by", "I understand"} {
		if !strings.Contains(welcome, want) {
			t.Fatalf("welcome lacks %q:\n%s", want, welcome)
		}
	}
	if code, _, hd = c.post(welcomePath, url.Values{"csrf": {csrfOf(t, welcome)}}); code != 303 || hd.Get("Location") != "/ui/setup/link" {
		t.Fatalf("I understand: %d %q", code, hd.Get("Location"))
	}
	if _, _, hd = c.get("/ui/setup"); hd.Get("Location") != "/ui/setup/link" {
		t.Fatalf("the First open screen showed twice: %q", hd.Get("Location"))
	}

	_, page, _ := c.get("/ui/setup/link")
	if strings.Contains(page, qrImage) || !strings.Contains(page, "Getting a code from WhatsApp") || !strings.Contains(page, `http-equiv="refresh"`) {
		t.Fatalf("before any code:\n%s", page)
	}
	const secret = "2@PAIRINGSECRETPAIRINGSECRET,abc,def"
	state.SetQR(secret)
	_, page, _ = c.get("/ui/setup/link")
	if !strings.Contains(page, qrImage) || strings.Contains(page, "PAIRINGSECRET") || !strings.Contains(page, `http-equiv="refresh"`) {
		t.Fatalf("with a code: the page must show an image, never the code as text:\n%.600s", page)
	}
	state.SetFailure("Linking stopped: phone said no")
	_, page, _ = c.get("/ui/setup/link")
	if !strings.Contains(page, "phone said no") || strings.Contains(page, qrImage) || !strings.Contains(page, "Get a new code") {
		t.Fatalf("after failure:\n%.600s", page)
	}
	csrf := csrfOf(t, page)
	if code, _, _ := c.post("/ui/setup/link", url.Values{"csrf": {"wrong"}}); code != 403 {
		t.Fatalf("forged new-code form: %d", code)
	}
	if _, _, hd := c.post("/ui/setup/link", url.Values{"csrf": {csrf}}); !strings.Contains(hd.Get("Location"), "n=newcode") {
		t.Fatalf("new code: %v", hd)
	}
	select {
	case <-state.Retried():
	default:
		t.Fatal("the linker was not asked for a new code")
	}
	if _, _, hd := c.post("/ui/setup/link", url.Values{"csrf": {csrf}}); strings.Contains(hd.Get("Location"), "n=newcode") {
		t.Fatal("a second click started a second attempt")
	}
	select {
	case <-state.Retried():
		t.Fatal("the linker was asked twice")
	default:
	}
	state.SetQR(secret)
	state.SetPaired()
	_, page, _ = c.get("/ui/setup/link")
	if !strings.Contains(page, "Linked.") || !strings.Contains(page, "How will you use wasync?") || strings.Contains(page, qrImage) || strings.Contains(page, "PAIRINGSECRET") {
		t.Fatalf("after pairing:\n%.600s", page)
	}
}

func TestSetupShowsTheCodeAgainForAnArchiveWhoseDeviceWasUnlinked(t *testing.T) {
	f := setupAccess(t) // its archive still has the account from before
	state := &linking.State{}
	server, err := StartOwner(Owner{Addr: "127.0.0.1:0", Dir: f.dir, Store: f.store, Setup: state, Human: humancheck.NewGate(&promptStub{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	c := &pageClient{t: t, base: server.URL, http: newPageClient(t, accessFixture{server: server}).http}
	c.get(strings.TrimPrefix(server.NewLink("setup"), server.URL))
	state.SetQR("2@RELINKCODE,abc,def")
	if _, _, hd := c.get("/ui/setup"); hd.Get("Location") != "/ui/setup/link" {
		t.Fatalf("an unlinked archive was not sent to linking: %q", hd.Get("Location"))
	}
	_, page, _ := c.get("/ui/setup/link")
	if strings.Contains(page, "Linked.") || !strings.Contains(page, qrImage) {
		t.Fatalf("an old archive hid the code to scan:\n%.600s", page)
	}
	state.SetPaired()
	if _, page, _ = c.get("/ui/setup/link"); !strings.Contains(page, "Linked.") {
		t.Fatalf("after linking again:\n%.600s", page)
	}

	// Outside setup the running owner's archive account is what says linked.
	running := newPageClient(t, f)
	running.get(linkPath(t, f))
	if _, page, _ = running.get("/ui/setup/link"); !strings.Contains(page, "Linked.") {
		t.Fatalf("running owner:\n%.600s", page)
	}
}

func TestSetupStepsLeadFromLinkingToHome(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	if _, _, hd := c.get("/ui/setup"); hd.Get("Location") != "/ui/setup/link" {
		t.Fatalf("a linked phone with setup not done: %q", hd.Get("Location"))
	}
	_, page, _ := c.get("/ui/setup/link")
	for _, want := range []string{`aria-current="step"`, "</span> Link", "</span> Chats", "</span> Keep running", "</span> Agent", `name="use" value="agents" checked`} {
		if !strings.Contains(page, want) {
			t.Fatalf("step 1 lacks %q:\n%s", want, page)
		}
	}
	if code, _, _ := c.post("/ui/setup/use", url.Values{"csrf": {csrf}, "use": {"everything"}}); code != 303 {
		t.Fatalf("bad choice: %d", code)
	}
	if _, _, hd := c.post("/ui/setup/use", url.Values{"csrf": {csrf}, "use": {"agents"}}); hd.Get("Location") != "/ui/setup/chats" {
		t.Fatalf("use: %q", hd.Get("Location"))
	}
	if s, _ := prefs.Open(sf.dir).Get(); s.Use != prefs.UseAgents {
		t.Fatalf("use was not saved: %+v", s)
	}

	// Step 2 is Choose chats with nothing ticked, and Next goes on to step 3.
	_, page, _ = c.get("/ui/setup/chats")
	if !strings.Contains(page, "Choose chats to sync") || !strings.Contains(page, "Selected · <span data-count>0<") || !strings.Contains(page, `src="/ui/picker.js"`) {
		t.Fatalf("step 2:\n%s", page)
	}
	if _, _, hd := c.post("/ui/setup/chats", url.Values{"csrf": {csrf}, "sync": {familyChat}}); !strings.HasPrefix(hd.Get("Location"), "/ui/setup/chats") || !sf.group(t, familyChat).Enabled {
		t.Fatalf("tick in setup: %q", hd.Get("Location"))
	}
	if _, _, hd := c.post("/ui/setup/chats", url.Values{"csrf": {csrf}, "action": {"next"}, "save_media": {"on"}}); hd.Get("Location") != "/ui/setup/running" {
		t.Fatalf("next: %q", hd.Get("Location"))
	}

	// Step 3 turns start-at-login on, and stays on the step.
	_, page, _ = c.get("/ui/setup/running")
	if !strings.Contains(page, "Start wasync when I log in") || !strings.Contains(page, `role="switch" aria-checked="false"`) {
		t.Fatalf("step 3:\n%s", page)
	}
	if _, _, hd := c.post("/ui/setup/service", url.Values{"csrf": {csrf}, "action": {"install"}, "back": {"/ui/setup/running"}}); !strings.HasPrefix(hd.Get("Location"), "/ui/setup/running?n=loginon") || !sf.item.installed {
		t.Fatalf("start at login: %q", hd.Get("Location"))
	}

	// Step 4 offers the apps; skipping it finishes setup on Home.
	_, page, _ = c.get("/ui/setup/agent")
	for _, want := range []string{"Connect your AI app", "ChatGPT &amp; Codex", "Claude Code", "Claude Desktop", "Other", "Skip for now"} {
		if !strings.Contains(page, want) {
			t.Fatalf("step 4 lacks %q:\n%s", want, page)
		}
	}
	if code, _, _ := c.post("/ui/setup/finish", url.Values{"csrf": {"forged"}}); code != 403 {
		t.Fatalf("forged finish: %d", code)
	}
	if _, _, hd := c.post("/ui/setup/finish", url.Values{"csrf": {csrf}}); hd.Get("Location") != "/ui/home" {
		t.Fatalf("finish: %q", hd.Get("Location"))
	}
	if _, _, hd := c.get("/ui/setup"); hd.Get("Location") != "/ui/settings" {
		t.Fatalf("after setup, Set up opens Settings: %q", hd.Get("Location"))
	}
	if _, _, hd := c.get("/ui/"); hd.Get("Location") != "/ui/home" {
		t.Fatalf("agent-only start page: %q", hd.Get("Location"))
	}
	// Agents only: the chats are not shown in wasync.
	for _, path := range []string{"/ui/chats", "/ui/chats/new"} {
		if code, _, hd := c.get(path); code != 303 || hd.Get("Location") != "/ui/home" {
			t.Fatalf("%s in agent-only mode: %d %q", path, code, hd.Get("Location"))
		}
	}
	_, page, _ = c.get("/ui/home")
	if !strings.Contains(page, `href="/ui/home"`) || strings.Contains(page, `<a class="rail-i" href="/ui/chats"`) || !strings.Contains(page, "Family Group") {
		t.Fatalf("home:\n%s", page)
	}
}

func TestSetupConnectsAnAgentOnlyWhenThePersonConfirms(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	form := func(extra url.Values) url.Values {
		v := url.Values{"csrf": {csrf}, "name": {"codex-axi"}}
		for k, vals := range extra {
			v[k] = vals
		}
		return v
	}
	code, _, hd := c.post("/ui/setup/agent", form(nil))
	if code != 303 || !strings.Contains(hd.Get("Location"), "n=declined") {
		t.Fatalf("declined: %d %v", code, hd)
	}
	if got, _ := agentauth.ActiveByName(sf.dir, "codex-axi"); got != nil {
		t.Fatal("agent created without the person")
	}
	if _, err := os.Stat(sf.axiFile); !os.IsNotExist(err) {
		t.Fatal("credential saved without the person")
	}
	if len(sf.stub.seen()) != 1 || !strings.Contains(sf.stub.seen()[0], `"codex-axi"`) || !strings.Contains(sf.stub.seen()[0], "read, reply and react in every chat you sync in wasync") {
		t.Fatalf("prompt: %q", sf.stub.seen())
	}

	sf.stub.set(true)
	// Asking for more in the form still connects the agent with read, reply
	// and react in the synced chats, and no expiry.
	code, _, hd = c.post("/ui/setup/agent", form(url.Values{"access": {"read-send"}, "expires": {"90d"}}))
	if code != 303 || !strings.Contains(hd.Get("Location"), "n=connected") {
		t.Fatalf("confirmed: %d %v", code, hd)
	}
	client, _ := agentauth.ActiveByName(sf.dir, "codex-axi")
	if client == nil || !client.SyncedChats || client.OnDemand || client.ExpiresAt != nil ||
		!reflect.DeepEqual(client.Scopes, agentauth.ConnectScopes) {
		t.Fatalf("client %+v", client)
	}
	saved, err := axicred.Existing(sf.axiFile)
	if err != nil || !strings.HasPrefix(saved, "wsa_") {
		t.Fatalf("saved credential: %q %v", saved, err)
	}
	_, page, _ := c.get("/ui/setup")
	if strings.Contains(page, saved) || strings.Contains(page, "wsa_") {
		t.Fatal("the credential secret reached the page")
	}

	// A second agent must not silently replace the saved credential or reuse a name.
	if code, _, hd = c.post("/ui/setup/agent", form(nil)); !strings.Contains(hd.Get("Location"), "n=exists") {
		t.Fatalf("duplicate name: %d %v", code, hd)
	}
	if _, _, hd = c.post("/ui/setup/agent", form(url.Values{"name": {"second-axi"}})); !strings.Contains(hd.Get("Location"), "n=replace") {
		t.Fatalf("replace guard: %v", hd)
	}
	if again, _ := axicred.Existing(sf.axiFile); again != saved {
		t.Fatal("the saved credential was replaced without being asked")
	}
	if _, _, hd = c.post("/ui/setup/agent", form(url.Values{"name": {"second-axi"}, "replace": {"yes"}})); !strings.Contains(hd.Get("Location"), "n=connected") {
		t.Fatalf("explicit replace: %v", hd)
	}
	if _, _, hd = c.post("/ui/setup/agent", form(url.Values{"name": {"Bad Name!"}})); !strings.Contains(hd.Get("Location"), "n=badname") {
		t.Fatalf("bad name: %v", hd)
	}
}

func TestSetupCannotConnectAnAgentWithoutASystemPrompt(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, false)
	_, page, _ := c.get("/ui/setup/agent?app=other")
	if strings.Contains(page, `action="/ui/setup/agent"`) || !strings.Contains(page, "cannot show a system confirmation") {
		t.Fatalf("page offers a form it cannot honour:\n%s", page)
	}
	_, _, hd := c.post("/ui/setup/agent", url.Values{"csrf": {csrf}, "name": {"codex-axi"}})
	if !strings.Contains(hd.Get("Location"), "n=nogate") {
		t.Fatalf("posted anyway: %v", hd)
	}
	if got, _ := agentauth.ActiveByName(sf.dir, "codex-axi"); got != nil {
		t.Fatal("agent created with no check available")
	}
}

func TestSetupLoginItemTurnsOnAndOffWithoutAPrompt(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	_, _, hd := c.post("/ui/setup/service", url.Values{"csrf": {csrf}, "action": {"install"}})
	if !strings.Contains(hd.Get("Location"), "n=loginon") || !sf.item.installed || sf.item.spec.Exe != "/opt/wasync" {
		t.Fatalf("on: %v %+v", hd, sf.item)
	}
	_, _, hd = c.post("/ui/setup/service", url.Values{"csrf": {csrf}, "action": {"remove"}})
	if !strings.Contains(hd.Get("Location"), "n=loginoff") || sf.item.installed {
		t.Fatalf("off: %v", hd)
	}
	if len(sf.stub.seen()) != 0 {
		t.Fatalf("start-at-login asked for a confirmation: %q", sf.stub.seen())
	}
	if code, _, _ := c.post("/ui/setup/service", url.Values{"csrf": {"forged"}, "action": {"install"}}); code != 403 || sf.item.installed {
		t.Fatalf("forged form: %d", code)
	}
}

func TestTheAppReopensOnTheSetupStepsUntilTheyAreFinished(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	if _, _, hd := c.get("/ui/"); hd.Get("Location") != "/ui/setup" {
		t.Fatalf("a linked phone with setup not done opens: %q", hd.Get("Location"))
	}
	if _, _, hd := c.post("/ui/setup/use", url.Values{"csrf": {csrf}, "use": {"chats"}}); hd.Get("Location") != "/ui/setup/chats" {
		t.Fatalf("use: %q", hd.Get("Location"))
	}

	// The person quits after step 1; opening the app again, in a new
	// browser session, goes back to the step they reached.
	again := newPageClient(t, sf.accessFixture)
	again.get(linkPath(t, sf.accessFixture))
	if _, _, hd := again.get("/ui/"); hd.Get("Location") != "/ui/setup" {
		t.Fatalf("reopened after step 1: %q", hd.Get("Location"))
	}
	if _, _, hd := again.get("/ui/setup"); hd.Get("Location") != "/ui/setup/chats" {
		t.Fatalf("setup resumes at: %q", hd.Get("Location"))
	}

	if _, _, hd := c.post("/ui/setup/finish", url.Values{"csrf": {csrf}}); hd.Get("Location") != "/ui/chats" {
		t.Fatalf("finish: %q", hd.Get("Location"))
	}
	if _, _, hd := again.get("/ui/"); hd.Get("Location") != "/ui/chats" {
		t.Fatalf("after finishing, the app opens: %q", hd.Get("Location"))
	}
}
