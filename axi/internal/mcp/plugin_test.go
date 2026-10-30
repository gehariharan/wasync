package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The plugin package is machine-consumed by Codex, so these tests parse the
// manifests into typed models and check what Codex will act on: which program
// it launches and which skills it loads. (Installing the package into a real
// Codex home was also checked by hand; that needs the codex program and is not
// part of the suite.)

const repoRoot = "../../.."

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestPluginLaunchesTheToolServerAndNothingElse(t *testing.T) {
	var m struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			URL     string   `json:"url"`
		} `json:"mcpServers"`
	}
	readJSON(t, "plugins/whatsapp/mcp.json", &m)
	if len(m.Servers) != 1 {
		t.Fatalf("expected one server, got %v", m.Servers)
	}
	s := m.Servers["whatsapp"]
	if s.Type != "stdio" || s.Command != "whatsapp-axi" || len(s.Args) != 1 || s.Args[0] != "mcp" || s.URL != "" {
		t.Fatalf("the plugin must launch `whatsapp-axi mcp` over stdio and open no network endpoint: %+v", s)
	}
}

func TestMarketplacePointsAtThePlugin(t *testing.T) {
	var mk struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
			Policy struct {
				Installation   string `json:"installation"`
				Authentication string `json:"authentication"`
			} `json:"policy"`
			Category string `json:"category"`
		} `json:"plugins"`
	}
	readJSON(t, ".agents/plugins/marketplace.json", &mk)
	if len(mk.Plugins) != 1 {
		t.Fatalf("plugins %+v", mk.Plugins)
	}
	p := mk.Plugins[0]
	if p.Source.Source != "local" || !strings.HasPrefix(p.Source.Path, "./") || p.Policy.Installation != "AVAILABLE" || p.Policy.Authentication == "" || p.Category == "" {
		t.Fatalf("entry %+v", p)
	}
	var plugin struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		Extensions struct {
			OpenAI struct {
				Onboarding string `json:"onboardingSkill"`
				Interface  struct {
					DisplayName string `json:"displayName"`
				} `json:"interface"`
			} `json:"com.openai"`
		} `json:"extensions"`
	}
	readJSON(t, filepath.Join(p.Source.Path, "plugin.json"), &plugin)
	if plugin.Name != p.Name || plugin.Version == "" || plugin.Extensions.OpenAI.Interface.DisplayName == "" {
		t.Fatalf("plugin %+v does not match its marketplace entry %q", plugin, p.Name)
	}
	skill := filepath.Join(repoRoot, p.Source.Path, plugin.Extensions.OpenAI.Onboarding)
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("onboarding skill missing: %v", err)
	}
}

// frontMatter parses the front matter Codex reads from a skill: the key: value
// lines between the opening and closing "---".
func frontMatter(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "---\n")
	if !ok {
		t.Fatalf("%s has no front matter", path)
	}
	block, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		t.Fatalf("%s: front matter is not closed", path)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(block, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("%s: front matter line %q is not key: value", path, line)
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return fields
}

func TestSkillsAreNamedForTheirFolderAndDescribed(t *testing.T) {
	skills, err := filepath.Glob(filepath.Join(repoRoot, "plugins/whatsapp/skills", "*", "SKILL.md"))
	if err != nil || len(skills) != 2 {
		t.Fatalf("skills %v %v", skills, err)
	}
	for _, path := range skills {
		fm := frontMatter(t, path)
		if want := filepath.Base(filepath.Dir(path)); fm["name"] != want {
			t.Errorf("%s: name %q, want the folder name %q", path, fm["name"], want)
		}
		if fm["description"] == "" {
			t.Errorf("%s: description is empty", path)
		}
	}
}
