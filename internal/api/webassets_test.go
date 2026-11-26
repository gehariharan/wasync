package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTheWebAppOpensOnChatsAndApprovalsKeepTheirPage(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	// wasync approvals asks for no page; its link still opens the approval page.
	if code, _, hd := c.get(linkPath(t, f)); code != 303 || hd.Get("Location") != "/ui/approvals" {
		t.Fatalf("approvals link: %d %q", code, hd.Get("Location"))
	}
	for _, path := range []string{"/ui/", "/ui"} {
		if code, _, hd := c.get(path); code != 303 || hd.Get("Location") != "/ui/chats" {
			t.Fatalf("%s: %d %q", path, code, hd.Get("Location"))
		}
	}
	code, page, _ := c.get("/ui/approvals")
	if code != 200 || !strings.Contains(page, "<h1>wasync approvals</h1>") {
		t.Fatalf("approvals page: %d\n%s", code, page)
	}
	if !strings.Contains(page, `<a href="/ui/approvals">Approvals</a>`) {
		t.Fatalf("nav does not reach approvals:\n%s", page)
	}
	// Signed out, the bare address still lands on Chats, which then signs in.
	if code, _, hd := newPageClient(t, f).get("/ui/"); code != 303 || hd.Get("Location") != "/ui/chats" {
		t.Fatalf("signed out: %d %q", code, hd.Get("Location"))
	}
}

func TestEveryPageLinksTheManifestAndIcons(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	_, signedOut, _ := c.get("/ui/chats")
	c.get(linkPath(t, f))
	for _, path := range []string{"/ui/approvals", "/ui/chats", "/ui/sent", "/ui/agents", "/ui/setup"} {
		_, page, hd := c.get(path)
		if path == "/ui/chats" {
			page += signedOut
		}
		for _, want := range []string{`<link rel="manifest" href="/ui/manifest.webmanifest">`, `<link rel="icon" href="/ui/icon.svg"`, `<link rel="apple-touch-icon" href="/ui/apple-touch-icon.png">`} {
			if !strings.Contains(page, want) {
				t.Fatalf("%s lacks %s", path, want)
			}
		}
		if !strings.Contains(hd.Get("Content-Security-Policy"), "manifest-src 'self'") {
			t.Fatalf("%s: the content policy blocks the manifest: %q", path, hd.Get("Content-Security-Policy"))
		}
	}
}

func TestTheManifestAndIconsNeedNoSession(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	for path, kind := range map[string]string{
		"/ui/manifest.webmanifest": "application/manifest+json",
		"/ui/icon.svg":             "image/svg+xml",
		"/ui/icon-192.png":         "image/png",
		"/ui/icon-512.png":         "image/png",
		"/ui/apple-touch-icon.png": "image/png",
		"/ui/favicon.ico":          "image/x-icon",
		"/favicon.ico":             "image/x-icon",
		"/apple-touch-icon.png":    "image/png",
	} {
		code, body, hd := c.get(path)
		if code != 200 || hd.Get("Content-Type") != kind || len(body) == 0 || hd.Get("Set-Cookie") != "" {
			t.Fatalf("%s: %d %q %d bytes", path, code, hd.Get("Content-Type"), len(body))
		}
	}
	_, body, _ := c.get("/ui/manifest.webmanifest")
	var m struct {
		Name, Scope, Display string
		ShortName            string `json:"short_name"`
		StartURL             string `json:"start_url"`
		Icons                []struct{ Src, Sizes string }
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "wasync" || m.ShortName != "wasync" || m.StartURL != "/ui/chats" || m.Scope != "/ui/" || m.Display != "standalone" {
		t.Fatalf("manifest: %+v", m)
	}
	sizes := map[string]bool{}
	for _, i := range m.Icons {
		sizes[i.Sizes] = true
		if code, _, _ := c.get(i.Src); code != 200 {
			t.Fatalf("manifest icon %s: %d", i.Src, code)
		}
	}
	if !sizes["192x192"] || !sizes["512x512"] {
		t.Fatalf("manifest icons: %+v", m.Icons)
	}
	req, _ := http.NewRequest("POST", c.base+"/ui/manifest.webmanifest", nil)
	if code, _, _ := c.do(req); code != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d", code)
	}
}

func TestTheTailnetNameServesTheIconsButNotTheAPI(t *testing.T) {
	f := setupAccess(t)
	allowTailnet(t, f)
	c := newTailnetClient(t, f, tailnetLogin)
	for _, path := range []string{"/ui/manifest.webmanifest", "/ui/icon-512.png", "/favicon.ico", "/apple-touch-icon.png"} {
		if code, _, _ := c.get(path); code != 200 {
			t.Fatalf("%s through the tailnet: %d", path, code)
		}
	}
	for _, path := range []string{"/api/status", "/api/v1/health", "/icon.svg", "/"} {
		if code, body, _ := c.get(path); code != 403 || !strings.Contains(body, "only the web app") {
			t.Fatalf("%s through the tailnet: %d %s", path, code, body)
		}
	}
	// Another login gets nothing, icons included.
	if code, _, _ := newTailnetClient(t, f, "someone@example.com").get("/favicon.ico"); code != 403 {
		t.Fatalf("unknown login: %d", code)
	}
}
