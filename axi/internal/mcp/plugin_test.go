package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The plugin package is machine-consumed by Codex, so these tests parse the
// manifests into typed models and check what Codex will act on: which program
// it launches, which skills it loads, and that the skills only name tools this
// server really serves. (Installing the package into a real Codex home was also
// checked by hand; that needs the codex program and is not part of the suite.)

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

var toolName = regexp.MustCompile("`(whatsapp_[a-z_]+)`")

func TestSkillsOnlyNameToolsTheServerServes(t *testing.T) {
	dir := filepath.Join(repoRoot, "plugins/whatsapp/skills")
	skills, err := filepath.Glob(filepath.Join(dir, "*", "SKILL.md"))
	if err != nil || len(skills) != 2 {
		t.Fatalf("skills %v %v", skills, err)
	}
	named := map[string]bool{}
	for _, path := range skills {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if !strings.HasPrefix(text, "---\nname: "+filepath.Base(filepath.Dir(path))+"\ndescription: ") {
			t.Errorf("%s: front matter must start with the skill's own name and a description", path)
		}
		for _, m := range toolName.FindAllStringSubmatch(text, -1) {
			named[m[1]] = true
			if _, ok := findTool(m[1]); !ok {
				t.Errorf("%s names a tool the server does not serve: %s", path, m[1])
			}
		}
	}
	for _, must := range []string{"whatsapp_status", "whatsapp_start_setup", "whatsapp_request_access", "whatsapp_access_status", "whatsapp_doctor"} {
		if !named[must] {
			t.Errorf("no skill tells the agent about %s", must)
		}
	}
}
