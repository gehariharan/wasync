package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/accessmode"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
)

// The approval page is a plain server-rendered page on the same loopback
// listener, with no script. It is a convenience for people without a terminal,
// not a security boundary: a program that can reach the listener can open the
// page too, so the page never approves anything by itself. Every approval it
// forwards still waits for the operating system's confirmation prompt, which
// only the person at the computer can answer. When the computer has no such
// prompt the page is read-only for approvals.

const (
	uiLinkPath = "/api/ui/link"
	// uiApprovalsPath is the approval page; the app itself opens on Chats.
	uiApprovalsPath = "/ui/approvals"
	uiCookie        = "wasync_ui"
	uiLinkTTL       = 2 * time.Minute
	// uiSessionTTL is how long a session lasts without a request; each request
	// starts it again, up to uiSessionMax after signing in.
	uiSessionTTL = 30 * time.Minute
	uiSessionMax = 12 * time.Hour
	uiMaxTokens  = 8
)

type uiSession struct {
	csrf    string
	started time.Time
	expires time.Time
	// login is the Tailscale login a remote session belongs to; "" for a
	// session opened on this computer with a one-time link.
	login string
}

// uiState holds single-use sign-in links and the sessions they were swapped for.
// It lives in memory only, so restarting the owner signs everyone out.
type uiState struct {
	mu       sync.Mutex
	links    map[string]time.Time
	sessions map[string]uiSession
}

func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("random source unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func (s *uiState) prune(now time.Time) {
	for k, t := range s.links {
		if now.After(t) {
			delete(s.links, k)
		}
	}
	for k, v := range s.sessions {
		if now.After(v.expires) {
			delete(s.sessions, k)
		}
	}
}

// newLink returns a one-time address that opens the approval page. It works
// once, for two minutes.
func (s *uiState) newLink() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	if s.links == nil {
		s.links = map[string]time.Time{}
	}
	if len(s.links) >= uiMaxTokens {
		for k := range s.links {
			delete(s.links, k)
			break
		}
	}
	t := token()
	s.links[t] = now.Add(uiLinkTTL)
	return t
}

// enter swaps a sign-in link for a session and returns its cookie value.
func (s *uiState) enter(link string) (cookie string, sess uiSession, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	expiry, found := s.links[link]
	if !found || now.After(expiry) {
		return "", uiSession{}, false
	}
	delete(s.links, link)
	cookie, sess = s.add(now, "")
	return cookie, sess, true
}

// start opens a session for a remote browser whose Tailscale login was checked.
func (s *uiState) start(login string) (cookie string, sess uiSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	return s.add(now, login)
}

// add records a new session; the caller holds s.mu.
func (s *uiState) add(now time.Time, login string) (string, uiSession) {
	if s.sessions == nil {
		s.sessions = map[string]uiSession{}
	}
	if len(s.sessions) >= uiMaxTokens {
		for k := range s.sessions {
			delete(s.sessions, k)
			break
		}
	}
	cookie := token()
	sess := uiSession{csrf: token(), started: now, expires: now.Add(uiSessionTTL), login: login}
	s.sessions[cookie] = sess
	return cookie, sess
}

// session finds the request's session and, since it is in use, extends it by
// uiSessionTTL, never past uiSessionMax after it started. cookie is its value,
// to send again with the new lifetime.
func (s *uiState) session(r *http.Request) (sess uiSession, cookie string, ok bool) {
	c, err := r.Cookie(uiCookie)
	if err != nil {
		return uiSession{}, "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	sess, ok = s.sessions[c.Value]
	if !ok {
		return uiSession{}, "", false
	}
	sess.expires = now.Add(uiSessionTTL)
	if limit := sess.started.Add(uiSessionMax); sess.expires.After(limit) {
		sess.expires = limit
	}
	s.sessions[c.Value] = sess
	return sess, c.Value, true
}

// setSessionCookie sends the session cookie, living as long as the session.
// A session opened through the tailnet arrives over HTTPS, so its cookie is
// marked Secure.
func setSessionCookie(w http.ResponseWriter, cookie string, sess uiSession) {
	life := int(time.Until(sess.expires) / time.Second)
	if life < 1 {
		life = 1
	}
	http.SetCookie(w, &http.Cookie{Name: uiCookie, Value: cookie, Path: "/ui/", HttpOnly: true, Secure: sess.login != "", SameSite: http.SameSiteStrictMode, MaxAge: life})
}

// uiURL is the one-time address for a page; only the pages listed here can be named.
func (h *handler) uiURL(link, page string) string {
	next := ""
	if page == "setup" || page == "chats" {
		next = "&next=" + page
	}
	return "http://" + h.authority + "/ui/enter?t=" + url.QueryEscape(link) + next
}

// uiLink hands a signed caller a one-time address for the approval page.
func (h *handler) uiLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Page string `json:"page"`
	}
	if body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10)); len(body) > 0 {
		if json.Unmarshal(body, &in) != nil || (in.Page != "" && in.Page != "setup" && in.Page != "chats") {
			fail(w, 400, `page must be "", "setup" or "chats"`)
			return
		}
	}
	writeJSON(w, 200, map[string]string{"url": h.uiURL(h.ui.newLink(), in.Page)})
}

func isUI(path string) bool { return path == "/ui" || strings.HasPrefix(path, "/ui/") }

var notices = map[string]struct {
	text string
	ok   bool
}{
	"approved":   {"Approved. The agent has the access you confirmed.", true},
	"done":       {"Done.", true},
	"rejected":   {"Rejected. Nothing was given.", true},
	"declined":   {"Not confirmed on this computer, so nothing changed.", false},
	"busy":       {"A confirmation prompt is already open on this computer. Answer it first.", false},
	"locked":     {"Too many prompts went unconfirmed. Try again in a few minutes.", false},
	"invalid":    {"That choice is not valid for this request, so nothing changed.", false},
	"failed":     {"That did not work and nothing changed. The request may have expired.", false},
	"nogate":     {"This computer cannot show a system confirmation, so this cannot be done on this page.", false},
	"remote":     {"Set up this device with a passkey first (Set up, Approve from this device), or do this on the computer running wasync.", false},
	"pkfail":     {"Not confirmed with your passkey, so nothing changed.", false},
	"pkexpired":  {"That page waited too long or was already used, so nothing changed. Try again.", false},
	"pkchanged":  {"What you were confirming changed in the meantime, so nothing changed. Try again.", false},
	"badcode":    {"That code is not valid. Codes work once, for 10 minutes; get a new one on the computer running wasync.", false},
	"deviceon":   {"This device is set up. Approvals here now ask for its passkey.", true},
	"deviceoff":  {"That device can no longer approve anything.", true},
	"devicehere": {"Add devices on the computer running wasync.", false},
	"toomany":    {"Too many devices are set up. Remove one first.", false},
	"notailnet":  {"Opening wasync from your tailnet is off. Turn it on with wasync tailnet allow.", false},
	"saved":      {"Saved.", true},
	"sentok":     {"Sent: WhatsApp accepted it. It shows in the chat once WhatsApp passes it back.", true},
	"sendfail":   {"That was not sent, or WhatsApp did not confirm it. Check WhatsApp before sending it again.", false},
	"offline":    {"wasync is not connected to WhatsApp, so nothing was sent.", false},
	"notext":     {"Write a message first.", false},
	"badto":      {"Enter the phone number with its country code, such as +15551230000.", false},
	"dlqueued":   {"Downloading. Refresh in a moment to see it.", true},
	"dlfail":     {"That could not be downloaded. WhatsApp may be disconnected or no longer have the file.", false},
	"dlpolicy":   {"Saved. New attachments in this chat follow it.", true},
	"sendon":     {"That agent may now send in the chats you approve for it.", true},
	"sendoff":    {"That agent can no longer send.", true},
	"openon":     {"Open sending is on for the next hour.", true},
	"openoff":    {"Open sending is off.", true},
	"liston":     {"Approved. That agent may now message the numbers on its list.", true},
	"listoff":    {"The list is removed. That agent can no longer message those numbers.", true},
	"listpause":  {"Paused. Nothing is sent until you resume it.", true},
	"listresume": {"Resumed.", true},
	"badlist":    {"Every line must be one phone number with its country code, such as +15551230000. Nothing changed.", false},
	"nolist":     {"Paste some numbers or choose a file first.", false},
	"biglist":    {"A list can hold at most 1000 numbers. Nothing changed.", false},
	"needsend":   {"Allow sending for that agent first.", false},
	"agentall":   {"That agent reads every chat, so it cannot be given sending here. Set it up again in a terminal with a chat list.", false},
	"unpaired":   {"Link WhatsApp first (step 1).", false},
	"newcode":    {"Asking WhatsApp for a new code…", true},
	"exists":     {"An agent with that name already exists. Choose another name, or revoke it first.", false},
	"replace":    {"wasync-axi already has a different saved credential. Tick the box to replace it, or choose another place.", false},
	"badname":    {"The name must be 1 to 32 characters: a to z, 0 to 9 and dashes.", false},
	"connected":  {"Connected. The credential was saved where wasync-axi reads it and is not shown anywhere.", true},
	"loginon":    {"On. wasync starts at every login.", true},
	"loginoff":   {"wasync will no longer start by itself when you log in.", true},
}

func (h *handler) serveUI(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; font-src 'self'; img-src 'self'; media-src 'self'; manifest-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Referrer-Policy", "same-origin")
	login := remoteLogin(r)
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/ui/" || r.URL.Path == "/ui"):
		// The app opens on Chats; approvals have their own page.
		http.Redirect(w, r, "/ui/chats", http.StatusSeeOther)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/ui/enter" && login != "":
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/ui/enter":
		h.uiEnter(w, r)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/ui/signin" && login == "":
		h.uiSignIn(w, r)
		return
	}
	sess, cookie, ok := h.ui.session(r)
	if ok && sess.login != login {
		ok = false // a session belongs to the one place and login it was opened for
	}
	if !ok && login != "" && r.Method == http.MethodGet {
		// The Tailscale login was checked on this request; it signs the browser in.
		cookie, sess = h.ui.start(login)
		ok = true
	}
	if !ok {
		h.signedOut(w, r, "")
		return
	}
	setSessionCookie(w, cookie, sess)
	if login != "" {
		r = h.withRemoteAsk(r, cookie, sess)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == passkeyConfirmPath:
			h.passkeyAnswer(w, r, sess)
			return
		case r.Method == http.MethodPost && r.URL.Path == passkeyBeginPath:
			h.passkeyBegin(w, r, sess)
			return
		case r.Method == http.MethodPost && r.URL.Path == passkeyRegisterPath:
			h.passkeyRegister(w, r, sess)
			return
		}
	}
	h.uiDispatch(w, r, sess)
}

// uiDispatch runs the page or form a signed-in request names.
func (h *handler) uiDispatch(w http.ResponseWriter, r *http.Request, sess uiSession) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == uiApprovalsPath:
		h.uiHome(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/decide":
		h.uiDecide(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/mode":
		h.uiMode(w, r, sess)
	case r.Method == http.MethodGet && r.URL.Path == "/ui/chats":
		h.chatsPage(w, r, sess)
	case r.Method == http.MethodGet && r.URL.Path == "/ui/agents":
		h.agentsPage(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/agents":
		h.agentsAct(w, r, sess)
	case r.Method == http.MethodGet && r.URL.Path == "/ui/sent":
		h.sentPage(w, r, sess)
	case r.Method == http.MethodGet && r.URL.Path == "/ui/chats/new":
		h.composeNewPage(w, r, sess)
	case r.Method == http.MethodGet && r.URL.Path == "/ui/media":
		h.mediaFile(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/chats/media":
		h.chatsMedia(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/chats/send":
		h.composeSend(w, r, sess)
	case r.Method == http.MethodGet && r.URL.Path == "/ui/chats/pick":
		h.pickPage(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/chats/pick":
		h.pickSave(w, r, sess)
	case r.Method == http.MethodGet && r.URL.Path == "/ui/setup":
		h.setupHome(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/setup/link" && h.setup != nil:
		h.setupLink(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/setup/agent":
		h.setupAgent(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/setup/service":
		h.setupService(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/setup/devices":
		h.setupDevices(w, r, sess)
	default:
		http.NotFound(w, r)
	}
}

// signInPages are the pages a sign-in may return to.
var signInPages = map[string]bool{"/ui/": true, uiApprovalsPath: true, "/ui/chats": true, "/ui/sent": true, "/ui/agents": true, "/ui/setup": true}

// signedOut answers a request with no session: the page says how to sign in
// again, and on this computer offers to do it with the system confirmation.
func (h *handler) signedOut(w http.ResponseWriter, r *http.Request, notice string) {
	d := &uiData{Signedout: true, Title: "wasync", Remote: remoteLogin(r) != "", Next: "/ui/"}
	if !d.Remote && h.human.Enabled() {
		d.Gate = h.human.Name()
	}
	if signInPages[r.URL.Path] {
		d.Next = r.URL.Path
	}
	if n, ok := notices[notice]; ok {
		d.Notice, d.NoticeOK = n.text, n.ok
	}
	h.uiRender(w, r, http.StatusUnauthorized, d)
}

// uiSignIn opens a session for this browser once the person confirms it with
// the system prompt, so a signed-out page needs no terminal. Like an approval,
// it cannot happen by itself: only the person at the computer can answer the
// prompt, and it is never offered through the tailnet, where the Tailscale
// login signs a browser in.
func (h *handler) uiSignIn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	next := r.PostForm.Get("next")
	if !signInPages[next] {
		next = "/ui/"
	}
	r.URL.Path = next // the signed-out page returns to it
	if !h.human.Enabled() {
		h.signedOut(w, r, "nogate")
		return
	}
	if err := h.confirmError(r.Context(), "Approve: sign in to the wasync web app in this browser. It shows your chats and lets you manage agents."); err != nil {
		h.signedOut(w, r, noticeFor(err))
		return
	}
	cookie, sess := h.ui.start("")
	setSessionCookie(w, cookie, sess)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (h *handler) uiEnter(w http.ResponseWriter, r *http.Request) {
	cookie, sess, ok := h.ui.enter(r.URL.Query().Get("t"))
	if !ok {
		h.signedOut(w, r, "")
		return
	}
	setSessionCookie(w, cookie, sess)
	to := uiApprovalsPath // wasync approvals asks for no page
	switch r.URL.Query().Get("next") {
	case "setup":
		to = "/ui/setup"
	case "chats":
		to = "/ui/chats"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// uiForm reads a small POST form after checking its request-forgery token.
func (h *handler) uiForm(w http.ResponseWriter, r *http.Request, sess uiSession) bool {
	return h.uiFormLimit(w, r, sess, 8<<10)
}

// uiFormLimit is uiForm for a form that may be larger, such as a message.
func (h *handler) uiFormLimit(w http.ResponseWriter, r *http.Request, sess uiSession, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := r.ParseForm(); err != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(sess.csrf)) != 1 {
		http.Error(w, "invalid form", http.StatusForbidden)
		return false
	}
	return true
}

func (h *handler) uiBack(w http.ResponseWriter, r *http.Request, notice string) {
	to := uiApprovalsPath
	if strings.HasPrefix(r.URL.Path, "/ui/setup") {
		to = "/ui/setup"
	}
	http.Redirect(w, r, to+"?n="+url.QueryEscape(notice), http.StatusSeeOther)
}

func noticeFor(err error) string {
	var se statusError
	switch {
	case errors.As(err, &se) && se.notice != "":
		return se.notice
	case errors.As(err, &se) && se.code == http.StatusTooManyRequests:
		if strings.Contains(se.msg, "already open") {
			return "busy"
		}
		return "locked"
	case errors.As(err, &se) && se.code == http.StatusForbidden:
		return "declined"
	case errors.As(err, &se) && se.code == http.StatusConflict:
		return "invalid"
	}
	return "failed"
}

func (h *handler) uiDecide(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil || h.approvals == nil {
		h.uiBack(w, r, "failed")
		return
	}
	id := r.PostForm.Get("id")
	switch r.PostForm.Get("action") {
	case "reject":
		if h.reject == nil || h.reject(account.ID, id) != nil {
			h.uiBack(w, r, "failed")
			return
		}
		h.uiBack(w, r, "rejected")
	case "approve":
		if !h.canConfirm(r) || h.approve == nil {
			h.uiBack(w, r, noGate(r))
			return
		}
		var choice *accessChoice
		if request := h.approvals.Get(account.ID, id); request != nil && request.Kind == approval.KindChatAccess {
			choice = &accessChoice{Chat: r.PostForm.Get("chat"), History: r.PostForm.Get("history")}
		}
		if err := h.approveRequest(r.Context(), account.ID, id, choice); err != nil {
			h.uiRefused(w, r, err, func(n string) { h.uiBack(w, r, n) })
			return
		}
		h.uiBack(w, r, "approved")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func (h *handler) uiMode(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil {
		h.uiBack(w, r, "failed")
		return
	}
	mode := accessmode.Mode{Mode: r.PostForm.Get("mode")}
	if mode.Mode == accessmode.All {
		mode.History = r.PostForm.Get("history")
	}
	if mode.Validate() != nil {
		h.uiBack(w, r, "invalid")
		return
	}
	if mode.Mode == accessmode.All {
		if !h.canConfirm(r) {
			h.uiBack(w, r, noGate(r))
			return
		}
		if err := h.confirmError(r.Context(), "Approve: "+mode.Effect()); err != nil {
			h.uiRefused(w, r, err, func(n string) { h.uiBack(w, r, n) })
			return
		}
	}
	if _, err := accessmode.Apply(r.Context(), h.store, h.dataDir, account.ID, mode); err != nil {
		h.uiBack(w, r, "failed")
		return
	}
	h.uiBack(w, r, "done")
}

type uiCandidate struct{ Chat, Name string }

type uiRequest struct {
	ID, Short  string
	Agent      string
	Asked      string
	Effect     string
	Access     bool
	NoMatch    bool
	Candidates []uiCandidate
	Truncated  bool
	Minutes    int
}

type uiData struct {
	CSRF, Notice string
	NoticeOK     bool
	Gate         string
	Pending      []uiRequest
	Mode         string
	ModeAll      bool
	ModeHistory  string
	Signedout    bool
	Next         string // where signing in again returns to
	Remote       bool   // opened from another computer through the tailnet
	Title        string
	Script       bool // the page runs wasync's passkey script
	Refresh      int
	Wide         bool // use the wide two-pane frame
	Full         bool // with Wide, fill the whole browser window
	// Warn is a short line on every page while wasync would stop with its
	// terminal; WarnSetup links it to Start at login.
	Warn      string
	WarnSetup bool
	// Page names the rail item to mark as current; Waiting is the count of
	// requests waiting for approval, shown on the rail.
	Page    string
	Waiting int
}

func (d *uiData) frame() *uiData { return d }

// framed is page data built on uiData.
type framed interface{ frame() *uiData }

// warnBackground fills the line every page shows while this wasync runs from
// a terminal instead of in the background, so closing that terminal would
// stop capture, approvals and list sends. A computer without a login item,
// or a wasync whose start cannot be told and that has one, shows nothing.
func (h *handler) warnBackground(data any) {
	f, ok := data.(framed)
	if !ok || f.frame().Signedout || h.service == nil || h.service.Where() == "" || h.info.StartedBy == "login item" {
		return
	}
	d := f.frame()
	switch installed, err := h.service.Installed(); {
	case err != nil:
	case !installed:
		d.Warn, d.WarnSetup = "wasync stops when its terminal closes.", true
	case h.info.StartedBy != "":
		d.Warn = "Not in the background: wasync stops when its terminal closes. Run wasync service restart."
	}
}

// railPage names the rail item a page's address marks as current: its first
// part after /ui/, so a form posted under /ui/setup/ stays on Set up.
func railPage(path string) string {
	first, _, _ := strings.Cut(strings.TrimPrefix(path, "/ui/"), "/")
	switch first {
	case "chats", "sent", "agents", "approvals", "setup":
		return first
	}
	return ""
}

// frameFor fills what every signed-in page's frame shows: the background
// warning, the current rail item and how many requests wait for approval.
func (h *handler) frameFor(r *http.Request, data any) {
	h.warnBackground(data)
	f, ok := data.(framed)
	if !ok || f.frame().Signedout {
		return
	}
	d := f.frame()
	d.Page = railPage(r.URL.Path)
	if h.approvals == nil {
		return
	}
	if account, err := h.store.Account(r.Context()); err == nil && account != nil {
		d.Waiting = len(h.approvals.PendingList(account.ID))
	}
}

func (h *handler) uiHome(w http.ResponseWriter, r *http.Request, sess uiSession) {
	data := &uiData{CSRF: sess.csrf, Gate: h.confirmName(r), Title: "wasync approvals", Remote: remoteLogin(r) != ""}
	if n, ok := notices[r.URL.Query().Get("n")]; ok {
		data.Notice, data.NoticeOK = n.text, n.ok
	}
	account, err := h.store.Account(r.Context())
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	if account != nil && h.approvals != nil {
		for _, p := range h.approvals.PendingList(account.ID) {
			v := uiRequest{ID: p.ID, Short: p.ID[len(p.ID)-6:], Minutes: int(time.Until(p.Expires).Round(time.Minute) / time.Minute)}
			if p.Kind == approval.KindChatAccess && p.Access != nil {
				v.Access, v.NoMatch, v.Truncated = true, p.NoMatch(), p.Access.Truncated
				v.Agent, v.Asked = humancheck.Clean(p.Access.Agent, 60), humancheck.Clean(p.Access.Query, 120)
				v.Effect = "Wants to read"
				switch {
				case p.Access.Send && p.Access.Edit:
					v.Effect = "Wants to read, send, reply, react and edit its own messages in"
				case p.Access.Send:
					v.Effect = "Wants to read, send, reply and react in"
				case p.Access.Edit:
					v.Effect = "Wants to read and edit its own messages in"
				}
				for _, c := range p.Access.Candidates {
					v.Candidates = append(v.Candidates, uiCandidate{c.Chat, humancheck.Clean(c.Name, 80)})
				}
			} else {
				v.Effect = "Destructive request: " + p.Effect(h.chatName(r.Context(), account.ID, p.Chat))
			}
			data.Pending = append(data.Pending, v)
		}
	}
	m, set, err := accessmode.Load(h.dataDir)
	data.Mode = "Ask me each time (default)"
	if err == nil && m.Mode == accessmode.All {
		data.ModeAll, data.ModeHistory = true, m.History
		data.Mode = "Sync all chats: agents get any chat they ask for without asking you"
	} else if set {
		data.Mode = "Ask me each time"
	}
	h.uiRender(w, r, http.StatusOK, data)
}

func (h *handler) uiRender(w http.ResponseWriter, r *http.Request, code int, data *uiData) {
	h.frameFor(r, data)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = uiTemplate.Execute(w, data)
}

// uiHead is the page frame every web app page shares: the design system's
// stylesheets, the icon rail and the notices. Pages carry no styles of their
// own; components.css holds them all.
const uiHead = `{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
{{if .Refresh}}<meta http-equiv="refresh" content="{{.Refresh}}">{{end}}
<title>{{.Title}}</title>
<link rel="manifest" href="/ui/manifest.webmanifest"><link rel="icon" href="/ui/favicon.ico" sizes="48x48"><link rel="icon" href="/ui/icon.svg" type="image/svg+xml"><link rel="apple-touch-icon" href="/ui/apple-touch-icon.png">
<meta name="application-name" content="wasync"><meta name="apple-mobile-web-app-title" content="wasync">
<meta name="theme-color" content="#f6f7f5" media="(prefers-color-scheme: light)"><meta name="theme-color" content="#111513" media="(prefers-color-scheme: dark)">
<link rel="preload" href="/ui/inter.woff2" as="font" type="font/woff2" crossorigin>
<link rel="stylesheet" href="/ui/tokens.css?v={{tokensv}}"><link rel="stylesheet" href="/ui/components.css?v={{componentsv}}">
{{if .Script}}<script src="/ui/passkey.js" defer></script>{{end}}
</head><body class="{{if .Signedout}}bare{{else}}app-shell{{end}}">
{{if not .Signedout}}{{template "rail" .}}{{end}}<main{{if .Full}} class="full"{{else if .Wide}} class="wide"{{end}}>
{{if .Warn}}<p class="notice warn" role="status"><strong>{{.Warn}}</strong>{{if .WarnSetup}} <a href="/ui/setup#login">Start at login</a>{{end}}</p>{{end}}{{end}}
{{define "notice"}}{{if .Notice}}<p role="status" class="notice {{if .NoticeOK}}ok{{else}}bad{{end}}"><strong>{{.Notice}}</strong></p>{{end}}{{end}}
{{define "rail"}}<nav class="rail" aria-label="wasync">
<a class="rail-logo" href="/ui/chats" title="{{if .Warn}}{{.Warn}}{{else}}wasync is running{{end}}"><img src="/ui/icon.svg" alt="wasync" width="36" height="36"><span class="rail-dot{{if .Warn}} warn{{end}}"></span></a>
<a class="rail-i" href="/ui/chats" title="Chats" aria-label="Chats"{{if eq .Page "chats"}} aria-current="page"{{end}}>{{template "icon" "chats"}}</a>
<a class="rail-i" href="/ui/sent" title="Sent" aria-label="Sent"{{if eq .Page "sent"}} aria-current="page"{{end}}>{{template "icon" "sent"}}</a>
<a class="rail-i" href="/ui/agents" title="Agents" aria-label="Agents"{{if eq .Page "agents"}} aria-current="page"{{end}}>{{template "icon" "agents"}}</a>
<a class="rail-i" href="/ui/approvals" title="Approvals{{if .Waiting}}: {{.Waiting}} waiting{{end}}" aria-label="Approvals{{if .Waiting}}, {{.Waiting}} waiting{{end}}"{{if eq .Page "approvals"}} aria-current="page"{{end}}>{{template "icon" "approvals"}}{{if .Waiting}}<span class="rail-badge" aria-hidden="true">{{.Waiting}}</span>{{end}}</a>
<span class="rail-sp"></span>
<a class="rail-i" href="/ui/setup" title="Set up and settings" aria-label="Set up and settings"{{if eq .Page "setup"}} aria-current="page"{{end}}>{{template "icon" "settings"}}</a>
</nav>{{end}}
` + uiIcons

// uiIcons are the line icons the rail and the chats tools use, drawn inline
// so they follow the text colour. Each is decorative: its link names it.
const uiIcons = `{{define "icon"}}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">{{if eq . "chats"}}<path d="M4 5h16v11H9l-5 4z"/>{{else if eq . "sent"}}<path d="M21 3 10 14"/><path d="m21 3-7 18-4-7-7-4z"/>{{else if eq . "agents"}}<rect x="5" y="8" width="14" height="11" rx="3"/><path d="M12 8V4"/><circle cx="12" cy="3.5" r="1"/><circle cx="9.5" cy="13" r="1"/><circle cx="14.5" cy="13" r="1"/>{{else if eq . "approvals"}}<path d="M12 3l7 3v6c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V6z"/><path d="m9 12 2 2 4-4"/>{{else if eq . "settings"}}<circle cx="12" cy="12" r="3"/><path d="M12 2v3M12 19v3M2 12h3M19 12h3M4.9 4.9 7 7M17 17l2.1 2.1M4.9 19.1 7 17M17 7l2.1-2.1"/>{{else if eq . "all"}}<path d="M4 6h16M4 12h16M4 18h16"/>{{else if eq . "pick"}}<rect x="4" y="4" width="16" height="16" rx="3"/><path d="m8 12 3 3 5-6"/>{{else if eq . "new"}}<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="m13 7 4 4"/>{{end}}</svg>{{end}}`

// uiFuncs are the helpers every page template may call.
var uiFuncs = template.FuncMap{
	"tokensv":     func() string { return tokensCSSVersion },
	"componentsv": func() string { return componentsCSSVersion },
}

var uiTemplate = template.Must(template.New("ui").Funcs(uiFuncs).Parse(uiHead + `{{template "head" .}}
{{if .Signedout}}<div class="brand-head"><img src="/ui/icon.svg" alt="" width="40" height="40"><h1>wasync approvals</h1></div>
{{template "notice" .}}<section class="card">{{if .Remote}}
<p>Signed out. Reload the page.</p>{{else}}
<p>Signed out.</p>
{{if .Gate}}<form method="post" action="/ui/signin" class="btn-row"><input type="hidden" name="next" value="{{.Next}}"><button class="go">Sign in…</button> <span class="mut">Asks you to confirm with {{.Gate}}.</span></form>
<p class="mut">Or run <code>wasync web</code> in a terminal.</p>{{else}}
<p>Run <code>wasync web</code> in a terminal to sign in.</p>{{end}}{{end}}</section>
{{else}}
<h1>wasync approvals</h1>
{{template "notice" .}}
{{if .Gate}}<p class="mut">Every approval here asks you for {{.Gate}} on this {{if .Remote}}device{{else}}computer{{end}}, showing exactly what you are approving. This page cannot approve anything by itself.</p>
{{else if .Remote}}<p class="notice bad">To approve from this device, <a href="/ui/setup#devices">set it up with a passkey</a> once. Or approve on the computer running wasync.</p>
{{else}}<p class="notice bad">This computer cannot show a system confirmation, so this page only shows requests. Approve in a terminal with <code>wasync actions approve REQUEST_ID</code>.</p>{{end}}
<h2>Waiting for you</h2>
{{range .Pending}}<section class="card">
<form method="post" action="/ui/decide">
<input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}">
{{if .Access}}
<p><strong>Agent {{.Agent}}</strong> asked for “{{.Asked}}”. <span class="mut">Request {{.Short}}, about {{.Minutes}} min left.</span></p>
{{if .NoMatch}}<p class="mut">No chat matched, so there is nothing to approve. You can reject it or let it expire.</p>
{{else}}<fieldset><legend>{{.Effect}} which chat?</legend>
{{range $i, $c := .Candidates}}<label><input type="radio" name="chat" value="{{$c.Chat}}"{{if eq $i 0}} checked{{end}}> {{$c.Name}} <span class="mut">{{$c.Chat}}</span></label>{{end}}
{{if .Truncated}}<p class="mut">More chats matched. The agent can ask again with a more specific name.</p>{{end}}</fieldset>
<fieldset><legend>History</legend>
<label><input type="radio" name="history" value="now" checked> From now on only (no older messages requested)</label>
<label><input type="radio" name="history" value="full"> Full history (also asks your phone for older messages, best effort)</label></fieldset>{{end}}
{{else}}<p><strong>{{.Effect}}</strong> <span class="mut">Request {{.Short}}, about {{.Minutes}} min left.</span></p>{{end}}
<div class="btn-row">{{if or (not .Access) (not .NoMatch)}}{{if $.Gate}}<button class="go" name="action" value="approve">Approve…</button>{{end}}{{end}}
<button name="action" value="reject">Reject</button></div>
</form></section>
{{else}}<p class="card mut">Nothing is waiting.</p>{{end}}
<p><a href="/ui/approvals">Refresh</a></p>
<h2>How agents get chats</h2>
<section class="card">
<p><strong>Now:</strong> {{.Mode}}{{if .ModeAll}} (history: {{.ModeHistory}}){{end}}</p>
<form method="post" action="/ui/mode"><input type="hidden" name="csrf" value="{{.CSRF}}">
<fieldset>
<label><input type="radio" name="mode" value="ask" checked> Ask me each time</label>
<label><input type="radio" name="mode" value="all"> Sync all chats. Any agent that asks gets any chat, with read, send, reply and react, without asking you.</label>
</fieldset>
<fieldset><legend>If syncing all chats</legend>
<label><input type="radio" name="history" value="now" checked> From now on only</label>
<label><input type="radio" name="history" value="full"> Full history</label></fieldset>
<div class="btn-row"><button>Save…</button> <span class="mut">Choosing sync all asks you to confirm{{if .Gate}} with {{.Gate}}{{else if .Remote}} on the computer running wasync{{else}} on this computer{{end}}.</span></div>
</form></section>
{{end}}
</main></body></html>
`))
