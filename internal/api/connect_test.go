package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/agentconnect"
	"github.com/gehariharan/wasync/internal/axicred"
	"github.com/gehariharan/wasync/internal/humancheck"
	linking "github.com/gehariharan/wasync/internal/setup"
)

type connectFixture struct {
	accessFixture
	env    agentconnect.Env
	axiDir string
	axi    string
	stub   *promptStub
	ran    *[]string
}

// newConnectFixture starts an owner whose "computer" is scratch folders:
// a home with no apps, wasync and wasync-axi side by side, and a fake codex.
func newConnectFixture(t *testing.T, withCodex bool) (connectFixture, *pageClient, string) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "app")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	for _, name := range []string{"wasync", "wasync-axi"} {
		if err := os.WriteFile(filepath.Join(bin, name+exe), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ran := &[]string{}
	f := connectFixture{axiDir: filepath.Join(home, "config", "wasync-axi"), axi: filepath.Join(bin, "wasync-axi"+exe), stub: &promptStub{}, ran: ran}
	f.env = agentconnect.Env{
		GOOS: runtime.GOOS, Home: home, ConfigDir: filepath.Join(home, "config"), Self: filepath.Join(bin, "wasync"+exe),
		Getenv: func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			if name == "codex" && withCodex {
				return filepath.Join(bin, "codex"), nil
			}
			return "", errors.New("not found")
		},
		Run: func(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
			*ran = append(*ran, filepath.Base(name)+" "+strings.Join(args, " "))
			return nil, nil
		},
	}
	f.accessFixture = setupAccessOwner(t, humancheck.NewGate(f.stub), func(o *Owner) {
		state := &linking.State{}
		state.SetPaired()
		o.Setup = state
		o.AxiPath = func() (string, error) { return filepath.Join(f.axiDir, axicred.FileName), nil }
		o.AgentConnect = &f.env
	})
	c := newPageClient(t, f.accessFixture)
	c.get(linkPath(t, f.accessFixture))
	_, page, _ := c.get("/ui/setup")
	return f, c, csrfOf(t, page)
}

var digestField = regexp.MustCompile(`name="digest" value="([0-9a-f]+)"`)

// preview opens the Connect preview for kind and returns the page and its digest.
func preview(t *testing.T, c *pageClient, kind string) (string, string) {
	t.Helper()
	_, page, _ := c.get("/ui/setup?connect=" + kind)
	m := digestField.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no Connect form in the preview:\n%s", page)
	}
	return page, m[1]
}

func connections(t *testing.T, c *pageClient) map[string]connectRow {
	t.Helper()
	code, body, hd := c.get("/ui/setup/connections")
	if code != 200 || !strings.HasPrefix(hd.Get("Content-Type"), "application/json") {
		t.Fatalf("status: %d %s", code, body)
	}
	var out struct{ Agents []connectRow }
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	rows := map[string]connectRow{}
	for _, r := range out.Agents {
		rows[r.Agent] = r
	}
	return rows
}

func TestConnectCodexWritesItsSettingsOnlyAfterThePersonConfirms(t *testing.T) {
	f, c, csrf := newConnectFixture(t, true)
	cfg := filepath.Join(f.env.Home, ".codex", "config.toml")
	before := "model = \"gpt-5\"\n"
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if rows := connections(t, c); !rows["codex"].Found || rows["codex"].Connected || rows["claude-desktop"].Found {
		t.Fatalf("rows before: %+v", rows)
	}

	page, digest := preview(t, c, "codex")
	for _, want := range []string{"[mcp_servers.whatsapp]", f.axi, "WASYNC_AXI_CONFIG_DIR", "codex plugin add whatsapp@wasync", cfg} {
		if !strings.Contains(page, want) && !strings.Contains(page, strings.ReplaceAll(want, `\`, `\\`)) {
			t.Fatalf("preview lacks %q:\n%s", want, page)
		}
	}
	form := url.Values{"csrf": {csrf}, "agent": {"codex"}, "digest": {digest}}

	// Declined: nothing is granted, written or run.
	if _, _, hd := c.post("/ui/setup/connect", form); !strings.Contains(hd.Get("Location"), "n=declined") {
		t.Fatalf("declined: %v", hd)
	}
	if got, _ := agentauth.ActiveByName(f.dir, "codex"); got != nil {
		t.Fatal("credential issued without the person")
	}
	if b, _ := os.ReadFile(cfg); string(b) != before || len(*f.ran) != 0 {
		t.Fatalf("changed without the person: %q ran %q", b, *f.ran)
	}
	seen := f.stub.seen()
	if len(seen) != 1 || !strings.Contains(seen[0], `"codex"`) || !strings.Contains(seen[0], "ChatGPT & Codex's settings") || !strings.Contains(seen[0], "send") {
		t.Fatalf("prompt: %q", seen)
	}

	// A stale preview is refused before the prompt.
	f.stub.set(true)
	stale := url.Values{"csrf": {csrf}, "agent": {"codex"}, "digest": {"0000"}}
	if _, _, hd := c.post("/ui/setup/connect", stale); !strings.Contains(hd.Get("Location"), "n=connectchanged") || len(f.stub.seen()) != 1 {
		t.Fatalf("stale digest: %v, prompts %d", hd, len(f.stub.seen()))
	}

	if _, _, hd := c.post("/ui/setup/connect", form); !strings.Contains(hd.Get("Location"), "n=agentconnected") {
		t.Fatalf("connect: %v", hd)
	}
	client, _ := agentauth.ActiveByName(f.dir, "codex")
	if client == nil || !client.HasScope(agentauth.ScopeSend) || !client.HasScope(agentauth.ScopeReact) || client.HasScope(agentauth.ScopeEditOwn) {
		t.Fatalf("client %+v", client)
	}
	credDir := filepath.Join(f.axiDir, "agents", "codex")
	secret, err := axicred.Existing(filepath.Join(credDir, axicred.FileName))
	if err != nil || !strings.HasPrefix(secret, "wsa_") {
		t.Fatalf("credential %q %v", secret, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(filepath.Join(credDir, axicred.FileName)); info.Mode().Perm() != 0o600 {
			t.Fatalf("credential mode %v", info.Mode())
		}
	}
	data, _ := os.ReadFile(cfg)
	var parsed struct {
		Model string
		MCP   map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		t.Fatal(err)
	}
	entry := parsed.MCP["whatsapp"]
	if parsed.Model != "gpt-5" || entry.Command != f.axi || entry.Args[0] != "mcp" || entry.Env["WASYNC_AXI_CONFIG_DIR"] != credDir || entry.Env["WASYNC_DATA_DIR"] != f.dir || entry.Env["WASYNC_BIN"] != f.env.Self {
		t.Fatalf("config.toml:\n%s", data)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("the secret was written into the app's settings")
	}
	if len(*f.ran) != 2 || !strings.HasPrefix((*f.ran)[0], "codex plugin marketplace add ") || (*f.ran)[1] != "codex plugin add whatsapp@wasync" {
		t.Fatalf("ran %q", *f.ran)
	}
	backups, _ := filepath.Glob(cfg + ".wasync-backup-*")
	if len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != before {
		t.Fatalf("backup holds %q", b)
	}
	_, page, _ = c.get("/ui/setup")
	if strings.Contains(page, secret) {
		t.Fatal("the secret reached the page")
	}

	// Waiting, then the agent's first call shows as its last call.
	row := connections(t, c)["codex"]
	if !row.Connected || row.Credential != "active" || !row.InSettings || row.LastCall != nil || !row.CodexPlugin || !strings.Contains(row.Status, "Waiting") {
		t.Fatalf("row after connect: %+v", row)
	}
	if code, body := f.do(t, "GET", "/api/v1/chats", secret, nil); code != 200 {
		t.Fatalf("agent call: %d %s", code, body)
	}
	if row = connections(t, c)["codex"]; row.LastCall == nil || !strings.Contains(row.Status, "last call") {
		t.Fatalf("row after a call: %+v", row)
	}
	if _, _, hd := c.post("/ui/setup/connect", form); !strings.Contains(hd.Get("Location"), "n=agentalready") {
		t.Fatalf("second connect: %v", hd)
	}

	// Disconnect needs no prompt, and undoes everything.
	prompts := len(f.stub.seen())
	f.stub.set(false)
	*f.ran = nil
	if _, _, hd := c.post("/ui/setup/disconnect", url.Values{"csrf": {csrf}, "agent": {"codex"}}); !strings.Contains(hd.Get("Location"), "n=agentdisconnected") {
		t.Fatalf("disconnect: %v", hd)
	}
	if len(f.stub.seen()) != prompts {
		t.Fatal("disconnect asked for a confirmation")
	}
	if b, _ := os.ReadFile(cfg); string(b) != before {
		t.Fatalf("config after disconnect: %q", b)
	}
	if code, _ := f.do(t, "GET", "/api/v1/chats", secret, nil); code != 401 {
		t.Fatalf("revoked credential still works: %d", code)
	}
	if _, err := os.Stat(credDir); !os.IsNotExist(err) {
		t.Fatal("credential folder left behind")
	}
	if strings.Join(*f.ran, "\n") != "codex plugin remove whatsapp@wasync\ncodex plugin marketplace remove wasync" {
		t.Fatalf("ran %q", *f.ran)
	}
	if row = connections(t, c)["codex"]; row.Connected {
		t.Fatalf("still connected: %+v", row)
	}
}

func TestConnectClaudeAppsAndRestoreTheirEarlierEntry(t *testing.T) {
	f, c, csrf := newConnectFixture(t, false)
	f.stub.set(true)
	desktop := f.env.ConfigPath(agentconnect.ClaudeDesktop)
	before := "{\n  \"mcpServers\": {\n    \"whatsapp\": {\n      \"command\": \"whatsapp-axi\",\n      \"args\": [\"mcp\"]\n    }\n  },\n  \"theme\": \"dark\"\n}\n"
	if err := os.MkdirAll(filepath.Dir(desktop), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"claude-code", "claude-desktop"} {
		page, digest := preview(t, c, kind)
		if kind == "claude-desktop" && !strings.Contains(page, "It replaces this entry") {
			t.Fatalf("preview does not say it replaces the earlier entry:\n%s", page)
		}
		if _, _, hd := c.post("/ui/setup/connect", url.Values{"csrf": {csrf}, "agent": {kind}, "digest": {digest}}); !strings.Contains(hd.Get("Location"), "n=agentconnected") {
			t.Fatalf("%s: %v", kind, hd)
		}
	}
	code := f.env.ConfigPath(agentconnect.ClaudeCode)
	var cc struct {
		MCP map[string]map[string]any `json:"mcpServers"`
	}
	b, _ := os.ReadFile(code)
	if err := json.Unmarshal(b, &cc); err != nil || cc.MCP["whatsapp"]["type"] != "stdio" || cc.MCP["whatsapp"]["command"] != f.axi {
		t.Fatalf("~/.claude.json: %s %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(code); info.Mode().Perm() != 0o600 {
			t.Fatalf("new settings file mode %v", info.Mode())
		}
	}
	if len(*f.ran) != 0 {
		t.Fatalf("ran %q", *f.ran)
	}
	for _, kind := range []string{"claude-code", "claude-desktop"} {
		if _, _, hd := c.post("/ui/setup/disconnect", url.Values{"csrf": {csrf}, "agent": {kind}}); !strings.Contains(hd.Get("Location"), "n=agentdisconnected") {
			t.Fatalf("%s: %v", kind, hd)
		}
	}
	var got, want any
	b, _ = os.ReadFile(desktop)
	_ = json.Unmarshal(b, &got)
	_ = json.Unmarshal([]byte(before), &want)
	if x, y := mustJSON(got), mustJSON(want); x != y {
		t.Fatalf("the earlier entry was not put back:\n%s", b)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestConnectRefusesASettingsFileItCannotEdit(t *testing.T) {
	f, c, csrf := newConnectFixture(t, false)
	f.stub.set(true)
	path := f.env.ConfigPath(agentconnect.ClaudeCode)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, page, _ := c.get("/ui/setup?connect=claude-code")
	if !strings.Contains(page, "cannot change") || strings.Contains(page, `name="digest"`) {
		t.Fatalf("preview:\n%s", page)
	}
	_, _, hd := c.post("/ui/setup/connect", url.Values{"csrf": {csrf}, "agent": {"claude-code"}, "digest": {"x"}})
	if !strings.Contains(hd.Get("Location"), "n=connectfail") {
		t.Fatalf("connect: %v", hd)
	}
	if _, page, _ := c.get(hd.Get("Location")); !strings.Contains(page, "cannot change") {
		t.Fatalf("the page the failure returns to does not show the preview's reason:\n%s", page)
	}
	if len(f.stub.seen()) != 0 {
		t.Fatal("asked the person to approve a change that cannot be made")
	}
	if got, _ := agentauth.ActiveByName(f.dir, "claude-code"); got != nil {
		t.Fatal("credential issued for nothing")
	}
}

func TestConnectNeedsWasyncAxiAndTakesAFreeName(t *testing.T) {
	f, c, csrf := newConnectFixture(t, false)
	f.stub.set(true)
	if _, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "claude-desktop"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	page, digest := preview(t, c, "claude-desktop")
	if !strings.Contains(page, "<strong>claude-desktop-2</strong>") {
		t.Fatalf("name:\n%s", page)
	}
	if err := os.Remove(f.axi); err != nil {
		t.Fatal(err)
	}
	if _, _, hd := c.post("/ui/setup/connect", url.Values{"csrf": {csrf}, "agent": {"claude-desktop"}, "digest": {digest}}); !strings.Contains(hd.Get("Location"), "n=noaxi") {
		t.Fatalf("without wasync-axi: %v", hd)
	}
	if code, _, _ := c.post("/ui/setup/connect", url.Values{"csrf": {csrf}, "agent": {"../etc"}}); code != 400 {
		t.Fatalf("unknown agent: %d", code)
	}
	if code, _, _ := c.post("/ui/setup/disconnect", url.Values{"csrf": {"forged"}, "agent": {"codex"}}); code != 403 {
		t.Fatalf("forged disconnect: %d", code)
	}
}

func TestConnectionsNeedASession(t *testing.T) {
	f, _, _ := newConnectFixture(t, false)
	anon := newPageClient(t, f.accessFixture)
	if code, _, _ := anon.get("/ui/setup/connections"); code != 401 {
		t.Fatalf("status without a session: %d", code)
	}
	if code, _, _ := anon.post("/ui/setup/disconnect", url.Values{"agent": {"codex"}}); code != 401 {
		t.Fatalf("disconnect without a session: %d", code)
	}
}

func TestDisconnectKeepsTheRecordWhenTheSettingsCannotBeCleaned(t *testing.T) {
	f, c, csrf := newConnectFixture(t, false)
	f.stub.set(true)
	_, digest := preview(t, c, "claude-code")
	c.post("/ui/setup/connect", url.Values{"csrf": {csrf}, "agent": {"claude-code"}, "digest": {digest}})
	conn, _ := agentconnect.Find(f.dir, agentconnect.ClaudeCode)
	if conn == nil {
		t.Fatal("not connected")
	}
	if err := os.WriteFile(conn.ConfigPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, hd := c.post("/ui/setup/disconnect", url.Values{"csrf": {csrf}, "agent": {"claude-code"}}); !strings.Contains(hd.Get("Location"), "n=disconnectleft") {
		t.Fatalf("disconnect: %v", hd)
	}
	if got, _ := agentauth.ActiveByName(f.dir, conn.Client); got != nil {
		t.Fatal("credential not revoked")
	}
	if again, _ := agentconnect.Find(f.dir, agentconnect.ClaudeCode); again == nil {
		t.Fatal("record dropped, so Disconnect cannot be tried again")
	}
	if err := os.WriteFile(conn.ConfigPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, hd := c.post("/ui/setup/disconnect", url.Values{"csrf": {csrf}, "agent": {"claude-code"}}); !strings.Contains(hd.Get("Location"), "n=agentdisconnected") {
		t.Fatalf("second disconnect: %v", hd)
	}
}
