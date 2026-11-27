package desktop

import (
	"strings"
	"testing"
)

func TestExternalLinkOpensOnlyWebAndMailLinksFromWasyncPages(t *testing.T) {
	page := "http://127.0.0.1:8787/ui/setup"
	for _, tc := range []struct {
		message, from, want string
	}{
		{openLinkPrefix + "https://gehariharan.com/wasync", page, "https://gehariharan.com/wasync"},
		{openLinkPrefix + "http://example.com/a?b=c#d", "http://localhost:9000/ui/chats", "http://example.com/a?b=c#d"},
		{openLinkPrefix + "mailto:someone@example.com", "http://[::1]:8787/ui/", "mailto:someone@example.com"},
		// Not a request to open a link.
		{"https://gehariharan.com", page, ""},
		{"wails:runtime:ready", page, ""},
		// Not from one of wasync's pages on this computer.
		{openLinkPrefix + "https://gehariharan.com", "https://evil.example/", ""},
		{openLinkPrefix + "https://gehariharan.com", "http://192.168.1.4:8787/ui/", ""},
		{openLinkPrefix + "https://gehariharan.com", "https://127.0.0.1:8787/ui/", ""},
		{openLinkPrefix + "https://gehariharan.com", "", ""},
		{openLinkPrefix + "https://gehariharan.com", "wails://localhost/", ""},
		// Not a web or email address.
		{openLinkPrefix + "file:///etc/passwd", page, ""},
		{openLinkPrefix + "javascript:alert(1)", page, ""},
		{openLinkPrefix + "x-apple.systempreferences:com.apple.preference", page, ""},
		{openLinkPrefix + "ms-settings:startupapps", page, ""},
		{openLinkPrefix + "https:///no-host", page, ""},
		{openLinkPrefix + "https://user:pass@example.com/", page, ""},
		{openLinkPrefix + "mailto:", page, ""},
		{openLinkPrefix + "https://example.com/" + strings.Repeat("a", maxLink), page, ""},
	} {
		got, ok := ExternalLink(tc.message, tc.from)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("ExternalLink(%q, %q) = %q, %v; want %q", tc.message, tc.from, got, ok, tc.want)
		}
	}
}

func TestLinkScriptSendsTheMessageExternalLinkReads(t *testing.T) {
	if !strings.Contains(LinkScript, `"`+openLinkPrefix+`"`) {
		t.Fatal("the script and ExternalLink disagree on the message prefix")
	}
}
