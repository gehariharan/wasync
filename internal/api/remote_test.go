package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/tailnet"
)

const (
	tailnetHost  = "machine.example.ts.net"
	tailnetLogin = "owner@example.com"
)

// tailnetClient is a browser on another computer reaching the owner through
// `tailscale serve`: the request arrives on loopback with the tailnet name as
// Host and the Tailscale login header that serve adds.
type tailnetClient struct {
	*pageClient
	host, login string
	headers     http.Header
}

func newTailnetClient(t *testing.T, f accessFixture, login string) *tailnetClient {
	return &tailnetClient{pageClient: newPageClient(t, f), host: tailnetHost, login: login, headers: http.Header{}}
}

func (c *tailnetClient) prepare(req *http.Request) *http.Request {
	req.Host = c.host
	if c.login != "" {
		req.Header.Set(tailnet.LoginHeader, c.login)
	}
	for k, v := range c.headers {
		req.Header[k] = v
	}
	return req
}

func (c *tailnetClient) get(path string) (int, string, http.Header) {
	req, _ := http.NewRequest("GET", c.base+path, nil)
	return c.do(c.prepare(req))
}

func (c *tailnetClient) post(path string, form url.Values) (int, string, http.Header) {
	req, _ := http.NewRequest("POST", c.base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://"+c.host)
	return c.do(c.prepare(req))
}

func allowTailnet(t *testing.T, f accessFixture) {
	t.Helper()
	if err := tailnet.Save(f.dir, tailnet.Setting{Host: tailnetHost, Login: tailnetLogin}); err != nil {
		t.Fatal(err)
	}
}

func TestWithoutTheTailnetSettingOtherHostsAreRefusedAsBefore(t *testing.T) {
	f, c, _ := chatsFixture(t)
	remote := newTailnetClient(t, f, tailnetLogin)
	for _, path := range []string{"/ui/", "/ui/chats", "/api/status", "/api/v1/health"} {
		code, body, _ := remote.get(path)
		if code != 403 || strings.Contains(body, "Family Group") {
			t.Fatalf("%s through a tailnet name with no setting: %d %s", path, code, body)
		}
	}
	// The loopback page still works exactly as it did.
	if code, page, _ := c.get("/ui/chats"); code != 200 || !strings.Contains(page, "Family Group") {
		t.Fatalf("loopback page: %d", code)
	}
}

func TestAnAllowedTailnetLoginOpensTheWebApp(t *testing.T) {
	stub := &promptStub{confirm: true}
	f := setupAccessWith(t, humancheck.NewGate(stub))
	allowTailnet(t, f)
	c := newTailnetClient(t, f, tailnetLogin)
	code, page, hd := c.get("/ui/chats/pick")
	if code != 200 || !strings.Contains(page, "Family Group") {
		t.Fatalf("remote page: %d\n%s", code, page)
	}
	cookie := hd.Get("Set-Cookie")
	if !strings.Contains(cookie, "Secure") || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Strict") {
		t.Fatalf("remote session cookie: %q", cookie)
	}
	// The same session posts a form; the browser sends its tailnet origin.
	csrf := csrfOf(t, page)
	if code, _, hd := c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"clearall"}}); code != 303 || !strings.HasPrefix(hd.Get("Location"), "/ui/") {
		t.Fatalf("remote form: %d %q", code, hd.Get("Location"))
	}
	// A one-time link address on the tailnet name just goes to the app.
	if code, _, hd := c.get("/ui/enter?t=whatever"); code != 303 || hd.Get("Location") != "/ui/" {
		t.Fatalf("enter: %d %q", code, hd.Get("Location"))
	}
	// The default HTTPS port in Host is the same name.
	c.host = tailnetHost + ":443"
	if code, _, _ := c.get("/ui/chats"); code != 200 {
		t.Fatalf("with :443: %d", code)
	}
	if len(stub.seen()) != 0 {
		t.Fatalf("opening the page asked for confirmation: %v", stub.seen())
	}
}

func TestTailnetRequestsWithTheWrongHostOrLoginAreRefused(t *testing.T) {
	f := setupAccess(t)
	allowTailnet(t, f)
	cases := map[string]func(c *tailnetClient){
		"unknown host":       func(c *tailnetClient) { c.host = "other.example.ts.net" },
		"look-alike host":    func(c *tailnetClient) { c.host = tailnetHost + ".evil.example" },
		"missing login":      func(c *tailnetClient) { c.login = "" },
		"unknown login":      func(c *tailnetClient) { c.login = "someone@example.com" },
		"login case":         func(c *tailnetClient) { c.login = strings.ToUpper(tailnetLogin) },
		"two logins":         func(c *tailnetClient) { c.headers[tailnet.LoginHeader] = []string{tailnetLogin, "someone@example.com"} },
		"through funnel":     func(c *tailnetClient) { c.headers.Set(tailnet.FunnelHeader, "?1") },
		"wrong port in host": func(c *tailnetClient) { c.host = tailnetHost + ":8443" },
		"https port origin":  func(c *tailnetClient) { c.headers.Set("Origin", "https://"+tailnetHost+":8443") },
	}
	for name, change := range cases {
		c := newTailnetClient(t, f, tailnetLogin)
		change(c)
		code, body, hd := c.get("/ui/chats")
		if code != 403 || strings.Contains(body, "Family Group") || hd.Get("Set-Cookie") != "" {
			t.Fatalf("%s: %d %s", name, code, body)
		}
	}
}

func TestTheTailnetNameServesOnlyTheWebApp(t *testing.T) {
	f := setupAccess(t)
	allowTailnet(t, f)
	c := newTailnetClient(t, f, tailnetLogin)
	for _, path := range []string{"/health", "/api/status", "/api/owner", "/api/v1/health", "/api/v1/chats"} {
		req, _ := http.NewRequest("GET", c.base+path, nil)
		req.Header.Set("Authorization", "Bearer "+f.jit)
		if code, body, _ := c.do(c.prepare(req)); code != 403 {
			t.Fatalf("%s through the tailnet name: %d %s", path, code, body)
		}
	}
	// Agent credentials keep working on the loopback address.
	if code, _ := f.do(t, "GET", "/api/v1/chats", f.jit, nil); code != 200 {
		t.Fatalf("agent on loopback: %d", code)
	}
}

func TestTailnetFormsNeedTheirOwnOriginAndSession(t *testing.T) {
	f := setupAccess(t)
	allowTailnet(t, f)
	c := newTailnetClient(t, f, tailnetLogin)
	_, page, _ := c.get("/ui/chats/pick")
	csrf := csrfOf(t, page)
	for name, origin := range map[string]string{"plain http": "http://" + tailnetHost, "another site": "https://evil.example", "loopback": f.server.URL} {
		req, _ := http.NewRequest("POST", c.base+"/ui/chats/pick", strings.NewReader(url.Values{"csrf": {csrf}, "action": {"clearall"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		if code, _, _ := c.do(c.prepare(req)); code != 403 {
			t.Fatalf("%s origin: %d", name, code)
		}
	}
	// A post with no session is not signed in by the login header alone.
	fresh := newTailnetClient(t, f, tailnetLogin)
	if code, _, _ := fresh.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"clearall"}}); code != 401 {
		t.Fatalf("post without a session: %d", code)
	}
	// The remote session is bound to its login: the loopback address, or a
	// login allowed later in place of it, does not get it.
	local := &pageClient{t: t, base: f.server.URL, http: c.http}
	if code, _, _ := local.get("/ui/chats"); code != 401 {
		t.Fatalf("remote session reused on loopback: %d", code)
	}
	if err := tailnet.Save(f.dir, tailnet.Setting{Host: tailnetHost, Login: "new-owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	c.login = "new-owner@example.com"
	if code, _, _ := c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"clearall"}}); code != 401 {
		t.Fatalf("session reused by another login: %d", code)
	}
}

func TestTurningTheTailnetSettingOffStopsItAtOnce(t *testing.T) {
	f := setupAccess(t)
	allowTailnet(t, f)
	c := newTailnetClient(t, f, tailnetLogin)
	if code, _, _ := c.get("/ui/chats"); code != 200 {
		t.Fatalf("before: %d", code)
	}
	if err := tailnet.Remove(f.dir); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := c.get("/ui/chats"); code != 403 {
		t.Fatalf("after turning it off: %d", code)
	}
}

// The system prompt shows on the computer running wasync, which a person at
// another computer cannot answer, so remote pages never raise it. Without a
// passkey set up there, they cannot approve anything either.
func TestRemotePagesNeverRaiseTheSystemPrompt(t *testing.T) {
	stub := &promptStub{confirm: true}
	f := setupAccessWith(t, humancheck.NewGate(stub))
	allowTailnet(t, f)
	r := f.askSend(t, familyChat, "hello")
	c := newTailnetClient(t, f, tailnetLogin)
	code, page, _ := c.get("/ui/approvals")
	if code != 200 || !strings.Contains(page, "set it up with a passkey") || strings.Contains(page, `value="approve"`) {
		t.Fatalf("remote approvals page: %d\n%s", code, page)
	}
	csrf := csrfOf(t, page)
	_, _, hd := c.post("/ui/decide", url.Values{"csrf": {csrf}, "id": {r.ID}, "action": {"approve"}})
	if !strings.Contains(hd.Get("Location"), "n=remote") {
		t.Fatalf("remote approve: %q", hd.Get("Location"))
	}
	_, _, hd = c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"openon"}})
	if !strings.Contains(hd.Get("Location"), "n=remote") {
		t.Fatalf("remote open sending: %q", hd.Get("Location"))
	}
	if _, notice, _ := c.get("/ui/approvals?n=remote"); !strings.Contains(notice, "or do this on the computer running wasync.") {
		t.Fatalf("notice:\n%s", notice)
	}
	if len(stub.seen()) != 0 {
		t.Fatalf("a remote page raised the system prompt: %v", stub.seen())
	}
	if got := f.queue.Get(f.account, r.ID); got.Status != approval.StatusPending {
		t.Fatalf("request changed: %+v", got)
	}
	// Rejecting needs no prompt and still works remotely.
	_, _, hd = c.post("/ui/decide", url.Values{"csrf": {csrf}, "id": {r.ID}, "action": {"reject"}})
	if !strings.Contains(hd.Get("Location"), "n=rejected") {
		t.Fatalf("remote reject: %q", hd.Get("Location"))
	}
	if got := f.queue.Get(f.account, r.ID); got.Status == approval.StatusPending {
		t.Fatalf("request still pending after a remote reject: %+v", got)
	}
}
