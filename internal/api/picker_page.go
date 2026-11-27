package api

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/prefs"
)

// The Choose chats page without script: the same list as /ui/api/chats,
// through links and buttons. Each Sync, Stop or Remove button saves that one
// chat at once, so nothing ticked is lost by searching or changing tabs.

type pickRow struct {
	ID, Name, Last, Picture string
	Syncing                 bool
}

type pickTab struct {
	Label, Href string
	N           int
	Current     bool
}

type pickData struct {
	uiData
	Paired bool
	// InSetup is step 2 of setup; Base is the page's own address.
	InSetup        bool
	Base           string
	Tabs           []pickTab
	Tab, Query     string
	Show           int
	Rows           []pickRow
	Unnamed        int
	UnnamedHref    string
	MoreHref       string
	Narrow         bool
	Selected       []pickRow
	Syncing, Total int
	History        bool
	SaveMedia      bool
	// DoneHref is where Done goes outside setup.
	DoneHref string
}

const (
	pickPagePath  = "/ui/chats/pick"
	pickSetupPath = "/ui/setup/chats"
)

// pickHref is the page address for a tab, search and number of rows shown.
func pickHref(base, tab, query string, show int) string {
	v := url.Values{}
	if tab != pickTabAll {
		v.Set("tab", tab)
	}
	if query != "" {
		v.Set("q", query)
	}
	if show > pickPage {
		v.Set("show", strconv.Itoa(show))
	}
	if len(v) == 0 {
		return base
	}
	return base + "?" + v.Encode()
}

func pickShow(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < pickPage {
		return pickPage
	}
	return min(n, pickMaxShow)
}

// lastActive says when a chat was last active in a few words.
func lastActive(t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h ago"
	case d < 48*time.Hour:
		return "Yesterday"
	}
	return t.Local().Format("2 Jan")
}

func pickRows(chats []pickChat) []pickRow {
	rows := make([]pickRow, 0, len(chats))
	now := time.Now()
	for _, c := range chats {
		row := pickRow{ID: c.ID, Name: c.Name, Syncing: c.Syncing, Picture: c.Picture}
		if c.LastActive != nil {
			row.Last = lastActive(*c.LastActive, now)
		}
		rows = append(rows, row)
	}
	return rows
}

func (h *handler) pickPage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	h.pickView(w, r, sess, false)
}

func (h *handler) chatsStep(w http.ResponseWriter, r *http.Request, sess uiSession) {
	h.pickView(w, r, sess, true)
}

func (h *handler) pickView(w http.ResponseWriter, r *http.Request, sess uiSession, inSetup bool) {
	d := &pickData{uiData: uiData{CSRF: sess.csrf, Title: "Choose chats to sync · wasync", Wide: true, Scripts: []string{"/ui/picker.js"}}, InSetup: inSetup, Base: pickPagePath, DoneHref: h.homePath()}
	if inSetup {
		d.Base, d.Setup = pickSetupPath, h.setupFrameFor(stepChats, true)
	}
	h.noticeFrom(r, &d.uiData)
	q := r.URL.Query()
	d.Tab, d.Query, d.Show = pickTabKey(q.Get("tab")), humancheck.Clean(q.Get("q"), 60), pickShow(q.Get("show"))
	if d.Tab == pickTabSelected {
		d.Tab = pickTabAll // the Selected list has its own panel
	}
	list, err := h.listPick(r.Context(), pickQuery{Tab: d.Tab, Query: d.Query, Limit: d.Show})
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	if d.Paired = list.Paired; !d.Paired {
		if inSetup {
			http.Redirect(w, r, "/ui/setup/link", http.StatusSeeOther)
			return
		}
		h.webRender(w, r, pickTemplate, d)
		return
	}
	d.Syncing, d.Total, d.Rows = list.Syncing, list.Total, pickRows(list.Chats)
	for _, t := range []struct {
		key, label string
		n          int
	}{{pickTabAll, "All", list.Counts.All}, {pickTabGroups, "Groups", list.Counts.Groups}, {pickTabPeople, "People", list.Counts.People}} {
		d.Tabs = append(d.Tabs, pickTab{Label: t.label, Href: pickHref(d.Base, t.key, d.Query, pickPage), N: t.n, Current: t.key == d.Tab})
	}
	if list.Unnamed != nil {
		d.Unnamed, d.UnnamedHref = list.Unnamed.Count, pickHref(d.Base, pickTabUnnamed, "", pickPage)
	}
	if list.NextOffset != nil {
		if d.Show < pickMaxShow {
			d.MoreHref = pickHref(d.Base, d.Tab, d.Query, d.Show+pickPage)
		} else {
			d.Narrow = true
		}
	}
	selected, err := h.listPick(r.Context(), pickQuery{Tab: pickTabSelected, Limit: pickMaxShow})
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	d.Selected = pickRows(selected.Chats)
	s, _ := h.prefs.Get()
	d.History, d.SaveMedia = !s.AgentOnly(), s.MediaOn()
	h.webRender(w, r, pickTemplate, d)
}

// pickSave saves one change from the page: sync=CHAT or stop=CHAT for one
// chat, action=clearall to stop syncing everything, action=media for the
// "Also save photos, videos and files" box, or action=next (setup) and
// action=done, which save the box and move on.
func (h *handler) pickSave(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	base := pickPagePath
	if r.URL.Path == pickSetupPath {
		base = pickSetupPath
	}
	back := func(notice, chat string) {
		to := pickHref(base, pickTabKey(r.PostForm.Get("tab")), humancheck.Clean(r.PostForm.Get("q"), 60), pickShow(r.PostForm.Get("show")))
		if notice != "" {
			sep := "?"
			if strings.Contains(to, "?") {
				sep = "&"
			}
			to += sep + "n=" + url.QueryEscape(notice)
		}
		if chat != "" && safeFragment(chat) {
			to += "#c-" + chat // back to the row that was pressed
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	}
	switch action := r.PostForm.Get("action"); action {
	case "media", "next", "done":
		on := r.PostForm.Get("save_media") == "on"
		if _, err := h.prefs.Update(func(s *prefs.Settings) { s.SaveMedia = &on }); err != nil {
			back("failed", "")
			return
		}
		switch action {
		case "next":
			http.Redirect(w, r, "/ui/setup/running", http.StatusSeeOther)
		case "done":
			http.Redirect(w, r, h.homePath(), http.StatusSeeOther)
		default:
			back("saved", "")
		}
		return
	}
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil {
		back("unpaired", "")
		return
	}
	var ch pickChange
	chat := ""
	switch {
	case r.PostForm.Get("action") == "clearall":
		ch.All = true
	case r.PostForm.Get("sync") != "":
		chat = r.PostForm.Get("sync")
		ch.Chats, ch.Sync = []string{chat}, true
		// The page's box says whether to ask for older messages; unticked
		// boxes are not sent, so the box's presence is marked separately.
		ch.History = r.PostForm.Get("history") == "yes" || r.PostForm.Get("historybox") != "1" && h.historyDefault()
	case r.PostForm.Get("stop") != "":
		chat = r.PostForm.Get("stop")
		ch.Chats = []string{chat}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if _, err = h.applyPick(r.Context(), account.ID, ch); err != nil {
		if errors.Is(err, errUnknownChat) {
			back("invalid", "")
			return
		}
		back("failed", "")
		return
	}
	back("", chat)
}

// safeFragment reports whether a chat ID can be put in an address's fragment
// as it is: WhatsApp's IDs are digits, letters, dots, dashes and one @.
func safeFragment(id string) bool {
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '@' || r == '.' || r == '-' || r == '_' || r == ':') {
			return false
		}
	}
	return id != ""
}

var pickTemplate = template.Must(template.New("pick").Funcs(uiFuncs).Parse(uiHead + `{{template "head" .}}
<div class="stage">
<h1>Choose chats to sync</h1>
{{template "notice" .}}
{{if not .Paired}}<p class="card mut">No WhatsApp is linked yet. <a href="/ui/setup">Link it</a> first.</p>
{{else}}
<p class="lead">wasync copies these chats to this computer and keeps them up to date, for you and your agents. Nothing changes on your phone.</p>
<form method="post" action="{{.Base}}" id="pick" data-csrf="{{.CSRF}}">
<input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="tab" value="{{.Tab}}"><input type="hidden" name="q" value="{{.Query}}"><input type="hidden" name="show" value="{{.Show}}"><input type="hidden" name="historybox" value="1">
<div class="pick-grid">
<div class="pick-find">
<div class="search" role="search"><input type="search" name="q" value="{{.Query}}" placeholder="Search {{.Total}} chats by name or number" aria-label="Search chats" form="pick-search"> <button form="pick-search">Search</button></div>
<nav class="pick-tabs" aria-label="Kinds of chat">{{range .Tabs}}<a href="{{.Href}}"{{if .Current}} aria-current="true"{{end}}>{{.Label}}<span class="n">{{.N}}</span></a>{{end}}</nav>
<div class="pick-list">
{{range .Rows}}<button type="submit" class="pick-row" id="c-{{.ID}}" name="{{if .Syncing}}stop{{else}}sync{{end}}" value="{{.ID}}" aria-pressed="{{.Syncing}}" aria-label="{{if .Syncing}}Stop syncing{{else}}Sync{{end}} {{.Name}}" data-chat="{{.ID}}" data-name="{{.Name}}" data-picture="{{.Picture}}"><span class="tick" aria-hidden="true"></span><img class="avatar" src="{{.Picture}}" alt="" width="36" height="36" loading="lazy"><span class="nm">{{.Name}}</span><span class="sub">{{.Last}}</span></button>
{{else}}{{if not .Unnamed}}<p class="empty">No chats match.</p>{{end}}{{end}}
{{if .Unnamed}}<a class="pick-fold" href="{{.UnnamedHref}}">{{.Unnamed}} chats without a name</a>{{end}}
{{if .MoreHref}}<p class="pick-more"><a href="{{.MoreHref}}">Show 50 more</a></p>{{else if .Narrow}}<p class="pick-more mut">More chats match. Search to narrow the list.</p>{{end}}
</div>
<label class="sub"><input type="checkbox" name="history" value="yes"{{if .History}} checked{{end}}> Also ask your phone for older messages when a chat starts syncing (best effort)</label>
</div>
<aside class="selected" aria-label="Selected chats">
<div class="selected-head"><h2>Selected · <span data-count>{{.Syncing}}</span></h2>{{if .Selected}}<button name="action" value="clearall" class="ghost">Clear</button>{{end}}</div>
<ul class="selected-list" data-selected>
{{range .Selected}}<li data-chat="{{.ID}}"><img class="avatar sm" src="{{.Picture}}" alt="" width="26" height="26" loading="lazy"><span class="nm">{{.Name}}</span><button name="stop" value="{{.ID}}" aria-label="Remove {{.Name}}" title="Remove">×</button></li>
{{else}}<li class="mut" data-empty>Nothing is syncing yet.</li>{{end}}
</ul>
<div class="selected-foot">
<label><input type="checkbox" name="save_media" value="on"{{if .SaveMedia}} checked{{end}}> Also save photos, videos and files<span class="sub">Untick to save text only.</span></label>
{{if .InSetup}}<div class="row-btns"><a class="btn ghost" href="/ui/setup/link">← Back</a><button class="go" name="action" value="next">Next →</button></div>
{{else}}<button class="go" name="action" value="done">Done</button>{{end}}
</div>
</aside>
</div>
</form>
<form id="pick-search" method="get" action="{{.Base}}">{{if ne .Tab "all"}}<input type="hidden" name="tab" value="{{.Tab}}">{{end}}</form>
{{end}}
</div>
</main></body></html>
`))
