package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/service"
)

func TestASessionInUseStaysSignedInUpToTheLimit(t *testing.T) {
	var s uiState
	cookie, _ := s.start("")
	req := httptest.NewRequest("GET", "/ui/chats", nil)
	req.AddCookie(&http.Cookie{Name: uiCookie, Value: cookie})
	// Pretend it was opened 25 minutes ago and last used then.
	s.mu.Lock()
	old := s.sessions[cookie]
	old.started, old.expires = time.Now().Add(-25*time.Minute), time.Now().Add(5*time.Minute)
	s.sessions[cookie] = old
	s.mu.Unlock()
	sess, got, ok := s.session(req)
	if !ok || got != cookie || time.Until(sess.expires) < uiSessionTTL-time.Minute {
		t.Fatalf("a session in use was not extended: %v %v", ok, time.Until(sess.expires))
	}
	// Near the limit it is extended only to the limit, and past it it is gone.
	s.mu.Lock()
	old = s.sessions[cookie]
	old.started = time.Now().Add(-uiSessionMax + 10*time.Minute)
	s.sessions[cookie] = old
	s.mu.Unlock()
	if sess, _, ok = s.session(req); !ok || time.Until(sess.expires) > 11*time.Minute {
		t.Fatalf("extended past the limit: %v", time.Until(sess.expires))
	}
	s.mu.Lock()
	old = s.sessions[cookie]
	old.expires = time.Now().Add(-time.Second)
	s.sessions[cookie] = old
	s.mu.Unlock()
	if _, _, ok = s.session(req); ok {
		t.Fatal("an expired session was used")
	}
}

func TestEveryPageRenewsTheSessionCookie(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	code, _, hd := c.get("/ui/chats")
	if code != 200 {
		t.Fatalf("chats: %d", code)
	}
	var renewed *http.Cookie
	for _, ck := range (&http.Response{Header: hd}).Cookies() {
		if ck.Name == uiCookie {
			renewed = ck
		}
	}
	if renewed == nil || renewed.MaxAge < int((uiSessionTTL-time.Minute)/time.Second) || !renewed.HttpOnly || renewed.SameSite != http.SameSiteStrictMode {
		t.Fatalf("the page did not renew the session cookie: %+v", renewed)
	}
}

func TestASignedOutPageSignsInAgainAfterTheSystemConfirmation(t *testing.T) {
	stub := &promptStub{}
	f := setupAccessWith(t, humancheck.NewGate(stub))
	c := newPageClient(t, f)
	code, page, _ := c.get("/ui/agents")
	if code != 401 || !strings.Contains(page, `action="/ui/signin"`) || !strings.Contains(page, `name="next" value="/ui/agents"`) || !strings.Contains(page, "wasync web") || strings.Contains(page, "wasync approvals</code>") {
		t.Fatalf("signed-out page: %d\n%s", code, page)
	}
	code, page, _ = c.post("/ui/signin", url.Values{"next": {"/ui/agents"}})
	if code != 401 || !strings.Contains(page, "Not confirmed") || len(stub.seen()) != 1 || !strings.Contains(stub.seen()[0], "sign in to the wasync web app") {
		t.Fatalf("a declined prompt: %d %q\n%s", code, stub.seen(), page)
	}
	if code, _, _ = c.get("/ui/agents"); code != 401 {
		t.Fatalf("a declined prompt signed the browser in: %d", code)
	}
	stub.set(true)
	code, _, hd := c.post("/ui/signin", url.Values{"next": {"/ui/agents"}})
	if code != 303 || hd.Get("Location") != "/ui/agents" {
		t.Fatalf("a confirmed prompt: %d %v", code, hd)
	}
	if code, _, _ = c.get("/ui/agents"); code != 200 {
		t.Fatalf("not signed in after confirming: %d", code)
	}
	// Only the app's own pages can be returned to.
	if _, _, hd = c.post("/ui/signin", url.Values{"next": {"//evil.example/"}}); hd.Get("Location") != "/ui/" {
		t.Fatalf("returned to %q", hd.Get("Location"))
	}
	// Another site cannot start a sign-in.
	req, _ := http.NewRequest("POST", c.base+"/ui/signin", strings.NewReader("next=%2Fui%2F"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	before := len(stub.seen())
	if code, _, _ = c.do(req); code != 403 || len(stub.seen()) != before {
		t.Fatalf("a foreign origin reached the prompt: %d", code)
	}
}

func TestASignedOutPageWithoutAConfirmationPointsToTheTerminal(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	code, page, _ := c.get("/ui/chats")
	if code != 401 || strings.Contains(page, `action="/ui/signin"`) || !strings.Contains(page, "wasync web") {
		t.Fatalf("signed-out page without a prompt: %d\n%s", code, page)
	}
	if code, _, _ = c.post("/ui/signin", url.Values{"next": {"/ui/chats"}}); code != 401 {
		t.Fatalf("signed in without a confirmation: %d", code)
	}
	if code, _, _ = c.get("/ui/chats"); code != 401 {
		t.Fatalf("signed in without a confirmation: %d", code)
	}
}

func TestEveryPageWarnsWhileWasyncIsNotInTheBackground(t *testing.T) {
	for _, tc := range []struct {
		startedBy  string
		installed  bool
		want, deny string
	}{
		{"manually", false, "stops when its terminal closes", ""},
		{"manually", true, "wasync service restart", ""},
		{"login item", true, "", "terminal closes"},
		{"", true, "", "terminal closes"}, // cannot tell how it started
		{"", false, "stops when its terminal closes", ""},
	} {
		item := &fakeLoginItem{installed: tc.installed}
		f := setupAccessOwner(t, nil, func(o *Owner) {
			o.Service, o.ServiceSpec, o.Info.StartedBy = item, service.Spec{Exe: "/opt/wasync", DataDir: "/data/wasync"}, tc.startedBy
		})
		c := newPageClient(t, f)
		c.get(linkPath(t, f))
		for _, path := range []string{"/ui/approvals", "/ui/chats", "/ui/sent", "/ui/agents", "/ui/setup"} {
			_, page, _ := c.get(path)
			if tc.want != "" && !strings.Contains(page, tc.want) || tc.deny != "" && strings.Contains(page, tc.deny) {
				t.Fatalf("%+v %s:\n%s", tc, path, page)
			}
		}
	}
	// Without a login item on this computer there is nothing to offer.
	f := setupAccessOwner(t, nil, func(o *Owner) { o.Info.StartedBy = "manually" })
	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	if _, page, _ := c.get("/ui/chats"); strings.Contains(page, "terminal closes") {
		t.Fatalf("warned with no login item to offer:\n%s", page)
	}
}
