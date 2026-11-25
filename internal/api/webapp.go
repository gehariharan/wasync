package api

import (
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/storage"
)

// The chats pages are the start of wasync's web app: a place to see the chats
// being synced and read their messages, and to choose which chats are synced.
// Like the other pages they are server-rendered and carry no script. They show
// message text, so they are only served to a browser that holds a session, and
// choosing chats is a capture choice that needs no system prompt: an agent
// still sees nothing it has not been given.

const (
	maxChatRows   = 300
	threadLimit   = 100
	maxBodyRunes  = 4000
	pickTabAll    = "all"
	pickTabGroups = "groups"
	pickTabPeople = "people"
	pickTabNumber = "numbers"
)

type chatRow struct {
	ID, Name, Initials string
	Syncing, Agent     bool
	Selected           bool
	Count              int64
	Last               string
}

type chatMsg struct {
	Me                bool
	Who, Text, Time   string
	Edited, Deleted   bool
	Sent, Unavailable bool
	Att               *chatAttachment
}

type chatsData struct {
	uiData
	Paired         bool
	Rows           []chatRow
	More           int
	All            bool
	Cur            *chatRow
	Messages       []chatMsg
	OlderNotShown  bool
	Syncing, Total int
	CanSend        bool
	Picked         bool // a chat was asked for by name, so a phone shows it instead of the list
	New            bool // the right pane is a new message to a phone number
	MediaMode      string
	CanDownload    bool
}

type pickRow struct {
	ID, Name, Kind string
	Syncing        bool
	Count          int64
}

type pickTab struct {
	Key, Label string
	N          int
	Current    bool
}

type pickData struct {
	uiData
	Paired         bool
	Tabs           []pickTab
	Tab, Query     string
	Rows           []pickRow
	More           int
	Syncing, Total int
}

func initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "#"
	}
	return string(out)
}

// numberName reports whether a direct chat is known only by its number.
func numberName(id, name string) bool {
	return name == id || strings.HasPrefix(name, "+") || strings.Contains(name, "@")
}

func chatTab(id, name string) string {
	switch {
	case storage.ChatKind(id) == "group":
		return pickTabGroups
	case numberName(id, name):
		return pickTabNumber
	}
	return pickTabPeople
}

// bodyText makes message text safe to show: control characters other than line
// breaks are dropped and very long text is cut.
func bodyText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > maxBodyRunes {
		s = string(r[:maxBodyRunes-1]) + "…"
	}
	return strings.TrimSpace(s)
}

func shortTime(t time.Time) string {
	t = t.Local()
	if time.Since(t) < 20*time.Hour {
		return t.Format("15:04")
	}
	return t.Format("2 Jan 15:04")
}

// readingAgents lists the active agents that may read message text.
func (h *handler) readingAgents() []agentauth.Client {
	clients, _ := agentauth.List(h.dataDir)
	now := time.Now()
	var out []agentauth.Client
	for _, c := range clients {
		if c.Status(now) == "active" && c.HasScope(agentauth.ScopeReadMessages) {
			out = append(out, c)
		}
	}
	return out
}

func agentCanRead(agents []agentauth.Client, chat string) bool {
	for i := range agents {
		if agents[i].AllowsChat(chat) {
			return true
		}
	}
	return false
}

func (h *handler) webRender(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		http.Error(w, "page error", http.StatusInternalServerError)
	}
}

func (h *handler) chatsPage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	h.chatsView(w, r, sess, false)
}

// chatsView renders the chats page; with newMessage the right pane is a new
// message to a phone number instead of an open chat.
func (h *handler) chatsView(w http.ResponseWriter, r *http.Request, sess uiSession, newMessage bool) {
	d := &chatsData{uiData: uiData{CSRF: sess.csrf, Title: "wasync chats", Wide: true, Full: true}, CanSend: h.sender != nil, CanDownload: h.mediaDownload != nil}
	if n, ok := notices[r.URL.Query().Get("n")]; ok {
		d.Notice, d.NoticeOK = n.text, n.ok
	}
	account, err := h.store.Account(r.Context())
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	if account == nil {
		h.webRender(w, chatsTemplate, d)
		return
	}
	d.Paired = true
	groups, err := h.store.GroupSummaries(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	// Newest conversation first, like a chat app; chats that are not syncing follow.
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		switch {
		case a.Enabled != b.Enabled:
			return a.Enabled
		case a.LastMessage == nil || b.LastMessage == nil:
			return a.LastMessage != nil
		}
		return a.LastMessage.After(*b.LastMessage)
	})
	agents := h.readingAgents()
	d.All = r.URL.Query().Get("all") == "1"
	want := r.URL.Query().Get("c")
	d.New = newMessage
	d.Picked = want != "" || newMessage
	names := map[string]string{}
	d.Total = len(groups)
	var cur *storage.Group
	for i := range groups {
		g := &groups[i]
		names[g.ID] = humancheck.Clean(g.Name, 80)
		if g.Enabled {
			d.Syncing++
		}
		if g.ID == want {
			cur = g
		}
		if !g.Enabled && !d.All {
			continue
		}
		if len(d.Rows) == maxChatRows {
			d.More++
			continue
		}
		row := chatRow{ID: g.ID, Name: names[g.ID], Initials: initials(names[g.ID]), Syncing: g.Enabled, Agent: g.Enabled && agentCanRead(agents, g.ID), Count: g.MessageCount}
		if g.LastMessage != nil {
			row.Last = shortTime(*g.LastMessage)
		}
		d.Rows = append(d.Rows, row)
		if cur == nil && want == "" && !newMessage && len(d.Rows) == 1 {
			cur = g
		}
	}
	if cur != nil {
		name := names[cur.ID]
		d.Cur = &chatRow{ID: cur.ID, Name: name, Initials: initials(name), Syncing: cur.Enabled, Agent: cur.Enabled && agentCanRead(agents, cur.ID), Count: cur.MessageCount}
		for i := range d.Rows {
			d.Rows[i].Selected = d.Rows[i].ID == cur.ID
		}
		page, err := h.store.Messages(r.Context(), account.ID, cur.ID, storage.MessageQuery{Limit: threadLimit})
		if err != nil {
			http.Error(w, "archive unavailable", http.StatusInternalServerError)
			return
		}
		d.OlderNotShown = page.Next != (storage.Cursor{})
		if d.MediaMode, err = h.store.MediaPolicy(r.Context(), account.ID, cur.ID); err != nil {
			d.MediaMode = "inherit"
		}
		for i := len(page.Items) - 1; i >= 0; i-- {
			m := page.Items[i]
			msg := chatMsg{Me: m.FromMe, Time: shortTime(m.Timestamp), Edited: m.EditedAt != nil, Deleted: m.DeletedAt != nil, Att: attachmentFor(cur.ID, m)}
			switch {
			case m.DeletedAt != nil:
				msg.Text = "This message was deleted."
			case strings.TrimSpace(m.Text) != "":
				msg.Text = bodyText(m.Text)
			case msg.Att != nil:
			default:
				msg.Text = "[" + humancheck.Clean(m.Type, 30) + "]"
			}
			switch {
			case m.FromMe:
				msg.Who = "You"
			case storage.ChatKind(cur.ID) == "direct":
				msg.Who = name
			default:
				msg.Who = senderName(names, m)
			}
			d.Messages = append(d.Messages, msg)
		}
	}
	h.webRender(w, chatsTemplate, d)
}

func senderName(names map[string]string, m storage.Message) string {
	for _, id := range []string{m.SenderID, m.SenderAlt} {
		if n := names[id]; n != "" {
			return n
		}
	}
	id := m.SenderID
	if id == "" {
		id = m.SenderAlt
	}
	if user, server, ok := strings.Cut(id, "@"); ok && server == "s.whatsapp.net" && user != "" {
		return "+" + humancheck.Clean(user, 20)
	}
	return "Member"
}

func pickTabKey(s string) string {
	switch s {
	case pickTabGroups, pickTabPeople, pickTabNumber:
		return s
	}
	return pickTabAll
}

func (h *handler) pickPage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	d := &pickData{uiData: uiData{CSRF: sess.csrf, Title: "wasync chats to sync", Wide: true}}
	if n, ok := notices[r.URL.Query().Get("n")]; ok {
		d.Notice, d.NoticeOK = n.text, n.ok
	}
	d.Tab = pickTabKey(r.URL.Query().Get("tab"))
	d.Query = humancheck.Clean(r.URL.Query().Get("q"), 60)
	account, err := h.store.Account(r.Context())
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	if account == nil {
		h.webRender(w, pickTemplate, d)
		return
	}
	d.Paired = true
	groups, err := h.store.GroupSummaries(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	counts := map[string]int{}
	needle := strings.ToLower(d.Query)
	d.Total = len(groups)
	for _, g := range groups {
		name := humancheck.Clean(g.Name, 80)
		if g.Enabled {
			d.Syncing++
		}
		tab := chatTab(g.ID, name)
		if needle != "" && !strings.Contains(strings.ToLower(name+" "+g.ID), needle) {
			continue
		}
		counts[pickTabAll]++
		counts[tab]++
		if d.Tab != pickTabAll && d.Tab != tab {
			continue
		}
		if len(d.Rows) == maxChatRows {
			d.More++
			continue
		}
		d.Rows = append(d.Rows, pickRow{ID: g.ID, Name: name, Kind: tab, Syncing: g.Enabled, Count: g.MessageCount})
	}
	for _, t := range []pickTab{{Key: pickTabAll, Label: "All"}, {Key: pickTabGroups, Label: "Groups"}, {Key: pickTabPeople, Label: "People"}, {Key: pickTabNumber, Label: "Numbers"}} {
		t.N, t.Current = counts[t.Key], t.Key == d.Tab
		d.Tabs = append(d.Tabs, t)
	}
	h.webRender(w, pickTemplate, d)
}

// pickSave changes which chats are synced. It acts only on the chats the page
// showed, so searching and then saving never touches the chats that were hidden.
func (h *handler) pickSave(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	back := func(notice string) {
		q := url.Values{"n": {notice}, "tab": {pickTabKey(r.PostForm.Get("tab"))}}
		if s := humancheck.Clean(r.PostForm.Get("q"), 60); s != "" {
			q.Set("q", s)
		}
		http.Redirect(w, r, "/ui/chats/pick?"+q.Encode(), http.StatusSeeOther)
	}
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil {
		back("unpaired")
		return
	}
	groups, err := h.store.GroupSummaries(r.Context(), account.ID)
	if err != nil {
		back("failed")
		return
	}
	enabled := make(map[string]bool, len(groups))
	for _, g := range groups {
		enabled[g.ID] = g.Enabled
	}
	action := r.PostForm.Get("action")
	want := map[string]bool{}
	var scope []string
	switch action {
	case "clearall":
		for _, g := range groups {
			scope = append(scope, g.ID)
		}
	case "save", "tickshown", "clearshown":
		for _, id := range r.PostForm["shown"] {
			if _, known := enabled[id]; known {
				scope = append(scope, id)
			}
		}
		for _, id := range r.PostForm["chat"] {
			want[id] = true
		}
		if action == "tickshown" {
			for _, id := range scope {
				want[id] = true
			}
		}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	history := r.PostForm.Get("history") == "yes"
	var off []string
	for _, id := range scope {
		switch {
		case want[id] && !enabled[id]:
			if err := h.store.EnableCapture(r.Context(), account.ID, id, history); err != nil {
				back("failed")
				return
			}
		case !want[id] && enabled[id]:
			off = append(off, id)
		}
	}
	if len(off) > 0 {
		if err := h.store.SetEnabled(r.Context(), account.ID, off, false); err != nil {
			back("failed")
			return
		}
	}
	back("saved")
}

type sentRow struct {
	Time, Who, To, Text, Outcome    string
	Agent, Problem, Open, NewNumber bool
	Kind                            string
}

type sentData struct {
	uiData
	Paired bool
	Rows   []sentRow
}

const sentLogLimit = 100

// sentPage lists what was sent from this computer, by the owner or an agent,
// newest first. The log keeps no message text, so the text shown here is read
// back from the archive by its WhatsApp message ID, and is absent when the
// message never got an ID or is no longer archived.
func (h *handler) sentPage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	d := &sentData{uiData: uiData{CSRF: sess.csrf, Title: "wasync sent messages", Wide: true}}
	if n, ok := notices[r.URL.Query().Get("n")]; ok {
		d.Notice, d.NoticeOK = n.text, n.ok
	}
	account, err := h.store.Account(r.Context())
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	if account == nil {
		h.webRender(w, sentTemplate, d)
		return
	}
	d.Paired = true
	entries, err := h.sent.Recent(sentLogLimit)
	if err != nil {
		http.Error(w, "sent log unavailable", http.StatusInternalServerError)
		return
	}
	groups, err := h.store.GroupSummaries(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	names := make(map[string]string, len(groups))
	for _, g := range groups {
		names[g.ID] = humancheck.Clean(g.Name, 80)
	}
	for _, e := range entries {
		row := sentRow{Time: e.Time.Local().Format("2 Jan 15:04"), Kind: e.Kind, To: names[e.Chat], Agent: e.Actor != sentlog.Owner, Open: e.Open, NewNumber: e.NewNumber}
		if row.To == "" {
			row.To = humancheck.Clean(e.Chat, 80)
		}
		row.Who = "You"
		if row.Agent {
			row.Who = humancheck.Clean(e.Actor, 40)
		}
		row.Outcome, row.Problem = sentOutcome(e), e.Outcome != sentlog.OutcomeSent
		if e.MessageID != "" {
			if m, err := h.store.MessageByWhatsAppID(r.Context(), account.ID, e.Chat, e.MessageID); err == nil && m.DeletedAt == nil {
				row.Text = bodyText(m.Text)
			}
		}
		d.Rows = append(d.Rows, row)
	}
	h.webRender(w, sentTemplate, d)
}

// uiWideCSS is the two-pane layout the chats pages share, added to the page
// head when a page asks for the wide frame.
const uiWideCSS = `{{define "widecss"}}<style>
main.wide{max-width:74rem}
main.full{max-width:none;height:100vh;height:100dvh;box-sizing:border-box;padding:.6rem 12px;display:flex;flex-direction:column}
.app{flex:1;min-height:0;display:grid;grid-template-columns:minmax(16rem,22rem) minmax(0,1fr);grid-template-rows:minmax(0,1fr);border:1px solid var(--line);border-radius:12px;overflow:hidden;background:var(--card)}
.list{border-right:1px solid var(--line);overflow:auto}
.pane{display:flex;flex-direction:column;min-height:0;min-width:0}
.back{display:none}
@media (max-width:760px){main.full{padding:.4rem 6px}.app{grid-template-columns:1fr}.list{border-right:0}.app.picked .list,.app:not(.picked) .pane{display:none}.back{display:inline}}
.tools{display:flex;gap:.4rem;padding:.5rem .6rem;border-bottom:1px solid var(--line);align-items:center}
.ic{display:inline-grid;place-items:center;width:2.1rem;height:2.1rem;border:1px solid var(--line);border-radius:8px;background:var(--card);color:var(--fg);text-decoration:none;font-size:1.05rem}
.ic[aria-pressed="true"]{background:var(--fg);color:var(--bg)}
.row{display:grid;grid-template-columns:2.2rem minmax(0,1fr) auto;gap:.6rem;align-items:center;padding:.55rem .7rem;border-bottom:1px solid var(--line);color:inherit;text-decoration:none}
.row[aria-current="true"]{background:rgba(15,122,74,.12)}
.av{position:relative;width:2.2rem;height:2.2rem;border-radius:50%;background:var(--line);display:grid;place-items:center;font-weight:700;font-size:.8rem}
.bot{position:absolute;right:-6px;bottom:-6px;font-size:.6rem;line-height:1;background:var(--card);border:1px solid var(--line);border-radius:50%;width:1.1rem;height:1.1rem;display:grid;place-items:center}
.nm{font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sub{font-size:.8rem;color:var(--mut)}
.head{display:flex;justify-content:space-between;gap:.6rem;flex-wrap:wrap;align-items:center;padding:.6rem .9rem;border-bottom:1px solid var(--line)}
.thread{flex:1;min-height:0;overflow:auto;display:flex;flex-direction:column-reverse;padding:.8rem 1rem}
.msgs{display:flex;flex-direction:column;align-items:flex-start;gap:.35rem}
.b{max-width:min(75%,44rem);padding:.35rem .6rem;border-radius:10px;border:1px solid var(--line);white-space:pre-wrap;word-wrap:break-word}
.b.me{align-self:flex-end;background:rgba(15,122,74,.14)}
.b .by{display:block;font-size:.72rem;font-weight:700;color:var(--mut)}
.b .tm{display:block;text-align:right;font-size:.7rem;color:var(--mut)}
.pickrow{display:grid;grid-template-columns:auto minmax(0,1fr) auto;gap:.6rem;align-items:center;padding:.35rem .7rem;border-bottom:1px solid var(--line)}
.tabs{display:flex;gap:.4rem;flex-wrap:wrap;margin:.5rem 0}
.tabs a{padding:.2rem .7rem;border:1px solid var(--line);border-radius:99px;text-decoration:none;color:var(--fg)}
.tabs a[aria-current="true"]{background:var(--fg);color:var(--bg)}
.newmsg{flex:1;min-height:0;display:flex;flex-direction:column}
.to{display:flex;gap:.5rem;align-items:center;margin:0;padding:.5rem .9rem;border-bottom:1px solid var(--line)}.to input{flex:1;font:inherit;padding:.3rem .5rem}
.dlmode{display:flex;gap:.3rem;align-items:center;font-size:.85rem}.dlmode select,.dlmode button{font:inherit;font-size:.85rem;padding:.15rem .4rem;margin:0}
.media{display:block;width:24rem;max-width:100%;height:auto;max-height:24rem;object-fit:contain;border-radius:8px;margin:.25rem 0;white-space:normal}
.ph{display:grid;place-items:center;width:20rem;max-width:100%;max-height:16rem;border-radius:8px;margin:.25rem 0;background:repeating-linear-gradient(45deg,rgba(127,127,127,.12) 0 8px,rgba(127,127,127,.2) 8px 16px);color:var(--mut);font-size:.8rem;text-align:center;white-space:normal}
.file{display:grid;grid-template-columns:2.2rem minmax(0,1fr) auto;gap:.5rem;align-items:center;margin:.25rem 0;white-space:normal;min-width:14rem}
.file .ico{width:2.2rem;height:2.2rem;border-radius:8px;display:grid;place-items:center;background:var(--line)}
.file .fn{display:block;font-weight:600;font-size:.88rem;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.dl{display:inline;margin:0}.dl button,a.open{font:inherit;font-size:.8rem;padding:.2rem .6rem;margin:0;border-radius:7px;border:1px solid var(--line);background:var(--card);color:var(--fg);text-decoration:none;white-space:nowrap}.dl button{background:var(--acc);border-color:var(--acc);color:#fff}
.compose{display:flex;gap:.5rem;padding:.6rem .9rem;border-top:1px solid var(--line)}.compose textarea{flex:1;font:inherit}
.sent{width:100%;border-collapse:collapse;background:var(--card)}.sent th,.sent td{border:1px solid var(--line);padding:.4rem .6rem;text-align:left;vertical-align:top;font-size:.9rem}.sent tr.problem td{background:rgba(164,38,44,.08)}
.pickbox{border:1px solid var(--line);border-radius:10px;background:var(--card);max-height:60vh;overflow:auto}
</style>{{end}}`

// attView is what the attachment template needs: the attachment and the page
// it sits on, for the form token and the open chat.
type attView struct {
	D *chatsData
	A *chatAttachment
}

// uiAttachment draws one attachment inside a chat bubble.
const uiAttachment = `{{define "dlbtn"}}{{if and .D.CanDownload .D.Cur.Syncing}}<form class="dl" method="post" action="/ui/chats/media"><input type="hidden" name="csrf" value="{{.D.CSRF}}"><input type="hidden" name="chat" value="{{.D.Cur.ID}}"><input type="hidden" name="msg" value="{{.A.MsgID}}"><button name="action" value="download">{{if eq .A.State "failed"}}Try again{{else}}Download{{end}}</button></form>{{end}}{{end}}
{{define "attstate"}}{{if eq .A.State "pending"}}<span class="sub">Downloading… refresh to see it</span>{{else if eq .A.State "failed"}}<span class="sub">Couldn't download</span>{{end}}{{end}}
{{define "att"}}{{$a := .A}}{{if eq $a.State "ready"}}{{if eq $a.Show "img"}}<a href="{{$a.URL}}" target="_blank" rel="noopener"><img class="media" src="{{$a.URL}}" alt="{{$a.Label}}"{{if $a.Ratio}} style="aspect-ratio:{{$a.Ratio}}"{{end}}></a>{{else if eq $a.Show "video"}}<video class="media" controls preload="metadata" src="{{$a.URL}}"{{if $a.Ratio}} style="aspect-ratio:{{$a.Ratio}}"{{end}}></video>{{else if eq $a.Show "audio"}}<audio controls preload="metadata" src="{{$a.URL}}" aria-label="{{$a.Label}}"></audio><span class="sub" style="display:block">{{$a.Icon}} {{$a.Label}}{{if $a.Meta}} · {{$a.Meta}}{{end}}</span>{{else}}<div class="file"><span class="ico">{{$a.Icon}}</span><span><span class="fn">{{$a.Name}}</span><span class="sub">{{$a.Meta}}</span></span><a class="open" href="{{$a.URL}}" target="_blank" rel="noopener">Open</a></div>{{end}}{{else if and (or (eq $a.Kind "image") (eq $a.Kind "video")) $a.Ratio}}<div class="ph" style="aspect-ratio:{{$a.Ratio}}"><span>{{$a.Icon}}<br>{{$a.Label}}{{if $a.Meta}} · {{$a.Meta}}{{end}}<br>{{template "attstate" .}}{{if ne $a.State "pending"}} {{template "dlbtn" .}}{{end}}</span></div>{{else}}<div class="file"><span class="ico">{{$a.Icon}}</span><span><span class="fn">{{$a.Name}}</span><span class="sub">{{$a.Meta}}</span> {{template "attstate" .}}</span><span>{{if ne $a.State "pending"}}{{template "dlbtn" .}}{{end}}</span></div>{{end}}{{end}}`

var chatsTemplate = template.Must(template.New("chats").Funcs(template.FuncMap{"attctx": func(d *chatsData, a *chatAttachment) attView { return attView{d, a} }}).Parse(uiHead + uiAttachment + `{{template "head" .}}
{{if .Notice}}<p role="status" class="{{if .NoticeOK}}ok{{else}}bad{{end}}"><strong>{{.Notice}}</strong></p>{{end}}
{{if not .Paired}}<h1>Chats</h1><p class="mut">No WhatsApp is linked yet. Link it on the <a href="/ui/setup">Set up</a> page first.</p>
{{else}}
<div class="app{{if .Picked}} picked{{end}}">
<div class="list">
<div class="tools">
<a class="ic" href="/ui/chats{{if not .All}}?all=1{{end}}" title="{{if .All}}Show syncing chats only{{else}}Show all chats{{end}}" aria-label="{{if .All}}Show syncing chats only{{else}}Show all chats{{end}}" aria-pressed="{{.All}}">☰</a>
<a class="ic" href="/ui/chats/pick" title="Choose chats to sync" aria-label="Choose chats to sync">☑</a>
<a class="ic" href="/ui/chats/new" title="New message" aria-label="New message">✎</a>
<span class="sub">{{.Syncing}} of {{.Total}} syncing</span>
</div>
{{if .New}}<a class="row" href="/ui/chats/new" aria-current="true"><span class="av">✎</span><span class="nm">New message</span><span></span></a>{{end}}
{{range .Rows}}<a class="row" href="/ui/chats?c={{.ID}}{{if $.All}}&amp;all=1{{end}}"{{if .Selected}} aria-current="true"{{end}}>
<span class="av">{{.Initials}}{{if .Agent}}<span class="bot" title="Agents can read this chat" aria-label="Agents can read this chat">🤖</span>{{end}}</span>
<span><span class="nm" style="display:block">{{.Name}}</span><span class="sub">{{if .Syncing}}{{.Count}} messages{{else}}not syncing{{end}}</span></span>
<span class="sub">{{.Last}}</span></a>
{{else}}<p class="mut" style="padding:.8rem">No chats are syncing yet. Use the ☑ icon to choose some.</p>{{end}}
{{if .More}}<p class="sub" style="padding:.6rem .8rem">{{.More}} more chats are not listed.</p>{{end}}
</div>
<div class="pane">
{{if .New}}
<div class="head"><span><a class="back" href="/ui/chats{{if .All}}?all=1{{end}}" aria-label="Back to chats">‹</a> <strong>New message</strong></span></div>
{{if .CanSend}}<form class="newmsg" method="post" action="/ui/chats/send"><input type="hidden" name="csrf" value="{{.CSRF}}">
<label class="to">To <input name="to" placeholder="+15551230000" aria-label="Phone number, with its country code" autocomplete="off" required autofocus></label>
<div class="thread"><p class="mut">Sent once, as you; it shows in <a href="/ui/sent">Sent</a>.</p></div>
<div class="compose"><textarea name="text" rows="2" maxlength="16000" required placeholder="Message" aria-label="Message"></textarea><button>Send</button></div></form>
{{else}}<p class="bad" style="padding:1rem">wasync is not connected to WhatsApp, so nothing can be sent. Start it with <code>wasync serve</code> or <code>wasync tui</code> on a linked account.</p>{{end}}
{{else if .Cur}}
<div class="head"><span><a class="back" href="/ui/chats{{if .All}}?all=1{{end}}" aria-label="Back to chats">‹</a> <strong>{{.Cur.Name}}</strong> <span class="sub">{{if .Cur.Syncing}}syncing{{else}}not syncing{{end}}{{if .Cur.Agent}} · 🤖 agents can read this chat{{end}}</span></span>
{{if not .Cur.Syncing}}<a href="/ui/chats/pick">Choose chats to sync</a>{{else}}<form class="dlmode" method="post" action="/ui/chats/media"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="chat" value="{{.Cur.ID}}"><input type="hidden" name="action" value="policy">
<label for="dlmode">Downloads</label> <select id="dlmode" name="mode"><option value="manual"{{if eq .MediaMode "manual"}} selected{{end}}>Only when I ask</option><option value="auto"{{if eq .MediaMode "auto"}} selected{{end}}>Always in this chat</option><option value="inherit"{{if eq .MediaMode "inherit"}} selected{{end}}>Same as wasync default</option></select> <button>Save</button></form>{{end}}</div>
<div class="thread" aria-label="Messages in {{.Cur.Name}}"><div class="msgs">
{{if .OlderNotShown}}<p class="sub">Older messages are not shown here.</p>{{end}}
{{range .Messages}}<div class="b{{if .Me}} me{{end}}"><span class="by">{{.Who}}</span>{{with .Att}}{{template "att" (attctx $ .)}}{{end}}{{.Text}}<span class="tm">{{.Time}}{{if .Edited}} · edited{{end}}</span></div>
{{else}}<p class="mut">No messages kept for this chat yet.</p>{{end}}
</div></div>
{{if .CanSend}}<form class="compose" method="post" action="/ui/chats/send"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="chat" value="{{.Cur.ID}}">
<textarea name="text" rows="2" maxlength="16000" required placeholder="Message {{.Cur.Name}}" aria-label="Message {{.Cur.Name}}"></textarea><button>Send</button></form>{{end}}
{{else}}<p class="mut" style="padding:1rem">Choose a chat on the left.</p>{{end}}
</div>
</div>
{{end}}
</main></body></html>
`))

var sentTemplate = template.Must(template.New("sent").Parse(uiHead + `{{template "head" .}}
<h1>Sent</h1>
{{if .Notice}}<p role="status" class="{{if .NoticeOK}}ok{{else}}bad{{end}}"><strong>{{.Notice}}</strong></p>{{end}}
{{if not .Paired}}<p class="mut">No WhatsApp is linked yet. Link it on the <a href="/ui/setup">Set up</a> page first.</p>
{{else}}
<p class="mut">What was sent from this computer, by you or by an agent, newest first. WhatsApp's own tick is not tracked here: "Sent" means WhatsApp accepted the message, not that it was delivered or read. The text is read from your archive; the log itself keeps no message text.</p>
<table class="sent">
<thead><tr><th>Time</th><th>By</th><th>To</th><th>Message</th><th>Outcome</th></tr></thead>
<tbody>
{{range .Rows}}<tr{{if .Problem}} class="problem"{{end}}><td>{{.Time}}</td><td>{{if .Agent}}🤖 {{end}}{{.Who}}</td><td>{{.To}}</td><td>{{if .Text}}{{.Text}}{{else}}<span class="mut">text not kept</span>{{end}}{{if eq .Kind "reply"}} <span class="mut">(reply)</span>{{end}}{{if .Open}} <span class="mut">(open sending{{if .NewNumber}}, new number{{end}})</span>{{end}}</td><td>{{.Outcome}}</td></tr>
{{else}}<tr><td colspan="5" class="mut">Nothing has been sent from this computer yet.</td></tr>{{end}}
</tbody></table>
{{end}}
</main></body></html>
`))

var pickTemplate = template.Must(template.New("pick").Parse(uiHead + `{{template "head" .}}
<h1>Choose chats to sync</h1>
{{if .Notice}}<p role="status" class="{{if .NoticeOK}}ok{{else}}bad{{end}}"><strong>{{.Notice}}</strong></p>{{end}}
{{if not .Paired}}<p class="mut">No WhatsApp is linked yet. Link it on the <a href="/ui/setup">Set up</a> page first.</p>
{{else}}
<p class="mut"><strong>{{.Syncing}} of {{.Total}} syncing.</strong> wasync keeps only the chats you tick, on this computer. Search first, then tick the ones you want.</p>
<form method="get" action="/ui/chats/pick" role="search"><input type="hidden" name="tab" value="{{.Tab}}"><input type="search" name="q" value="{{.Query}}" placeholder="Search chats" aria-label="Search chats"> <button>Search</button></form>
<div class="tabs">{{range .Tabs}}<a href="/ui/chats/pick?tab={{.Key}}{{if $.Query}}&amp;q={{$.Query}}{{end}}"{{if .Current}} aria-current="true"{{end}}>{{.Label}} {{.N}}</a>{{end}}</div>
<form method="post" action="/ui/chats/pick">
<input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="tab" value="{{.Tab}}"><input type="hidden" name="q" value="{{.Query}}">
<div class="pickbox">
{{range .Rows}}<label class="pickrow"><input type="checkbox" name="chat" value="{{.ID}}"{{if .Syncing}} checked{{end}}><span>{{.Name}}</span><span class="sub">{{.Count}} messages</span><input type="hidden" name="shown" value="{{.ID}}"></label>
{{else}}<p class="mut" style="padding:.8rem">No chats match.</p>{{end}}
</div>
{{if .More}}<p class="mut">{{.More}} more match. Narrow the search to see them.</p>{{end}}
<label><input type="checkbox" name="history" value="yes" checked> Also ask your phone for older messages when a chat is newly ticked (best effort)</label>
<p><button class="go" name="action" value="save">Save</button> <button name="action" value="tickshown">Tick all shown</button> <button name="action" value="clearshown">Clear all shown</button> <button name="action" value="clearall">Stop syncing everything</button></p>
</form>
<p><a href="/ui/chats">Back to chats</a></p>
{{end}}
</main></body></html>
`))
