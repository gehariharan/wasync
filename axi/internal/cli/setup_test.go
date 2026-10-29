package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/agentsetup"
	"github.com/gehariharan/wasync/axi/internal/installhint"
	"github.com/gehariharan/wasync/axi/skills"
)

// The harness runs whatsapp-axi as /home/user/bin/whatsapp-axi with
// nothing on PATH, so the hook command uses that path.
var (
	harnessBin  = filepath.Join(home, "bin", "whatsapp-axi")
	harnessHook = agentsetup.HookCommand(harnessBin, false, runtime.GOOS)
)

func agentHome(h *harness) string { return h.env["WHATSAPP_AXI_AGENT_HOME"] }

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func setupJSONRun(t *testing.T, h *harness, args ...string) (setupJSON, int) {
	t.Helper()
	out, code := h.run(t, append([]string{"setup"}, append(args, "--json")...)...)
	var r setupJSON
	if code == ExitOK {
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	return r, code
}

const existingSettings = `{
    "model": "opus",
    "hooks": {
        "SessionStart": [{"matcher": "startup", "hooks": [{"type": "command", "command": "echo hello"}]}],
        "Stop": [{"hooks": [{"type": "command", "command": "notify"}]}]
    }
}
`

func TestSetupHooksIsIdempotentAndPreservesSettings(t *testing.T) {
	h := newHarness(t)
	settings := filepath.Join(agentHome(h), ".claude", "settings.json")
	writeFile(t, settings, existingSettings)

	out, code := h.run(t, "setup", "hooks")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "setup: hooks", "dry_run: false", "hook_command: "+harnessHook,
		"changes[1]{agent,item,path,change}:", "claude,session_hooks,", ",add", "written: 1", "whatsapp-axi setup hooks --remove")
	installed := readFile(t, settings)
	if !strings.Contains(installed, `"command": "echo hello"`) || !strings.Contains(installed, `"command": "notify"`) || !strings.Contains(installed, harnessHook) {
		t.Fatalf("settings:\n%s", installed)
	}

	// Rerunning changes nothing, not even the bytes.
	r, code := setupJSONRun(t, h, "hooks")
	if code != ExitOK || r.Written != 0 || len(r.Changes) != 1 || r.Changes[0].Change != agentsetup.ActionNone || r.Help[0] != "Already set up; nothing changed" {
		t.Fatalf("%d %+v", code, r)
	}
	if readFile(t, settings) != installed {
		t.Fatal("second run rewrote the file")
	}

	// A reinstall elsewhere repairs the path in place.
	moved := strings.ReplaceAll(installed, harnessHook, "/opt/old/whatsapp-axi --brief")
	writeFile(t, settings, moved)
	if r, _ := setupJSONRun(t, h, "hooks"); r.Changes[0].Change != agentsetup.ActionUpdate || r.Written != 1 {
		t.Fatalf("%+v", r)
	}
	if readFile(t, settings) != installed {
		t.Fatalf("path repair:\n%s", readFile(t, settings))
	}

	out, code = h.run(t, "setup", "hooks", "--remove")
	if code != ExitOK || readFile(t, settings) != existingSettings {
		t.Fatalf("remove: %d\n%s\n%s", code, out, readFile(t, settings))
	}
	mustContain(t, out, "claude,session_hooks,", ",remove", "codex,session_hooks,", ",none", "written: 1")
	out, _ = h.run(t, "setup", "hooks", "--remove")
	mustContain(t, out, "written: 0", "Nothing to remove")
}

func TestSetupDryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	root := agentHome(h)
	for _, what := range []string{"hooks", "skill"} {
		r, code := setupJSONRun(t, h, what, "--dry-run", "--agent", "all")
		if code != ExitOK || !r.DryRun || r.Written != 0 || !strings.HasPrefix(r.Help[0], "Dry run: nothing was written") {
			t.Fatalf("%s: %d %+v", what, code, r)
		}
		for _, c := range r.Changes {
			if c.Change != agentsetup.ActionAdd {
				t.Errorf("%s: %+v", what, c)
			}
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("dry run created %v", entries)
	}
	settings := filepath.Join(root, ".claude", "settings.json")
	writeFile(t, settings, existingSettings)
	if _, code := h.run(t, "setup", "hooks", "--dry-run"); code != ExitOK || readFile(t, settings) != existingSettings {
		t.Fatal("dry run changed settings")
	}
	if _, code := h.run(t, "setup", "hooks"); code != ExitOK {
		t.Fatal("install failed")
	}
	before := readFile(t, settings)
	if r, _ := setupJSONRun(t, h, "hooks", "--remove", "--dry-run"); r.Changes[0].Change != agentsetup.ActionRemove || r.Written != 0 {
		t.Fatalf("%+v", r)
	}
	if readFile(t, settings) != before {
		t.Fatal("dry-run remove changed settings")
	}
}

func TestSetupRefusesMalformedSettings(t *testing.T) {
	h := newHarness(t)
	root := agentHome(h)
	settings := filepath.Join(root, ".claude", "settings.json")
	bad := "{\n  \"model\": \"opus\",\n}\n"
	writeFile(t, settings, bad)
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, code := h.run(t, "setup", "hooks")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "is not valid JSON (line 3, column 1", "code: config_invalid", "help: fix the JSON by hand", "never rewrites a file it cannot parse")
	if readFile(t, settings) != bad {
		t.Fatal("malformed file modified")
	}
	// Nothing is written for any agent when one file cannot be edited.
	for _, p := range []string{filepath.Join(root, ".codex", "hooks.json"), filepath.Join(root, ".codex", "config.toml")} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s written despite the refusal", p)
		}
	}
	var e map[string]string
	out, _ = h.run(t, "setup", "hooks", "--json")
	if json.Unmarshal([]byte(out), &e) != nil || e["error_code"] != agentsetup.CodeConfigInvalid {
		t.Fatalf("json error: %s", out)
	}
}

func TestSetupAgentSelection(t *testing.T) {
	h := newHarness(t)
	root := agentHome(h)
	r, _ := setupJSONRun(t, h, "hooks", "--dry-run")
	if len(r.Changes) != 1 || r.Changes[0].Agent != "claude" {
		t.Fatalf("no agent dirs: %+v", r.Changes)
	}
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	r, _ = setupJSONRun(t, h, "hooks")
	if len(r.Changes) != 2 || r.Changes[0].Agent != "codex" || r.Changes[1].Item != agentsetup.ItemHooksFeature || r.Written != 2 {
		t.Fatalf("codex only: %+v", r.Changes)
	}
	if got := readFile(t, filepath.Join(root, ".codex", "config.toml")); got != "[features]\nhooks = true\n" {
		t.Fatalf("config.toml %q", got)
	}
	r, _ = setupJSONRun(t, h, "hooks", "--agent", "claude,codex,claude")
	if len(r.Changes) != 3 || r.Written != 1 {
		t.Fatalf("explicit: %+v", r)
	}
	out, code := h.run(t, "setup", "hooks", "--agent", "cursor")
	if code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, `unknown agent "cursor"`, "claude, codex, opencode or all")
	out, code = h.run(t, "setup", "nonsense")
	if code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, `unknown setup target "nonsense"`)
	if _, code := h.run(t, "setup"); code != ExitUsage {
		t.Fatal("setup without a target")
	}
}

func TestSetupDirOverridesAgentHome(t *testing.T) {
	h := newHarness(t)
	project := t.TempDir()
	if _, code := h.run(t, "setup", "hooks", "--dir", project); code != ExitOK {
		t.Fatal("setup --dir failed")
	}
	if st := agentsetup.InspectHook(agentsetup.Agents[0], project, harnessHook); !st.Current {
		t.Fatalf("%+v", st)
	}
	if entries, _ := os.ReadDir(agentHome(h)); len(entries) != 0 {
		t.Fatal("--dir also wrote to the agent home")
	}
}

func TestSetupFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
	h := newHarness(t)
	for _, what := range []string{"hooks", "skill"} {
		if _, code := h.run(t, "setup", what); code != ExitOK {
			t.Fatal(what)
		}
	}
	root := agentHome(h)
	for _, p := range []string{filepath.Join(root, ".claude", "settings.json"), filepath.Join(root, ".claude", "skills", "whatsapp-axi", "SKILL.md")} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", p, info.Mode(), err)
		}
	}
}

func TestSetupSkillAndDoctorChecks(t *testing.T) {
	h := newHarness(t)
	root := agentHome(h)
	r, code := setupJSONRun(t, h, "skill")
	if code != ExitOK || r.Written != 1 || r.Changes[0].Item != agentsetup.ItemSkill {
		t.Fatalf("%d %+v", code, r)
	}
	path := filepath.Join(root, ".claude", "skills", "whatsapp-axi", "SKILL.md")
	if readFile(t, path) != string(skills.WhatsappAXI) {
		t.Fatal("installed skill differs from the embedded one")
	}
	if r, _ := setupJSONRun(t, h, "skill"); r.Written != 0 || r.Changes[0].Change != agentsetup.ActionNone {
		t.Fatalf("not idempotent: %+v", r)
	}

	checks := func() map[string]doctorCheck {
		out, _ := h.run(t, "doctor", "--json")
		var rep struct {
			Checks []doctorCheck `json:"checks"`
		}
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		m := map[string]doctorCheck{}
		for _, c := range rep.Checks {
			if c.Area == areaHooks {
				m[c.Name] = c
			}
		}
		return m
	}
	c := checks()
	if c["skill"].Status != statusPass || c["hooks"].Status != statusSkip || !strings.Contains(c["hooks"].Detail, "skill is installed instead") {
		t.Fatalf("skill only: %+v", c)
	}
	writeFile(t, path, string(skills.WhatsappAXI)+"\nlocal edit\n")
	if c := checks()["skill"]; c.Status != statusWarn || c.Fix != "whatsapp-axi setup skill" {
		t.Fatalf("stale skill: %+v", c)
	}
	if _, code := h.run(t, "setup", "skill", "--remove"); code != ExitOK {
		t.Fatal("remove")
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty skill directory left behind")
	}

	c = checks()
	if c["hooks"].Status != statusWarn || c["hooks"].Fix != "whatsapp-axi setup hooks" || !strings.Contains(c["hooks"].Detail, "not installed") {
		t.Fatalf("nothing installed: %+v", c["hooks"])
	}
	if c["skill"].Status != statusSkip {
		t.Fatalf("%+v", c["skill"])
	}
	settings := filepath.Join(root, ".claude", "settings.json")
	writeFile(t, settings, `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "/opt/old/whatsapp-axi --brief"}]}]}}`)
	if c := checks()["hooks"]; c.Status != statusWarn || c.Fix != "whatsapp-axi setup hooks" || !strings.Contains(c.Detail, "/opt/old/whatsapp-axi --brief") {
		t.Fatalf("stale hook: %+v", c)
	}
	writeFile(t, settings, `{"hooks": `)
	if c := checks()["hooks"]; c.Status != statusWarn || !strings.Contains(c.Detail, "not valid JSON") {
		t.Fatalf("malformed: %+v", c)
	}
	writeFile(t, settings, `{}`)
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, code := h.run(t, "setup", "hooks"); code != ExitOK {
		t.Fatal("setup hooks")
	}
	if c := checks()["hooks"]; c.Status != statusPass || !strings.Contains(c.Detail, "Claude Code") || !strings.Contains(c.Detail, "Codex") {
		t.Fatalf("installed: %+v", c)
	}
	writeFile(t, filepath.Join(root, ".codex", "config.toml"), "[features]\nhooks = false\n")
	if c := checks()["hooks"]; c.Status != statusWarn || c.Fix != "whatsapp-axi setup hooks --agent codex" {
		t.Fatalf("codex feature off: %+v", c)
	}
	writeFile(t, filepath.Join(root, ".codex", "config.toml"), "[\"features\"]\n'hooks' = true\n")
	if c := checks()["hooks"]; c.Status != statusPass {
		t.Fatalf("quoted Codex feature: %+v", c)
	}
	writeFile(t, filepath.Join(root, ".codex", "config.toml"), "[features]\nhooks = true\nhooks = false\n")
	if c := checks()["hooks"]; c.Status != statusWarn || !strings.Contains(c.Detail, "config.toml") {
		t.Fatalf("invalid Codex config: %+v", c)
	}
}

func TestBriefHomeView(t *testing.T) {
	h, _ := queryHarness(t)
	out, code := h.run(t, "--brief")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "whatsapp-axi: ", "wasync: offline (transport query; credential missing)", "chats: 4", briefMoreHint, untrustedHint)
	for _, leak := range []string{"Family", "Team A", "untrusted_name", "recent["} {
		if strings.Contains(out, leak) {
			t.Errorf("brief view prints %q:\n%s", leak, out)
		}
	}
	if lines := strings.Count(out, "\n"); lines > 10 {
		t.Errorf("brief view has %d lines", lines)
	}
	var j struct {
		ChatsTotal *int `json:"chats_total"`
		Wasync     struct {
			State string `json:"state"`
		} `json:"wasync"`
		Help []string `json:"help"`
	}
	out, _ = h.run(t, "--brief", "--json")
	if err := json.Unmarshal([]byte(out), &j); err != nil || j.ChatsTotal == nil || *j.ChatsTotal != 4 || j.Wasync.State != StateOffline {
		t.Fatalf("%v %s", err, out)
	}
	// Without wasync the brief view still exits 0 with the install hint.
	out, code = newHarness(t).run(t, "--brief")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "wasync: not_installed", installhint.InstallCore())
}

func TestSetupCodexInvalidConfigWritesNothing(t *testing.T) {
	for _, content := range []string{
		"features = { hooks = false }\n",
		"[\"features\"]\nhooks = false\n",
		"[features]\n\"hooks\" = false\n",
		"features.hooks = false\n",
		"[features]\nhooks = false\nhooks = true\n",
	} {
		h := newHarness(t)
		root := agentHome(h)
		path := agentsetup.CodexConfigPath(root)
		writeFile(t, path, content)
		out, code := h.run(t, "setup", "hooks", "--agent", "codex", "--json")
		var result struct {
			Code string `json:"error_code"`
		}
		if code != ExitError || json.Unmarshal([]byte(out), &result) != nil || result.Code != agentsetup.CodeConfigInvalid {
			t.Fatalf("%q: exit %d %s", content, code, out)
		}
		if got := readFile(t, path); got != content {
			t.Fatalf("config changed: %q", got)
		}
		if _, err := os.Stat(filepath.Join(root, ".codex", "hooks.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("hook written before validation: %v", err)
		}
	}
}
