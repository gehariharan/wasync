package api

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/opensend"
	"github.com/gehariharan/wasync/internal/passkey"
	"github.com/gehariharan/wasync/internal/passkey/passkeytest"
	"github.com/gehariharan/wasync/internal/sendlist"
	"github.com/gehariharan/wasync/internal/tailnet"
)

// These tests play the person at another device on the tailnet: a browser
// that reaches the owner through `tailscale serve` and holds a passkey
// (passkeytest), plus the person at the computer running wasync, who answers
// its system prompt (promptStub) and reads the one-time code off its screen.

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func attrOf(t *testing.T, page, name string) string {
	t.Helper()
	m := regexp.MustCompile(` ` + name + `="([^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no %s in page:\n%s", name, page)
	}
	return m[1]
}

func fieldOf(t *testing.T, page, name string) string {
	t.Helper()
	m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no field %s in page:\n%s", name, page)
	}
	return m[1]
}

func unb64Test(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newDeviceCode signs in on the computer running wasync, asks for a code for a
// new device and reads it off the page.
func newDeviceCode(t *testing.T, f accessFixture) string {
	t.Helper()
	local := newPageClient(t, f)
	local.get(linkPath(t, f))
	_, page, _ := local.get("/ui/settings")
	code, page, _ := local.post("/ui/setup/devices", url.Values{"csrf": {csrfOf(t, page)}, "action": {"add"}})
	m := regexp.MustCompile(`<strong>([A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4})</strong>`).FindStringSubmatch(page)
	if code != 200 || m == nil {
		t.Fatalf("no device code: %d\n%s", code, page)
	}
	return m[1]
}

// beginEnroll enters a code on the remote device's Set up page.
func beginEnroll(t *testing.T, c *tailnetClient, code string) (int, string, http.Header) {
	t.Helper()
	_, page, _ := c.get("/ui/settings")
	return c.post(passkeyBeginPath, url.Values{"csrf": {csrfOf(t, page)}, "code": {code}, "name": {"Laptop"}})
}

// enroll sets up a on the remote device c with a code from the computer running wasync.
func enroll(t *testing.T, f accessFixture, c *tailnetClient, a *passkeytest.Authenticator) {
	t.Helper()
	status, page, _ := beginEnroll(t, c, newDeviceCode(t, f))
	if status != 200 || attrOf(t, page, "data-mode") != "create" || attrOf(t, page, "data-rp") != tailnetHost {
		t.Fatalf("set-up page: %d\n%s", status, page)
	}
	reg := a.Register(tailnetHost, unb64Test(t, attrOf(t, page, "data-challenge")))
	_, _, hd := c.post(passkeyRegisterPath, url.Values{"csrf": {csrfOf(t, page)}, "ask": {fieldOf(t, page, "ask")}, "credential": {b64(reg.ID)}, "client": {b64(reg.ClientDataJSON)}, "attestation": {b64(reg.AttestationObject)}})
	if notice(hd) != "deviceon" || !strings.HasPrefix(hd.Get("Location"), "/ui/settings?") {
		t.Fatalf("registering: %q", hd.Get("Location"))
	}
}

// answer confirms the action on a passkey page with a.
func answer(t *testing.T, c *tailnetClient, a *passkeytest.Authenticator, page string) http.Header {
	t.Helper()
	assertion := a.Assert(tailnetHost, unb64Test(t, attrOf(t, page, "data-challenge")))
	_, _, hd := c.post(passkeyConfirmPath, assertionForm(page, assertion))
	return hd
}

func assertionForm(page string, a passkey.Assertion) url.Values {
	pick := func(name string) string {
		m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(page)
		if m == nil {
			return ""
		}
		return m[1]
	}
	return url.Values{"csrf": {pick("csrf")}, "ask": {pick("ask")}, "back": {pick("back")}, "credential": {b64(a.ID)}, "client": {b64(a.ClientDataJSON)}, "authdata": {b64(a.AuthenticatorData)}, "signature": {b64(a.Signature)}}
}

// remoteOwner is an owner with the tailnet opt-in on, a computer that can show
// the system prompt (answered yes) and a remote browser for the allowed login.
func remoteOwner(t *testing.T) (accessFixture, *promptStub, *tailnetClient) {
	t.Helper()
	stub := &promptStub{confirm: true}
	f := setupAccessWith(t, humancheck.NewGate(stub))
	allowTailnet(t, f)
	return f, stub, newTailnetClient(t, f, tailnetLogin)
}

func TestATailnetDeviceWithAPasskeyCanDoOwnerActions(t *testing.T) {
	f, stub, c := remoteOwner(t)
	device := passkeytest.New(passkey.AlgES256)
	enroll(t, f, c, device)
	if prompts := stub.seen(); len(prompts) != 1 || !strings.Contains(prompts[0], "one more device") || !strings.Contains(prompts[0], tailnetLogin) {
		t.Fatalf("adding the device asked: %q", prompts)
	}

	_, page, _ := c.get("/ui/agents")
	if !strings.Contains(page, "Turn on for 1 hour") || !strings.Contains(page, "Asks you to confirm with your passkey") || strings.Contains(page, "set it up with a passkey") {
		t.Fatalf("agents page with a passkey:\n%s", page)
	}
	csrf := csrfOf(t, page)

	// Open sending: the page asks for the passkey, showing the same sentence
	// the system prompt would, and nothing changes until it is answered.
	status, ask, hd := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"openon"}})
	if status != 200 || !strings.Contains(ask, "ANY chat or phone number") || attrOf(t, ask, "data-mode") != "get" || attrOf(t, ask, "data-allow") != b64(device.ID) {
		t.Fatalf("open sending from the tailnet: %d\n%s", status, ask)
	}
	if csp := hd.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("passkey page policy: %q", csp)
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); on {
		t.Fatal("open sending went on before the passkey answered")
	}
	if got := notice(answer(t, c, device, ask)); got != "openon" {
		t.Fatalf("confirmed open sending: %q", got)
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); !on {
		t.Fatal("open sending is still off")
	}

	// An agent's request to send a message.
	r := f.askSend(t, familyChat, "see you at 6")
	_, page, _ = c.get("/ui/approvals")
	if !strings.Contains(page, `value="approve"`) || !strings.Contains(page, "asks for your passkey on this device") {
		t.Fatalf("approvals page with a passkey:\n%s", page)
	}
	_, ask, _ = c.post("/ui/decide", url.Values{"csrf": {csrf}, "id": {r.ID}, "action": {"approve"}})
	if !strings.Contains(ask, "Family Group") || !strings.Contains(ask, "jit") || !strings.Contains(ask, "see you at 6") {
		t.Fatalf("approval page:\n%s", ask)
	}
	if got := notice(answer(t, c, device, ask)); got != "approved" {
		t.Fatalf("confirmed approval: %q", got)
	}
	if got := f.queue.Get(f.account, r.ID); got.Status == approval.StatusPending || len(*f.sent) != 1 {
		t.Fatalf("approved request: %+v", got)
	}

	// A list of numbers, with some of them in an uploaded-style text field.
	_, ask, _ = c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"liston"}, "agent": {"jit"}, "numbers": {"+15551230000\n+15551230001"}, "rate": {"5"}, "ends": {"7d"}})
	if !strings.Contains(ask, "these 2 number(s)") {
		t.Fatalf("list page:\n%s", ask)
	}
	if got := notice(answer(t, c, device, ask)); got != "liston" {
		t.Fatalf("confirmed list: %q", got)
	}
	lists, err := sendlist.Load(f.dir)
	if err != nil || len(lists) != 1 || len(lists[0].Numbers) != 2 {
		t.Fatalf("lists: %+v %v", lists, err)
	}

	// Only the device was confirmed on this computer; the rest came from the passkey.
	if len(stub.seen()) != 1 {
		t.Fatalf("remote actions raised the system prompt: %q", stub.seen())
	}
	devices, _ := tailnet.LoadDevices(f.dir)
	if len(devices) != 1 || devices[0].UsedAt == nil || devices[0].Name != "Laptop" {
		t.Fatalf("device record: %+v", devices)
	}
	log, _ := os.ReadFile(filepath.Join(f.dir, agentauth.AuditFileName))
	for _, event := range []string{"tailnet_device_code", "tailnet_device_add", "tailnet_passkey_confirm"} {
		if !strings.Contains(string(log), event) {
			t.Errorf("audit log lacks %s", event)
		}
	}
}

func TestATailnetDeviceWithoutAPasskeyCannotDoOwnerActions(t *testing.T) {
	f, stub, c := remoteOwner(t)
	// Another device has a passkey; this browser holds a key nobody set up.
	enroll(t, f, newTailnetClient(t, f, tailnetLogin), passkeytest.New(passkey.AlgES256))
	stranger := passkeytest.New(passkey.AlgES256)
	_, page, _ := c.get("/ui/agents")
	_, ask, _ := c.post("/ui/agents", url.Values{"csrf": {csrfOf(t, page)}, "action": {"openon"}})
	if got := notice(answer(t, c, stranger, ask)); got != "pkfail" {
		t.Fatalf("a passkey nobody set up: %q", got)
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); on {
		t.Fatal("an unknown passkey turned open sending on")
	}

	// With no device set up at all the page offers nothing to approve.
	if _, found, err := tailnet.RemoveDevice(f.dir, mustDevices(t, f)[0].ID); !found || err != nil {
		t.Fatal(found, err)
	}
	_, page, _ = c.get("/ui/agents")
	if !strings.Contains(page, "set it up with a passkey") || strings.Contains(page, "Turn on for 1 hour") {
		t.Fatalf("agents page without a passkey:\n%s", page)
	}
	csrf := csrfOf(t, page)
	for _, form := range []url.Values{{"action": {"openon"}}, {"action": {"allowsend"}, "agent": {"other"}}, {"action": {"liston"}, "agent": {"jit"}, "numbers": {"+15551230000"}, "rate": {"5"}, "ends": {"7d"}}} {
		form.Set("csrf", csrf)
		if _, _, hd := c.post("/ui/agents", form); notice(hd) != "remote" {
			t.Fatalf("%v without a passkey: %q", form, hd.Get("Location"))
		}
	}
	for path, form := range map[string]url.Values{"/ui/setup/agent": {"name": {"newagent"}}} {
		form.Set("csrf", csrf)
		if _, _, hd := c.post(path, form); notice(hd) != "remote" {
			t.Fatalf("%s without a passkey: %q", path, hd.Get("Location"))
		}
	}
	// Made-up answers to a page that was never shown are refused too.
	if _, _, hd := c.post(passkeyConfirmPath, assertionForm(`name="csrf" value="`+csrf+`"`, stranger.Assert(tailnetHost, []byte("made up")))); notice(hd) != "pkexpired" {
		t.Fatalf("an answer to nothing: %q", hd.Get("Location"))
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); on || f.client(t, "other").HasScope(agentauth.ScopeSend) {
		t.Fatal("something changed without a passkey")
	}
	if lists, _ := sendlist.Load(f.dir); len(lists) != 0 {
		t.Fatalf("lists: %+v", lists)
	}
	if len(stub.seen()) != 1 { // the one device set up above
		t.Fatalf("a remote page raised the system prompt: %q", stub.seen())
	}
}

func mustDevices(t *testing.T, f accessFixture) []tailnet.Device {
	t.Helper()
	devices, err := tailnet.LoadDevices(f.dir)
	if err != nil || len(devices) == 0 {
		t.Fatalf("devices: %v %v", devices, err)
	}
	return devices
}

func TestPasskeyAnswersAreOneTimeAndBoundToTheirPage(t *testing.T) {
	f, _, c := remoteOwner(t)
	device := passkeytest.New(passkey.AlgES256)
	enroll(t, f, c, device)
	_, page, _ := c.get("/ui/agents")
	csrf := csrfOf(t, page)
	start := func(c *tailnetClient) string {
		_, ask, _ := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"openon"}})
		return ask
	}
	isOn := func() bool { _, on := opensend.ActiveNow(f.dir, time.Now()); return on }

	// An answer signed for another page's challenge.
	first, second := start(c), start(c)
	wrong := device.Assert(tailnetHost, unb64Test(t, attrOf(t, first, "data-challenge")))
	if _, _, hd := c.post(passkeyConfirmPath, assertionForm(second, wrong)); notice(hd) != "pkfail" || isOn() {
		t.Fatalf("answer for another challenge: %q", hd.Get("Location"))
	}
	// That page was used up by the wrong answer.
	if got := notice(answer(t, c, device, second)); got != "pkexpired" || isOn() {
		t.Fatalf("a used page: %q", got)
	}

	// Another browser signed in as the same login cannot answer this one's page.
	other := newTailnetClient(t, f, tailnetLogin)
	_, otherPage, _ := other.get("/ui/agents")
	ask := start(c)
	form := assertionForm(ask, device.Assert(tailnetHost, unb64Test(t, attrOf(t, ask, "data-challenge"))))
	form.Set("csrf", csrfOf(t, otherPage))
	if _, _, hd := other.post(passkeyConfirmPath, form); notice(hd) != "pkexpired" || isOn() {
		t.Fatalf("another session's page: %q", hd.Get("Location"))
	}

	// The right answer works once; sending it again does nothing.
	ask = start(c)
	form = assertionForm(ask, device.Assert(tailnetHost, unb64Test(t, attrOf(t, ask, "data-challenge"))))
	if _, _, hd := c.post(passkeyConfirmPath, form); notice(hd) != "openon" || !isOn() {
		t.Fatalf("right answer: %q", hd.Get("Location"))
	}
	if err := opensend.Stop(f.dir); err != nil {
		t.Fatal(err)
	}
	if _, _, hd := c.post(passkeyConfirmPath, form); notice(hd) != "pkexpired" || isOn() {
		t.Fatalf("replayed answer: %q", hd.Get("Location"))
	}

	// If what was confirmed would now read differently, nothing happens: here
	// the agent was replaced by one with a chat list while the page waited.
	_, ask, _ = c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"allowsend"}, "agent": {"other"}})
	if !strings.Contains(ask, "the 0 chat(s) it can already reach") {
		t.Fatalf("allow sending page:\n%s", ask)
	}
	if _, err := agentauth.Revoke(f.dir, "other", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "other", Chats: []string{familyChat}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := notice(answer(t, c, device, ask)); got != "pkchanged" || f.client(t, "other").HasScope(agentauth.ScopeSend) {
		t.Fatalf("changed in between: %q", got)
	}

	// A device that is not verified (no fingerprint, face or PIN) is refused.
	device.Flags = 0x01
	if got := notice(answer(t, c, device, start(c))); got != "pkfail" || isOn() {
		t.Fatalf("not verified: %q", got)
	}
}

func TestOnlyTheComputerRunningWasyncHandsOutDeviceCodes(t *testing.T) {
	f, stub, c := remoteOwner(t)
	_, page, _ := c.get("/ui/settings")
	if !strings.Contains(page, `id="devices"`) || strings.Contains(page, "Add a device…") || !strings.Contains(page, "Set up this device…") {
		t.Fatalf("remote set-up page:\n%s", page)
	}
	if _, _, hd := c.post("/ui/setup/devices", url.Values{"csrf": {csrfOf(t, page)}, "action": {"add"}}); notice(hd) != "devicehere" {
		t.Fatalf("remote add: %q", hd.Get("Location"))
	}

	// On this computer the system prompt must be confirmed first.
	stub.set(false)
	local := newPageClient(t, f)
	local.get(linkPath(t, f))
	_, page, _ = local.get("/ui/settings")
	if !strings.Contains(page, "Add a device…") {
		t.Fatalf("local set-up page:\n%s", page)
	}
	if _, _, hd := local.post("/ui/setup/devices", url.Values{"csrf": {csrfOf(t, page)}, "action": {"add"}}); notice(hd) != "declined" {
		t.Fatalf("declined add: %q", hd.Get("Location"))
	}
	stub.set(true)

	// A code works once.
	code := newDeviceCode(t, f)
	device := passkeytest.New(passkey.AlgEdDSA)
	if status, _, _ := beginEnroll(t, c, strings.ToLower(strings.ReplaceAll(code, "-", " "))); status != 200 {
		t.Fatalf("code typed loosely: %d", status)
	}
	if _, _, hd := beginEnroll(t, c, code); notice(hd) != "badcode" {
		t.Fatalf("code used twice: %q", hd.Get("Location"))
	}

	// Five wrong codes in a row cancel every code.
	code = newDeviceCode(t, f)
	for range deviceCodeMisses {
		if _, _, hd := beginEnroll(t, c, "AAAA-BBBB-CCCC"); notice(hd) != "badcode" || !strings.HasPrefix(hd.Get("Location"), "/ui/settings?") {
			t.Fatalf("wrong code: %q", hd.Get("Location"))
		}
	}
	if _, _, hd := beginEnroll(t, c, code); notice(hd) != "badcode" {
		t.Fatalf("code after guessing: %q", hd.Get("Location"))
	}

	// A code belongs to the login it was made for.
	code = newDeviceCode(t, f)
	if err := tailnet.Save(f.dir, tailnet.Setting{Host: tailnetHost, Login: "new-owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	newcomer := newTailnetClient(t, f, "new-owner@example.com")
	if _, _, hd := beginEnroll(t, newcomer, code); notice(hd) != "badcode" {
		t.Fatalf("code for another login: %q", hd.Get("Location"))
	}
	allowTailnet(t, f)

	// A finished set-up page cannot be reused to add a second passkey.
	status, setupPage, _ := beginEnroll(t, c, newDeviceCode(t, f))
	if status != 200 {
		t.Fatal(status)
	}
	reg := device.Register(tailnetHost, unb64Test(t, attrOf(t, setupPage, "data-challenge")))
	form := url.Values{"csrf": {csrfOf(t, setupPage)}, "ask": {fieldOf(t, setupPage, "ask")}, "credential": {b64(reg.ID)}, "client": {b64(reg.ClientDataJSON)}, "attestation": {b64(reg.AttestationObject)}}
	if _, _, hd := c.post(passkeyRegisterPath, form); notice(hd) != "deviceon" {
		t.Fatalf("register: %q", hd.Get("Location"))
	}
	second := passkeytest.New(passkey.AlgES256)
	reg = second.Register(tailnetHost, unb64Test(t, attrOf(t, setupPage, "data-challenge")))
	form.Set("credential", b64(reg.ID))
	form.Set("client", b64(reg.ClientDataJSON))
	form.Set("attestation", b64(reg.AttestationObject))
	if _, _, hd := c.post(passkeyRegisterPath, form); notice(hd) != "pkexpired" {
		t.Fatalf("set-up page reused: %q", hd.Get("Location"))
	}
	if n := len(mustDevices(t, f)); n != 1 {
		t.Fatalf("devices: %d", n)
	}
}

func TestPasskeysBelongToTheTailnetLoginAndHost(t *testing.T) {
	f, _, c := remoteOwner(t)
	device := passkeytest.New(passkey.AlgRS256)
	enroll(t, f, c, device)

	// Requests that the tailnet check refuses never reach the passkey pages.
	cases := map[string]func(c *tailnetClient){
		"unknown host":   func(c *tailnetClient) { c.host = "other.example.ts.net" },
		"unknown login":  func(c *tailnetClient) { c.login = "someone@example.com" },
		"missing login":  func(c *tailnetClient) { c.login = "" },
		"through funnel": func(c *tailnetClient) { c.headers.Set(tailnet.FunnelHeader, "?1") },
	}
	_, page, _ := c.get("/ui/agents")
	_, ask, _ := c.post("/ui/agents", url.Values{"csrf": {csrfOf(t, page)}, "action": {"openon"}})
	form := assertionForm(ask, device.Assert(tailnetHost, unb64Test(t, attrOf(t, ask, "data-challenge"))))
	for name, change := range cases {
		bad := &tailnetClient{pageClient: c.pageClient, host: c.host, login: c.login, headers: http.Header{}}
		change(bad)
		for _, path := range []string{passkeyConfirmPath, passkeyBeginPath, passkeyRegisterPath, "/ui/setup/devices"} {
			if status, _, _ := bad.post(path, form); status != 403 {
				t.Fatalf("%s to %s: %d", name, path, status)
			}
		}
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); on {
		t.Fatal("a refused request turned open sending on")
	}

	// Allowing another login: the passkey no longer counts, for it or anyone.
	if err := tailnet.Save(f.dir, tailnet.Setting{Host: tailnetHost, Login: "new-owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	newcomer := newTailnetClient(t, f, "new-owner@example.com")
	_, page, _ = newcomer.get("/ui/agents")
	if !strings.Contains(page, "set it up with a passkey") {
		t.Fatalf("another login sees the old device:\n%s", page)
	}
	if _, _, hd := newcomer.post("/ui/agents", url.Values{"csrf": {csrfOf(t, page)}, "action": {"openon"}}); notice(hd) != "remote" {
		t.Fatalf("another login: %q", hd.Get("Location"))
	}
	// Moving to another host name is the same.
	if err := tailnet.Save(f.dir, tailnet.Setting{Host: "laptop.example.ts.net", Login: tailnetLogin}); err != nil {
		t.Fatal(err)
	}
	moved := newTailnetClient(t, f, tailnetLogin)
	moved.host = "laptop.example.ts.net"
	if _, page, _ = moved.get("/ui/agents"); !strings.Contains(page, "set it up with a passkey") {
		t.Fatalf("another host sees the old device:\n%s", page)
	}
}

func TestAgentsAndTheLocalAPICannotUsePasskeys(t *testing.T) {
	f, stub, c := remoteOwner(t)
	device := passkeytest.New(passkey.AlgES256)
	enroll(t, f, c, device)
	_, page, _ := c.get("/ui/agents")
	_, ask, _ := c.post("/ui/agents", url.Values{"csrf": {csrfOf(t, page)}, "action": {"openon"}})
	form := assertionForm(ask, device.Assert(tailnetHost, unb64Test(t, attrOf(t, ask, "data-challenge"))))
	sendToken, _ := loadNamedToken(f.dir, "send.token")

	// An agent credential, the send credential or the approval credential gets
	// nothing from these pages, on loopback or through the tailnet name.
	for name, auth := range map[string]func(*http.Request){
		"agent":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+f.jit) },
		"send":     func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+sendToken) },
		"approver": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+f.approver) },
	} {
		for _, path := range []string{passkeyConfirmPath, passkeyBeginPath, passkeyRegisterPath, "/ui/setup/devices"} {
			req, _ := http.NewRequest("POST", f.server.URL+path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			auth(req)
			if status, _, _ := (&pageClient{t: t, http: http.DefaultClient}).do(req); status != 401 {
				t.Fatalf("%s on loopback %s: %d", name, path, status)
			}
			req, _ = http.NewRequest("POST", f.server.URL+path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "https://"+tailnetHost)
			req.Host = tailnetHost
			auth(req)
			if status, _, _ := (&pageClient{t: t, http: http.DefaultClient}).do(req); status != 403 {
				t.Fatalf("%s through the tailnet name without a login %s: %d", name, path, status)
			}
		}
	}
	// Neither the API nor /api/v1 has a way in.
	for _, path := range []string{"/api/v1/devices", "/api/v1/passkeys", "/api/passkeys", "/api/tailnet"} {
		if status, _ := f.do(t, "POST", path, f.jit, nil); status != 404 && status != 401 && status != 403 {
			t.Fatalf("agent to %s: %d", path, status)
		}
		if status, _ := f.signed(t, "POST", path, f.approver, nil); status == 200 {
			t.Fatalf("approval credential to %s: %d", path, status)
		}
	}
	// A browser signed in on this computer has the system prompt instead; the
	// passkey pages are not there for it.
	local := newPageClient(t, f)
	local.get(linkPath(t, f))
	_, page, _ = local.get("/ui/agents")
	for _, path := range []string{passkeyConfirmPath, passkeyBeginPath, passkeyRegisterPath} {
		form.Set("csrf", csrfOf(t, page))
		if status, _, _ := local.post(path, form); status != 404 {
			t.Fatalf("local session %s: %d", path, status)
		}
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); on {
		t.Fatal("something other than the remote browser answered the passkey page")
	}
	if len(stub.seen()) != 1 {
		t.Fatalf("prompts: %q", stub.seen())
	}
	// The remote browser that opened the page still can.
	if _, _, hd := c.post(passkeyConfirmPath, assertionForm(ask, device.Assert(tailnetHost, unb64Test(t, attrOf(t, ask, "data-challenge"))))); notice(hd) != "openon" {
		t.Fatalf("the right browser: %q", hd.Get("Location"))
	}
}

func TestRemovingADeviceStopsItAtOnce(t *testing.T) {
	f, stub, c := remoteOwner(t)
	device := passkeytest.New(passkey.AlgES256)
	enroll(t, f, c, device)
	_, page, _ := c.get("/ui/agents")
	_, ask, _ := c.post("/ui/agents", url.Values{"csrf": {csrfOf(t, page)}, "action": {"openon"}})

	// Removing needs no confirmation, on this computer or the device itself.
	_, page, _ = c.get("/ui/settings")
	if !strings.Contains(page, "Laptop") {
		t.Fatalf("remote set-up page lists no device:\n%s", page)
	}
	if _, _, hd := c.post("/ui/setup/devices", url.Values{"csrf": {csrfOf(t, page)}, "action": {"remove"}, "id": {b64(device.ID)}}); notice(hd) != "deviceoff" {
		t.Fatalf("remove: %q", hd.Get("Location"))
	}
	if got := notice(answer(t, c, device, ask)); got != "remote" {
		t.Fatalf("a removed device answered: %q", got)
	}
	if _, on := opensend.ActiveNow(f.dir, time.Now()); on {
		t.Fatal("a removed device turned open sending on")
	}
	if len(stub.seen()) != 1 {
		t.Fatalf("prompts: %q", stub.seen())
	}
}

func TestThePasskeyScriptIsServedAndOnlyItsPagesMayRunIt(t *testing.T) {
	f, _, c := remoteOwner(t)
	code, body, hd := c.get("/ui/passkey.js")
	if code != 200 || !strings.HasPrefix(hd.Get("Content-Type"), "text/javascript") || !strings.Contains(body, "navigator.credentials") {
		t.Fatalf("script: %d %q", code, hd.Get("Content-Type"))
	}
	for _, path := range []string{"/ui/chats", "/ui/agents", "/ui/settings", "/ui/home", "/ui/setup/link", "/ui/approvals"} {
		_, page, hd := c.get(path)
		if strings.Contains(hd.Get("Content-Security-Policy"), "script-src") || strings.Contains(page, "<script") {
			t.Fatalf("%s may run script: %q", path, hd.Get("Content-Security-Policy"))
		}
	}
	_ = f
}
