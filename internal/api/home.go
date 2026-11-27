package api

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/agentconnect"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
)

// Home is the app's first page for "For my AI agents only" (decision D13): a
// one-line status, the send requests that need the person, the connected
// agents and the chats they can use. It never shows message text, apart from
// the message an agent asks to send, which the person must read to decide.

// homeRequests is how many waiting requests Home shows before "See all".
const homeRequests = 3

// homeAgent is one connected agent on Home.
type homeAgent struct {
	// Name is the agent's credential; Kind its app, when wasync connected one.
	Name, Kind, Title, Can, Used string
	// Next says how to make an app that never called pick up the tools.
	Next    string
	Waiting bool
}

type homeData struct {
	uiData
	Status     string
	StatusWarn bool
	Requests   []uiAgentRequest
	AllWaiting int
	Agents     []homeAgent
	Chats      int
	Faces      []pickRow
	Names      string
}

func (h *handler) homePage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	d := &homeData{uiData: uiData{CSRF: sess.csrf, Gate: h.confirmName(r), Title: "wasync", Remote: remoteLogin(r) != ""}}
	h.noticeFrom(r, &d.uiData)
	account, err := h.store.Account(r.Context())
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	if account == nil {
		http.Redirect(w, r, "/ui/setup", http.StatusSeeOther)
		return
	}
	reqs := h.agentRequestRows(r.Context(), account.ID)
	d.AllWaiting = len(reqs)
	if len(reqs) > homeRequests {
		reqs = reqs[:homeRequests]
	}
	d.Requests = reqs
	d.Agents = h.homeAgents(time.Now())
	groups, err := h.store.GroupSummaries(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	var last *time.Time
	var names []string
	for _, g := range groups {
		if !g.PersonSynced() {
			continue
		}
		d.Chats++
		if g.LastMessage != nil && (last == nil || g.LastMessage.After(*last)) {
			last = g.LastMessage
		}
		name := humancheck.Clean(g.Name, 40)
		if len(names) < 3 {
			names = append(names, name)
		}
		if len(d.Faces) < 5 {
			d.Faces = append(d.Faces, pickRow{ID: g.ID, Name: name, Picture: avatarPath(g.ID)})
		}
	}
	if d.Chats > len(names) {
		d.Names = fmt.Sprintf("%s and %d more", strings.Join(names, ", "), d.Chats-len(names))
	} else {
		d.Names = strings.Join(names, ", ")
	}
	d.Status, d.StatusWarn = h.statusLine(d.Chats, last)
	h.webRender(w, r, homeTemplate, d)
}

// statusLine is Home's first line: whether wasync is connected, how many chats
// it syncs and when the last message came.
func (h *handler) statusLine(chats int, last *time.Time) (string, bool) {
	state, warn := "Running", false
	if h.status != nil {
		switch c := h.status().Connection; c {
		case "online", "":
		case "connecting", "reconnecting":
			state, warn = "Connecting to WhatsApp", true
		default:
			state, warn = "WhatsApp needs attention ("+humancheck.Clean(c, 30)+")", true
		}
	}
	parts := []string{state, "syncing " + plural(chats, "chat")}
	if last != nil {
		parts = append(parts, "last message "+ago(time.Since(*last)))
	}
	return strings.Join(parts, " · "), warn
}

// homeAgents lists the active agents: the apps wasync connected first, then
// agents connected by name.
func (h *handler) homeAgents(now time.Time) []homeAgent {
	clients, _ := agentauth.List(h.dataDir)
	kinds := h.agentKinds()
	var out []homeAgent
	for i := range clients {
		c := &clients[i]
		if c.Status(now) != "active" {
			continue
		}
		kind := kinds[c.ID]
		a := homeAgent{Name: c.Name, Kind: kind, Title: humancheck.Clean(agentTitle(kind, c.Name), 40), Can: canDo(c), Used: "never used"}
		if app, ok := agentconnect.Lookup(kind); ok {
			a.Next = app.Next
		}
		if seen := h.agents.LastSeen(c.ID); !seen.IsZero() {
			a.Used = "used " + ago(now.Sub(seen))
		} else {
			a.Waiting = true
		}
		out = append(out, a)
	}
	// Apps wasync connected come first, in a stable order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Kind != "" && out[j-1].Kind == ""; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// canDo says in words what an agent's credential lets it do.
func canDo(c *agentauth.Client) string {
	var can []string
	if c.HasScope(agentauth.ScopeReadMessages) || c.HasScope(agentauth.ScopeReadChats) {
		can = append(can, "read")
	}
	if c.HasScope(agentauth.ScopeSend) {
		can = append(can, "reply")
	}
	if c.HasScope(agentauth.ScopeReact) {
		can = append(can, "react")
	}
	if c.HasScope(agentauth.ScopeEditOwn) {
		can = append(can, "edit")
	}
	switch len(can) {
	case 0:
		return "Can do nothing yet"
	case 1:
		return "Can " + can[0]
	}
	return "Can " + strings.Join(can[:len(can)-1], ", ") + " and " + can[len(can)-1]
}

func avatarPath(chat string) string { return "/ui/avatar?c=" + url.QueryEscape(chat) }

// agentRequestRows are the waiting agent send requests as the approval rows
// on Home and the Approvals page show them, oldest first.
func (h *handler) agentRequestRows(ctx context.Context, account int64) []uiAgentRequest {
	kinds := h.agentKinds()
	now := time.Now()
	var out []uiAgentRequest
	for _, p := range h.agentRequests(ctx, account) {
		v := uiAgentRequest{ID: p.ID, Short: p.ID[len(p.ID)-6:], Kind: kinds[p.agentID], Text: p.Text, Minutes: int(p.ExpiresAt.Sub(now).Round(time.Minute) / time.Minute), Asked: ago(now.Sub(p.AskedAt))}
		v.Agent = agentTitle(v.Kind, p.Agent)
		switch {
		case p.NewNumber:
			v.Where = "the new number " + p.Phone
		case p.ChatName != "" && !numberName(p.Chat, p.ChatName):
			v.Where = "“" + p.ChatName + "”, " + approval.AgentSend{Synced: p.Synced}.Target()
		case p.Phone != "":
			// Raw addresses are never shown: a chat known only by its number shows the number.
			v.Where = p.Phone + ", " + approval.AgentSend{Synced: p.Synced}.Target()
		default:
			v.Where = "a chat without a name, " + approval.AgentSend{Synced: p.Synced}.Target()
		}
		out = append(out, v)
	}
	return out
}

// uiRequestRows draws waiting agent send requests as compact rows: who, what
// in a plain sentence, the message, how long ago and when it lapses. Approve
// is one request at a time, each with its own system prompt naming exactly
// that message; there is deliberately no "Approve all".
// requestsView is what the request rows need from their page.
type requestsView struct {
	Requests         []uiAgentRequest
	CSRF, Gate, Back string
}

const uiRequestRows = `{{define "requests"}}<div class="card rows">{{range .Requests}}<div class="item">{{template "agenticon" .Kind}}<div class="grow"><div><strong>{{.Agent}}</strong> wants to send a message to <strong>{{.Where}}</strong></div><p class="quote">“{{.Text}}”</p><div class="meta">{{.Asked}} · expires in {{.Minutes}} min</div></div>
<form method="post" action="/ui/decide"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="back" value="{{$.Back}}"><button class="ghost" name="action" value="reject">Not now</button>{{if $.Gate}}<button class="go" name="action" value="approve">Approve</button>{{end}}</form></div>
{{end}}</div>{{end}}`

var homeTemplate = template.Must(template.New("home").Funcs(uiFuncs).Parse(uiHead + uiAgentIcons + uiRequestRows + `{{template "head" .}}
<h1 class="sr-only">Home</h1>
{{template "notice" .}}
<p class="status-line{{if .StatusWarn}} warn{{end}}" role="status">{{.Status}}</p>
{{if .Requests}}<div class="label label-row"><span>Needs you · {{.AllWaiting}}</span>{{if gt .AllWaiting 1}}<form method="post" action="/ui/decide"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="back" value="/ui/home"><button class="ghost" name="action" value="decline-all">Not now to all</button></form>{{end}}</div>
{{template "requests" (requests .Requests .CSRF .Gate "/ui/home")}}
{{if gt .AllWaiting (len .Requests)}}<p><a href="/ui/approvals">See all {{.AllWaiting}}</a></p>{{end}}
{{if not .Gate}}<p class="mut">{{if .Remote}}To approve from this device, <a href="/ui/settings#devices">set it up with a passkey</a>.{{else}}This computer cannot show a system confirmation, so requests can only be declined here.{{end}}</p>{{else}}<p class="sub">Each approval asks for {{.Gate}} and sends exactly that message once.</p>{{end}}{{end}}
<p class="label">Connected agents</p>
<div class="card rows">{{range .Agents}}<div class="item">{{template "agenticon" .Kind}}<div class="grow"><strong>{{.Title}}</strong><div class="meta">{{.Can}} · {{.Used}}</div></div><a href="/ui/agents">Manage</a></div>
{{else}}<div class="item"><div class="grow mut">No agent is connected yet.</div><a class="btn go" href="/ui/agents/connect">Connect an agent</a></div>{{end}}</div>
<p class="label">Chats agents can use</p>
<div class="card rows"><div class="item">{{if .Faces}}<span class="faces">{{range .Faces}}<img class="avatar sm" src="{{.Picture}}" alt="" width="26" height="26">{{end}}</span>{{end}}<div class="grow"><strong>{{.Chats}} {{if eq .Chats 1}}chat{{else}}chats{{end}}</strong>{{if .Names}}<div class="meta">{{.Names}}</div>{{end}}</div><a href="/ui/chats/pick">Change</a></div></div>
<p class="foot-note">Message text is never shown here. To read chats in wasync, turn on “Show my chats in wasync” in <a href="/ui/settings#use">Settings</a>.</p>
</main></body></html>
`))
