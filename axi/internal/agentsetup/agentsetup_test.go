package agentsetup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

const cmd = "whatsapp-axi --brief"

var claude, codex = Agents[0], Agents[1]

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
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

func plan(t *testing.T, root string, remove bool) Change {
	t.Helper()
	c, err := PlanHook(claude, root, cmd, remove)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func apply(t *testing.T, c Change) {
	t.Helper()
	if err := c.Apply(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallIntoMissingFile(t *testing.T) {
	root := t.TempDir()
	c := plan(t, root, false)
	if c.Action != ActionAdd {
		t.Fatalf("action %s", c.Action)
	}
	if _, err := os.Stat(claude.HookPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("planning wrote a file")
	}
	apply(t, c)
	want := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "whatsapp-axi --brief",
            "timeout": 10
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "whatsapp-axi session-end",
            "timeout": 10
          }
        ]
      }
    ]
  }
}
`
	if got := read(t, claude.HookPath(root)); got != want {
		t.Fatalf("got\n%s", got)
	}
	if c := plan(t, root, false); c.Action != ActionNone {
		t.Fatalf("second install: %s", c.Action)
	}
}

// settings with unusual formatting, unrelated hooks and a foreign
// SessionStart hook; every byte of it must survive install + remove.
const userSettings = "{\r\n" +
	"\t\"model\": \"opus\",\r\n" +
	"\t\"env\": {\"A\": \"1\", \"B\": \"x\\u00e9\"},\r\n" +
	"\t\"hooks\": {\r\n" +
	"\t\t\"PreToolUse\": [{\"matcher\": \"Bash\", \"hooks\": [{\"type\": \"command\", \"command\": \"echo pre\"}]}],\r\n" +
	"\t\t\"SessionStart\": [\r\n" +
	"\t\t\t{\"matcher\": \"startup\", \"hooks\": [{\"type\": \"command\", \"command\": \"other-axi --brief\"}]}\r\n" +
	"\t\t]\r\n" +
	"\t},\r\n" +
	"\t\"permissions\": {\"allow\": [\"Bash(ls:*)\"]},   \"z\": 1e3\r\n" +
	"}"

func TestInstallPreservesUnrelatedSettingsByteForByte(t *testing.T) {
	root := t.TempDir()
	path := claude.HookPath(root)
	write(t, path, userSettings)
	c := plan(t, root, false)
	if c.Action != ActionAdd {
		t.Fatalf("action %s", c.Action)
	}
	apply(t, c)
	got := read(t, path)
	if !strings.HasPrefix(got, userSettings[:strings.Index(userSettings, "\r\n\t\t]")]) {
		t.Fatalf("bytes before the insertion changed:\n%s", got)
	}
	if !strings.Contains(got, "\r\n\t\t\t{\r\n\t\t\t\t\"matcher\": \"\",") {
		t.Fatalf("new entry not indented like the file:\n%q", got)
	}
	var v struct {
		Model string
		Env   map[string]string
		Hooks map[string][]struct {
			Matcher string
			Hooks   []hookEntry
		}
		Z float64
	}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatal(err)
	}
	ss := v.Hooks["SessionStart"]
	if v.Model != "opus" || v.Env["B"] != "xé" || v.Z != 1000 || len(v.Hooks["PreToolUse"]) != 1 || len(ss) != 2 ||
		ss[0].Hooks[0].Command != "other-axi --brief" || ss[1].Hooks[0] != desiredEntry(cmd) {
		t.Fatalf("%+v", v)
	}
	if c := plan(t, root, false); c.Action != ActionNone {
		t.Fatalf("not idempotent: %s", c.Action)
	}
	apply(t, plan(t, root, true))
	if got := read(t, path); got != userSettings {
		t.Fatalf("remove did not restore the original bytes:\n%q", got)
	}
	if c := plan(t, root, true); c.Action != ActionNone {
		t.Fatalf("second remove: %s", c.Action)
	}
}

func TestInstallAddsToExistingShapes(t *testing.T) {
	cases := map[string]string{
		"empty object":       `{}`,
		"whitespace only":    " \n",
		"no SessionStart":    `{"hooks": {"Stop": []}}`,
		"empty SessionStart": `{"hooks": {"SessionStart": []}}`,
		"compact":            `{"a":1,"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"x"}]}]}}`,
	}
	for name, src := range cases {
		root := t.TempDir()
		write(t, claude.HookPath(root), src)
		apply(t, plan(t, root, false))
		st := InspectHook(claude, root, cmd)
		if !st.Installed || !st.Current || st.Err != nil {
			t.Errorf("%s: %+v\n%s", name, st, read(t, claude.HookPath(root)))
		}
		if c := plan(t, root, false); c.Action != ActionNone {
			t.Errorf("%s: not idempotent", name)
		}
		apply(t, plan(t, root, true))
		if st := InspectHook(claude, root, cmd); st.Installed {
			t.Errorf("%s: still installed after remove", name)
		}
	}
}

func TestPathRepairAndDuplicates(t *testing.T) {
	root := t.TempDir()
	path := claude.HookPath(root)
	write(t, path, `{"hooks": {"SessionStart": [
  {"matcher": "", "hooks": [{"type": "command", "command": "echo keep"}, {"type": "command", "command": "'/old dir/whatsapp-axi' --brief", "timeout": 5}]},
  {"matcher": "", "hooks": [{"type": "command", "command": "/usr/local/bin/whatsapp-axi --brief"}]}
]}}`)
	st := InspectHook(claude, root, cmd)
	if !st.Installed || st.Current || st.Command != "'/old dir/whatsapp-axi' --brief" {
		t.Fatalf("%+v", st)
	}
	c := plan(t, root, false)
	if c.Action != ActionUpdate {
		t.Fatalf("action %s", c.Action)
	}
	apply(t, c)
	got := read(t, path)
	if strings.Count(got, "whatsapp-axi") != 2 || !strings.Contains(got, "echo keep") || strings.Contains(got, "/usr/local/bin") {
		t.Fatalf("got\n%s", got)
	}
	if st := InspectHook(claude, root, cmd); !st.Current {
		t.Fatalf("%+v", st)
	}
	apply(t, plan(t, root, true))
	if got := read(t, path); strings.Contains(got, "whatsapp-axi") || !strings.Contains(got, "echo keep") {
		t.Fatalf("remove: %s", got)
	}
}

func TestRefusesMalformedAndUnexpectedFiles(t *testing.T) {
	cases := map[string]string{
		`{"a": 1,}`:                       "line 1, column 9",
		"{\n  // comment\n}":              "line 2",
		`[]`:                              "top level is not a JSON object",
		`{"hooks": []}`:                   `"hooks" is not an object`,
		`{"hooks": {"SessionStart": {}}}`: `"hooks.SessionStart" is not an array`,
	}
	for src, want := range cases {
		root := t.TempDir()
		path := claude.HookPath(root)
		write(t, path, src)
		for _, remove := range []bool{false, true} {
			_, err := PlanHook(claude, root, cmd, remove)
			var ce *contract.Error
			if !errors.As(err, &ce) || ce.Code != CodeConfigInvalid || !strings.Contains(ce.Message, want) || !strings.Contains(ce.Help, "never rewrites") {
				t.Errorf("%q remove=%v: %v", src, remove, err)
			}
		}
		if read(t, path) != src {
			t.Errorf("%q was modified", src)
		}
		if st := InspectHook(claude, root, cmd); st.Err == nil {
			t.Errorf("%q: inspect saw no problem", src)
		}
	}
}

func TestWritesArePrivateAndAtomic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
	root := t.TempDir()
	apply(t, plan(t, root, false))
	path := claude.HookPath(root)
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %o, want %o", p, info.Mode().Perm(), want)
		}
	}
	// An existing world-readable file becomes private when rewritten.
	write(t, path, `{"x": 1}`)
	_ = os.Chmod(path, 0o644)
	apply(t, plan(t, root, false))
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("rewritten mode %o", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestSymlinkedSettingsAreEditedInPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	dotfiles := filepath.Join(t.TempDir(), "settings.json")
	write(t, dotfiles, `{"model": "opus"}`)
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, claude.HookPath(root)); err != nil {
		t.Fatal(err)
	}
	apply(t, plan(t, root, false))
	if info, err := os.Lstat(claude.HookPath(root)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a regular file")
	}
	if !strings.Contains(read(t, dotfiles), "whatsapp-axi --brief") {
		t.Fatal("link target not updated")
	}
}

func TestManagedCommandDetection(t *testing.T) {
	for c, want := range map[string]bool{
		"whatsapp-axi --brief":                            true,
		"/usr/local/bin/whatsapp-axi --brief":             true,
		"'/Users/a b/go/bin/whatsapp-axi' --brief":        true,
		`"C:/Program Files/axi/whatsapp-axi.exe" --brief`: true,
		`C:\Users\a\go\bin\whatsapp-axi.EXE --brief`:      true,
		"whatsapp-axi":              false,
		"whatsapp-axi chats":        false,
		"echo whatsapp-axi --brief": false,
		"other-axi --brief":         false,
		"whatsapp-axi-dev --brief":  false,
	} {
		if IsManagedCommand(c) != want {
			t.Errorf("%q: want %v", c, want)
		}
	}
	for _, c := range []struct{ exe, goos, want string }{
		{"/usr/local/bin/whatsapp-axi", "linux", "/usr/local/bin/whatsapp-axi --brief"},
		{"/Users/a b/bin/whatsapp-axi", "darwin", "'/Users/a b/bin/whatsapp-axi' --brief"},
		{`C:\Users\a\go\bin\whatsapp-axi.exe`, "windows", "C:/Users/a/go/bin/whatsapp-axi.exe --brief"},
		{`C:\Program Files\whatsapp-axi.exe`, "windows", `"C:/Program Files/whatsapp-axi.exe" --brief`},
	} {
		got := HookCommand(c.exe, false, c.goos)
		if got != c.want || !IsManagedCommand(got) {
			t.Errorf("%s: %q", c.exe, got)
		}
	}
	if HookCommand("/x/whatsapp-axi", true, "linux") != cmd {
		t.Error("on PATH should use the bare name")
	}
}

func TestCodexFeatureFlag(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "[features]\nhooks = true\n"},
		{"model = \"o3\"\n", "model = \"o3\"\n\n[features]\nhooks = true\n"},
		{"model = \"o3\"", "model = \"o3\"\n\n[features]\nhooks = true\n"},
		{"[features]\nx = 1\n", "[features]\nx = 1\nhooks = true\n"},
		{"[features]\nx = 1\n\n[profiles.a]\ny = 2\n", "[features]\nx = 1\n\nhooks = true\n[profiles.a]\ny = 2\n"},
		{"[features]\nhooks = false # off\n", "[features]\nhooks = true # off\n"},
		{"[features]\r\nx = 1\r\n[mcp]\r\n", "[features]\r\nx = 1\r\nhooks = true\r\n[mcp]\r\n"},
		{"[other]\nhooks = false\n", "[other]\nhooks = false\n\n[features]\nhooks = true\n"},
	}
	for _, c := range cases {
		got, changed, err := enableCodexHooks(c.in)
		if err != nil || got != c.want || !changed {
			t.Errorf("%q: got %q", c.in, got)
		}
		if again, changed, err := enableCodexHooks(got); err != nil || changed || again != got {
			t.Errorf("%q: not idempotent", c.in)
		}
	}
	root := t.TempDir()
	write(t, CodexConfigPath(root), "[features]\nhooks = true\n")
	if c, err := PlanCodexFeature(root); err != nil || c.Action != ActionNone {
		t.Fatalf("%+v %v", c, err)
	}
	if on, err := InspectCodexFeature(root); !on || err != nil {
		t.Fatal("feature not seen")
	}
}

func TestCodexHooksFile(t *testing.T) {
	root := t.TempDir()
	c, err := PlanHook(codex, root, cmd, false)
	if err != nil || c.Action != ActionAdd || c.Path != filepath.Join(root, ".codex", "hooks.json") {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestSkillPlan(t *testing.T) {
	root := t.TempDir()
	content := []byte("---\nname: whatsapp-axi\ndescription: x\n---\nbody\n")
	c, err := PlanSkill(claude, root, content, false)
	if err != nil || c.Action != ActionAdd {
		t.Fatalf("%+v %v", c, err)
	}
	apply(t, c)
	if st := InspectSkill(claude, root, content); !st.Current {
		t.Fatalf("%+v", st)
	}
	if c, _ := PlanSkill(claude, root, content, false); c.Action != ActionNone {
		t.Fatal("not idempotent")
	}
	newer := append([]byte(nil), content...)
	newer = append(newer, "more\n"...)
	if st := InspectSkill(claude, root, newer); !st.Installed || st.Current {
		t.Fatalf("stale not detected: %+v", st)
	}
	if c, _ := PlanSkill(claude, root, newer, false); c.Action != ActionUpdate {
		t.Fatal("stale skill not updated")
	}
	sibling := filepath.Join(filepath.Dir(claude.SkillPath(root)), "notes.md")
	write(t, sibling, "mine")
	c, _ = PlanSkill(claude, root, content, true)
	if c.Action != ActionRemove {
		t.Fatalf("%+v", c)
	}
	apply(t, c)
	if _, err := os.Stat(claude.SkillPath(root)); !errors.Is(err, os.ErrNotExist) || read(t, sibling) != "mine" {
		t.Fatal("remove deleted the wrong things")
	}
	if c, _ := PlanSkill(claude, root, content, true); c.Action != ActionNone {
		t.Fatal("second remove")
	}

	// A different skill at the same path is never replaced or removed.
	write(t, claude.SkillPath(root), "---\nname: someone-else\n---\n")
	for _, remove := range []bool{false, true} {
		var ce *contract.Error
		if _, err := PlanSkill(claude, root, content, remove); !errors.As(err, &ce) || ce.Code != CodeConfigInvalid {
			t.Errorf("remove=%v: %v", remove, err)
		}
	}
}

func TestCodexFeatureRepresentations(t *testing.T) {
	for _, content := range []string{
		"features = { hooks = false }\n",
		"[\"features\"]\nhooks = false\n",
		"['features']\nhooks = false\n",
		"[features]\n\"hooks\" = false\n",
		"[features]\n'hooks' = false\n",
		"features.hooks = false\n",
		"features = []\n",
		"[features]\nhooks = 'false'\n",
		"[features]\nhooks = false\nhooks = true\n",
		"model = \"\"\"\n[features]\nhooks = false\n\"\"\"\n",
	} {
		t.Run(content, func(t *testing.T) {
			root := t.TempDir()
			path := CodexConfigPath(root)
			write(t, path, content)
			c, err := PlanCodexFeature(root)
			var ce *contract.Error
			if !errors.As(err, &ce) || ce.Code != CodeConfigInvalid || c.Action != ActionNone {
				t.Fatalf("unsafe edit accepted: %+v %v", c, err)
			}
			if got := read(t, path); got != content {
				t.Fatalf("planning changed config: %q", got)
			}
		})
	}
	for _, content := range []string{
		"features = { hooks = true }\n",
		"[\"features\"]\n'hooks' = true\n",
		"['features']\n\"hooks\" = true\n",
		"features.hooks = true\n",
	} {
		root := t.TempDir()
		write(t, CodexConfigPath(root), content)
		if c, err := PlanCodexFeature(root); err != nil || c.Action != ActionNone {
			t.Fatalf("enabled config: %+v %v", c, err)
		}
		if enabled, err := InspectCodexFeature(root); err != nil || !enabled {
			t.Fatalf("enabled feature not recognized: %v %v", enabled, err)
		}
	}
}

const foreignSessionEnd = `{"hooks": {"SessionEnd": [{"matcher": "", "hooks": [{"type": "command", "command": "cleanup-tool run"}]}]}}`

func TestSessionEndHookInstallRepairAndRemove(t *testing.T) {
	root := t.TempDir()
	path := claude.HookPath(root)
	write(t, path, foreignSessionEnd)
	apply(t, plan(t, root, false))
	got := read(t, path)
	if !strings.Contains(got, "cleanup-tool run") || !strings.Contains(got, `"command": "whatsapp-axi session-end"`) || !strings.Contains(got, `"command": "whatsapp-axi --brief"`) {
		t.Fatalf("install: %s", got)
	}
	if st := InspectHook(claude, root, cmd); !st.Current {
		t.Fatalf("%+v", st)
	}

	// Only the end hook points at an old binary: setup repairs just that one.
	moved := strings.Replace(got, `"whatsapp-axi session-end"`, `"/old/whatsapp-axi session-end"`, 1)
	write(t, path, moved)
	if st := InspectHook(claude, root, cmd); !st.Installed || st.Current {
		t.Fatalf("stale end hook reported current: %+v", st)
	}
	c := plan(t, root, false)
	if c.Action != ActionUpdate {
		t.Fatalf("action %s", c.Action)
	}
	apply(t, c)
	if fixed := read(t, path); strings.Contains(fixed, "/old/") || strings.Count(fixed, "whatsapp-axi") != 2 || !strings.Contains(fixed, "cleanup-tool run") {
		t.Fatalf("repair:\n%s", fixed)
	}
	if st := InspectHook(claude, root, cmd); !st.Current {
		t.Fatalf("%+v", st)
	}
	if c := plan(t, root, false); c.Action != ActionNone {
		t.Fatalf("second install: %s", c.Action)
	}

	apply(t, plan(t, root, true))
	if left := read(t, path); strings.Contains(left, "whatsapp-axi") || !strings.Contains(left, "cleanup-tool run") {
		t.Fatalf("remove: %s", left)
	}
	if c := plan(t, root, true); c.Action != ActionNone {
		t.Fatalf("second remove: %s", c.Action)
	}
}

// A settings file from before the session-end hook existed has only the
// start hook; setup adds the end hook and doctor sees the file as stale.
func TestStartOnlyInstallGainsSessionEndHook(t *testing.T) {
	root := t.TempDir()
	path := claude.HookPath(root)
	write(t, path, `{"hooks": {"SessionStart": [{"matcher": "", "hooks": [{"type": "command", "command": "whatsapp-axi --brief", "timeout": 10}]}]}}`)
	if st := InspectHook(claude, root, cmd); !st.Installed || st.Current {
		t.Fatalf("%+v", st)
	}
	c := plan(t, root, false)
	if c.Action != ActionUpdate {
		t.Fatalf("action %s", c.Action)
	}
	apply(t, c)
	if st := InspectHook(claude, root, cmd); !st.Current {
		t.Fatalf("%+v", st)
	}
	if got := read(t, path); strings.Count(got, "whatsapp-axi --brief") != 1 || strings.Count(got, "whatsapp-axi session-end") != 1 {
		t.Fatalf("got\n%s", got)
	}
}

func TestEndCommandAndDetection(t *testing.T) {
	for _, c := range []struct{ start, end string }{
		{"whatsapp-axi --brief", "whatsapp-axi session-end"},
		{"'/opt/my tools/whatsapp-axi' --brief", "'/opt/my tools/whatsapp-axi' session-end"},
		{"C:/Program Files/whatsapp-axi.exe --brief", "C:/Program Files/whatsapp-axi.exe session-end"},
	} {
		if got := EndCommand(c.start); got != c.end {
			t.Errorf("EndCommand(%q) = %q, want %q", c.start, got, c.end)
		}
	}
	// Each hook is recognized by its own argument only.
	if isManagedCommand(endKind, "whatsapp-axi --brief") || isManagedCommand(startKind, "whatsapp-axi session-end") {
		t.Fatal("start and end commands mixed up")
	}
	if !isManagedCommand(endKind, "/x/whatsapp-axi.exe session-end") || isManagedCommand(endKind, "other-axi session-end") {
		t.Fatal("end command detection")
	}
}
