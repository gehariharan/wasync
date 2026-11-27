package agentconnect

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func testEnv(t *testing.T) Env {
	t.Helper()
	home := t.TempDir()
	return Env{
		GOOS: runtime.GOOS, Home: home, ConfigDir: filepath.Join(home, "config"),
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Now:      func() time.Time { return time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC) },
	}
}

var testEntry = Entry{Command: `/opt/wasync/bin/wasync-axi`, Args: []string{"mcp"}, Env: map[string]string{"WASYNC_AXI_CONFIG_DIR": `C:\Users\me "q"\agents\codex`, "WASYNC_BIN": "/opt/wasync/bin/wasync"}}

func agent(kind Kind) Agent {
	a, _ := Lookup(string(kind))
	return a
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// connectThenDisconnect connects, checks the entry, disconnects and returns
// the file's content after each step.
func connectThenDisconnect(t *testing.T, e Env, kind Kind) (connected, disconnected string, c Connection) {
	t.Helper()
	p, err := e.PlanConnect(agent(kind), testEntry)
	if err != nil {
		t.Fatal(err)
	}
	written, _, err := e.Write(p)
	if err != nil {
		t.Fatal(err)
	}
	connected = read(t, p.Path)
	c = Connection{Agent: kind, ConfigPath: p.Path, Entry: testEntry, Previous: written.Previous()}
	if !Wrote(c) {
		t.Fatalf("entry not recognised in:\n%s", connected)
	}
	d, kept, err := e.PlanDisconnect(c)
	if err != nil || kept {
		t.Fatalf("disconnect plan: kept=%v err=%v", kept, err)
	}
	if _, _, err := e.Write(d); err != nil {
		t.Fatal(err)
	}
	return connected, read(t, p.Path), c
}

func TestCodexRoundTripKeepsEveryOtherByte(t *testing.T) {
	cases := map[string]string{
		"empty":   "",
		"plain":   "model = \"gpt-5\"\n\n[features]\nhooks = true # keep\n",
		"no-nl":   "model = \"gpt-5\"",
		"crlf":    "model = \"gpt-5\"\r\n\r\n[mcp_servers.other]\r\ncommand = \"x\"\r\n",
		"servers": "[mcp_servers.other]\ncommand = \"x\"\nargs = [\"a\"]\n\n# comment for the next table\n[profiles.fast]\nmodel = \"o3\"\n",
	}
	for name, before := range cases {
		t.Run(name, func(t *testing.T) {
			e := testEnv(t)
			path := e.ConfigPath(Codex)
			if before != "" {
				write(t, path, before)
			}
			connected, after, _ := connectThenDisconnect(t, e, Codex)
			var cfg struct {
				MCP map[string]struct {
					Command string
					Args    []string
					Env     map[string]string
				} `toml:"mcp_servers"`
			}
			if _, err := toml.Decode(connected, &cfg); err != nil {
				t.Fatalf("connected file is not TOML: %v\n%s", err, connected)
			}
			got := cfg.MCP[ServerName]
			if got.Command != testEntry.Command || strings.Join(got.Args, " ") != "mcp" || got.Env["WASYNC_AXI_CONFIG_DIR"] != testEntry.Env["WASYNC_AXI_CONFIG_DIR"] {
				t.Fatalf("entry = %+v\n%s", got, connected)
			}
			if !strings.HasPrefix(connected, strings.TrimRight(before, "\r\n")) {
				t.Fatalf("connect changed existing bytes:\n%q\n%q", before, connected)
			}
			want := before
			if name == "no-nl" {
				want += "\n" // the file gains its final line break
			}
			if after != want {
				t.Fatalf("disconnect did not restore the file:\nwant %q\ngot  %q", want, after)
			}
		})
	}
}

func TestCodexReplacesAndRestoresAnEarlierEntry(t *testing.T) {
	e := testEnv(t)
	before := "model = \"gpt-5\"\n\n[mcp_servers.whatsapp]\ncommand = \"whatsapp-axi\"\nargs = [\"mcp\"]\n\n[mcp_servers.whatsapp.env]\nX = \"1\"\n\n[profiles.fast]\nmodel = \"o3\"\n"
	write(t, e.ConfigPath(Codex), before)
	p, err := e.PlanConnect(agent(Codex), testEntry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Removed, `command = "whatsapp-axi"`) || !strings.Contains(p.Removed, `X = "1"`) {
		t.Fatalf("removed = %q", p.Removed)
	}
	if strings.Contains(p.Removed, "profiles") {
		t.Fatalf("removed another table: %q", p.Removed)
	}
	_, after, _ := connectThenDisconnect(t, e, Codex)
	m, err := decodeTOML(after)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := decodeTOML(before)
	if !equalJSON(m, b) {
		t.Fatalf("restored %q, want the same settings as %q", after, before)
	}
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestCodexRefusesWhatItCannotEditExactly(t *testing.T) {
	for name, content := range map[string]string{
		"invalid": "model = \n",
		"inline":  "[mcp_servers]\nwhatsapp = { command = \"x\" }\n",
		"dotted":  "mcp_servers.whatsapp.command = \"x\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := testEnv(t)
			write(t, e.ConfigPath(Codex), content)
			if _, err := e.PlanConnect(agent(Codex), testEntry); err == nil {
				t.Fatal("expected a refusal")
			}
			if read(t, e.ConfigPath(Codex)) != content {
				t.Fatal("file changed")
			}
		})
	}
}

func TestJSONRoundTripKeepsEveryOtherByte(t *testing.T) {
	cases := map[string]string{
		"empty":   "",
		"object":  "{\n    \"numStartups\": 3,\n    \"projects\": {\"/x\": {\"a\": [1, 2]}}\n}\n",
		"servers": "{\n  \"mcpServers\": {\n    \"fs\": {\"command\": \"npx\", \"args\": [\"-y\"]}\n  },\n  \"z\": true\n}",
		"tabs":    "{\r\n\t\"mcpServers\": {}\r\n}\r\n",
	}
	for _, kind := range []Kind{ClaudeCode, ClaudeDesktop} {
		for name, before := range cases {
			t.Run(string(kind)+"/"+name, func(t *testing.T) {
				e := testEnv(t)
				path := e.ConfigPath(kind)
				if before != "" {
					write(t, path, before)
				}
				connected, after, _ := connectThenDisconnect(t, e, kind)
				var cfg struct {
					MCP map[string]map[string]any `json:"mcpServers"`
				}
				if err := json.Unmarshal([]byte(connected), &cfg); err != nil {
					t.Fatalf("connected file is not JSON: %v\n%s", err, connected)
				}
				got := cfg.MCP[ServerName]
				if got["command"] != testEntry.Command || (kind == ClaudeCode) != (got["type"] == "stdio") {
					t.Fatalf("entry = %v", got)
				}
				switch {
				case before == "":
					if strings.Join(strings.Fields(after), "") != `{"mcpServers":{}}` {
						t.Fatalf("after = %q", after)
					}
				case name == "object":
					// The emptied mcpServers object stays; everything else is as it was.
					if after != strings.Replace(before, "]}}\n}", "]}},\n    \"mcpServers\": {}\n}", 1) {
						t.Fatalf("after = %q", after)
					}
				default:
					if after != before {
						t.Fatalf("disconnect did not restore the file:\nwant %q\ngot  %q", before, after)
					}
				}
			})
		}
	}
}

func TestJSONReplacesAndRestoresAnEarlierEntry(t *testing.T) {
	e := testEnv(t)
	before := "{\n  \"mcpServers\": {\n    \"whatsapp\": {\n      \"command\": \"whatsapp-axi\",\n      \"args\": [\"mcp\"]\n    },\n    \"fs\": {}\n  }\n}\n"
	write(t, e.ConfigPath(ClaudeDesktop), before)
	p, err := e.PlanConnect(agent(ClaudeDesktop), testEntry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Removed, `"whatsapp-axi"`) {
		t.Fatalf("removed = %q", p.Removed)
	}
	_, after, _ := connectThenDisconnect(t, e, ClaudeDesktop)
	var a, b any
	_ = json.Unmarshal([]byte(after), &a)
	_ = json.Unmarshal([]byte(before), &b)
	if !equalJSON(a, b) {
		t.Fatalf("restored %s", after)
	}
}

func TestJSONRefusals(t *testing.T) {
	for name, content := range map[string]string{
		"invalid":   "{",
		"array":     "[]",
		"servers":   `{"mcpServers": []}`,
		"duplicate": `{"mcpServers": {"whatsapp": {}, "whatsapp": {}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := testEnv(t)
			write(t, e.ConfigPath(ClaudeCode), content)
			if _, err := e.PlanConnect(agent(ClaudeCode), testEntry); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}

func TestWriteBacksUpAndRefusesAChangedFile(t *testing.T) {
	e := testEnv(t)
	path := e.ConfigPath(ClaudeDesktop)
	write(t, path, `{"a": 1}`)
	p, err := e.PlanConnect(agent(ClaudeDesktop), testEntry)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, `{"a": 1, "mcpServers": {"whatsapp": {"command": "other"}}}`)
	if _, _, err := e.Write(p); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if got := read(t, path); got != `{"a": 1, "mcpServers": {"whatsapp": {"command": "other"}}}` {
		t.Fatalf("refused write changed the file: %s", got)
	}
	write(t, path, `{"a": 1}`)
	if p, err = e.PlanConnect(agent(ClaudeDesktop), testEntry); err != nil {
		t.Fatal(err)
	}
	write(t, path, `{"a": 2}`)
	_, bak, err := e.Write(p)
	if err != nil {
		t.Fatal(err)
	}
	if bak != path+".wasync-backup-20261126-100000" || read(t, bak) != `{"a": 2}` {
		t.Fatalf("backup %s = %q", bak, read(t, bak))
	}
	var got struct {
		A          int                       `json:"a"`
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &got); err != nil || got.A != 2 || !testEntry.matches(got.MCPServers[ServerName]) {
		t.Fatalf("after a change elsewhere, file = %s (err %v)", read(t, path), err)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want the file's own 0644", info.Mode())
	}
	if p, err = e.PlanConnect(agent(ClaudeDesktop), testEntry); err != nil {
		t.Fatal(err)
	}
	if p.Changes() {
		t.Fatal("connecting again should change nothing")
	}
}

func TestCodexWriteAfterTablesAreAddedBelowTheReplacedEntry(t *testing.T) {
	for name, tail := range map[string]string{"no final newline": "", "blank lines at the end": "\n\n\n"} {
		t.Run(name, func(t *testing.T) {
			e := testEnv(t)
			path := e.ConfigPath(Codex)
			before := "model = \"m\"\n\n[mcp_servers.whatsapp]\ncommand = \"old\"" + tail
			write(t, path, before)
			p, err := e.PlanConnect(agent(Codex), testEntry)
			if err != nil {
				t.Fatal(err)
			}
			added := "\n\n[marketplaces.wasync]\nsource = \"/x\"\n"
			if tail == "" {
				added = "\n" + added
			}
			write(t, path, before+added)
			if again, err := e.PlanConnect(agent(Codex), testEntry); err != nil || again.Digest != p.Digest {
				t.Fatalf("a table added after the entry changed the preview's fingerprint (err %v)", err)
			}
			written, _, err := e.Write(p)
			if err != nil {
				t.Fatalf("a table added after the entry blocked the write: %v", err)
			}
			got, err := decodeTOML(read(t, path))
			if err != nil {
				t.Fatal(err)
			}
			if !testEntry.matches(got["mcp_servers"].(map[string]any)[ServerName]) || got["marketplaces"] == nil || got["model"] != "m" {
				t.Fatalf("file after connect:\n%s", read(t, path))
			}
			c := Connection{Agent: Codex, ConfigPath: path, Entry: testEntry, Previous: written.Previous()}
			d, _, err := e.PlanDisconnect(c)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := e.Write(d); err != nil {
				t.Fatal(err)
			}
			if got, err = decodeTOML(read(t, path)); err != nil {
				t.Fatalf("restored file is not TOML: %v\n%s", err, read(t, path))
			}
			if got["mcp_servers"].(map[string]any)[ServerName].(map[string]any)["command"] != "old" || got["marketplaces"] == nil {
				t.Fatalf("file after disconnect:\n%s", read(t, path))
			}
		})
	}
}

func TestDisconnectRemovesAnEntryThatWasAlreadyThere(t *testing.T) {
	for _, kind := range []Kind{Codex, ClaudeCode, ClaudeDesktop} {
		t.Run(string(kind), func(t *testing.T) {
			e := testEnv(t)
			first, _ := e.PlanConnect(agent(kind), testEntry)
			if _, _, err := e.Write(first); err != nil {
				t.Fatal(err)
			}
			p, err := e.PlanConnect(agent(kind), testEntry)
			if err != nil {
				t.Fatal(err)
			}
			written, _, err := e.Write(p)
			if err != nil {
				t.Fatal(err)
			}
			c := Connection{Agent: kind, ConfigPath: p.Path, Entry: testEntry, Previous: written.Previous()}
			d, _, err := e.PlanDisconnect(c)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := e.Write(d); err != nil {
				t.Fatal(err)
			}
			if Wrote(c) {
				t.Fatalf("wasync's entry is still in the file after Disconnect:\n%s", read(t, p.Path))
			}
		})
	}
}

func TestNewFileIsPrivate(t *testing.T) {
	e := testEnv(t)
	p, err := e.PlanConnect(agent(ClaudeDesktop), testEntry)
	if err != nil || p.Exists {
		t.Fatalf("exists=%v err=%v", p.Exists, err)
	}
	if _, bak, err := e.Write(p); err != nil || bak != "" {
		t.Fatalf("backup=%q err=%v", bak, err)
	}
	if info, _ := os.Stat(p.Path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
}

func TestWriteFollowsASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	e := testEnv(t)
	real := filepath.Join(e.Home, "dotfiles", "claude.json")
	write(t, real, `{}`)
	if err := os.Symlink(real, e.ConfigPath(ClaudeCode)); err != nil {
		t.Fatal(err)
	}
	p, err := e.PlanConnect(agent(ClaudeCode), testEntry)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Write(p); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Lstat(e.ConfigPath(ClaudeCode)); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file")
	}
	if !strings.Contains(read(t, real), "wasync-axi") {
		t.Fatal("target not edited")
	}
}

func TestDisconnectLeavesAnEntryThePersonChanged(t *testing.T) {
	e := testEnv(t)
	p, _ := e.PlanConnect(agent(ClaudeCode), testEntry)
	if _, _, err := e.Write(p); err != nil {
		t.Fatal(err)
	}
	changed := `{"mcpServers": {"whatsapp": {"command": "mine"}}}`
	write(t, p.Path, changed)
	c := Connection{Agent: ClaudeCode, ConfigPath: p.Path, Entry: testEntry}
	d, kept, err := e.PlanDisconnect(c)
	if err != nil || !kept || d.Changes() {
		t.Fatalf("kept=%v changes=%v err=%v", kept, d.Changes(), err)
	}
	write(t, p.Path, `{}`)
	if _, kept, err = e.PlanDisconnect(c); err != nil || kept {
		t.Fatalf("removed entry: kept=%v err=%v", kept, err)
	}
}

func TestPaths(t *testing.T) {
	e := testEnv(t)
	env := map[string]string{}
	e.Getenv = func(k string) string { return env[k] }
	if got := e.ConfigPath(Codex); got != filepath.Join(e.Home, ".codex", "config.toml") {
		t.Fatal(got)
	}
	if got := e.ConfigPath(ClaudeCode); got != filepath.Join(e.Home, ".claude.json") {
		t.Fatal(got)
	}
	if got := e.ConfigPath(ClaudeDesktop); got != filepath.Join(e.ConfigDir, "Claude", "claude_desktop_config.json") {
		t.Fatal(got)
	}
	env["CODEX_HOME"], env["CLAUDE_CONFIG_DIR"] = "/x/codex", "/x/claude"
	if e.ConfigPath(Codex) != filepath.Join("/x/codex", "config.toml") || e.ConfigPath(ClaudeCode) != filepath.Join("/x/claude", ".claude.json") {
		t.Fatal("environment overrides ignored")
	}
}

func TestFound(t *testing.T) {
	e := testEnv(t)
	for _, k := range []Kind{Codex, ClaudeCode, ClaudeDesktop} {
		if e.Found(k) {
			t.Fatalf("%s found on an empty computer", k)
		}
	}
	write(t, e.ConfigPath(ClaudeCode), "{}")
	if err := os.MkdirAll(filepath.Join(e.Home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.ConfigDir, "Claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, k := range []Kind{Codex, ClaudeCode, ClaudeDesktop} {
		if !e.Found(k) {
			t.Fatalf("%s not found", k)
		}
	}
}

func TestRegistry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if c, err := Find(dir, Codex); err != nil || c != nil {
		t.Fatalf("%v %v", c, err)
	}
	c := Connection{Agent: Codex, Client: "codex", ClientID: "abc", ConfigPath: "/x", Entry: testEntry, ConnectedAt: time.Now().UTC()}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	c.Client = "codex-2"
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, Connection{Agent: ClaudeCode, Client: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	all, err := Connections(dir)
	if err != nil || len(all) != 2 || all[0].Client != "codex-2" {
		t.Fatalf("%+v %v", all, err)
	}
	if info, _ := os.Stat(filepath.Join(dir, RegistryFile)); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if err := Forget(dir, Codex); err != nil {
		t.Fatal(err)
	}
	if c, _ := Find(dir, Codex); c != nil {
		t.Fatal("not forgotten")
	}
}

func TestCodexPlugin(t *testing.T) {
	e := testEnv(t)
	bin := filepath.Join(e.Home, "bin", "codex")
	write(t, bin, "")
	e.LookPath = func(name string) (string, error) {
		if name == "codex" {
			return bin, nil
		}
		return "", errors.New("not found")
	}
	var ran []string
	e.Run = func(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
		var kept []string
		for _, v := range env {
			if !strings.HasPrefix(v, "PATH=") { // TestCodexPluginRunsWithLoginItemPath covers PATH
				kept = append(kept, v)
			}
		}
		ran = append(ran, strings.Join(kept, " ")+" "+filepath.Base(name)+" "+strings.Join(args, " "))
		return nil, nil
	}
	data := t.TempDir()
	p, err := e.PlanCodexPlugin(data)
	if err != nil || !p.Marketplace || !p.Plugin || len(p.Steps()) != 2 {
		t.Fatalf("%+v %v", p, err)
	}
	m, pl, err := e.InstallCodexPlugin(context.Background(), p)
	if err != nil || !m || !pl {
		t.Fatalf("%v %v %v", m, pl, err)
	}
	home := "CODEX_HOME=" + e.CodexHome()
	want := []string{home + " codex plugin marketplace add " + p.Dir, home + " codex plugin add whatsapp@wasync"}
	if strings.Join(ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran %q", ran)
	}
	if read(t, filepath.Join(p.Dir, ".agents", "plugins", "marketplace.json")) == "" || read(t, filepath.Join(p.Dir, "plugins", "whatsapp", "plugin.json")) == "" {
		t.Fatal("marketplace folder incomplete")
	}
	// Already installed (for example from GitHub): nothing to add, nothing to remove later.
	write(t, e.ConfigPath(Codex), "[marketplaces.wasync]\nsource = \"x\"\n\n[plugins.\"whatsapp@wasync\"]\nenabled = true\n")
	if p, err = e.PlanCodexPlugin(data); err != nil || p.Marketplace || p.Plugin || len(p.Steps()) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	ran = nil
	if err := e.RemoveCodexPlugin(context.Background(), data, true, true); err != nil {
		t.Fatal(err)
	}
	want = []string{home + " codex plugin remove whatsapp@wasync", home + " codex plugin marketplace remove wasync"}
	if strings.Join(ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran %q", ran)
	}
	if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
		t.Fatal("marketplace folder left behind")
	}
}

func TestNoCodexCommandMeansNoPlugin(t *testing.T) {
	e := testEnv(t)
	p, err := e.PlanCodexPlugin(t.TempDir())
	if err != nil || p.Codex != "" || p.Steps() != nil {
		t.Fatalf("%+v %v", p, err)
	}
	if m, pl, err := e.InstallCodexPlugin(context.Background(), p); m || pl || err != nil {
		t.Fatal("installed without codex")
	}
}

// A login item starts wasync with a bare PATH. codex installed by npm or
// Homebrew is a "#!/usr/bin/env node" script, so the folder wasync found
// codex in (and the other package-manager folders) must be on codex's PATH,
// or env cannot find node.
func TestCodexPluginRunsWithLoginItemPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shebang scripts")
	}
	e := testEnv(t)
	bin := filepath.Join(e.Home, "brew", "bin")
	ran := filepath.Join(e.Home, "ran")
	writeExec := func(path, content string) {
		write(t, path, content)
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExec(filepath.Join(bin, "fakenode"), "#!/bin/sh\necho \"$@\" >> "+ran+"\n")
	writeExec(filepath.Join(bin, "codex"), "#!/usr/bin/env fakenode\n")
	e.ExtraPath = []string{bin}
	e.Getenv = func(name string) string {
		if name == "PATH" {
			return "/usr/bin:/bin"
		}
		return ""
	}
	e.Run = runCommand
	p, err := e.PlanCodexPlugin(t.TempDir())
	if err != nil || p.Codex == "" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, _, err := e.InstallCodexPlugin(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got := read(t, ran); !strings.Contains(got, "plugin marketplace add") || !strings.Contains(got, "plugin add whatsapp@wasync") {
		t.Fatalf("codex ran with %q", got)
	}
}
