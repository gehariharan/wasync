package api

import (
	"context"
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

// readingAgents lists the active agents that may read message text, with
// the chats their approved recipient lists let them read.
func (h *handler) readingAgents(ctx context.Context) []agentauth.Client {
	clients, _ := agentauth.List(h.dataDir)
	now := time.Now()
	var out []agentauth.Client
	for _, c := range clients {
		if c.Status(now) == "active" && c.HasScope(agentauth.ScopeReadMessages) {
			if c.Restricted() {
				c.ListChats = h.listReads(ctx, c.ID)
			}
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

func (h *handler) webRender(w http.ResponseWriter, r *http.Request, t *template.Template, data any) {
	h.frameFor(r, data)
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
		h.webRender(w, r, chatsTemplate, d)
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
	agents := h.readingAgents(r.Context())
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
			case m.Source == storage.SourceSentLog:
				msg.Text = "(list message sent before wasync kept it in its chat; text not kept)"
			default:
				msg.Text = "[" + humancheck.Clean(m.Type, 30) + "]"
			}
			switch {
			case m.FromMe && m.SentBy != "":
				msg.Who = "🤖 " + humancheck.Clean(m.SentBy, 40)
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
	h.webRender(w, r, chatsTemplate, d)
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
		h.webRender(w, r, pickTemplate, d)
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
	h.webRender(w, r, pickTemplate, d)
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
	Time, Who, To, Text, Outcome          string
	Agent, Problem, Open, List, NewNumber bool
	Kind                                  string
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
		h.webRender(w, r, sentTemplate, d)
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
		row := sentRow{Time: e.Time.Local().Format("2 Jan 15:04"), Kind: e.Kind, To: names[e.Chat], Agent: e.Actor != sentlog.Owner, Open: e.Open, List: e.List, NewNumber: e.NewNumber}
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
	h.webRender(w, r, sentTemplate, d)
}

// attView is what the attachment template needs: the attachment and the page
// it sits on, for the form token and the open chat.
type attView struct {
	D *chatsData
	A *chatAttachment
}

// uiAttachment draws one attachment inside a chat bubble. A photo or video
// keeps its shape from its pixel size, given as width and height, before it
// loads; one not downloaded yet holds that shape with an empty drawing of the
// same proportions, so no style is needed on the page.
const uiAttachment = `{{define "dlbtn"}}{{if and .D.CanDownload .D.Cur.Syncing}}<form class="dl" method="post" action="/ui/chats/media"><input type="hidden" name="csrf" value="{{.D.CSRF}}"><input type="hidden" name="chat" value="{{.D.Cur.ID}}"><input type="hidden" name="msg" value="{{.A.MsgID}}"><button name="action" value="download">{{if eq .A.State "failed"}}Try again{{else}}Download{{end}}</button></form>{{end}}{{end}}
{{define "attstate"}}{{if eq .A.State "pending"}}<span class="sub">Downloading… refresh to see it</span>{{else if eq .A.State "failed"}}<span class="sub">Couldn't download</span>{{end}}{{end}}
{{define "att"}}{{$a := .A}}{{if eq $a.State "ready"}}{{if eq $a.Show "img"}}<a href="{{$a.URL}}" target="_blank" rel="noopener"><img class="media" src="{{$a.URL}}" alt="{{$a.Label}}"{{if $a.W}} width="{{$a.W}}" height="{{$a.H}}"{{end}}></a>{{else if eq $a.Show "video"}}<video class="media" controls preload="metadata" src="{{$a.URL}}"{{if $a.W}} width="{{$a.W}}" height="{{$a.H}}"{{end}}></video>{{else if eq $a.Show "audio"}}<audio controls preload="metadata" src="{{$a.URL}}" aria-label="{{$a.Label}}"></audio><span class="sub block">{{$a.Icon}} {{$a.Label}}{{if $a.Meta}} · {{$a.Meta}}{{end}}</span>{{else}}<div class="file"><span class="ico">{{$a.Icon}}</span><span><span class="fn">{{$a.Name}}</span><span class="sub">{{$a.Meta}}</span></span><a class="open" href="{{$a.URL}}" target="_blank" rel="noopener">Open</a></div>{{end}}{{else if and (or (eq $a.Kind "image") (eq $a.Kind "video")) $a.W}}<div class="ph"><svg class="ph-size" viewBox="0 0 {{$a.W}} {{$a.H}}" aria-hidden="true" focusable="false"></svg><span class="ph-body">{{$a.Icon}}<br>{{$a.Label}}{{if $a.Meta}} · {{$a.Meta}}{{end}}<br>{{template "attstate" .}}{{if ne $a.State "pending"}} {{template "dlbtn" .}}{{end}}</span></div>{{else}}<div class="file"><span class="ico">{{$a.Icon}}</span><span><span class="fn">{{$a.Name}}</span><span class="sub">{{$a.Meta}}</span> {{template "attstate" .}}</span><span>{{if ne $a.State "pending"}}{{template "dlbtn" .}}{{end}}</span></div>{{end}}{{end}}`

var chatsTemplate = template.Must(template.New("chats").Funcs(uiFuncs).Funcs(template.FuncMap{"attctx": func(d *chatsData, a *chatAttachment) attView { return attView{d, a} }}).Parse(uiHead + uiAttachment + `{{template "head" .}}
{{template "notice" .}}
{{if not .Paired}}<h1>Chats</h1><p class="card mut">No WhatsApp is linked yet. Link it on the <a href="/ui/setup">Set up</a> page first.</p>
{{else}}
<div class="app{{if .Picked}} picked{{end}}">
<div class="list">
<div class="tools">
<a class="ic" href="/ui/chats{{if not .All}}?all=1{{end}}" title="{{if .All}}Show syncing chats only{{else}}Show all chats{{end}}" aria-label="{{if .All}}Show syncing chats only{{else}}Show all chats{{end}}" aria-pressed="{{.All}}">{{template "icon" "all"}}</a>
<a class="ic" href="/ui/chats/pick" title="Choose chats to sync" aria-label="Choose chats to sync">{{template "icon" "pick"}}</a>
<a class="ic" href="/ui/chats/new" title="New message" aria-label="New message">{{template "icon" "new"}}</a>
<span class="sub">{{.Syncing}} of {{.Total}} syncing</span>
</div>
{{if .New}}<a class="row" href="/ui/chats/new" aria-current="true"><span class="av">{{template "icon" "new"}}</span><span class="nm">New message</span><span></span></a>{{end}}
{{range .Rows}}<a class="row" href="/ui/chats?c={{.ID}}{{if $.All}}&amp;all=1{{end}}"{{if .Selected}} aria-current="true"{{end}}>
<span class="av">{{.Initials}}{{if .Agent}}<span class="bot" title="Agents can read this chat" aria-label="Agents can read this chat">🤖</span>{{end}}</span>
<span><span class="nm">{{.Name}}</span><span class="sub">{{if .Syncing}}{{.Count}} messages{{else}}not syncing{{end}}</span></span>
<span class="sub">{{.Last}}</span></a>
{{else}}<p class="empty">No chats are syncing yet. Use the <a href="/ui/chats/pick">choose chats</a> button to pick some.</p>{{end}}
{{if .More}}<p class="empty sub">{{.More}} more chats are not listed.</p>{{end}}
</div>
<div class="pane">
{{if .New}}
<div class="head"><span><a class="back" href="/ui/chats{{if .All}}?all=1{{end}}" aria-label="Back to chats">‹</a> <strong>New message</strong></span></div>
{{if .CanSend}}<form class="newmsg" method="post" action="/ui/chats/send"><input type="hidden" name="csrf" value="{{.CSRF}}">
<label class="to">To <input name="to" placeholder="+15551230000" aria-label="Phone number, with its country code" autocomplete="off" required autofocus></label>
<div class="thread"><p class="mut">Sent once, as you; it shows in <a href="/ui/sent">Sent</a>.</p></div>
<div class="compose"><textarea name="text" rows="2" maxlength="16000" required placeholder="Message" aria-label="Message"></textarea><button class="go">Send</button></div></form>
{{else}}<p class="empty bad">wasync is not connected to WhatsApp, so nothing can be sent. Start it with <code>wasync serve</code> or <code>wasync tui</code> on a linked account.</p>{{end}}
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
<textarea name="text" rows="2" maxlength="16000" required placeholder="Message {{.Cur.Name}}" aria-label="Message {{.Cur.Name}}"></textarea><button class="go">Send</button></form>{{end}}
{{else}}<p class="empty">Choose a chat on the left.</p>{{end}}
</div>
</div>
{{end}}
</main></body></html>
`))

var sentTemplate = template.Must(template.New("sent").Funcs(uiFuncs).Parse(uiHead + `{{template "head" .}}
<h1>Sent</h1>
{{template "notice" .}}
{{if not .Paired}}<p class="card mut">No WhatsApp is linked yet. Link it on the <a href="/ui/setup">Set up</a> page first.</p>
{{else}}
<p class="mut">What was sent from this computer, by you or by an agent, newest first. WhatsApp's own tick is not tracked here: "Sent" means WhatsApp accepted the message, not that it was delivered or read. The text is read from your archive; the log itself keeps no message text.</p>
<div class="table-wrap"><table class="table">
<thead><tr><th>Time</th><th>By</th><th>To</th><th>Message</th><th>Outcome</th></tr></thead>
<tbody>
{{range .Rows}}<tr{{if .Problem}} class="problem"{{end}}><td>{{.Time}}</td><td>{{if .Agent}}🤖 {{end}}{{.Who}}</td><td>{{.To}}</td><td>{{if .Text}}{{.Text}}{{else}}<span class="mut">text not kept</span>{{end}}{{if eq .Kind "reply"}} <span class="mut">(reply)</span>{{end}}{{if .Open}} <span class="mut">(open sending{{if .NewNumber}}, new number{{end}})</span>{{end}}{{if .List}} <span class="mut">(approved list{{if .NewNumber}}, first message{{end}})</span>{{end}}</td><td>{{.Outcome}}</td></tr>
{{else}}<tr><td colspan="5" class="mut">Nothing has been sent from this computer yet.</td></tr>{{end}}
</tbody></table></div>
{{end}}
</main></body></html>
`))

var pickTemplate = template.Must(template.New("pick").Funcs(uiFuncs).Parse(uiHead + `{{template "head" .}}
<h1>Choose chats to sync</h1>
{{template "notice" .}}
{{if not .Paired}}<p class="card mut">No WhatsApp is linked yet. Link it on the <a href="/ui/setup">Set up</a> page first.</p>
{{else}}
<p class="mut"><strong>{{.Syncing}} of {{.Total}} syncing.</strong> wasync keeps only the chats you tick, on this computer. Search first, then tick the ones you want.</p>
<form class="search" method="get" action="/ui/chats/pick" role="search"><input type="hidden" name="tab" value="{{.Tab}}"><input type="search" name="q" value="{{.Query}}" placeholder="Search chats" aria-label="Search chats"> <button>Search</button></form>
<div class="tabs">{{range .Tabs}}<a href="/ui/chats/pick?tab={{.Key}}{{if $.Query}}&amp;q={{$.Query}}{{end}}"{{if .Current}} aria-current="true"{{end}}>{{.Label}} {{.N}}</a>{{end}}</div>
<form method="post" action="/ui/chats/pick">
<input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="tab" value="{{.Tab}}"><input type="hidden" name="q" value="{{.Query}}">
<div class="pickbox">
{{range .Rows}}<label class="pickrow"><input type="checkbox" name="chat" value="{{.ID}}"{{if .Syncing}} checked{{end}}><span>{{.Name}}</span><span class="sub">{{.Count}} messages</span><input type="hidden" name="shown" value="{{.ID}}"></label>
{{else}}<p class="empty">No chats match.</p>{{end}}
</div>
{{if .More}}<p class="mut">{{.More}} more match. Narrow the search to see them.</p>{{end}}
<label><input type="checkbox" name="history" value="yes" checked> Also ask your phone for older messages when a chat is newly ticked (best effort)</label>
<div class="btn-row"><button class="go" name="action" value="save">Save</button> <button name="action" value="tickshown">Tick all shown</button> <button name="action" value="clearshown">Clear all shown</button> <button name="action" value="clearall">Stop syncing everything</button></div>
</form>
<p><a href="/ui/chats">Back to chats</a></p>
{{end}}
</main></body></html>
`))
