package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The web app's look lives in two shared stylesheets. These tests keep it
// there: a page that brings its own <style> block or style attribute fails,
// and so does any template source that would.

var inlineStyle = regexp.MustCompile(`(?i)<style[\s>]|\sstyle\s*=`)

func TestNoTemplateCarriesItsOwnStyles(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if loc := inlineStyle.FindStringIndex(line); loc != nil {
				t.Errorf("%s:%d has an inline style; add a class to webassets/components.css instead: %.120s", f, i+1, line[loc[0]:])
			}
		}
	}
}

func TestEveryPageUsesTheSharedStylesheetsAndNoInlineStyles(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	_, signedOut, signedOutHd := c.get("/ui/chats")
	c.get(linkPath(t, f))
	pages := map[string]string{"signed out": signedOut}
	headers := map[string]string{"signed out": signedOutHd.Get("Content-Security-Policy")}
	for _, path := range []string{"/ui/approvals", "/ui/chats", "/ui/chats/pick", "/ui/chats/new", "/ui/sent", "/ui/agents", "/ui/setup"} {
		code, page, hd := c.get(path)
		if code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
		pages[path], headers[path] = page, hd.Get("Content-Security-Policy")
	}
	for path, page := range pages {
		if loc := inlineStyle.FindStringIndex(page); loc != nil {
			t.Errorf("%s has an inline style: %.120s", path, page[loc[0]:])
		}
		for _, want := range []string{`<link rel="stylesheet" href="/ui/tokens.css?v=` + tokensCSSVersion + `">`, `<link rel="stylesheet" href="/ui/components.css?v=` + componentsCSSVersion + `">`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s does not link %s", path, want)
			}
		}
		csp := headers[path]
		if !strings.Contains(csp, "style-src 'self'") || !strings.Contains(csp, "font-src 'self'") || strings.Contains(csp, "unsafe-inline") {
			t.Errorf("%s: the content policy must allow only wasync's own styles and font: %q", path, csp)
		}
	}
}

func TestTheRailMarksTheCurrentPageAndCountsWaitingRequests(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	_, signedOut, _ := c.get("/ui/chats")
	if strings.Contains(signedOut, `class="rail"`) {
		t.Fatal("the signed-out page shows the app's navigation")
	}
	c.get(linkPath(t, f))
	for path, item := range map[string]string{"/ui/chats": "Chats", "/ui/chats/pick": "Chats", "/ui/sent": "Sent", "/ui/agents": "Agents", "/ui/approvals": "Approvals", "/ui/setup": "Set up and settings"} {
		_, page, _ := c.get(path)
		if strings.Count(page, `aria-current="page"`) != 1 {
			t.Fatalf("%s: want exactly one current rail item:\n%s", path, page)
		}
		current := regexp.MustCompile(`<a class="rail-i" href="[^"]+" title="[^"]*" aria-label="([^"]+)" aria-current="page">`).FindStringSubmatch(page)
		if current == nil || current[1] != item {
			t.Fatalf("%s: current rail item %v, want %q", path, current, item)
		}
		for _, link := range []string{"/ui/chats", "/ui/sent", "/ui/agents", "/ui/approvals", "/ui/setup"} {
			if !strings.Contains(page, `<a class="rail-i" href="`+link+`"`) {
				t.Fatalf("%s: the rail does not reach %s", path, link)
			}
		}
	}
	if _, page, _ := c.get("/ui/chats"); strings.Contains(page, `class="rail-badge"`) {
		t.Fatal("a badge shows with nothing waiting")
	}
	if code, out := f.do(t, "POST", "/api/v1/access-requests", f.jit, map[string]any{"chat": familyChat}); code >= 300 {
		t.Fatalf("access request: %d %s", code, out)
	}
	_, page, _ := c.get("/ui/chats")
	if !strings.Contains(page, `<span class="rail-badge" aria-hidden="true">1</span>`) || !strings.Contains(page, `aria-label="Approvals, 1 waiting"`) {
		t.Fatalf("the rail does not count the waiting request:\n%s", page)
	}
}

func TestTheDesignSystemFilesNeedNoSession(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	for path, kind := range map[string]string{
		"/ui/tokens.css":        "text/css; charset=utf-8",
		"/ui/components.css":    "text/css; charset=utf-8",
		"/ui/inter.woff2":       "font/woff2",
		"/ui/inter-license.txt": "text/plain; charset=utf-8",
	} {
		code, body, hd := c.get(path)
		if code != 200 || hd.Get("Content-Type") != kind || len(body) == 0 || hd.Get("Set-Cookie") != "" || hd.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d %q %d bytes", path, code, hd.Get("Content-Type"), len(body))
		}
	}
	_, tokens, _ := c.get("/ui/tokens.css")
	// The font URL is relative to /ui/tokens.css, so it resolves to the /ui/inter.woff2 served above.
	if !strings.Contains(tokens, `url("inter.woff2")`) || !strings.Contains(tokens, "--brand: #128254") {
		t.Fatal("tokens.css lost the bundled typeface or the brand colour")
	}
	if _, license, _ := c.get("/ui/inter-license.txt"); !strings.Contains(license, "SIL Open Font License") {
		t.Fatal("the typeface ships without its licence")
	}
}
