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
	f, c, csrf := newSetupFixture(t, true)
	firstOpenAgain(t, f.accessFixture)
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

// Start-at-login is the person's switch on this computer, like the
// installer's tick box: it turns on and off without a system prompt.
func TestDesktopStartAtLoginTurnsOnAndOffWithoutAPrompt(t *testing.T) {
	for _, withPrompt := range []bool{true, false} {
		sf, c, csrf := newSetupFixture(t, withPrompt)
		d := desktopJSON(t)(c.get(desktopPath))
		if want := (desktopLoginItem{Available: true, Where: "a test login item"}); d.StartAtLogin != want {
			t.Fatalf("before: %+v", d.StartAtLogin)
		}
		if code, body, _ := c.post(desktopLoginPath, url.Values{"csrf": {csrf}, "on": {"yes"}}); code != 400 || !strings.Contains(body, "error") {
			t.Fatalf("a bad value: %d %s", code, body)
		}
		d = desktopJSON(t)(c.post(desktopLoginPath, url.Values{"csrf": {csrf}, "on": {"true"}}))
		if !d.StartAtLogin.On || !sf.item.installed || sf.item.spec.Exe != "/opt/wasync" {
			t.Fatalf("turned on: %+v %+v", d, sf.item)
		}
		d = desktopJSON(t)(c.post(desktopLoginPath, url.Values{"csrf": {csrf}, "on": {"false"}}))
		if d.StartAtLogin.On || sf.item.installed {
			t.Fatalf("turned off: %+v", d)
		}
		if got := sf.stub.seen(); len(got) != 0 {
			t.Fatalf("start-at-login asked for a prompt: %q", got)
		}
	}
}

func TestDesktopSettingsNeedASignedInPage(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	if code, _, _ := c.get(desktopPath); code != 401 {
		t.Fatalf("signed out: %d", code)
	}
}
