package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// desktopJSON reads a settings answer: desktopJSON(t)(c.get(path)).
func desktopJSON(t *testing.T) func(int, string, http.Header) desktopSettings {
	t.Helper()
	return func(code int, body string, _ http.Header) desktopSettings {
		t.Helper()
		return readDesktop(t, code, body)
	}
}

func readDesktop(t *testing.T, code int, body string) desktopSettings {
	t.Helper()
	if code != 200 {
		t.Fatalf("HTTP %d: %s", code, body)
	}
	var d desktopSettings
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	return d
}

func TestDesktopSettingsFirstOpenIsShownOnceAndSaved(t *testing.T) {
	_, c, csrf := newSetupFixture(t, true)
	code, body, hd := c.get(desktopPath)
	d := readDesktop(t, code, body)
	if !d.FirstOpen || hd.Get("Content-Type") != "application/json" || hd.Get("Cache-Control") != "no-store" {
		t.Fatalf("a new data directory: %+v %v", d, hd)
	}
	if code, _, _ := c.post(desktopFirstOpenPath, url.Values{"csrf": {"wrong"}}); code != 403 {
		t.Fatalf("without the page's csrf field: %d", code)
	}
	if code, _, _ := c.get(desktopFirstOpenPath); code != 404 {
		t.Fatalf("marking it seen needs a form: %d", code)
	}
	code, body, _ = c.post(desktopFirstOpenPath, url.Values{"csrf": {csrf}})
	if d = readDesktop(t, code, body); d.FirstOpen {
		t.Fatalf("after I understand: %+v", d)
	}
	if d = desktopJSON(t)(c.get(desktopPath)); d.FirstOpen {
		t.Fatal("the First open screen came back")
	}
	if code, _, _ := c.get(desktopPath + "/elsewhere"); code != 404 {
		t.Fatalf("an unknown desktop path: %d", code)
	}
}

func TestDesktopStartAtLoginAsksToTurnOnButNotOff(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, true)
	d := desktopJSON(t)(c.get(desktopPath))
	if want := (desktopLoginItem{Available: true, Where: "a test login item", Confirm: "the test prompt"}); d.StartAtLogin != want {
		t.Fatalf("before: %+v", d.StartAtLogin)
	}
	if code, body, _ := c.post(desktopLoginPath, url.Values{"csrf": {csrf}, "on": {"yes"}}); code != 400 || !strings.Contains(body, "error") {
		t.Fatalf("a bad value: %d %s", code, body)
	}
	if code, body, _ := c.post(desktopLoginPath, url.Values{"csrf": {csrf}, "on": {"true"}}); code != 403 || sf.item.installed {
		t.Fatalf("declined at the prompt: %d %s", code, body)
	}
	sf.stub.set(true)
	d = desktopJSON(t)(c.post(desktopLoginPath, url.Values{"csrf": {csrf}, "on": {"true"}}))
	if !d.StartAtLogin.On || !sf.item.installed || sf.item.spec.Exe != "/opt/wasync" {
		t.Fatalf("confirmed: %+v %+v", d, sf.item)
	}
	if got := sf.stub.seen(); got[len(got)-1] != loginItemPrompt {
		t.Fatalf("prompt %q", got)
	}
	asked := len(sf.stub.seen())
	d = desktopJSON(t)(c.post(desktopLoginPath, url.Values{"csrf": {csrf}, "on": {"false"}}))
	if d.StartAtLogin.On || sf.item.installed || len(sf.stub.seen()) != asked {
		t.Fatalf("turning it off must not prompt: %+v", d)
	}
}

func TestDesktopStartAtLoginNeedsASystemPromptToTurnOn(t *testing.T) {
	sf, c, csrf := newSetupFixture(t, false)
	d := desktopJSON(t)(c.get(desktopPath))
	if !d.StartAtLogin.Available || d.StartAtLogin.Confirm != "" {
		t.Fatalf("no prompt here: %+v", d.StartAtLogin)
	}
	if code, _, _ := c.post(desktopLoginPath, url.Values{"csrf": {csrf}, "on": {"true"}}); code != 403 || sf.item.installed {
		t.Fatalf("turned on with no prompt: %d", code)
	}
}

func TestDesktopSettingsNeedASignedInPage(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	if code, _, _ := c.get(desktopPath); code != 401 {
		t.Fatalf("signed out: %d", code)
	}
}
