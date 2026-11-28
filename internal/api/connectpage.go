package api

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/agentconnect"
	"github.com/gehariharan/wasync/internal/axicred"
	"github.com/gehariharan/wasync/internal/humancheck"
)

// The Connect an agent screen: step 4 of setup, and "Connect another agent"
// from the Agents page. One tile per app (ChatGPT & Codex, Claude Code,
// Claude Desktop) plus Other for any app that runs a local tool server. The
// chosen tile shows what the agent may do, one Connect button that asks for
// the computer's own check, and the lines it adds on request; once connected
// it shows whether the app has called wasync yet.

// otherApp is the Other tile: a credential saved where wasync-axi reads it,
// and a block to paste into the app's own settings.
const otherApp = "other"

func validApp(kind string) bool {
	_, ok := agentconnect.Lookup(kind)
	return ok
}

// connectTile is one app on the screen.
type connectTile struct {
	Kind, Title, Href, Sub string
	Found, Connected       bool
	Current                bool
}

type connectData struct {
	uiData
	InSetup bool
	Back    string
	Tiles   []connectTile
	// Sel is the chosen tile; Cur its app's state, Preview what Connect
	// would change.
	Sel     string
	Cur     *connectRow
	Preview *connectPreview
	// Ago is how long ago the connected app last called wasync.
	Ago string
	// Other is the Other tile: the agents connected by name, where
	// wasync-axi reads its credential, and the block to paste.
	Agents       []setupAgent
	AxiPath      string
	HasSavedCred bool
	Block        string
	Remote       bool
	Connected    int
}

type setupAgent struct{ Name, Status string }

func (h *handler) agentStep(w http.ResponseWriter, r *http.Request, sess uiSession) {
	h.reachedStep(stepAgent)
	h.connectView(w, r, sess, true)
}

func (h *handler) agentsConnect(w http.ResponseWriter, r *http.Request, sess uiSession) {
	h.connectView(w, r, sess, false)
}

func (h *handler) connectView(w http.ResponseWriter, r *http.Request, sess uiSession, inSetup bool) {
	d := &connectData{uiData: uiData{CSRF: sess.csrf, Gate: h.confirmName(r), Title: "Connect an agent · wasync", Remote: remoteLogin(r) != ""}, InSetup: inSetup, Back: "/ui/agents/connect"}
	if inSetup {
		d.Setup, d.Back = h.setupFrameFor(stepAgent, true), "/ui/setup/agent"
	}
	h.noticeFrom(r, &d.uiData)
	now := time.Now()
	rows := h.connectRows(now)
	d.Sel = r.URL.Query().Get("app")
	if d.Sel != otherApp && !validApp(d.Sel) {
		d.Sel = ""
		// Open on the first app that is connected, else the first found.
		for _, row := range rows {
			if row.Connected {
				d.Sel = row.Agent
				break
			}
		}
		for _, row := range rows {
			if d.Sel == "" && row.Found {
				d.Sel = row.Agent
			}
		}
		if d.Sel == "" && len(rows) > 0 {
			d.Sel = rows[0].Agent
		}
	}
	for i := range rows {
		row := &rows[i]
		t := connectTile{Kind: row.Agent, Title: row.Title, Found: row.Found, Connected: row.Connected, Current: row.Agent == d.Sel, Href: d.Back + "?app=" + url.QueryEscape(row.Agent)}
		switch {
		case row.Connected:
			t.Sub = "Connected"
			d.Connected++
		case row.Found:
			t.Sub = "Found"
		default:
			t.Sub = "Not found"
		}
		d.Tiles = append(d.Tiles, t)
		if t.Current {
			d.Cur = row
		}
	}
	d.Tiles = append(d.Tiles, connectTile{Kind: otherApp, Title: "Other", Sub: "Copy one block", Current: d.Sel == otherApp, Href: d.Back + "?app=" + otherApp})
	switch {
	case d.Cur != nil && d.Cur.Connected:
		if d.Cur.LastCall != nil {
			d.Ago = ago(now.Sub(*d.Cur.LastCall))
		} else {
			d.Refresh = 5 // the screen turns to "connected" on the app's first call
		}
	case d.Cur != nil:
		d.Preview = h.previewFor(d.Cur.Agent)
	case d.Sel == otherApp:
		h.otherApp(d)
	}
	h.webRender(w, r, connectTemplate, d)
}

// otherApp fills the Other tile: agents connected by name, and the block that
// runs wasync-axi's tool server, for an app wasync cannot set up itself.
func (h *handler) otherApp(d *connectData) {
	clients, _ := agentauth.List(h.dataDir)
	now := time.Now()
	for _, c := range clients {
		if c.Status(now) == "active" {
			d.Agents = append(d.Agents, setupAgent{Name: humancheck.Clean(c.Name, 40), Status: c.Status(now)})
		}
	}
	if p, err := h.axiCredentialPath(); err == nil {
		d.AxiPath = p
		if existing, err := axicred.Existing(p); err == nil && existing != "" {
			d.HasSavedCred = true
		}
	}
	command := h.connectEnv().AxiCommand()
	if command == "" {
		command = "wasync-axi"
	}
	block := map[string]any{"mcpServers": map[string]any{"whatsapp": map[string]any{"command": command, "args": []string{"mcp"}, "env": map[string]string{"WASYNC_API_ADDR": h.authority, "WASYNC_DATA_DIR": h.dataDir}}}}
	if b, err := json.MarshalIndent(block, "", "  "); err == nil {
		d.Block = string(b)
	}
}

// agentKinds maps a credential's ID to the app it was made for, from the
// record of connected apps; "" for an agent connected by name, which shows the
// robot.
func (h *handler) agentKinds() map[string]string {
	kinds := map[string]string{}
	conns, _ := agentconnect.Connections(h.dataDir)
	for _, c := range conns {
		kinds[c.ClientID] = string(c.Agent)
	}
	return kinds
}

// agentTitle is how a screen names an agent: its app's name when wasync
// connected that app, otherwise the credential's own name.
func agentTitle(kind, name string) string {
	if a, ok := agentconnect.Lookup(kind); ok {
		return a.Title
	}
	return name
}

// uiAgentIcons draws each agent's icon: Codex and Claude Code have their own,
// every other agent the robot. They are simple drawings of the apps' marks,
// in their colours, carried by wasync so the page loads nothing from outside.
const uiAgentIcons = `{{define "agenticon"}}{{if eq . "codex"}}<svg class="agent-ic" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><rect width="20" height="20" rx="5" fill="#111"/><path d="M5.5 7l3 3-3 3M10.5 13.5h4" stroke="#fff" stroke-width="1.8" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>{{else if eq . "claude-code"}}<svg class="agent-ic" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><rect width="20" height="20" rx="5" fill="#D97757"/><path d="M10 4.5v11M4.5 10h11M6.1 6.1l7.8 7.8M13.9 6.1l-7.8 7.8" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/></svg>{{else}}<svg class="agent-ic" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><rect width="20" height="20" rx="5" fill="#5f6a63"/><rect x="5" y="7" width="10" height="8" rx="2" fill="none" stroke="#fff" stroke-width="1.5"/><path d="M10 7V4.5" stroke="#fff" stroke-width="1.5" stroke-linecap="round"/><circle cx="8" cy="11" r="1" fill="#fff"/><circle cx="12" cy="11" r="1" fill="#fff"/></svg>{{end}}{{end}}`

var connectTemplate = template.Must(template.New("connect").Funcs(uiFuncs).Parse(uiHead + uiAgentIcons + `{{template "head" .}}
<div class="stage">
<h1>{{if .InSetup}}Connect your AI app{{else}}Connect an agent{{end}}</h1>
{{template "notice" .}}
<nav class="tiles" aria-label="Apps">{{range .Tiles}}<a class="tile" href="{{.Href}}"{{if .Current}} aria-current="true"{{end}}><strong>{{template "agenticon" .Kind}} {{.Title}}</strong><span class="sub{{if or .Found .Connected}} found{{end}}">{{.Sub}}</span></a>{{end}}</nav>
{{with .Cur}}{{if .Connected}}
<section class="card center">
<div class="done-mark" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="m5 12.5 4.5 4.5L19 7.5"/></svg></div>
<h2>{{template "agenticon" .Agent}} {{.Title}} is connected</h2>
{{if and (eq .Credential "active") .InSettings}}<p class="status-line" role="status">{{.Status}}</p>{{if not .LastCall}}<p class="sub">Not seeing it? {{.Next}}</p>{{end}}{{else}}<p class="bad">{{.Status}}</p>{{end}}
<p class="sub">It can read, reply and react in chats you sync. Anything else asks under Approvals.</p>
<form method="post" action="/ui/setup/disconnect" class="btn-row center"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="agent" value="{{.Agent}}"><input type="hidden" name="back" value="{{$.Back}}"><button>Disconnect</button></form>
</section>
{{else}}
<div class="abilities" aria-label="What it may do"><span>Read</span><span>Reply</span><span>React</span></div>
<p class="center mut">In every chat you sync, now and later. Editing stays off.</p>
{{with $.Preview}}{{if .Error}}<p class="notice bad">{{.Error}}</p>
{{else}}<div class="connect-go">{{if $.Gate}}<form method="post" action="/ui/setup/connect"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="agent" value="{{.Kind}}"><input type="hidden" name="digest" value="{{.Digest}}"><input type="hidden" name="back" value="{{$.Back}}"><button class="go">Connect {{.Title}}</button></form><span class="sub">Asks for {{$.Gate}}.</span>
{{else if $.Remote}}<p class="notice bad">Connect agents on the computer running wasync, or <a href="/ui/settings#devices">set up this device</a> with a passkey first.</p>
{{else}}<p class="notice bad">This computer cannot show a system confirmation, so agents cannot be connected here.</p>{{end}}</div>
<details class="lines"><summary>Show the lines it adds</summary>
<p>{{if .Exists}}It changes <code>{{.Path}}</code> and keeps a copy of it next to it first.{{else}}It creates <code>{{.Path}}</code>.{{end}} {{.Title}} gets its own credential, <strong>{{.Name}}</strong>, which is never shown.</p>
{{if .Removed}}<p>It replaces this entry:</p><pre>{{.Removed}}</pre>{{end}}
<p>It adds:</p><pre>{{.Added}}</pre>
{{if .Steps}}<p>It also adds the WhatsApp plugin to Codex by running:</p><pre>{{range .Steps}}{{.}}
{{end}}</pre>{{end}}
</details>{{end}}{{end}}
{{end}}{{end}}
{{if eq .Sel "other"}}<section class="card">
<h2>{{template "agenticon" "other"}} Any other app</h2>
<p>For an app that runs a local tool server (MCP). wasync saves a credential where wasync-axi reads it, then you paste this block into the app's settings.</p>
{{if .Agents}}<p class="mut">Agents already connected: {{range $i, $a := .Agents}}{{if $i}}, {{end}}{{$a.Name}}{{end}}.</p>{{end}}
{{if .Gate}}<form method="post" action="/ui/setup/agent"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="back" value="{{.Back}}"><input type="hidden" name="app" value="other">
<label>Name <input type="text" name="name" value="wasync-axi" maxlength="32" pattern="[a-z0-9-]{1,32}"></label>
{{if .HasSavedCred}}<label><input type="checkbox" name="replace" value="yes"> Replace the credential wasync-axi already has saved</label>{{end}}
<div class="btn-row"><button class="go">Connect</button> <span class="sub">Asks for {{.Gate}}. The credential goes to {{.AxiPath}} and is never shown.</span></div></form>
{{else if .Remote}}<p class="notice bad">Connect agents on the computer running wasync, or <a href="/ui/settings#devices">set up this device</a> with a passkey first.</p>
{{else}}<p class="notice bad">This computer cannot show a system confirmation, so agents cannot be connected here.</p>{{end}}
<p>Then add this to the app's tool servers:</p><pre>{{.Block}}</pre>
</section>{{end}}
{{if .InSetup}}<form method="post" action="/ui/setup/finish" class="step-foot"><input type="hidden" name="csrf" value="{{.CSRF}}"><a class="btn ghost" href="/ui/setup/running">← Back</a><span class="foot-go">{{if .Connected}}<span class="sub">Click Open wasync to finish.</span><button class="go">Open wasync</button>{{else}}<span class="sub">Connect an app, or skip for now.</span><button class="ghost">Skip for now</button>{{end}}</span></form>
{{else}}<p><a href="/ui/agents">← Agents</a></p>{{end}}
</div>
</main></body></html>
`))

// connectBackTo is the Connect screen a connect or disconnect form returns to.
func connectBackTo(r *http.Request) string {
	if b := r.PostForm.Get("back"); b == "/ui/setup/agent" || b == "/ui/agents/connect" {
		return b
	}
	return "/ui/agents/connect"
}
