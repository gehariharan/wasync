package agentconnect

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gehariharan/wasync/plugins"
)

// The WhatsApp plugin adds wasync's skills to Codex. wasync installs it from
// a local marketplace folder it writes from the copy built into the program,
// so no Git or download is involved and the plugin matches this wasync. Its
// own tool server is replaced by the [mcp_servers.whatsapp] entry, which has
// the same name and wins over the plugin's (checked with Codex CLI 0.172.0).

const (
	codexPlugin      = "whatsapp@wasync"
	codexMarketplace = "wasync"
	// MarketplaceDir is the folder in wasync's data folder that holds it.
	MarketplaceDir = "codex-marketplace"
)

// CodexPluginPlan says what installing the plugin would do.
type CodexPluginPlan struct {
	// Codex is the codex command; "" when it is not installed, and then
	// nothing is done (the ChatGPT desktop app needs only the entry).
	Codex string
	// Marketplace and Plugin say which of the two are still to be added.
	Marketplace, Plugin bool
	Dir                 string
}

// Steps are the commands it runs, in plain form for the preview.
func (p CodexPluginPlan) Steps() []string {
	var out []string
	if p.Codex == "" {
		return nil
	}
	if p.Marketplace {
		out = append(out, "codex plugin marketplace add "+p.Dir)
	}
	if p.Plugin {
		out = append(out, "codex plugin add "+codexPlugin)
	}
	return out
}

// PlanCodexPlugin looks at what Codex has already.
func (e Env) PlanCodexPlugin(dataDir string) (CodexPluginPlan, error) {
	p := CodexPluginPlan{Codex: e.Command("codex"), Dir: filepath.Join(dataDir, MarketplaceDir)}
	if p.Codex == "" {
		return p, nil
	}
	f, err := readFile(e.ConfigPath(Codex))
	if err != nil {
		return p, err
	}
	content := string(f.Data)
	p.Marketplace = !tomlTable(content, "marketplaces", codexMarketplace)
	p.Plugin = !tomlTable(content, "plugins", codexPlugin)
	return p, nil
}

func (e Env) codex(ctx context.Context, path string, args ...string) error {
	if e.Run == nil {
		return fmt.Errorf("cannot run codex here")
	}
	home := e.CodexHome()
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	out, err := e.Run(ctx, []string{"CODEX_HOME=" + home, "PATH=" + e.childPath(path)}, path, args...)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return fmt.Errorf("codex %s: %v: %s", strings.Join(args, " "), err, msg)
	}
	return nil
}

// childPath is the PATH a program found by Command runs with: wasync's own
// PATH, then the program's folder and ExtraPath. A login item's PATH lacks
// the package-manager folders, and codex from npm or Homebrew is a
// "#!/usr/bin/env node" script that needs node from that same folder.
func (e Env) childPath(program string) string {
	var dirs []string
	seen := map[string]bool{"": true}
	add := func(dir string) {
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	for _, dir := range filepath.SplitList(e.getenv("PATH")) {
		add(dir)
	}
	add(filepath.Dir(program))
	for _, dir := range e.ExtraPath {
		add(dir)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// InstallCodexPlugin carries out p. added says what it added, so removing
// takes out only that.
func (e Env) InstallCodexPlugin(ctx context.Context, p CodexPluginPlan) (marketplace, plugin bool, err error) {
	if p.Codex == "" {
		return false, false, nil
	}
	if p.Marketplace {
		if err := writeMarketplace(p.Dir); err != nil {
			return false, false, err
		}
		if err := e.codex(ctx, p.Codex, "plugin", "marketplace", "add", p.Dir); err != nil {
			return false, false, err
		}
		marketplace = true
	}
	if p.Plugin {
		if err := e.codex(ctx, p.Codex, "plugin", "add", codexPlugin); err != nil {
			return marketplace, false, err
		}
		plugin = true
	}
	return marketplace, plugin, nil
}

// RemoveCodexPlugin removes what InstallCodexPlugin added.
func (e Env) RemoveCodexPlugin(ctx context.Context, dataDir string, marketplace, plugin bool) error {
	if !marketplace && !plugin {
		return nil
	}
	path := e.Command("codex")
	if path == "" {
		return fmt.Errorf("the codex command is no longer found; remove the WhatsApp plugin in Codex by hand")
	}
	var errs []string
	if plugin {
		if err := e.codex(ctx, path, "plugin", "remove", codexPlugin); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if marketplace {
		if err := e.codex(ctx, path, "plugin", "marketplace", "remove", codexMarketplace); err != nil {
			errs = append(errs, err.Error())
		} else {
			_ = os.RemoveAll(filepath.Join(dataDir, MarketplaceDir))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// writeMarketplace writes the marketplace folder afresh from the built-in copy.
func writeMarketplace(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "plugins"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".agents", "plugins", "marketplace.json"), []byte(plugins.Marketplace), 0o600); err != nil {
		return err
	}
	return fs.WalkDir(plugins.WhatsApp, "whatsapp", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, "plugins", filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := plugins.WhatsApp.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}
