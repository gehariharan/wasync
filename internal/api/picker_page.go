package api

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/prefs"
)

// The Choose chats page without script: the same list as /ui/api/chats,
// through links and buttons. Each Sync, Stop or Remove button saves that one
// chat at once, so nothing ticked is lost by searching or changing tabs.

type pickRow struct {
	ID, Name, Last string
	Syncing        bool
}

type pickTab struct {
	Label, Href string
	N           int
	Current     bool
}

type pickData struct {
	uiData
	Paired         bool
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
}

// pickHref is the page address for a tab, search and number of rows shown.
func pickHref(tab, query string, show int) string {
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
		return "/ui/chats/pick"
	}
	return "/ui/chats/pick?" + v.Encode()
}

func pickShow(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < pickPage {
		return pickPage
	}
	return min(n, pickMaxShow)
}

func pickRows(chats []pickChat) []pickRow {
	rows := make([]pickRow, 0, len(chats))
	for _, c := range chats {
		row := pickRow{ID: c.ID, Name: c.Name, Syncing: c.Syncing}
		if c.LastActive != nil {
			row.Last = shortTime(*c.LastActive)
		}
		rows = append(rows, row)
	}
	return rows
}

func (h *handler) pickPage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	d := &pickData{uiData: uiData{CSRF: sess.csrf, Title: "wasync chats to sync", Wide: true}}
	if n, ok := notices[r.URL.Query().Get("n")]; ok {
		d.Notice, d.NoticeOK = n.text, n.ok
	}
	q := r.URL.Query()
	d.Tab, d.Query, d.Show = pickTabKey(q.Get("tab")), humancheck.Clean(q.Get("q"), 60), pickShow(q.Get("show"))
	list, err := h.listPick(r.Context(), pickQuery{Tab: d.Tab, Query: d.Query, Limit: d.Show})
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	if d.Paired = list.Paired; !d.Paired {
		h.webRender(w, r, pickTemplate, d)
		return
	}
	d.Syncing, d.Total, d.Rows = list.Syncing, list.Total, pickRows(list.Chats)
	for _, t := range []struct {
		key, label string
		n          int
	}{{pickTabAll, "All", list.Counts.All}, {pickTabGroups, "Groups", list.Counts.Groups}, {pickTabPeople, "People", list.Counts.People}} {
		d.Tabs = append(d.Tabs, pickTab{Label: t.label, Href: pickHref(t.key, d.Query, pickPage), N: t.n, Current: t.key == d.Tab})
	}
	if list.Unnamed != nil {
		d.Unnamed, d.UnnamedHref = list.Unnamed.Count, pickHref(pickTabUnnamed, "", pickPage)
	}
	if list.NextOffset != nil {
		if d.Show < pickMaxShow {
			d.MoreHref = pickHref(d.Tab, d.Query, d.Show+pickPage)
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
// chat, action=clearall to stop syncing everything, or action=media for the
// "Also save photos, videos and files" box.
func (h *handler) pickSave(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	back := func(notice string) {
		to := pickHref(pickTabKey(r.PostForm.Get("tab")), humancheck.Clean(r.PostForm.Get("q"), 60), pickShow(r.PostForm.Get("show")))
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		http.Redirect(w, r, to+sep+"n="+url.QueryEscape(notice), http.StatusSeeOther)
	}
	if r.PostForm.Get("action") == "media" {
		on := r.PostForm.Get("save_media") == "on"
		if _, err := h.prefs.Update(func(s *prefs.Settings) { s.SaveMedia = &on }); err != nil {
			back("failed")
			return
		}
		back("saved")
		return
	}
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil {
		back("unpaired")
		return
	}
	var ch pickChange
	switch {
	case r.PostForm.Get("action") == "clearall":
		ch.All = true
	case r.PostForm.Get("sync") != "":
		ch.Chats, ch.Sync = []string{r.PostForm.Get("sync")}, true
		// The page's box says whether to ask for older messages; unticked
		// boxes are not sent, so the box's presence is marked separately.
		ch.History = r.PostForm.Get("history") == "yes" || r.PostForm.Get("historybox") != "1" && h.historyDefault()
	case r.PostForm.Get("stop") != "":
		ch.Chats = []string{r.PostForm.Get("stop")}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if _, err = h.applyPick(r.Context(), account.ID, ch); err != nil {
		if errors.Is(err, errUnknownChat) {
			back("invalid")
			return
		}
		back("failed")
		return
	}
	back("saved")
}

var pickTemplate = template.Must(template.New("pick").Funcs(uiFuncs).Parse(uiHead + `{{template "head" .}}
<h1>Choose chats to sync</h1>
{{template "notice" .}}
{{if not .Paired}}<p class="card mut">No WhatsApp is linked yet. Link it on the <a href="/ui/setup">Set up</a> page first.</p>
{{else}}
<p class="mut"><strong>{{.Syncing}} of {{.Total}} syncing.</strong> wasync keeps only the chats you sync, on this computer. Agents you connect can use these chats.</p>
<form class="search" method="get" action="/ui/chats/pick" role="search">{{if ne .Tab "all"}}<input type="hidden" name="tab" value="{{.Tab}}">{{end}}<input type="search" name="q" value="{{.Query}}" placeholder="Search names and numbers" aria-label="Search chats"> <button>Search</button></form>
<div class="tabs">{{range .Tabs}}<a href="{{.Href}}"{{if .Current}} aria-current="true"{{end}}>{{.Label}} {{.N}}</a>{{end}}</div>
<form method="post" action="/ui/chats/pick">
<input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="tab" value="{{.Tab}}"><input type="hidden" name="q" value="{{.Query}}"><input type="hidden" name="show" value="{{.Show}}"><input type="hidden" name="historybox" value="1">
<div class="pickbox">
{{range .Rows}}<div class="pickrow">{{if .Syncing}}<button name="stop" value="{{.ID}}" aria-label="Stop syncing {{.Name}}">Stop</button>{{else}}<button name="sync" value="{{.ID}}" aria-label="Sync {{.Name}}">Sync</button>{{end}}<span>{{.Name}}</span><span class="sub">{{.Last}}</span></div>
{{else}}{{if not .Unnamed}}<p class="empty">No chats match.</p>{{end}}{{end}}
{{if .Unnamed}}<div class="pickrow"><span></span><a href="{{.UnnamedHref}}">{{.Unnamed}} chats without a name</a><span></span></div>{{end}}
</div>
{{if .MoreHref}}<p><a href="{{.MoreHref}}">Show 50 more</a></p>{{else if .Narrow}}<p class="mut">More chats match. Search to narrow the list.</p>{{end}}
<label><input type="checkbox" name="history" value="yes"{{if .History}} checked{{end}}> Also ask your phone for older messages when a chat starts syncing (best effort)</label>
<h2>Selected · {{.Syncing}}</h2>
<div class="pickbox">
{{range .Selected}}<div class="pickrow"><button name="stop" value="{{.ID}}" aria-label="Remove {{.Name}}">Remove</button><span>{{.Name}}</span><span class="sub">{{.Last}}</span></div>
{{else}}<p class="empty">Nothing is syncing yet.</p>{{end}}
</div>
{{if .Selected}}<div class="btn-row"><button name="action" value="clearall">Clear all</button></div>{{end}}
</form>
<form method="post" action="/ui/chats/pick"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="tab" value="{{.Tab}}"><input type="hidden" name="q" value="{{.Query}}"><input type="hidden" name="show" value="{{.Show}}"><input type="hidden" name="action" value="media">
<label><input type="checkbox" name="save_media" value="on"{{if .SaveMedia}} checked{{end}}> Also save photos, videos and files</label> <span class="mut">Untick to keep text only.</span> <button>Save</button></form>
<p><a href="/ui/chats">Back to chats</a></p>
{{end}}
</main></body></html>
`))
