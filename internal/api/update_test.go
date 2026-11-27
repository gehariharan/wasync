package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/update"
)

// fakeUpdater stands in for internal/update.Manager.
type fakeUpdater struct {
	mu     sync.Mutex
	status update.Status
	checks int
	starts int
}

func (f *fakeUpdater) Status() update.Status { f.mu.Lock(); defer f.mu.Unlock(); return f.status }
func (f *fakeUpdater) Check(context.Context) update.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.status
}
func (f *fakeUpdater) Start(version string) (update.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status.Latest == nil || version != f.status.Latest.Version {
		return f.status, update.ErrNotReady
	}
	f.starts++
	f.status.State, f.status.CanInstall, f.status.Progress = update.StateDownloading, false, &update.Progress{Done: 25, Total: 100}
	return f.status, nil
}
func (f *fakeUpdater) counts() (checks, starts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.starts
}

func availableUpdate() update.Status {
	return update.Status{Current: "0.1.0-preview.1", State: update.StateAvailable, CanInstall: true,
		Channel: update.Channel{Kind: update.KindMacApp, OneClick: true},
		Latest:  &update.Latest{Version: "0.1.0-preview.2", Page: "https://github.com/gehariharan/wasync/releases/tag/v0.1.0-preview.2"}}
}

type updateFixture struct {
	accessFixture
	updater *fakeUpdater
	stub    *promptStub
	page    *pageClient
	csrf    string
}

func newUpdateFixture(t *testing.T, withPrompt bool, status update.Status) updateFixture {
	t.Helper()
	f := updateFixture{updater: &fakeUpdater{status: status}, stub: &promptStub{}}
	var gate *humancheck.Gate
	if withPrompt {
		gate = humancheck.NewGate(f.stub)
	}
	f.accessFixture = setupAccessOwner(t, gate, func(o *Owner) { o.Update = f.updater })
	f.page = newPageClient(t, f.accessFixture)
	f.page.get(linkPath(t, f.accessFixture))
	_, setup, _ := f.page.get("/ui/settings")
	f.csrf = csrfOf(t, setup)
	return f
}

// postJSON is what a page script sends: the form token in a header.
func (f updateFixture) postJSON(t *testing.T, path, csrf string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.page.base+path, nil)
	req.Header.Set("Origin", f.page.origin())
	if csrf != "" {
		req.Header.Set("X-Wasync-CSRF", csrf)
	}
	code, body, _ := f.page.do(req)
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	return code, out
}

func TestUpdatesNeedTheOwnersBrowserSession(t *testing.T) {
	f := newUpdateFixture(t, true, availableUpdate())
	f.stub.set(true)
	for _, token := range []string{f.jit, f.approver, ""} {
		if code, _ := f.do(t, "GET", updateProgressPath, token, nil); code != http.StatusUnauthorized {
			t.Errorf("GET %s with an agent or owner token and no session: %d", updateProgressPath, code)
		}
		for _, path := range []string{updateCheckPath, updateStartPath} {
			if code, _ := f.do(t, "POST", path, token, nil); code != http.StatusUnauthorized {
				t.Errorf("POST %s with an agent or owner token and no session: %d", path, code)
			}
		}
	}
	if code, _ := f.signed(t, "POST", updateStartPath, f.approver, nil); code != http.StatusUnauthorized {
		t.Errorf("a signed owner request is not a browser session: %d", code)
	}
	if checks, starts := f.updater.counts(); checks != 0 || starts != 0 {
		t.Fatalf("checked %d, started %d without a session", checks, starts)
	}
	if got := f.stub.seen(); len(got) != 0 {
		t.Fatalf("prompted without a session: %v", got)
	}
}

func TestUpdateRoutesNeedTheFormToken(t *testing.T) {
	f := newUpdateFixture(t, true, availableUpdate())
	f.stub.set(true)
	for _, path := range []string{updateCheckPath, updateStartPath} {
		if code, _ := f.postJSON(t, path, ""); code != http.StatusForbidden {
			t.Errorf("%s without the token: %d", path, code)
		}
		if code, _ := f.postJSON(t, path, "wrong"); code != http.StatusForbidden {
			t.Errorf("%s with a wrong token: %d", path, code)
		}
	}
	if checks, starts := f.updater.counts(); checks != 0 || starts != 0 {
		t.Fatalf("checked %d, started %d without the token", checks, starts)
	}
	req, _ := http.NewRequest("POST", f.page.base+updateStartPath, nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("X-Wasync-CSRF", f.csrf)
	if code, _, _ := f.page.do(req); code != http.StatusForbidden {
		t.Errorf("a foreign origin: %d", code)
	}
}

func TestUpdateProgressCheckAndResult(t *testing.T) {
	status := availableUpdate()
	status.LastResult = &update.Result{From: "0.1.0-preview.0", To: "0.1.0-preview.1", OK: true}
	f := newUpdateFixture(t, true, status)
	code, body, _ := f.page.get(updateProgressPath)
	var got update.Status
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || got.State != update.StateAvailable || !got.CanInstall || got.Latest.Version != "0.1.0-preview.2" || got.LastResult == nil || got.LastResult.To != "0.1.0-preview.1" {
		t.Fatalf("progress: %d %s", code, body)
	}
	if code, out := f.postJSON(t, updateCheckPath, f.csrf); code != 200 || out["state"] != update.StateAvailable {
		t.Fatalf("check: %d %v", code, out)
	}
	if checks, _ := f.updater.counts(); checks != 1 {
		t.Fatalf("checks %d", checks)
	}
}

func TestStartingAnUpdateAsksThePersonFirst(t *testing.T) {
	f := newUpdateFixture(t, true, availableUpdate())
	code, out := f.postJSON(t, updateStartPath, f.csrf)
	if code != http.StatusForbidden {
		t.Fatalf("declined: %d %v", code, out)
	}
	if _, starts := f.updater.counts(); starts != 0 {
		t.Fatal("a declined prompt must not start the update")
	}
	prompts := f.stub.seen()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "from 0.1.0-preview.1 to 0.1.0-preview.2") {
		t.Fatalf("prompts %v", prompts)
	}
	f.stub.set(true)
	if code, out = f.postJSON(t, updateStartPath, f.csrf); code != http.StatusAccepted || out["state"] != update.StateDownloading {
		t.Fatalf("confirmed: %d %v", code, out)
	}
	if _, starts := f.updater.counts(); starts != 1 {
		t.Fatalf("starts %d", starts)
	}
	// Nothing to install now, so nobody is asked again.
	if code, _ = f.postJSON(t, updateStartPath, f.csrf); code != http.StatusConflict {
		t.Fatalf("second start: %d", code)
	}
	if len(f.stub.seen()) != 2 {
		t.Fatalf("prompts %v", f.stub.seen())
	}
}

func TestNoUpdateWithoutASystemPromptOrSomethingToInstall(t *testing.T) {
	f := newUpdateFixture(t, false, availableUpdate())
	if code, _ := f.postJSON(t, updateStartPath, f.csrf); code != http.StatusServiceUnavailable {
		t.Fatalf("no prompt on this computer: %d", code)
	}
	brew := update.Status{Current: "1.0.0", State: update.StateAvailable, Note: "Update it with brew.", Latest: &update.Latest{Version: "1.1.0"}}
	f = newUpdateFixture(t, true, brew)
	f.stub.set(true)
	if code, _ := f.postJSON(t, updateStartPath, f.csrf); code != http.StatusConflict {
		t.Fatalf("not one-click: %d", code)
	}
	if len(f.stub.seen()) != 0 {
		t.Fatal("nobody should be asked when there is nothing to install")
	}
	if _, starts := f.updater.counts(); starts != 0 {
		t.Fatal("started an update that cannot be installed")
	}
}

func TestSetupPageOffersTheUpdate(t *testing.T) {
	f := newUpdateFixture(t, true, availableUpdate())
	_, page, _ := f.page.get("/ui/settings")
	for _, want := range []string{`id="update"`, "This is wasync 0.1.0-preview.1.", "wasync 0.1.0-preview.2 is available.", `value="install"`, `value="check"`, "Asks for the test prompt, then downloads"} {
		if !strings.Contains(page, want) {
			t.Errorf("setup page lacks %q", want)
		}
	}
	if strings.Contains(page[strings.Index(page, `id="update"`):], "style=") {
		t.Error("the update notice must stay unstyled")
	}
	code, _, hdr := f.page.post(setupUpdatePath, url.Values{"csrf": {f.csrf}, "action": {"check"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/ui/settings") {
		t.Fatalf("check form: %d %v", code, hdr)
	}
	f.stub.set(true)
	code, _, hdr = f.page.post(setupUpdatePath, url.Values{"csrf": {f.csrf}, "action": {"install"}})
	if code != http.StatusSeeOther || hdr.Get("Location") != "/ui/settings?n=updstart" {
		t.Fatalf("install form: %d %v", code, hdr)
	}
	if checks, starts := f.updater.counts(); checks != 1 || starts != 1 {
		t.Fatalf("checks %d, starts %d", checks, starts)
	}
	_, page, _ = f.page.get("/ui/settings")
	if !strings.Contains(page, "Downloading wasync 0.1.0-preview.2 (25%)") || !strings.Contains(page, `http-equiv="refresh"`) {
		t.Fatalf("an update under way must show and refresh:\n%s", page)
	}
}

func TestSetupPageWithoutAnUpdaterHasNoUpdateSection(t *testing.T) {
	f, c, _ := newSetupFixture(t, true)
	_ = f
	if _, page, _ := c.get("/ui/settings"); strings.Contains(page, `id="update"`) {
		t.Fatal("no updater, no update section")
	}
	if code, _, _ := c.get(updateProgressPath); code != http.StatusServiceUnavailable {
		t.Fatalf("progress without an updater: %d", code)
	}
}
