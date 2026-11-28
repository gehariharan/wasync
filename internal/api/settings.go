package api

import (
	"html/template"
	"net/http"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/buildinfo"
	"github.com/gehariharan/wasync/internal/prefs"
	"github.com/gehariharan/wasync/internal/update"
)

// Settings holds everything the setup steps set, to change later: how wasync
// is used, how it looks, which chats sync and whether files are saved,
// start-at-login, the agents, the devices that may approve over the tailnet, where the data is,
// updates, the note that wasync is not WhatsApp, and Remove wasync.

type settingsData struct {
	uiData
	Paired    bool
	ShowChats bool
	SaveMedia bool
	// Appearance is "system", "light" or "dark".
	Appearance string
	Syncing    int
	Agents     int
	// Start-at-login.
	Service          string
	ServiceInstalled bool
	ServiceAvailable bool
	Quietly          string
	// Tailnet is on when the web app opens from other devices on the tailnet.
	Tailnet      bool
	TailnetHost  string
	TailnetLogin string
	Devices      []deviceRow
	// CanAddDevice is set on this computer when it can show the system prompt.
	CanAddDevice bool
	// DeviceCode is a code just made for setting up a device.
	DeviceCode string
	// Update is where updates stand; nil when this wasync cannot update.
	Update *update.Status
	// DataDir is the folder that holds everything wasync keeps.
	DataDir string
	Version string
	Note    string
}

func (h *handler) settingsPage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	h.settingsView(w, r, sess, nil)
}

// settingsView renders Settings; tweak, when set, adds to what it shows.
func (h *handler) settingsView(w http.ResponseWriter, r *http.Request, sess uiSession, tweak func(*settingsData)) {
	d := &settingsData{uiData: uiData{CSRF: sess.csrf, Gate: h.confirmName(r), Title: "Settings · wasync", Remote: remoteLogin(r) != ""}, DataDir: h.dataDir, Version: buildinfo.Version, Note: notWhatsApp}
	h.noticeFrom(r, &d.uiData)
	account, err := h.store.Account(r.Context())
	if err != nil {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	s, _ := h.prefs.Get()
	d.ShowChats, d.SaveMedia = !s.AgentOnly(), s.MediaOn()
	d.Appearance = s.Theme
	if d.Appearance == "" {
		d.Appearance = themeSystem
	}
	if account != nil {
		d.Paired = true
		groups, err := h.store.GroupSummaries(r.Context(), account.ID)
		if err != nil {
			http.Error(w, "archive unavailable", http.StatusInternalServerError)
			return
		}
		for _, g := range groups {
			if g.PersonSynced() {
				d.Syncing++
			}
		}
	}
	if clients, err := agentauth.List(h.dataDir); err == nil {
		for i := range clients {
			if clients[i].Status(time.Now()) == "active" {
				d.Agents++
			}
		}
	}
	if h.service != nil && h.service.Where() != "" {
		d.ServiceAvailable, d.Service = true, h.service.Where()
		d.ServiceInstalled, _ = h.service.Installed()
		d.Quietly = quietly(d.Service)
	}
	if st, devices := h.remoteDevices(); st.Host != "" {
		d.Tailnet, d.TailnetHost, d.TailnetLogin = true, st.Host, st.Login
		d.Devices = deviceRows(devices)
		d.CanAddDevice = !d.Remote && h.human.Enabled()
	}
	if h.update != nil {
		st := h.update.Status()
		d.Update = &st
		if updateBusy(d.Update) {
			d.Refresh = 3
		}
	}
	if tweak != nil {
		tweak(d)
	}
	h.webRender(w, r, settingsTemplate, d)
}

// themeSystem is the Appearance choice that follows the system.
const themeSystem = "system"

// settingsChange saves a choice on the page: "Show my chats in wasync",
// Appearance, or "Also save photos, videos and files". None lets an agent do
// more.
func (h *handler) settingsChange(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	if r.PostForm.Get("action") == "theme" {
		h.themeChange(w, r)
		return
	}
	var on bool
	switch r.PostForm.Get("on") {
	case "true":
		on = true
	case "false":
	default:
		http.Error(w, `on must be "true" or "false"`, http.StatusBadRequest)
		return
	}
	var change func(*prefs.Settings)
	switch r.PostForm.Get("action") {
	case "chats":
		change = func(s *prefs.Settings) {
			s.Use = prefs.UseAgents
			if on {
				s.Use = prefs.UseChats
			}
		}
	case "media":
		change = func(s *prefs.Settings) { s.SaveMedia = &on }
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if _, err := h.prefs.Update(change); err != nil {
		h.uiBack(w, r, "failed")
		return
	}
	h.uiBack(w, r, "saved")
}

// themeChange saves Appearance: System, Light or Dark.
func (h *handler) themeChange(w http.ResponseWriter, r *http.Request) {
	theme := r.PostForm.Get("theme")
	switch theme {
	case themeSystem:
		theme = ""
	case prefs.ThemeLight, prefs.ThemeDark:
	default:
		http.Error(w, `theme must be "system", "light" or "dark"`, http.StatusBadRequest)
		return
	}
	if _, err := h.prefs.Update(func(s *prefs.Settings) { s.Theme = theme }); err != nil {
		h.uiBack(w, r, "failed")
		return
	}
	h.uiBack(w, r, "saved")
}

var settingsTemplate = template.Must(template.New("settings").Funcs(uiFuncs).Parse(uiHead + uiUpdateSection + `{{template "head" .}}
<h1>Settings</h1>
{{template "notice" .}}
<div class="settings">
<p class="label" id="use">General</p>
<section class="card">
<form method="post" action="/ui/settings" class="settings-row"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="chats"><input type="hidden" name="on" value="{{not .ShowChats}}">
<span class="grow"><strong id="chats-label">Show my chats in wasync</strong>{{if not .ShowChats}}<span class="sub block">For your AI agents only: no message text is shown.</span>{{end}}</span>
<button class="switch" role="switch" aria-checked="{{.ShowChats}}" aria-labelledby="chats-label"></button></form>
<form method="post" action="/ui/settings" class="settings-row" id="appearance"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="theme">
<span class="grow"><strong id="theme-label">Appearance</strong></span>
<span class="seg" role="group" aria-labelledby="theme-label"><button name="theme" value="system" aria-pressed="{{eq .Appearance "system"}}">System</button><button name="theme" value="light" aria-pressed="{{eq .Appearance "light"}}">Light</button><button name="theme" value="dark" aria-pressed="{{eq .Appearance "dark"}}">Dark</button></span></form>
{{if .ServiceAvailable}}{{if .Remote}}<div class="settings-row" id="login"><span class="grow"><strong>Start wasync when I log in</strong><span class="sub block">{{if .ServiceInstalled}}On{{else}}Off{{end}}. Change it on the computer running wasync.</span></span></div>
{{else}}<form method="post" action="/ui/setup/service" class="settings-row" id="login"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="back" value="/ui/settings">
<span class="grow"><strong id="login-label">Start wasync when I log in</strong><span class="sub block">{{.Quietly}}</span></span>
<button class="switch" role="switch" aria-checked="{{.ServiceInstalled}}" aria-labelledby="login-label" name="action" value="{{if .ServiceInstalled}}remove{{else}}install{{end}}"></button></form>{{end}}{{end}}
</section>

<p class="label" id="chats">Chats and agents</p>
<section class="card">
<div class="settings-row"><span class="grow"><strong>{{if .Paired}}Syncing {{.Syncing}} {{if eq .Syncing 1}}chat{{else}}chats{{end}}{{else}}No phone is linked{{end}}</strong></span>{{if .Paired}}<a class="btn" href="/ui/chats/pick">Change</a>{{else}}<a class="btn go" href="/ui/setup">Link your phone</a>{{end}}</div>
<form method="post" action="/ui/settings" class="settings-row"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="media"><input type="hidden" name="on" value="{{not .SaveMedia}}">
<span class="grow"><strong id="media-label">Also save photos, videos and files</strong>{{if not .SaveMedia}}<span class="sub block">Text only; attachments are listed but not saved.</span>{{end}}</span>
<button class="switch" role="switch" aria-checked="{{.SaveMedia}}" aria-labelledby="media-label"></button></form>
<div class="settings-row" id="agents"><span class="grow"><strong>{{.Agents}} {{if eq .Agents 1}}agent{{else}}agents{{end}} connected</strong></span><a class="btn" href="/ui/agents">Manage</a><a class="btn" href="/ui/agents/connect">Connect another</a></div>
</section>

{{if .Tailnet}}<p class="label" id="devices">{{if .Remote}}Approve from this device{{else}}Approve from your other devices{{end}}</p>
<section class="card">
<p class="mut">wasync opens at <code>https://{{.TailnetHost}}/ui/</code> for {{.TailnetLogin}} on your tailnet. A device set up here can approve agents' requests and give agents more power: each time, it shows what you are approving and asks for its passkey (Touch ID, Face ID, Windows Hello or the device's PIN). Only the computer running wasync can add a device.</p>
{{if .Devices}}<div class="table-wrap"><table class="table"><thead><tr><th>Device</th><th>Added</th><th>Last approved</th><th></th></tr></thead><tbody>
{{range .Devices}}<tr><td>{{.Name}}</td><td>{{.Added}}</td><td>{{.Used}}</td><td><form method="post" action="/ui/setup/devices" class="inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="remove"><input type="hidden" name="id" value="{{.ID}}"><button>Remove</button></form></td></tr>
{{end}}</tbody></table></div>{{else}}<p>No device is set up yet.</p>{{end}}
{{if .Remote}}<form method="post" action="/ui/passkey/begin"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p><label>Code from the computer running wasync <input type="text" name="code" autocomplete="one-time-code" autocapitalize="characters" spellcheck="false" size="16" maxlength="20" required></label></p>
<p><label>Name this device <input type="text" name="name" maxlength="40" placeholder="My laptop"></label></p>
<p class="btn-row"><button class="go">Set up this device…</button> <span class="mut">Get a code there: open wasync, Settings, Add a device. A device that already has a passkey for wasync (one synced from another device too) needs no setting up.</span></p></form>
{{else if .DeviceCode}}<p class="ok" role="status">On the other device, open <code>https://{{.TailnetHost}}/ui/settings</code> and enter this code within 10 minutes. It works once.</p>
<p class="code-big"><strong>{{.DeviceCode}}</strong></p>
{{else if .CanAddDevice}}<form method="post" action="/ui/setup/devices#devices"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="action" value="add"><div class="btn-row"><button class="go">Add a device…</button> <span class="mut">Asks for {{.Gate}}, then shows a one-time code to enter on the other device.</span></div></form>
{{else}}<p class="bad">This computer cannot show a system confirmation, so devices cannot be added.</p>{{end}}
</section>{{end}}

{{template "update" .}}

<p class="label" id="about">About</p>
<section class="card">
<div class="settings-row"><span class="grow"><strong>wasync {{.Version}}</strong> by <a href="https://gehariharan.com" target="_blank" rel="noopener">@gehariharan</a></span></div>
<details class="settings-row about-note"><summary>Not affiliated with WhatsApp or Meta.</summary><p class="disclaimer"><strong>wasync is not WhatsApp.</strong> {{.Note}}</p></details>
<form method="post" action="/ui/setup/folder" class="settings-row" id="data"><input type="hidden" name="csrf" value="{{.CSRF}}"><span class="grow">Your data is stored in <code>{{.DataDir}}</code></span>{{if not .Remote}}<button>Show folder</button>{{end}}</form>
</section>

<p class="label" id="remove">Remove wasync</p>
<section class="card danger-zone">
<div class="settings-row"><span class="grow">Disconnects agents and unlinks your phone; your chats stay unless you delete them.</span>{{if .Remote}}<span class="mut">Do this on the computer running wasync.</span>{{else}}<a class="btn" href="/ui/settings/remove">Remove wasync…</a>{{end}}</div>
</section>
</div>
</main></body></html>
`))
