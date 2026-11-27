package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/passkey"
	"github.com/gehariharan/wasync/internal/tailnet"
)

// Owner actions from another device on the tailnet.
//
// The tailnet login proves which Tailscale user a request came from, but a
// program on this computer can send the same header to the loopback port (see
// remote.go), so it is never enough to approve anything. On this computer the
// operating system's prompt is the proof that a person is there; on another
// device it is a passkey that device holds, unlocked with Touch ID, Face ID,
// Windows Hello or the device's PIN.
//
// Setting a device up needs the computer running wasync: there, after the
// system prompt, the Set up page shows a one-time code; typed on the other
// device, it lets that browser register a passkey for the tailnet name. Only
// the public key is kept (tailnet.Device).
//
// Every owner action that would raise the system prompt on this computer
// instead shows the remote browser a page with the same sentence and a
// "Confirm with passkey" button. The action's form is kept here, not in the
// page, with a one-time challenge bound to that browser session. The signed
// answer comes back to /ui/passkey/confirm, which runs the same form again;
// the action goes through only when the passkey's signature checks out and
// the sentence it builds is the one the person confirmed.

const (
	passkeyConfirmPath  = "/ui/passkey/confirm"
	passkeyBeginPath    = "/ui/passkey/begin"
	passkeyRegisterPath = "/ui/passkey/register"
	// passkeyTTL is how long a page that asks for the passkey works.
	passkeyTTL = 5 * time.Minute
	// deviceCodeTTL is how long a code for setting up a device works.
	deviceCodeTTL = 10 * time.Minute
	// deviceCodeMisses wrong codes in a row cancel every code.
	deviceCodeMisses = 5
	maxPasskeyAsks   = 8
	maxDeviceCodes   = 4
	// passkeyName is what a remote page says the person confirms with.
	passkeyName = "your passkey"
	// deviceCodeAlphabet has no 0/O or 1/I, so a code read aloud is not misread.
	deviceCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// passkeyAsk is an action waiting for the person's passkey.
type passkeyAsk struct {
	challenge []byte
	prompt    string
	asked     time.Time // when the person first sent the form
	login     string
	session   string // the cookie of the browser session that started it
	path      string
	form      url.Values
	expires   time.Time
}

// passkeyEnrollment is a device setting up its passkey after a valid code.
type passkeyEnrollment struct {
	challenge   []byte
	login, host string
	session     string
	name        string
	expires     time.Time
}

type deviceCode struct {
	hash        [32]byte
	login, host string
	expires     time.Time
}

// passkeyState lives in memory only, like sign-in links: restarting the
// owner cancels every code and every page waiting for a passkey.
type passkeyState struct {
	mu      sync.Mutex
	asks    map[string]passkeyAsk
	enrolls map[string]passkeyEnrollment
	codes   []deviceCode
	misses  int
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("random source unavailable: " + err.Error())
	}
	return b
}

func (s *passkeyState) prune(now time.Time) {
	for k, v := range s.asks {
		if now.After(v.expires) {
			delete(s.asks, k)
		}
	}
	for k, v := range s.enrolls {
		if now.After(v.expires) {
			delete(s.enrolls, k)
		}
	}
	kept := s.codes[:0]
	for _, c := range s.codes {
		if !now.After(c.expires) {
			kept = append(kept, c)
		}
	}
	s.codes = kept
}

// dropOldest makes room in a map of pending entries, oldest first.
func dropOldest[V any](m map[string]V, expires func(V) time.Time, limit int) {
	for len(m) >= limit {
		var oldest string
		for k, v := range m {
			if oldest == "" || expires(v).Before(expires(m[oldest])) {
				oldest = k
			}
		}
		delete(m, oldest)
	}
}

func (s *passkeyState) addAsk(a passkeyAsk) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	if s.asks == nil {
		s.asks = map[string]passkeyAsk{}
	}
	dropOldest(s.asks, func(a passkeyAsk) time.Time { return a.expires }, maxPasskeyAsks)
	id := token()
	a.expires = now.Add(passkeyTTL)
	s.asks[id] = a
	return id
}

// takeAsk removes and returns the waiting action id, if it belongs to this
// browser session and login. Each one is answered once, right or wrong.
func (s *passkeyState) takeAsk(id, session, login string) (passkeyAsk, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	a, ok := s.asks[id]
	if !ok || a.session != session || a.login != login {
		return passkeyAsk{}, false
	}
	delete(s.asks, id)
	return a, true
}

// newCode hands out a code that lets one device on the tailnet set up a passkey for s.
func (s *passkeyState) newCode(t tailnet.Setting) string {
	raw := randomBytes(12)
	var code strings.Builder
	for i, b := range raw {
		if i > 0 && i%4 == 0 {
			code.WriteByte('-')
		}
		code.WriteByte(deviceCodeAlphabet[int(b)%len(deviceCodeAlphabet)])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	if len(s.codes) >= maxDeviceCodes {
		s.codes = s.codes[1:]
	}
	s.codes = append(s.codes, deviceCode{hash: codeHash(code.String()), login: t.Login, host: t.Host, expires: now.Add(deviceCodeTTL)})
	s.misses = 0
	return code.String()
}

func codeHash(code string) [32]byte {
	code = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r == '-' || r == ' ':
			return -1
		}
		return r
	}, code)
	return sha256.Sum256([]byte(code))
}

// useCode spends a code for s. Wrong codes count, and enough of them in a row
// cancel every code, so guessing one is hopeless.
func (s *passkeyState) useCode(code string, t tailnet.Setting) bool {
	h := codeHash(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	for i, c := range s.codes {
		if subtle.ConstantTimeCompare(c.hash[:], h[:]) == 1 && c.login == t.Login && c.host == t.Host {
			s.codes = append(s.codes[:i], s.codes[i+1:]...)
			s.misses = 0
			return true
		}
	}
	if s.misses++; s.misses >= deviceCodeMisses {
		s.codes, s.misses = nil, 0
	}
	return false
}

func (s *passkeyState) addEnrollment(e passkeyEnrollment) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	if s.enrolls == nil {
		s.enrolls = map[string]passkeyEnrollment{}
	}
	dropOldest(s.enrolls, func(e passkeyEnrollment) time.Time { return e.expires }, maxPasskeyAsks)
	id := token()
	e.expires = now.Add(passkeyTTL)
	s.enrolls[id] = e
	return id
}

func (s *passkeyState) takeEnrollment(id, session, login string) (passkeyEnrollment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	e, ok := s.enrolls[id]
	if !ok || e.session != session || e.login != login {
		return passkeyEnrollment{}, false
	}
	delete(s.enrolls, id)
	return e, true
}

// remoteAsk is what a remote request needs to ask for, or check, a passkey.
type remoteAsk struct {
	req     *http.Request
	session string
	csrf    string
	started time.Time
	// answer is the person's passkey answer when this request runs an action
	// again after it was confirmed.
	answer *passkeyReply
	used   bool
}

type passkeyReply struct {
	ask       passkeyAsk
	assertion passkey.Assertion
}

type remoteAskKey struct{}

func (h *handler) withRemoteAsk(r *http.Request, cookie string, sess uiSession) *http.Request {
	ask := &remoteAsk{session: cookie, csrf: sess.csrf, started: time.Now()}
	r = r.WithContext(context.WithValue(r.Context(), remoteAskKey{}, ask))
	ask.req = r
	return r
}

func remoteAskFrom(ctx context.Context) *remoteAsk {
	a, _ := ctx.Value(remoteAskKey{}).(*remoteAsk)
	return a
}

// askedAt is when the person asked for the action r carries out. When a
// passkey confirmed it, that is the time of the first request, so a sentence
// that names a time (a list's end) reads the same both times.
func askedAt(r *http.Request) time.Time {
	ask := remoteAskFrom(r.Context())
	switch {
	case ask == nil:
		return time.Now()
	case ask.answer != nil:
		return ask.answer.ask.asked
	}
	return ask.started
}

// remoteDevices is the tailnet setting and the devices set up for it.
func (h *handler) remoteDevices() (tailnet.Setting, []tailnet.Device) {
	s, on, err := tailnet.Load(h.dataDir)
	if err != nil || !on {
		return tailnet.Setting{}, nil
	}
	all, err := tailnet.LoadDevices(h.dataDir)
	if err != nil {
		return s, nil
	}
	return s, s.For(all)
}

// confirmName says what confirms an owner action for this request: the
// system prompt on this computer, the person's passkey on another device that
// has one set up, or "" when neither can.
func (h *handler) confirmName(r *http.Request) string {
	if remoteLogin(r) != "" {
		if _, devices := h.remoteDevices(); len(devices) > 0 {
			return passkeyName
		}
		return ""
	}
	if h.human.Enabled() {
		return h.human.Name()
	}
	return ""
}

func (h *handler) canConfirm(r *http.Request) bool { return h.confirmName(r) != "" }

// passkeyNeeded is the first answer to an owner action on another device: the
// person has to confirm it with their passkey.
type passkeyNeeded struct {
	id, prompt string
	challenge  []byte
	devices    []tailnet.Device
	host       string
}

func (passkeyNeeded) Error() string { return "confirm with your passkey" }

// confirmRemote is confirmError for a request from another device.
func (h *handler) confirmRemote(ctx context.Context, login, prompt string) error {
	s, devices := h.remoteDevices()
	ask := remoteAskFrom(ctx)
	if len(devices) == 0 || ask == nil || s.Login != login {
		return statusError{code: http.StatusForbidden, msg: "set up this device with a passkey, or confirm on the computer running wasync; nothing changed", notice: "remote"}
	}
	if ask.answer == nil {
		challenge := randomBytes(passkey.ChallengeSize)
		form := url.Values{}
		for k, v := range ask.req.PostForm {
			if k != "csrf" {
				form[k] = append([]string(nil), v...)
			}
		}
		id := h.passkeys.addAsk(passkeyAsk{challenge: challenge, prompt: prompt, asked: ask.started, login: login, session: ask.session, path: ask.req.URL.Path, form: form})
		return passkeyNeeded{id: id, prompt: prompt, challenge: challenge, devices: devices, host: s.Host}
	}
	if ask.used {
		return statusError{code: http.StatusForbidden, msg: "one passkey answer confirms one action; nothing changed", notice: "pkfail"}
	}
	ask.used = true
	if ask.answer.ask.prompt != prompt {
		return statusError{code: http.StatusConflict, msg: "what you confirmed has changed since; nothing changed", notice: "pkchanged"}
	}
	for _, d := range devices {
		key, err := d.Key()
		if err != nil || subtle.ConstantTimeCompare(key.ID, ask.answer.assertion.ID) != 1 {
			continue
		}
		count, err := passkey.VerifyAssertion(s.Host, ask.answer.ask.challenge, key, ask.answer.assertion)
		if err != nil {
			break
		}
		_ = tailnet.UsedDevice(h.dataDir, d.ID, count, time.Now())
		_ = agentauth.NewAuditLog(h.dataDir).Append(agentauth.AuditEntry{Event: "tailnet_passkey_confirm", ClientName: d.Name, Outcome: agentauth.OutcomeAllowed})
		return nil
	}
	_ = agentauth.NewAuditLog(h.dataDir).Append(agentauth.AuditEntry{Event: "tailnet_passkey_confirm", Outcome: agentauth.OutcomeDenied})
	return statusError{code: http.StatusForbidden, msg: "not confirmed with a passkey set up for this tailnet; nothing changed", notice: "pkfail"}
}

// uiRefused answers an owner action that was not confirmed. On another device
// with a passkey, the first answer is the page that asks for it.
func (h *handler) uiRefused(w http.ResponseWriter, r *http.Request, err error, back func(string)) {
	var need passkeyNeeded
	if errors.As(err, &need) {
		h.passkeyPage(w, r, need)
		return
	}
	back(noticeFor(err))
}

// passkeyBack is the page an action returns to.
func passkeyBack(path string) string {
	switch {
	case strings.HasPrefix(path, "/ui/agents"):
		return "/ui/agents"
	case strings.HasPrefix(path, "/ui/setup"), strings.HasPrefix(path, "/ui/settings"):
		return "/ui/settings"
	}
	return uiApprovalsPath
}

type passkeyData struct {
	uiData
	Prompt, Back string
	// Mode is "get" to confirm an action, "create" to set up a device.
	Mode            string
	RPID, Challenge string
	// Allow lists the passkeys that may answer (or, for create, that this
	// device must not already hold), as base64url IDs separated by commas.
	Allow                   string
	User, Login, DeviceName string
	Ask                     string
	DeviceCount             int
	ExpiresMinutes          int
}

func credentialIDs(devices []tailnet.Device) string {
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	return strings.Join(ids, ",")
}

// passkeyPage asks the person to confirm the action with their passkey. It
// shows the same sentence the system prompt would.
func (h *handler) passkeyPage(w http.ResponseWriter, r *http.Request, need passkeyNeeded) {
	ask := remoteAskFrom(r.Context())
	d := &passkeyData{uiData: uiData{Title: "Confirm with your passkey", Remote: true, Script: true}, Mode: "get"}
	if ask != nil {
		d.CSRF = ask.csrf
	}
	d.Prompt = humancheck.Clean(need.prompt, 400)
	d.Back = passkeyBack(r.URL.Path)
	d.RPID, d.Challenge, d.Allow, d.Ask = need.host, base64.RawURLEncoding.EncodeToString(need.challenge), credentialIDs(need.devices), need.id
	d.ExpiresMinutes = int(passkeyTTL / time.Minute)
	h.passkeyRender(w, r, d)
}

func (h *handler) passkeyRender(w http.ResponseWriter, r *http.Request, d *passkeyData) {
	// These two pages run one script of wasync's own, served from this origin,
	// because a passkey can only be used from script.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; manifest-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.webRender(w, r, passkeyTemplate, d)
}

// passkeyAnswer takes the person's passkey answer for an action started in
// this browser and runs that action again, from the form kept here, with it.
func (h *handler) passkeyAnswer(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiFormLimit(w, r, sess, 64<<10) {
		return
	}
	back := passkeyBack(r.PostForm.Get("back"))
	ask := remoteAskFrom(r.Context())
	pending, ok := h.passkeys.takeAsk(r.PostForm.Get("ask"), ask.session, remoteLogin(r))
	if !ok {
		http.Redirect(w, r, back+"?n=pkexpired", http.StatusSeeOther)
		return
	}
	assertion, ok := assertionFrom(r.PostForm)
	if !ok {
		http.Redirect(w, r, passkeyBack(pending.path)+"?n=pkfail", http.StatusSeeOther)
		return
	}
	ask.answer = &passkeyReply{ask: pending, assertion: assertion}
	replay := r.Clone(r.Context())
	replay.URL.Path, replay.URL.RawQuery = pending.path, ""
	form := url.Values{}
	for k, v := range pending.form {
		form[k] = append([]string(nil), v...)
	}
	form.Set("csrf", sess.csrf)
	replay.PostForm, replay.Form, replay.MultipartForm = form, form, nil
	replay.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replay.Body = http.NoBody
	ask.req = replay
	h.uiDispatch(w, replay, sess)
}

func unb64(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

func assertionFrom(form url.Values) (passkey.Assertion, bool) {
	a := passkey.Assertion{ID: unb64(form.Get("credential")), ClientDataJSON: unb64(form.Get("client")), AuthenticatorData: unb64(form.Get("authdata")), Signature: unb64(form.Get("signature"))}
	return a, len(a.ID) > 0 && len(a.ClientDataJSON) > 0 && len(a.AuthenticatorData) > 0 && len(a.Signature) > 0
}

// setupDevices adds a device (on this computer only, after the system prompt)
// or removes one (from anywhere: removing is always safe).
func (h *handler) setupDevices(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	s, on, err := tailnet.Load(h.dataDir)
	if err != nil || !on {
		devicesBack(w, r, "notailnet")
		return
	}
	audit := func(event, name string) {
		_ = agentauth.NewAuditLog(h.dataDir).Append(agentauth.AuditEntry{Event: event, ClientName: name, Outcome: agentauth.OutcomeAllowed})
	}
	switch r.PostForm.Get("action") {
	case "add":
		if remoteLogin(r) != "" {
			// Another device never adds one: that needs the computer running wasync.
			devicesBack(w, r, "devicehere")
			return
		}
		if !h.human.Enabled() {
			devicesBack(w, r, "nogate")
			return
		}
		prompt := "Approve: let one more device on your tailnet, signed in to Tailscale as " + s.Login + ", approve agents' requests and give agents more power at https://" + s.Host + "/ui/, each time after you confirm with a passkey on that device"
		if err := h.confirmError(r.Context(), prompt); err != nil {
			devicesBack(w, r, noticeFor(err))
			return
		}
		code := h.passkeys.newCode(s)
		audit("tailnet_device_code", "")
		h.settingsView(w, r, sess, func(d *settingsData) { d.DeviceCode = code })
	case "remove":
		removed, found, err := tailnet.RemoveDevice(h.dataDir, r.PostForm.Get("id"))
		if err != nil || !found {
			devicesBack(w, r, "failed")
			return
		}
		audit("tailnet_device_remove", removed.Name)
		devicesBack(w, r, "deviceoff")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// passkeyBegin checks the code from the computer running wasync and lets this
// browser make a passkey for the tailnet name.
func (h *handler) passkeyBegin(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	s, on, err := tailnet.Load(h.dataDir)
	if err != nil || !on || s.Login != remoteLogin(r) {
		devicesBack(w, r, "failed")
		return
	}
	all, err := tailnet.LoadDevices(h.dataDir)
	switch {
	case err != nil:
		devicesBack(w, r, "failed")
		return
	case len(all) >= tailnet.MaxDevices:
		devicesBack(w, r, "toomany") // before the code is spent
		return
	case !h.passkeys.useCode(r.PostForm.Get("code"), s):
		devicesBack(w, r, "badcode")
		return
	}
	devices := s.For(all)
	name := humancheck.Clean(r.PostForm.Get("name"), tailnet.MaxDeviceName)
	if name == "" {
		name = "Device added " + time.Now().Format("2 Jan 2006")
	}
	challenge := randomBytes(passkey.ChallengeSize)
	ask := remoteAskFrom(r.Context())
	id := h.passkeys.addEnrollment(passkeyEnrollment{challenge: challenge, login: s.Login, host: s.Host, session: ask.session, name: name})
	// The user handle stays the same for this host and login, so setting the
	// same device up again replaces its old passkey there.
	user := sha256.Sum256([]byte("wasync tailnet\x00" + s.Host + "\x00" + s.Login))
	d := &passkeyData{uiData: uiData{CSRF: sess.csrf, Title: "Set up this device", Remote: true, Script: true}, Mode: "create"}
	d.RPID, d.Challenge, d.Allow, d.Ask = s.Host, base64.RawURLEncoding.EncodeToString(challenge), credentialIDs(devices), id
	d.User, d.Login, d.DeviceName, d.DeviceCount = base64.RawURLEncoding.EncodeToString(user[:16]), s.Login, name, len(devices)
	d.Back, d.ExpiresMinutes = "/ui/settings", int(passkeyTTL/time.Minute)
	h.passkeyRender(w, r, d)
}

// passkeyRegister keeps the passkey this browser made, once it checks out.
func (h *handler) passkeyRegister(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiFormLimit(w, r, sess, 64<<10) {
		return
	}
	ask := remoteAskFrom(r.Context())
	e, ok := h.passkeys.takeEnrollment(r.PostForm.Get("ask"), ask.session, remoteLogin(r))
	s, on, err := tailnet.Load(h.dataDir)
	if !ok || err != nil || !on || s.Host != e.host || s.Login != e.login {
		devicesBack(w, r, "pkexpired")
		return
	}
	reg := passkey.Registration{ID: unb64(r.PostForm.Get("credential")), ClientDataJSON: unb64(r.PostForm.Get("client")), AttestationObject: unb64(r.PostForm.Get("attestation"))}
	key, err := passkey.VerifyRegistration(s.Host, e.challenge, reg)
	if err != nil {
		_ = agentauth.NewAuditLog(h.dataDir).Append(agentauth.AuditEntry{Event: "tailnet_device_add", ClientName: e.name, Outcome: agentauth.OutcomeDenied})
		devicesBack(w, r, "pkfail")
		return
	}
	device := tailnet.Device{ID: base64.RawURLEncoding.EncodeToString(key.ID), PublicKey: key.PublicKey, Alg: key.Alg, SignCount: key.SignCount, Host: s.Host, Login: s.Login, Name: e.name, AddedAt: time.Now().UTC().Truncate(time.Second)}
	switch err := tailnet.AddDevice(h.dataDir, device); {
	case errors.Is(err, tailnet.ErrTooManyDevices):
		devicesBack(w, r, "toomany")
		return
	case err != nil:
		devicesBack(w, r, "failed")
		return
	}
	_ = agentauth.NewAuditLog(h.dataDir).Append(agentauth.AuditEntry{Event: "tailnet_device_add", ClientName: e.name, Outcome: agentauth.OutcomeAllowed})
	devicesBack(w, r, "deviceon")
}

// devicesBack returns to the devices on the Set up page with a notice.
func devicesBack(w http.ResponseWriter, r *http.Request, notice string) {
	http.Redirect(w, r, "/ui/settings?n="+url.QueryEscape(notice)+"#devices", http.StatusSeeOther)
}

// deviceRow is one device in Settings.
type deviceRow struct {
	ID, Name, Added, Used string
}

func deviceRows(devices []tailnet.Device) []deviceRow {
	rows := make([]deviceRow, 0, len(devices))
	for _, d := range devices {
		row := deviceRow{ID: d.ID, Name: humancheck.Clean(d.Name, tailnet.MaxDeviceName), Added: d.AddedAt.Local().Format("2 Jan 2006"), Used: "never"}
		if d.UsedAt != nil {
			row.Used = d.UsedAt.Local().Format("2 Jan 15:04")
		}
		rows = append(rows, row)
	}
	return rows
}

var passkeyTemplate = template.Must(template.New("passkey").Funcs(uiFuncs).Parse(uiHead + `{{template "head" .}}
{{if eq .Mode "create"}}<h1>Set up this device</h1>
<section class="card">
<p>Make a passkey for wasync on this device, named <strong>{{.DeviceName}}</strong>. Your browser asks you to save it and to confirm with Touch ID, Face ID, Windows Hello or this device's PIN.</p>
<p class="mut">After that, giving agents more power from this device asks you for the passkey each time, showing what you are approving. Signed in to Tailscale as {{.Login}}.{{if .DeviceCount}} {{.DeviceCount}} other device(s) are set up already.{{end}}</p>
<form id="passkey" method="post" action="/ui/passkey/register" data-mode="create" data-rp="{{.RPID}}" data-challenge="{{.Challenge}}" data-exclude="{{.Allow}}" data-user="{{.User}}" data-login="{{.Login}}">
{{else}}<h1>Confirm with your passkey</h1>
<section class="card">
<p>You are approving:</p>
<p><strong>{{.Prompt}}</strong></p>
<form id="passkey" method="post" action="/ui/passkey/confirm" data-mode="get" data-rp="{{.RPID}}" data-challenge="{{.Challenge}}" data-allow="{{.Allow}}">
{{end}}<input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="ask" value="{{.Ask}}"><input type="hidden" name="back" value="{{.Back}}">
<input type="hidden" name="credential" value=""><input type="hidden" name="client" value=""><input type="hidden" name="authdata" value=""><input type="hidden" name="signature" value=""><input type="hidden" name="attestation" value="">
<div class="btn-row"><button type="button" class="go" id="passkey-go">{{if eq .Mode "create"}}Make the passkey…{{else}}Confirm with passkey…{{end}}</button> <a href="{{.Back}}">Cancel</a></div>
</form>
<p id="passkey-status" role="status" class="mut">Nothing changes until you confirm. This page works for {{.ExpiresMinutes}} minutes.</p>
<noscript><p class="notice bad">Passkeys need JavaScript, which is off in this browser.</p></noscript>
</section>
</main></body></html>
`))
