// Package agentconnect writes the wasync tool server into an AI app's own
// settings file, so "Connect an agent" needs no terminal: Codex and the
// ChatGPT desktop app (which share Codex's config.toml), Claude Code and
// Claude Desktop.
//
// It changes only the one server entry it owns ("whatsapp"), keeps every
// other byte of the file, refuses a file it cannot parse or edit exactly,
// keeps a backup of the file before each change and writes atomically. The
// caller decides whether a change may happen: wasync shows the preview and
// asks the person with the operating system's prompt before it connects an
// agent. Removing the entry again needs no prompt, because it only takes
// power away.
//
// Where each app keeps its settings, from the apps' own documentation
// (checked 2026-11-26):
//   - Codex CLI, IDE extension and the ChatGPT desktop app share
//     $CODEX_HOME/config.toml (default ~/.codex/config.toml), with a
//     [mcp_servers.<name>] table holding command, args and env
//     (https://learn.chatgpt.com/docs/extend/mcp,
//     https://learn.chatgpt.com/docs/config-file/config-reference,
//     https://learn.chatgpt.com/docs/config-file/environment-variables).
//   - Claude Code keeps user-scoped servers in ~/.claude.json under
//     "mcpServers" (https://code.claude.com/docs/en/mcp,
//     https://code.claude.com/docs/en/settings); CLAUDE_CONFIG_DIR moves its
//     home-directory files.
//   - Claude Desktop reads claude_desktop_config.json under "mcpServers" in
//     ~/Library/Application Support/Claude (macOS), %APPDATA%\Claude
//     (Windows) or ~/.config/Claude (Linux)
//     (https://modelcontextprotocol.io/docs/develop/connect-local-servers).
package agentconnect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Kind names an app wasync can connect.
type Kind string

const (
	// Codex is the Codex CLI, its IDE extension and the ChatGPT desktop app.
	Codex         Kind = "codex"
	ClaudeCode    Kind = "claude-code"
	ClaudeDesktop Kind = "claude-desktop"
)

// ServerName is the name of the tool server entry wasync writes. It matches
// the WhatsApp plugin's own server, so its tools keep their whatsapp_ names
// and the entry takes the plugin server's place in Codex.
const ServerName = "whatsapp"

// Agent describes one app.
type Agent struct {
	Kind Kind
	// Title is the app's name on screen.
	Title string
	// Next is what the person does after connecting, since wasync never
	// quits another app.
	Next string
}

// Agents lists the apps in the order they are offered.
var Agents = []Agent{
	{Kind: Codex, Title: "ChatGPT & Codex", Next: "Quit and reopen the ChatGPT or Codex app (or start a new codex session) so it loads the WhatsApp tools."},
	{Kind: ClaudeCode, Title: "Claude Code", Next: "Start a new Claude Code session so it loads the WhatsApp tools."},
	{Kind: ClaudeDesktop, Title: "Claude Desktop", Next: "Quit Claude Desktop completely and open it again so it loads the WhatsApp tools."},
}

// Lookup returns the agent of this kind.
func Lookup(kind string) (Agent, bool) {
	for _, a := range Agents {
		if string(a.Kind) == kind {
			return a, true
		}
	}
	return Agent{}, false
}

// Env is the part of the computer this package looks at. SystemEnv is the
// real one; tests point it at scratch folders.
type Env struct {
	GOOS string
	// Home is the user's home folder; ConfigDir is os.UserConfigDir.
	Home, ConfigDir string
	Getenv          func(string) string
	LookPath        func(string) (string, error)
	// ExtraPath are folders searched after LookPath. wasync may run as a
	// login item whose PATH lacks the folders package managers use.
	ExtraPath []string
	// Run runs a program with extra environment variables and returns what it printed.
	Run func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Self is the running wasync program's path ("" when unknown).
	Self string
}

// SystemEnv describes this computer.
func SystemEnv() Env {
	home, _ := os.UserHomeDir()
	cfg, _ := os.UserConfigDir()
	e := Env{GOOS: runtime.GOOS, Home: home, ConfigDir: cfg, Getenv: os.Getenv, LookPath: exec.LookPath, Run: runCommand}
	if self, err := os.Executable(); err == nil {
		e.Self = self
	}
	if runtime.GOOS != "windows" && home != "" {
		e.ExtraPath = []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".npm-global", "bin")}
	}
	return e
}

func runCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = nil
	return cmd.CombinedOutput()
}

func (e Env) getenv(name string) string {
	if e.Getenv == nil {
		return ""
	}
	return strings.TrimSpace(e.Getenv(name))
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// CodexHome is $CODEX_HOME, else ~/.codex.
func (e Env) CodexHome() string {
	if dir := e.getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(e.Home, ".codex")
}

// ConfigPath is the settings file wasync edits for kind.
func (e Env) ConfigPath(kind Kind) string {
	switch kind {
	case Codex:
		return filepath.Join(e.CodexHome(), "config.toml")
	case ClaudeCode:
		if dir := e.getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			return filepath.Join(dir, ".claude.json")
		}
		return filepath.Join(e.Home, ".claude.json")
	case ClaudeDesktop:
		return filepath.Join(e.ConfigDir, "Claude", "claude_desktop_config.json")
	}
	return ""
}

// Found reports whether the app seems to be on this computer: its settings
// folder or file, its command, or (on macOS) its application.
func (e Env) Found(kind Kind) bool {
	switch kind {
	case Codex:
		return exists(e.CodexHome()) || e.Command("codex") != "" || e.app("ChatGPT") || e.app("Codex")
	case ClaudeCode:
		return exists(e.ConfigPath(ClaudeCode)) || e.Command("claude") != ""
	case ClaudeDesktop:
		return exists(filepath.Dir(e.ConfigPath(ClaudeDesktop))) || e.app("Claude")
	}
	return false
}

func (e Env) app(name string) bool {
	if e.GOOS != "darwin" {
		return false
	}
	return exists(filepath.Join("/Applications", name+".app")) || exists(filepath.Join(e.Home, "Applications", name+".app"))
}

// Command finds a program the person installed, or returns "".
func (e Env) Command(name string) string {
	if e.LookPath != nil {
		if p, err := e.LookPath(name); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
		}
	}
	for _, dir := range e.ExtraPath {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// AxiCommand finds wasync-axi, the tool server an entry starts: next to
// wasync first, so the two match, then where the person installed it.
// Desktop apps do not see the shell's PATH, so the entry names it in full.
func (e Env) AxiCommand() string {
	exe := ""
	if e.GOOS == "windows" {
		exe = ".exe"
	}
	if e.Self != "" {
		for _, name := range []string{"wasync-axi", "whatsapp-axi"} {
			p := filepath.Join(filepath.Dir(e.Self), name+exe)
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				return p
			}
		}
	}
	if p := e.Command("wasync-axi"); p != "" {
		return p
	}
	return e.Command("whatsapp-axi")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
