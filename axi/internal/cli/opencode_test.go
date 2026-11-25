package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/agentsetup"
	"github.com/gehariharan/wasync/axi/skills"
)

func TestSetupOpenCodeConfigResolutionAndDoctor(t *testing.T) {
	for _, mode := range []string{"xdg", "default", "relative-xdg", "synthetic", "project"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			fixture := t.TempDir()
			h.homeDir = filepath.Join(fixture, "home")
			synthetic := agentHome(h)
			delete(h.env, "WASYNC_AXI_AGENT_HOME")
			xdg := filepath.Join(fixture, "config")
			h.env["XDG_CONFIG_HOME"] = xdg
			project := filepath.Join(fixture, "project")
			configDir := filepath.Join(xdg, "opencode")
			var flags []string
			switch mode {
			case "default":
				delete(h.env, "XDG_CONFIG_HOME")
				configDir = filepath.Join(h.homeDir, ".config", "opencode")
			case "relative-xdg":
				h.env["XDG_CONFIG_HOME"] = "relative-config"
				configDir = filepath.Join(h.homeDir, ".config", "opencode")
			case "synthetic":
				h.env["WASYNC_AXI_AGENT_HOME"] = synthetic
				configDir = filepath.Join(synthetic, ".config", "opencode")
			case "project":
				h.env["WASYNC_AXI_AGENT_HOME"] = synthetic
				flags = []string{"--dir", project}
				configDir = filepath.Join(project, ".opencode")
			}
			neighbors := []string{
				filepath.Join(h.homeDir, ".config", "opencode", "plugins", "other.js"),
				filepath.Join(synthetic, ".config", "opencode", "plugins", "other.js"),
				filepath.Join(xdg, "opencode", "plugins", "other.js"),
				filepath.Join(project, ".opencode", "skills", "other", "SKILL.md"),
				filepath.Join(configDir, "skills", "wasync-axi", "notes.md"),
			}
			for _, path := range neighbors {
				writeFile(t, path, "unrelated file\n")
			}
			snapshot := func() map[string]string {
				files := map[string]string{}
				for _, root := range []string{fixture, synthetic} {
					if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if !entry.IsDir() {
							files[path] = readFile(t, path)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				return files
			}
			before := snapshot()
			for _, what := range []string{"hooks", "skill"} {
				path := filepath.Join(configDir, "plugins", "wasync-axi.js")
				content := agentsetup.OpenCodePlugin(harnessBin)
				if what == "skill" {
					path = filepath.Join(configDir, "skills", "wasync-axi", "SKILL.md")
					content = skills.WasyncAXI
				}
				args := append([]string{what}, flags...)
				for _, selection := range [][]string{nil, {"--agent", "opencode"}, {"--agent", "all"}} {
					dry := append(append([]string{}, args...), selection...)
					r, code := setupJSONRun(t, h, append(dry, "--dry-run")...)
					if code != ExitOK || r.Written != 0 {
						t.Fatalf("dry run: %d %+v", code, r)
					}
					found := false
					for _, change := range r.Changes {
						if change.Agent == "opencode" {
							got := change.Path
							if strings.HasPrefix(got, "~"+string(filepath.Separator)) {
								got = filepath.Join(h.homeDir, got[2:])
							}
							if got != path || change.Change != agentsetup.ActionAdd {
								t.Fatalf("wrong OpenCode plan: %+v, want %s", change, path)
							}
							found = true
						}
					}
					if !found || !reflect.DeepEqual(before, snapshot()) {
						t.Fatal("dry run missed OpenCode or changed files")
					}
				}
				for attempt := 0; attempt < 2; attempt++ {
					r, code := setupJSONRun(t, h, args...)
					if code != ExitOK || r.Written != 1-attempt || len(r.Changes) != 1 || r.Changes[0].Agent != "opencode" {
						t.Fatalf("install/repeat: %d %+v", code, r)
					}
					if readFile(t, path) != string(content) {
						t.Fatal("installed contract differs")
					}
				}
				if mode != "project" {
					rep, _, _ := runDoctorJSON(t, h)
					check := rep.check(t, what, sourceAXI)
					if check.Status != statusPass || !strings.Contains(check.Detail, "OpenCode") {
						t.Fatalf("doctor missed installed %s: %+v", what, check)
					}
				}
				r, code := setupJSONRun(t, h, append(args, "--remove")...)
				if code != ExitOK || r.Written != 1 || !reflect.DeepEqual(before, snapshot()) {
					t.Fatalf("remove changed unrelated files: %d %+v", code, r)
				}
				rep, _, _ := runDoctorJSON(t, h)
				if check := rep.check(t, what, sourceAXI); check.Status == statusPass {
					t.Fatalf("doctor reports removed %s installed: %+v", what, check)
				}
				writeFile(t, path, "unowned content\n")
				for _, action := range [][]string{nil, {"--remove"}} {
					out, code := h.run(t, append(append([]string{"setup"}, args...), action...)...)
					if code != ExitError || readFile(t, path) != "unowned content\n" {
						t.Fatalf("ownership refusal: %d %s", code, out)
					}
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSetupOpenCodeUsesNativeGlobalAndProjectPaths(t *testing.T) {
	for _, project := range []bool{false, true} {
		h := newHarness(t)
		root := agentHome(h)
		relative := filepath.Join(".config", "opencode")
		args := []string{"hooks", "--agent", "opencode"}
		if project {
			root = t.TempDir()
			relative = ".opencode"
			args = append(args, "--dir", root)
		}
		plugin := filepath.Join(root, relative, "plugins", "wasync-axi.js")
		r, code := setupJSONRun(t, h, append(args, "--dry-run")...)
		if code != ExitOK || r.Written != 0 || len(r.Changes) != 1 {
			t.Fatalf("dry run: %d %+v", code, r)
		}
		if _, err := os.Stat(plugin); !os.IsNotExist(err) {
			t.Fatal("dry run installed plugin", err)
		}
		r, code = setupJSONRun(t, h, args...)
		if code != ExitOK || r.Written != 1 {
			t.Fatalf("install: %d %+v", code, r)
		}
		installed, err := os.ReadFile(plugin)
		if err != nil || string(installed) != string(agentsetup.OpenCodePlugin(harnessBin)) {
			t.Fatalf("plugin contract: %v", err)
		}
		r, code = setupJSONRun(t, h, args...)
		if code != ExitOK || r.Written != 0 {
			t.Fatalf("repeat: %d %+v", code, r)
		}
		r, code = setupJSONRun(t, h, append(args, "--remove")...)
		if code != ExitOK || r.Written != 1 {
			t.Fatalf("remove: %d %+v", code, r)
		}
		if _, err := os.Stat(plugin); !os.IsNotExist(err) {
			t.Fatal("remove left plugin", err)
		}
	}
}

func TestSetupPlansOpenCodeBeforeWritingAnyAgent(t *testing.T) {
	h := newHarness(t)
	root := agentHome(h)
	writeFile(t, filepath.Join(root, ".claude", "settings.json"), "not json")
	out, code := h.run(t, "setup", "hooks", "--agent", "opencode,claude")
	if code != ExitError {
		t.Fatalf("%d %s", code, out)
	}
	plugin := filepath.Join(root, ".config", "opencode", "plugins", "wasync-axi.js")
	if _, err := os.Stat(plugin); !os.IsNotExist(err) {
		t.Fatal("partial plugin install", err)
	}
	out, code = h.run(t, "setup", "hooks", "--agent", "all,absent")
	if code != ExitUsage {
		t.Fatalf("ignored invalid agent: %d %s", code, out)
	}
}
