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
	uiLinkPath   = "/api/ui/link"
	uiCookie     = "wasync_ui"
	uiLinkTTL    = 2 * time.Minute
	uiSessionTTL = 30 * time.Minute
	uiMaxTokens  = 8
)

type uiSession struct {
	csrf    string
	expires time.Time
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
func (s *uiState) enter(link string) (cookie string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	expiry, found := s.links[link]
	if !found || now.After(expiry) {
		return "", false
	}
	delete(s.links, link)
	if s.sessions == nil {
		s.sessions = map[string]uiSession{}
	}
	if len(s.sessions) >= uiMaxTokens {
		for k := range s.sessions {
			delete(s.sessions, k)
			break
		}
	}
	cookie = token()
	s.sessions[cookie] = uiSession{csrf: token(), expires: now.Add(uiSessionTTL)}
	return cookie, true
}

func (s *uiState) session(r *http.Request) (uiSession, bool) {
	c, err := r.Cookie(uiCookie)
	if err != nil {
		return uiSession{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	v, ok := s.sessions[c.Value]
	return v, ok
}

// uiURL is the one-time address for a page; only the pages listed here can be named.
func (h *handler) uiURL(link, page string) string {
	next := ""
	if page == "setup" {
		next = "&next=setup"
	}
	return "http://" + h.authority + "/ui/enter?t=" + url.QueryEscape(link) + next
}

// uiLink hands a signed caller a one-time address for the approval page.
func (h *handler) uiLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Page string `json:"page"`
	}
	if body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10)); len(body) > 0 {
		if json.Unmarshal(body, &in) != nil || (in.Page != "" && in.Page != "setup") {
			fail(w, 400, `page must be "" or "setup"`)
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
	"approved":  {"Approved. The agent has the access you confirmed.", true},
	"done":      {"Done.", true},
	"rejected":  {"Rejected. Nothing was given.", true},
	"declined":  {"Not confirmed on this computer, so nothing changed.", false},
	"busy":      {"A confirmation prompt is already open on this computer. Answer it first.", false},
	"locked":    {"Too many prompts went unconfirmed. Try again in a few minutes.", false},
	"invalid":   {"That choice is not valid for this request, so nothing changed.", false},
	"failed":    {"That did not work and nothing changed. The request may have expired.", false},
	"nogate":    {"This computer cannot show a system confirmation, so this cannot be done on this page.", false},
	"saved":     {"Saved.", true},
	"unpaired":  {"Link WhatsApp first (step 1).", false},
	"exists":    {"An agent with that name already exists. Choose another name, or revoke it first.", false},
	"replace":   {"whatsapp-axi already has a different saved credential. Tick the box to replace it, or choose another place.", false},
	"badname":   {"The name must be 1 to 32 characters: a to z, 0 to 9 and dashes.", false},
	"connected": {"Connected. The credential was saved where whatsapp-axi reads it and is not shown anywhere.", true},
	"loginon":   {"wasync will start by itself the next time you log in.", true},
	"loginoff":  {"wasync will no longer start by itself when you log in.", true},
}

func (h *handler) serveUI(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Referrer-Policy", "same-origin")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/ui/enter":
		h.uiEnter(w, r)
		return
	}
	sess, ok := h.ui.session(r)
	if !ok {
		h.uiRender(w, http.StatusUnauthorized, &uiData{Signedout: true, Title: "wasync approvals"})
		return
	}
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/ui/" || r.URL.Path == "/ui"):
		h.uiHome(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/decide":
		h.uiDecide(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/mode":
		h.uiMode(w, r, sess)
	case r.Method == http.MethodGet && r.URL.Path == "/ui/setup":
		h.setupHome(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/setup/chats":
		h.setupChats(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/setup/agent":
		h.setupAgent(w, r, sess)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/setup/service":
		h.setupService(w, r, sess)
	default:
		http.NotFound(w, r)
	}
}

func (h *handler) uiEnter(w http.ResponseWriter, r *http.Request) {
	cookie, ok := h.ui.enter(r.URL.Query().Get("t"))
	if !ok {
		h.uiRender(w, http.StatusUnauthorized, &uiData{Signedout: true, Title: "wasync approvals"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: uiCookie, Value: cookie, Path: "/ui/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(uiSessionTTL / time.Second)})
	to := "/ui/"
	if r.URL.Query().Get("next") == "setup" {
		to = "/ui/setup"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// uiForm reads a small POST form after checking its request-forgery token.
func (h *handler) uiForm(w http.ResponseWriter, r *http.Request, sess uiSession) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(sess.csrf)) != 1 {
		http.Error(w, "invalid form", http.StatusForbidden)
		return false
	}
	return true
}

func (h *handler) uiBack(w http.ResponseWriter, r *http.Request, notice string) {
	to := "/ui/"
	if strings.HasPrefix(r.URL.Path, "/ui/setup") {
		to = "/ui/setup"
	}
	http.Redirect(w, r, to+"?n="+url.QueryEscape(notice), http.StatusSeeOther)
}

func noticeFor(err error) string {
	var se statusError
	switch {
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
		if !h.human.Enabled() || h.approve == nil {
			h.uiBack(w, r, "nogate")
			return
		}
		var choice *accessChoice
		if request := h.approvals.Get(account.ID, id); request != nil && request.Kind == approval.KindChatAccess {
			choice = &accessChoice{Chat: r.PostForm.Get("chat"), History: r.PostForm.Get("history")}
		}
		if err := h.approveRequest(r.Context(), account.ID, id, choice); err != nil {
			h.uiBack(w, r, noticeFor(err))
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
		if !h.human.Enabled() {
			h.uiBack(w, r, "nogate")
			return
		}
		if err := h.confirmError(r.Context(), "Approve: "+mode.Effect()); err != nil {
			h.uiBack(w, r, noticeFor(err))
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
	Title        string
	Refresh      int
}

func (h *handler) uiHome(w http.ResponseWriter, r *http.Request, sess uiSession) {
	data := &uiData{CSRF: sess.csrf, Gate: h.human.Name(), Title: "wasync approvals"}
	if !h.human.Enabled() {
		data.Gate = ""
	}
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
	h.uiRender(w, http.StatusOK, data)
}

func (h *handler) uiRender(w http.ResponseWriter, code int, data *uiData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = uiTemplate.Execute(w, data)
}

// uiHead is the page frame shared by the approval and setup pages.
const uiHead = `{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
{{if .Refresh}}<meta http-equiv="refresh" content="{{.Refresh}}">{{end}}
<title>{{.Title}}</title>
<style>
:root{color-scheme:light dark;--bg:#fafaf7;--fg:#1c1f1d;--mut:#5d6660;--card:#fff;--line:#d9ddd8;--acc:#0f7a4a;--bad:#a4262c}
@media (prefers-color-scheme:dark){:root{--bg:#141716;--fg:#e7ebe8;--mut:#9aa59e;--card:#1c201e;--line:#2c3330;--acc:#4cc38a;--bad:#ff8a8f}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif}
nav{margin:0 0 .6rem}a{color:var(--acc)}
main{max-width:42rem;margin:0 auto;padding:1.25rem 16px 3rem}
h1{font-size:1.4rem;margin:.2rem 0 .6rem}h2{font-size:1.05rem;margin:1.8rem 0 .5rem}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:1rem;margin:.7rem 0}
.mut{color:var(--mut)}.ok{color:var(--acc)}.bad{color:var(--bad)}
label{display:block;margin:.25rem 0}fieldset{border:0;padding:0;margin:.6rem 0}legend{font-weight:600}
button{font:inherit;padding:.5rem 1rem;border-radius:8px;border:1px solid var(--line);background:var(--card);color:var(--fg);cursor:pointer;margin-right:.5rem}
button.go{background:var(--acc);border-color:var(--acc);color:#fff}
code{background:var(--line);padding:.05rem .3rem;border-radius:4px}
</style></head><body><main>
<nav><a href="/ui/">Approvals</a> · <a href="/ui/setup">Set up</a></nav>{{end}}`

var uiTemplate = template.Must(template.New("ui").Parse(uiHead + `{{template "head" .}}
<h1>wasync approvals</h1>
{{if .Signedout}}
<p>This page needs a fresh link. Run <code>wasync approvals</code> in a terminal and it opens the page for you.</p>
{{else}}
{{if .Notice}}<p role="status" class="{{if .NoticeOK}}ok{{else}}bad{{end}}"><strong>{{.Notice}}</strong></p>{{end}}
{{if .Gate}}<p class="mut">Every approval here asks you for {{.Gate}} on this computer, showing exactly what you are approving. This page cannot approve anything by itself.</p>
{{else}}<p class="bad">This computer cannot show a system confirmation, so this page only shows requests. Approve in a terminal with <code>wasync actions approve REQUEST_ID</code>.</p>{{end}}
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
{{if or (not .Access) (not .NoMatch)}}{{if $.Gate}}<button class="go" name="action" value="approve">Approve…</button>{{end}}{{end}}
<button name="action" value="reject">Reject</button>
</form></section>
{{else}}<p class="mut">Nothing is waiting.</p>{{end}}
<p><a href="/ui/">Refresh</a></p>
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
<button>Save…</button> <span class="mut">Choosing sync all asks you to confirm on this computer.</span>
</form></section>
{{end}}
</main></body></html>
`))
