package api

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/axicred"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/localfs"
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
	c := newPageClient(t, sf.accessFixture)
	c.get(linkPath(t, sf.accessFixture))
	_, page, _ := c.get("/ui/setup")
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

	_, page, _ := c.get("/ui/setup")
	if strings.Contains(page, "<svg") || !strings.Contains(page, "Waiting for WhatsApp") || !strings.Contains(page, `http-equiv="refresh"`) {
		t.Fatalf("before any code:\n%s", page)
	}
	const secret = "2@PAIRINGSECRETPAIRINGSECRET,abc,def"
	state.SetQR(secret)
	_, page, _ = c.get("/ui/setup")
	if !strings.Contains(page, "<svg") || strings.Contains(page, "PAIRINGSECRET") || !strings.Contains(page, `http-equiv="refresh"`) {
		t.Fatalf("with a code: the page must show an image, never the code as text:\n%.600s", page)
	}
	state.SetFailure("Linking stopped: phone said no")
	_, page, _ = c.get("/ui/setup")
	if !strings.Contains(page, "phone said no") || strings.Contains(page, "<svg") {
		t.Fatalf("after failure:\n%.600s", page)
	}
	state.SetPaired()
	_, page, _ = c.get("/ui/setup")
	if !strings.Contains(page, "Linked.") || strings.Contains(page, "<svg") || strings.Contains(page, "PAIRINGSECRET") {
		t.Fatalf("after pairing:\n%.600s", page)
	}
}

func TestSetupSavesTheChatsYouTick(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, false)
	_, page, _ := c.get("/ui/setup")
	if !strings.Contains(page, "Family Group") || strings.Contains(page, `value="`+familyChat+`" checked`) {
		t.Fatalf("chat list:\n%s", page)
	}
	if code, _, _ := c.post("/ui/setup/chats", url.Values{"csrf": {"wrong"}, "action": {"save"}, "chat": {familyChat}}); code != 403 {
		t.Fatalf("forged form: %d", code)
	}
	code, _, hd := c.post("/ui/setup/chats", url.Values{"csrf": {csrf}, "action": {"save"}, "chat": {familyChat}, "history": {"now"}})
	if code != 303 || !strings.Contains(hd.Get("Location"), "n=saved") || !sf.group(t, familyChat).Enabled {
		t.Fatalf("save: %d %v", code, hd)
	}
	if sf.group(t, phoneChat).Enabled {
		t.Fatal("an unticked chat was turned on")
	}
	if _, _, _ = c.post("/ui/setup/chats", url.Values{"csrf": {csrf}, "action": {"save"}, "chat": {"not-a-listed-chat@g.us"}}); sf.group(t, familyChat).Enabled {
		t.Fatal("saving a new list should turn the unticked chat off")
	}
	c.post("/ui/setup/chats", url.Values{"csrf": {csrf}, "action": {"all-groups"}})
	if !sf.group(t, familyChat).Enabled || sf.group(t, phoneChat).Enabled {
		t.Fatal("keep all groups should turn on groups only")
	}
	c.post("/ui/setup/chats", url.Values{"csrf": {csrf}, "action": {"none"}})
	if sf.group(t, familyChat).Enabled {
		t.Fatal("keep none left a chat on")
	}
}

func TestSetupConnectsAnAgentOnlyWhenThePersonConfirms(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	form := func(extra url.Values) url.Values {
		v := url.Values{"csrf": {csrf}, "name": {"codex-axi"}, "access": {"read"}, "expires": {""}}
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
	if len(sf.stub.seen()) != 1 || !strings.Contains(sf.stub.seen()[0], `"codex-axi"`) || !strings.Contains(sf.stub.seen()[0], "only the chats you approve") {
		t.Fatalf("prompt: %q", sf.stub.seen())
	}

	sf.stub.set(true)
	code, _, hd = c.post("/ui/setup/agent", form(url.Values{"access": {"read-send"}, "expires": {"90d"}}))
	if code != 303 || !strings.Contains(hd.Get("Location"), "n=connected") {
		t.Fatalf("confirmed: %d %v", code, hd)
	}
	client, _ := agentauth.ActiveByName(sf.dir, "codex-axi")
	if client == nil || !client.OnDemand || len(client.Chats) != 0 || client.ExpiresAt == nil || !strings.Contains(strings.Join(client.Scopes, ","), "send") {
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
	_, page, _ := c.get("/ui/setup")
	if strings.Contains(page, `action="/ui/setup/agent"`) || !strings.Contains(page, "wasync agents grant") {
		t.Fatalf("page offers a form it cannot honour:\n%s", page)
	}
	_, _, hd := c.post("/ui/setup/agent", url.Values{"csrf": {csrf}, "name": {"codex-axi"}, "access": {"read"}})
	if !strings.Contains(hd.Get("Location"), "n=nogate") {
		t.Fatalf("posted anyway: %v", hd)
	}
	if got, _ := agentauth.ActiveByName(sf.dir, "codex-axi"); got != nil {
		t.Fatal("agent created with no check available")
	}
	_, _, hd = c.post("/ui/setup/service", url.Values{"csrf": {csrf}, "action": {"install"}})
	if !strings.Contains(hd.Get("Location"), "n=nogate") || sf.item.installed {
		t.Fatalf("login item installed with no check available: %v", hd)
	}
}

func TestSetupLoginItemNeedsThePersonToTurnOnButNotOff(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	_, _, hd := c.post("/ui/setup/service", url.Values{"csrf": {csrf}, "action": {"install"}})
	if !strings.Contains(hd.Get("Location"), "n=declined") || sf.item.installed {
		t.Fatalf("declined: %v", hd)
	}
	sf.stub.set(true)
	_, _, hd = c.post("/ui/setup/service", url.Values{"csrf": {csrf}, "action": {"install"}})
	if !strings.Contains(hd.Get("Location"), "n=loginon") || !sf.item.installed || sf.item.spec.Exe != "/opt/wasync" {
		t.Fatalf("confirmed: %v %+v", hd, sf.item)
	}
	asked := len(sf.stub.seen())
	_, _, hd = c.post("/ui/setup/service", url.Values{"csrf": {csrf}, "action": {"remove"}})
	if !strings.Contains(hd.Get("Location"), "n=loginoff") || sf.item.installed || len(sf.stub.seen()) != asked {
		t.Fatalf("removing should not prompt: %v", hd)
	}
}
