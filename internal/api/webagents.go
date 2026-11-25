package api

import (
	"crypto/subtle"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/opensend"
	"github.com/gehariharan/wasync/internal/sendlist"
	"github.com/gehariharan/wasync/internal/sentlog"
)

// The Agents page shows which agents exist, whether each may send, the open
// sending switch and the approved recipient lists. Widening what an agent may
// do (letting it send, turning open sending on, approving a list) ends in the
// operating system's confirmation prompt, like every other approval in the web
// app, and is read-only on a computer that cannot show one. Narrowing
// (stopping sending, turning open sending off, removing a list) needs no
// prompt: stopping is always safe.

type agentRow struct {
	Name, Scopes string
	CanSend      bool
	ReadsAll     bool
	Chats        int
	Expires      string
}

type openView struct {
	On                  bool
	Until               string
	Used, Cap           int
	NewUsed, NewCap     int
	Minutes             int
	MaxMessages, MaxNew int
	Recent              []sentRow
	Gate                bool
	GateName            string
	Paused              string
}

// listRow is one approved recipient list.
type listRow struct {
	Agent                                  string
	Numbers, NotMessaged, Stopped, PerHour int
	Until                                  string
	Gone                                   bool // its agent is no longer active
}

type listsView struct {
	Rows    []listRow
	Senders []string // agents that may be given a list
	Paces   []int    // the new-numbers-an-hour choices
}

type agentsData struct {
	uiData
	Agents []agentRow
	Open   openView
	Lists  listsView
}

func (h *handler) agentsPage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	d := &agentsData{uiData: uiData{CSRF: sess.csrf, Title: "wasync agents", Wide: true, Remote: remoteLogin(r) != ""}}
	if n, ok := notices[r.URL.Query().Get("n")]; ok {
		d.Notice, d.NoticeOK = n.text, n.ok
	}
	now := time.Now()
	clients, err := agentauth.List(h.dataDir)
	if err != nil {
		http.Error(w, "agent registry unavailable", http.StatusInternalServerError)
		return
	}
	for i := range clients {
		c := &clients[i]
		if c.Status(now) != "active" {
			continue
		}
		row := agentRow{Name: humancheck.Clean(c.Name, 40), CanSend: c.HasScope(agentauth.ScopeSend), ReadsAll: !c.Restricted(), Chats: len(c.AllowedChats())}
		for j, s := range c.Scopes {
			if j > 0 {
				row.Scopes += ", "
			}
			row.Scopes += humancheck.Clean(s, 40)
		}
		if c.ExpiresAt != nil {
			row.Expires = c.ExpiresAt.Local().Format("2 Jan 15:04")
		}
		d.Agents = append(d.Agents, row)
		if row.CanSend {
			d.Lists.Senders = append(d.Lists.Senders, row.Name)
		}
	}
	for n := sendlist.MinPerHour; n <= sendlist.MaxPerHour; n += 5 {
		d.Lists.Paces = append(d.Lists.Paces, n)
	}
	_ = h.dropStopped(r.Context(), now, "")
	if lists, err := sendlist.Load(h.dataDir); err == nil {
		active := map[string]bool{}
		for _, c := range clients {
			active[c.ID] = c.Status(now) == "active"
		}
		for _, l := range lists {
			row := listRow{Agent: humancheck.Clean(l.Agent, 40), Numbers: len(l.Numbers), NotMessaged: l.NotMessaged(), Stopped: l.Stopped, PerHour: l.NewPerHour(), Until: "no end", Gone: !active[l.AgentID]}
			if l.Until != nil {
				row.Until = l.Until.Local().Format("2 Jan 15:04")
				if !l.Active(now) {
					row.Until = "ended " + row.Until
				}
			}
			d.Lists.Rows = append(d.Lists.Rows, row)
		}
	}
	d.Open = openView{Gate: h.gate(r).Enabled(), MaxMessages: opensend.MaxMessages, MaxNew: opensend.MaxNewNumbers, Cap: opensend.MaxMessages, NewCap: opensend.MaxNewNumbers}
	if d.Open.Gate {
		d.Open.GateName = h.human.Name()
	}
	if state, on := opensend.ActiveNow(h.dataDir, now); on {
		d.Open.On = true
		d.Open.Until = state.Until.Local().Format("15:04")
		d.Open.Minutes = int(state.Until.Sub(now).Minutes()) + 1
		since := now.Add(-opensend.Window)
		fresh := map[string]bool{}
		if entries, err := h.sent.Recent(1000); err == nil {
			for _, e := range entries {
				if !e.Open || e.Time.Before(since) {
					continue
				}
				d.Open.Used++
				if e.NewNumber {
					fresh[e.Chat] = true
				}
			}
		}
		d.Open.NewUsed = len(fresh)
	}
	if entries, err := h.sent.Recent(1000); err == nil {
		names := h.chatNames(r)
		for _, e := range entries {
			if !e.Open || len(d.Open.Recent) >= 10 {
				continue
			}
			row := sentRow{Time: e.Time.Local().Format("2 Jan 15:04"), Who: humancheck.Clean(e.Actor, 40), Agent: true, To: names[e.Chat], Outcome: sentOutcome(e), Problem: e.Outcome != sentlog.OutcomeSent}
			if row.To == "" {
				row.To = humancheck.Clean(e.Chat, 80)
			}
			d.Open.Recent = append(d.Open.Recent, row)
		}
	}
	h.webRender(w, agentsTemplate, d)
}

// chatNames maps chat IDs to the archive's names for them.
func (h *handler) chatNames(r *http.Request) map[string]string {
	names := map[string]string{}
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil {
		return names
	}
	groups, err := h.store.GroupSummaries(r.Context(), account.ID)
	if err != nil {
		return names
	}
	for _, g := range groups {
		names[g.ID] = humancheck.Clean(g.Name, 80)
	}
	return names
}

// sentOutcome is the plain-words outcome of one sent-log entry. WhatsApp's own
// delivery and read ticks are not tracked.
func sentOutcome(e sentlog.Entry) string {
	switch e.Outcome {
	case sentlog.OutcomeSent:
		return "Sent: WhatsApp accepted it"
	case sentlog.OutcomeUnknown:
		return "Not confirmed: check WhatsApp before sending again"
	case sentlog.OutcomeBlocked:
		return "Blocked"
	}
	return "Not sent"
}

// listUpload is the most a list form may carry: the pasted text plus a file.
const listUpload = 256 << 10

// listEnds are the end choices offered for a list.
var listEnds = map[string]time.Duration{"1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "none": 0}

func (h *handler) agentsAct(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		r.Body = http.MaxBytesReader(w, r.Body, listUpload+8<<10)
		if err := r.ParseMultipartForm(listUpload); err != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(sess.csrf)) != 1 {
			http.Error(w, "invalid form", http.StatusForbidden)
			return
		}
		defer r.MultipartForm.RemoveAll()
	} else if !h.uiForm(w, r, sess) {
		return
	}
	back := func(notice string) {
		http.Redirect(w, r, "/ui/agents?n="+url.QueryEscape(notice), http.StatusSeeOther)
	}
	audit := func(event, agent string) {
		_ = agentauth.NewAuditLog(h.dataDir).Append(agentauth.AuditEntry{Event: event, ClientName: humancheck.Clean(agent, 40), Outcome: agentauth.OutcomeAllowed})
	}
	name := r.PostForm.Get("agent")
	switch r.PostForm.Get("action") {
	case "allowsend", "stopsend":
		if err := agentauth.ValidName(name); err != nil {
			back("invalid")
			return
		}
		if r.PostForm.Get("action") == "stopsend" {
			if _, err := agentauth.RemoveScope(h.dataDir, name, agentauth.ScopeSend, time.Now()); err != nil {
				back("failed")
				return
			}
			audit("scope_remove", name)
			back("sendoff")
			return
		}
		if !h.gate(r).Enabled() {
			back(noGate(r))
			return
		}
		// Check the change would be accepted before bothering the person.
		clients, err := agentauth.List(h.dataDir)
		if err != nil {
			back("failed")
			return
		}
		var target *agentauth.Client
		for i := range clients {
			if clients[i].Name == name && clients[i].Status(time.Now()) == "active" {
				target = &clients[i]
			}
		}
		switch {
		case target == nil:
			back("failed")
			return
		case !target.Restricted():
			back("agentall")
			return
		}
		where := "the chats you approve for it"
		if len(target.Chats) > 0 {
			where = fmt.Sprintf("the %d chat(s) it can already reach, and any you approve later", len(target.Chats))
		}
		if err := h.confirmError(r.Context(), "Approve: let the agent "+agentauth.QuotedName(name)+" send messages in "+where); err != nil {
			back(noticeFor(err))
			return
		}
		if _, err := agentauth.AddScope(h.dataDir, name, agentauth.ScopeSend, time.Now()); err != nil {
			back("failed")
			return
		}
		audit("scope_add", name)
		back("sendon")
	case "openon":
		if !h.gate(r).Enabled() {
			back(noGate(r))
			return
		}
		if err := h.confirmError(r.Context(), "Approve: "+opensend.Effect()); err != nil {
			back(noticeFor(err))
			return
		}
		if _, err := opensend.Start(h.dataDir, time.Now()); err != nil {
			back("failed")
			return
		}
		audit("open_send_on", "")
		back("openon")
	case "liston":
		h.listOn(w, r, name, back, audit)
	case "listoff":
		lists, err := sendlist.Load(h.dataDir)
		if err != nil {
			back("failed")
			return
		}
		for _, l := range lists {
			if l.Agent == name {
				if err = sendlist.Remove(h.dataDir, l.AgentID); err != nil {
					back("failed")
					return
				}
			}
		}
		audit("send_list_off", name)
		back("listoff")
	case "openoff":
		if err := opensend.Stop(h.dataDir); err != nil {
			back("failed")
			return
		}
		audit("open_send_off", "")
		back("openoff")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// listOn approves a recipient list for one agent after the system confirmation.
func (h *handler) listOn(w http.ResponseWriter, r *http.Request, name string, back func(string), audit func(string, string)) {
	if !h.gate(r).Enabled() {
		back(noGate(r))
		return
	}
	end, ok := listEnds[r.PostForm.Get("ends")]
	perHour, err := strconv.Atoi(r.PostForm.Get("rate"))
	if agentauth.ValidName(name) != nil || !ok || err != nil || !sendlist.ValidPerHour(perHour) {
		back("invalid")
		return
	}
	text := r.PostForm.Get("numbers")
	if r.MultipartForm != nil && len(r.MultipartForm.File["file"]) > 0 {
		f, err := r.MultipartForm.File["file"][0].Open()
		if err != nil {
			back("failed")
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, listUpload))
		f.Close()
		if err != nil {
			back("failed")
			return
		}
		text += "\n" + string(data)
	}
	chats, bad := sendlist.Parse(text)
	switch {
	case bad != "":
		back("badlist")
		return
	case len(chats) == 0:
		back("nolist")
		return
	case len(chats) > sendlist.MaxNumbers:
		back("biglist")
		return
	}
	target, err := agentauth.ActiveByName(h.dataDir, name)
	now := time.Now()
	if err != nil || target == nil || target.Status(now) != "active" {
		back("failed")
		return
	}
	if !target.HasScope(agentauth.ScopeSend) {
		back("needsend")
		return
	}
	list := sendlist.List{AgentID: target.ID, Agent: target.Name, GrantedAt: now.UTC(), PerHour: perHour}
	ends := "until you remove it"
	if end > 0 {
		until := now.Add(end).UTC().Truncate(time.Second)
		list.Until = &until
		ends = "until " + until.Local().Format("2 Jan 15:04")
	}
	examples := ""
	for i, c := range chats {
		if i == 3 {
			examples += fmt.Sprintf(" and %d more", len(chats)-3)
			break
		}
		if i > 0 {
			examples += ", "
		}
		examples += sendlist.Shown(c)
	}
	prompt := fmt.Sprintf("Approve: let the agent %s message these %d number(s), and no others, %s: %s. At most %d new numbers an hour, spaced at random, one message each until they reply.", agentauth.QuotedName(name), len(chats), ends, examples, perHour)
	if err = h.confirmError(r.Context(), prompt); err != nil {
		back(noticeFor(err))
		return
	}
	for _, c := range chats {
		list.Numbers = append(list.Numbers, sendlist.Number{Chat: c})
	}
	if err = sendlist.Put(h.dataDir, list); err != nil {
		back("failed")
		return
	}
	audit("send_list_on", name)
	back("liston")
}

var agentsTemplate = template.Must(template.New("agents").Parse(uiHead + `{{template "head" .}}
<h1>Agents</h1>
{{if .Notice}}<p role="status" class="{{if .NoticeOK}}ok{{else}}bad{{end}}"><strong>{{.Notice}}</strong></p>{{end}}
{{if not .Open.Gate}}{{if .Remote}}<p class="bad">Read-only from another computer. Stopping things still works.</p>{{else}}<p class="bad">This computer cannot show a system confirmation, so this page is read-only. Anything that gives an agent more power cannot be done here; stopping things still can.</p>{{end}}{{end}}

<h2>Open sending</h2>
{{if .Open.On}}
<p class="ok"><strong>On until {{.Open.Until}}</strong> ({{.Open.Minutes}} min left). Agents that can send may message any chat or number, up to {{.Open.MaxMessages}} messages and {{.Open.MaxNew}} new numbers an hour. Used this hour: {{.Open.Used}} of {{.Open.MaxMessages}} messages, {{.Open.NewUsed}} of {{.Open.MaxNew}} new numbers.</p>
<form method="post" action="/ui/agents"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="openoff"><button>Turn off now</button></form>
{{else}}
<p>Off. Agents can send only in the chats you approved for them. Turn it on to let them message anyone, including a new number, for one hour, up to {{.Open.MaxMessages}} messages and {{.Open.MaxNew}} new numbers an hour. Each message shows in <a href="/ui/sent">Sent</a>.</p>
{{if .Open.Gate}}<form method="post" action="/ui/agents"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="openon"><button>Turn on for 1 hour…</button> <span class="mut">Asks you to confirm with {{.Open.GateName}}.</span></form>{{end}}
{{end}}
{{if .Open.Recent}}<h3>Recent open sends</h3>
<table class="sent"><thead><tr><th>Time</th><th>By</th><th>To</th><th>Outcome</th></tr></thead><tbody>
{{range .Open.Recent}}<tr{{if .Problem}} class="problem"{{end}}><td>{{.Time}}</td><td>🤖 {{.Who}}</td><td>{{.To}}</td><td>{{.Outcome}}</td></tr>{{end}}
</tbody></table>{{end}}

<h2>Approved numbers</h2>
<p>An agent may message the numbers you approve for it here, and no others: as many new numbers an hour as you choose, spaced at random, one message each until they reply. A number that replies STOP is removed.</p>
{{if .Lists.Rows}}<table class="sent"><thead><tr><th>Agent</th><th>Numbers</th><th>New an hour</th><th>Ends</th><th></th></tr></thead><tbody>
{{range .Lists.Rows}}<tr><td>🤖 {{.Agent}}{{if .Gone}} <span class="mut">(no longer active)</span>{{end}}</td><td>{{.Numbers}} <span class="mut">({{.NotMessaged}} not messaged yet{{if .Stopped}}, {{.Stopped}} removed after STOP{{end}})</span></td><td>{{.PerHour}}</td><td>{{.Until}}</td>
<td><form method="post" action="/ui/agents" style="display:inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="listoff"><input type="hidden" name="agent" value="{{.Agent}}"><button>Remove list</button></form></td></tr>
{{end}}</tbody></table>{{end}}
{{if .Open.Gate}}{{if .Lists.Senders}}<form method="post" action="/ui/agents" enctype="multipart/form-data"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="liston">
<p><label>Agent <select name="agent">{{range .Lists.Senders}}<option>{{.}}</option>{{end}}</select></label></p>
<p><label>Numbers, one per line, with country code<br><textarea name="numbers" rows="4" cols="40" placeholder="+15551230000"></textarea></label></p>
<p><label>Or a file (.txt or .csv) <input type="file" name="file" accept=".txt,.csv,text/plain,text/csv"></label></p>
<p><label>New numbers an hour <select name="rate">{{range .Lists.Paces}}<option>{{.}}</option>{{end}}</select></label> <span class="mut">Spaced at random through the hour.</span></p>
<p><label>Ends <select name="ends"><option value="1d">in 1 day</option><option value="7d" selected>in 7 days</option><option value="30d">in 30 days</option><option value="none">never (until removed)</option></select></label></p>
<p><button>Approve list…</button> <span class="mut">Asks you to confirm with {{.Open.GateName}}. Approving again replaces the agent's list.</span></p></form>
{{else}}<p class="mut">Allow sending for an agent first.</p>{{end}}{{end}}

<h2>Agents</h2>
{{if .Agents}}<table class="sent"><thead><tr><th>Name</th><th>Allowed to</th><th>Sending</th></tr></thead><tbody>
{{range .Agents}}<tr><td>🤖 {{.Name}}{{if .Expires}} <span class="mut">expires {{.Expires}}</span>{{end}}</td><td>{{.Scopes}}{{if .ReadsAll}} <span class="mut">(reads every chat)</span>{{else}} <span class="mut">({{.Chats}} chat(s) so far)</span>{{end}}</td>
<td>{{if .CanSend}}On <form method="post" action="/ui/agents" style="display:inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="stopsend"><input type="hidden" name="agent" value="{{.Name}}"><button>Stop sending</button></form>
{{else}}Off{{if $.Open.Gate}}{{if not .ReadsAll}} <form method="post" action="/ui/agents" style="display:inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="allowsend"><input type="hidden" name="agent" value="{{.Name}}"><button>Allow sending…</button></form>{{else}} <span class="mut">reads every chat, so set it up again in a terminal to allow sending</span>{{end}}{{end}}{{end}}</td></tr>
{{end}}</tbody></table>
{{else}}<p class="mut">No agents yet. Add one from <a href="/ui/setup">Set up</a>.</p>{{end}}
</main></body></html>
`))
