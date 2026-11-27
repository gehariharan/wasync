package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/agentconnect"
	"github.com/gehariharan/wasync/internal/axicred"
)

// "Connect an agent" from the setup page: after one human check, wasync
// issues the app its own credential, saves it in a folder for that app, and
// writes the wasync tool server into the app's own settings file, keeping a
// backup. Disconnect takes the entry out again (putting back any entry it
// replaced) and revokes the credential; it needs no check because it only
// takes power away. Every route here is owner-only: it needs a web app
// session, which only the person at this computer (or a passkey device on
// the tailnet) can open, and connecting still ends in the system prompt.

func init() {
	for k, v := range map[string]struct {
		text string
		ok   bool
	}{
		"agentconnected":    {"Connected. Its credential is saved for it and is not shown anywhere; a copy of its settings file was kept first. Follow the next step below.", true},
		"pluginfail":        {"Connected, but the WhatsApp plugin could not be added to Codex. The WhatsApp tools still work; follow the next step below.", true},
		"agentalready":      {"That app is already connected. Disconnect it first to connect it again.", false},
		"noaxi":             {"wasync-axi, the program agents use to reach wasync, is not installed next to wasync, so nothing changed.", false},
		"connectfail":       {"wasync could not change that app's settings file safely, so nothing changed. The preview says why.", false},
		"connectchanged":    {"That app's settings changed since the preview, so nothing changed. Check the new preview and connect again.", false},
		"agentdisconnected": {"Disconnected. Its credential no longer works and wasync's entry is out of its settings.", true},
		"pluginleft":        {"Disconnected, but the WhatsApp plugin could not be removed from Codex. Remove it there if you no longer want it.", true},
		"disconnectleft":    {"Its credential no longer works, but wasync could not take its entry out of the app's settings file. Fix or restore that file, then press Disconnect again.", false},
		"notconnected":      {"That app is not connected.", false},
		"agentremoved":      {"Removed. That agent's credential no longer works.", true},
	} {
		notices[k] = v
	}
}

func (h *handler) connectEnv() agentconnect.Env {
	if h.agentEnv != nil {
		return *h.agentEnv
	}
	return agentconnect.SystemEnv()
}

// connectCredentialDir is the folder that holds an app's credential, beside
// wasync-axi's own.
func (h *handler) connectCredentialDir(kind agentconnect.Kind) (string, error) {
	p, err := h.axiCredentialPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "agents", string(kind)), nil
}

// connectName is the first credential name free for kind: "codex", then
// "codex-2" and on, so an older credential of that name is left alone.
func (h *handler) connectName(kind agentconnect.Kind) (string, error) {
	for i := 1; i < 10; i++ {
		name := string(kind)
		if i > 1 {
			name = fmt.Sprintf("%s-%d", kind, i)
		}
		existing, err := agentauth.ActiveByName(h.dataDir, name)
		if err != nil {
			return "", err
		}
		if existing == nil {
			return name, nil
		}
	}
	return "", errors.New("too many agents with this name")
}

// connectPlan is everything Connect will do for one app, as previewed.
type connectPlan struct {
	agent   agentconnect.Agent
	name    string
	entry   agentconnect.Entry
	credDir string
	file    agentconnect.Plan
	plugin  agentconnect.CodexPluginPlan
	digest  string
}

// errNoAxi means wasync-axi, the tool server, is not installed.
var errNoAxi = errors.New("wasync-axi is not installed next to wasync")

func (h *handler) planConnect(a agentconnect.Agent) (*connectPlan, error) {
	env := h.connectEnv()
	axi := env.AxiCommand()
	if axi == "" {
		return nil, errNoAxi
	}
	name, err := h.connectName(a.Kind)
	if err != nil {
		return nil, err
	}
	dir, err := h.connectCredentialDir(a.Kind)
	if err != nil {
		return nil, err
	}
	entry := agentconnect.Entry{Command: axi, Args: []string{"mcp"}, Env: map[string]string{
		axicred.EnvConfigDir: dir,
		"WASYNC_API_ADDR":    h.authority,
		"WASYNC_DATA_DIR":    h.dataDir,
	}}
	if env.Self != "" {
		entry.Env["WASYNC_BIN"] = env.Self
	}
	p := &connectPlan{agent: a, name: name, entry: entry, credDir: dir}
	if p.file, err = env.PlanConnect(a, entry); err != nil {
		return nil, err
	}
	if a.Kind == agentconnect.Codex {
		if p.plugin, err = env.PlanCodexPlugin(h.dataDir); err != nil {
			return nil, err
		}
	}
	sum := sha256.Sum256([]byte(p.file.Digest + "\x00" + name + "\x00" + strings.Join(p.plugin.Steps(), "\x00")))
	p.digest = hex.EncodeToString(sum[:12])
	return p, nil
}

// connectPrompt is the sentence on the system prompt.
func connectPrompt(p *connectPlan, req agentauth.GrantRequest) string {
	return agentauth.GrantEffect(p.name, req.Scopes, req.Chats, req.SyncedChats, req.Expires) + ", and add it to " + p.agent.Title + "'s settings"
}

// connectPreview is what the setup page shows before Connect.
type connectPreview struct {
	Kind, Title, Name string
	Path              string
	Exists            bool
	Removed, Added    string
	Steps             []string
	Digest            string
	Error             string
}

func (h *handler) previewFor(kind string) *connectPreview {
	a, ok := agentconnect.Lookup(kind)
	if !ok {
		return nil
	}
	pv := &connectPreview{Kind: kind, Title: a.Title}
	if c, err := agentconnect.Find(h.dataDir, a.Kind); err != nil || c != nil {
		return nil // already connected: the row offers Disconnect instead
	}
	p, err := h.planConnect(a)
	if err != nil {
		pv.Error = connectProblem(err)
		return pv
	}
	pv.Name, pv.Path, pv.Exists = p.name, p.file.Path, p.file.Exists
	pv.Removed, pv.Added, pv.Steps, pv.Digest = p.file.Removed, p.file.Added, p.plugin.Steps(), p.digest
	return pv
}

func connectProblem(err error) string {
	if errors.Is(err, errNoAxi) {
		return "wasync-axi, the program agents use to reach wasync, is not installed next to wasync. Install it, then try again."
	}
	return err.Error()
}

// connectRow is one app on the setup page and in the status answer.
type connectRow struct {
	Agent       string     `json:"agent"`
	Title       string     `json:"title"`
	Found       bool       `json:"found"`
	Connected   bool       `json:"connected"`
	Name        string     `json:"name,omitempty"`
	Credential  string     `json:"credential,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	LastCall    *time.Time `json:"last_call,omitempty"`
	ConfigPath  string     `json:"config_path"`
	Backup      string     `json:"backup,omitempty"`
	// InSettings says the app's settings file still holds wasync's entry.
	InSettings  bool   `json:"in_settings"`
	CodexPlugin bool   `json:"codex_plugin,omitempty"`
	Next        string `json:"next"`
	// Status is the row's state in plain words.
	Status string `json:"status"`
}

func (h *handler) connectRows(now time.Time) []connectRow {
	env := h.connectEnv()
	clients, _ := agentauth.List(h.dataDir)
	var rows []connectRow
	for _, a := range agentconnect.Agents {
		row := connectRow{Agent: string(a.Kind), Title: a.Title, Found: env.Found(a.Kind), ConfigPath: env.ConfigPath(a.Kind), Next: a.Next, Status: "Not connected."}
		c, err := agentconnect.Find(h.dataDir, a.Kind)
		if err != nil {
			row.Status = "wasync cannot read its record of connected agents."
		}
		if c != nil {
			at := c.ConnectedAt
			row.Connected, row.Name, row.ConnectedAt = true, c.Client, &at
			row.ConfigPath, row.Backup, row.CodexPlugin = c.ConfigPath, c.Backup, c.CodexPlugin
			row.InSettings = agentconnect.Wrote(*c)
			row.Credential = "missing"
			for _, cl := range clients {
				if cl.ID == c.ClientID {
					row.Credential = cl.Status(now)
				}
			}
			if seen := h.agents.LastSeen(c.ClientID).UTC(); !seen.IsZero() {
				row.LastCall = &seen
			}
			row.Status = connectStatus(row, now)
		}
		rows = append(rows, row)
	}
	return rows
}

func connectStatus(row connectRow, now time.Time) string {
	switch {
	case row.Credential != "active":
		return "Its credential is " + row.Credential + ". Disconnect, then connect it again."
	case !row.InSettings:
		return "wasync's entry is no longer in " + row.Title + "'s settings. Disconnect, then connect it again."
	case row.LastCall == nil:
		return "Waiting for " + row.Title + " to call wasync…"
	}
	return "Connected, last call " + ago(now.Sub(*row.LastCall)) + "."
}

func ago(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
}

// setupConnections answers the page's status check with each app's state.
func (h *handler) setupConnections(w http.ResponseWriter, r *http.Request, sess uiSession) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"agents": h.connectRows(time.Now())})
}

func (h *handler) connectBack(w http.ResponseWriter, r *http.Request, notice, kind string) {
	v := url.Values{"n": {notice}}
	if kind != "" {
		v.Set("app", kind)
	}
	http.Redirect(w, r, connectBackTo(r)+"?"+v.Encode(), http.StatusSeeOther)
}

// setupConnect connects an app after the person confirms it.
func (h *handler) setupConnect(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	kind := r.PostForm.Get("agent")
	a, ok := agentconnect.Lookup(kind)
	if !ok {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	back := func(n string) { h.connectBack(w, r, n, kind) }
	if account, err := h.store.Account(r.Context()); err != nil || account == nil {
		back("unpaired")
		return
	}
	if !h.canConfirm(r) {
		back(noGate(r))
		return
	}
	h.connectMu.Lock()
	defer h.connectMu.Unlock()
	if c, err := agentconnect.Find(h.dataDir, a.Kind); err != nil {
		back("failed")
		return
	} else if c != nil {
		back("agentalready")
		return
	}
	p, err := h.planConnect(a)
	switch {
	case errors.Is(err, errNoAxi):
		back("noaxi")
		return
	case err != nil:
		back("connectfail")
		return
	case p.digest != r.PostForm.Get("digest"):
		back("connectchanged")
		return
	}
	// Read, reply and react in every synced chat, now and later; the
	// same credential Connect by name gives.
	synced, err := syncedChats(r.Context(), h.store, h.dataDir)
	if err != nil {
		back("failed")
		return
	}
	req := agentauth.ConnectRequest(p.name, synced)
	if agentauth.ValidateGrant(req) != nil {
		back("invalid")
		return
	}
	if err := h.confirmError(r.Context(), connectPrompt(p, req)); err != nil {
		h.uiRefused(w, r, err, back)
		return
	}
	notice, _ := h.connect(r.Context(), p, req)
	back(notice)
}

// connect carries out a confirmed plan. On failure it undoes what it did and
// returns the notice to show.
func (h *handler) connect(ctx context.Context, p *connectPlan, req agentauth.GrantRequest) (string, error) {
	env := h.connectEnv()
	audit := agentauth.NewAuditLog(h.dataDir)
	credential, client, err := agentauth.Grant(h.dataDir, req, time.Now())
	if err != nil {
		return "failed", err
	}
	_ = audit.Append(agentauth.AuditEntry{Event: "grant", ClientID: client.ID, ClientName: client.Name, Scope: strings.Join(client.Scopes, ","), Outcome: agentauth.OutcomeAllowed})
	credPath := filepath.Join(p.credDir, axicred.FileName)
	undoCredential := func() {
		_ = os.Remove(credPath)
		_ = os.Remove(p.credDir)
		if _, err := agentauth.Revoke(h.dataDir, client.Name, time.Now()); err == nil {
			_ = audit.Append(agentauth.AuditEntry{Event: "revoke", ClientID: client.ID, ClientName: client.Name, Outcome: agentauth.OutcomeAllowed})
		}
	}
	if err := axicred.Save(credPath, credential); err != nil {
		undoCredential()
		return "failed", err
	}
	conn := agentconnect.Connection{Agent: p.agent.Kind, Client: client.Name, ClientID: client.ID, CredentialDir: p.credDir, ConfigPath: p.file.Path, Entry: p.entry, ConnectedAt: time.Now().UTC().Truncate(time.Second)}
	notice := "agentconnected"
	if p.agent.Kind == agentconnect.Codex {
		var perr error
		conn.CodexMarketplace, conn.CodexPlugin, perr = env.InstallCodexPlugin(ctx, p.plugin)
		if perr != nil {
			notice = "pluginfail" // the entry alone gives Codex the tools
		}
	}
	undoPlugin := func() {
		_ = env.RemoveCodexPlugin(ctx, h.dataDir, conn.CodexMarketplace, conn.CodexPlugin)
	}
	written, backup, err := env.Write(p.file)
	if err != nil {
		undoPlugin()
		undoCredential()
		if errors.Is(err, agentconnect.ErrChanged) {
			return "connectchanged", err
		}
		return "connectfail", err
	}
	conn.Backup, conn.Previous = backup, written.Previous()
	if err := agentconnect.Save(h.dataDir, conn); err != nil {
		if undo, _, perr := env.PlanDisconnect(conn); perr == nil {
			_, _, _ = env.Write(undo)
		}
		undoPlugin()
		undoCredential()
		return "failed", err
	}
	_ = audit.Append(agentauth.AuditEntry{Event: "connect", ClientID: client.ID, ClientName: client.Name, Ref: string(p.agent.Kind), Outcome: agentauth.OutcomeAllowed})
	return notice, nil
}

// setupDisconnect takes an app's entry out of its settings and revokes its
// credential after the person confirms it, like revoking any agent.
func (h *handler) setupDisconnect(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	kind := r.PostForm.Get("agent")
	a, ok := agentconnect.Lookup(kind)
	if !ok {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	if !h.canConfirm(r) {
		h.connectBack(w, r, noGate(r), kind)
		return
	}
	h.connectMu.Lock()
	defer h.connectMu.Unlock()
	c, err := agentconnect.Find(h.dataDir, a.Kind)
	if err != nil {
		h.connectBack(w, r, "failed", kind)
		return
	}
	if c == nil {
		h.connectBack(w, r, "notconnected", kind)
		return
	}
	if err := h.confirmError(r.Context(), agentauth.RevokeEffect(c.Client)); err != nil {
		h.uiRefused(w, r, err, func(n string) { h.connectBack(w, r, n, kind) })
		return
	}
	h.connectBack(w, r, h.disconnect(r.Context(), *c), kind)
}

// disconnect undoes a connection as far as it can and returns the notice.
// The credential is always revoked; the record is kept only while wasync's
// entry is still in the app's settings, so Disconnect can be tried again.
func (h *handler) disconnect(ctx context.Context, c agentconnect.Connection) string {
	env := h.connectEnv()
	audit := agentauth.NewAuditLog(h.dataDir)
	if active, err := agentauth.ActiveByName(h.dataDir, c.Client); err == nil && active != nil && active.ID == c.ClientID {
		if _, err := agentauth.Revoke(h.dataDir, c.Client, time.Now()); err == nil {
			_ = audit.Append(agentauth.AuditEntry{Event: "revoke", ClientID: c.ClientID, ClientName: c.Client, Outcome: agentauth.OutcomeAllowed})
		}
	}
	if c.CredentialDir != "" {
		_ = os.Remove(filepath.Join(c.CredentialDir, axicred.FileName))
		_ = os.Remove(c.CredentialDir)
	}
	notice := "agentdisconnected"
	plan, _, err := env.PlanDisconnect(c)
	if err == nil && plan.Changes() {
		_, _, err = env.Write(plan)
	}
	if err != nil {
		_ = audit.Append(agentauth.AuditEntry{Event: "disconnect", ClientID: c.ClientID, ClientName: c.Client, Ref: string(c.Agent), Outcome: agentauth.OutcomeDenied, Code: "settings_unchanged"})
		return "disconnectleft"
	}
	if c.Agent == agentconnect.Codex {
		if err := env.RemoveCodexPlugin(ctx, h.dataDir, c.CodexMarketplace, c.CodexPlugin); err != nil {
			notice = "pluginleft"
		}
	}
	if err := agentconnect.Forget(h.dataDir, c.Agent); err != nil {
		return "failed"
	}
	_ = audit.Append(agentauth.AuditEntry{Event: "disconnect", ClientID: c.ClientID, ClientName: c.Client, Ref: string(c.Agent), Outcome: agentauth.OutcomeAllowed})
	return notice
}
