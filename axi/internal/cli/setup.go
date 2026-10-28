package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/agentsetup"
	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/output"
	"github.com/gehariharan/wasync/axi/skills"
)

// `setup` installs whatsapp-axi's opt-in agent integrations (AXI principle
// 7): a session-start hook that prints `whatsapp-axi --brief`, a session-end
// hook that runs `whatsapp-axi session-end`, and the agent skill shipped in
// skills/. Only a user-invoked setup command writes agent
// settings; no other command does. Setup never talks to wasync.

const (
	setupHooks = "hooks"
	setupSkill = "skill"

	setupUsage    = "whatsapp-axi setup hooks|skill [--agent claude|codex|opencode|all] [--dir DIR] [--remove] [--dry-run] [--json]"
	setupHooksFix = "whatsapp-axi setup hooks"
	setupSkillFix = "whatsapp-axi setup skill"
)

var setupCommand = &command{
	name:        "setup",
	usage:       setupUsage,
	description: "Install (or --remove) the session hooks, or the whatsapp-axi agent skill: a session-start hook that shows `whatsapp-axi --brief` in new agent sessions and a session-end hook that records which whatsapp-axi commands a session ran (names and counts only); idempotent, keeps unrelated settings",
	flags: []flagSpec{
		{name: "--agent", value: "LIST", description: "claude, codex, opencode or all, comma-separated (default: detected config directories, else claude; with --remove: all)"},
		{name: "--dir", value: "DIR", description: "project settings in DIR/.claude, DIR/.codex and DIR/.opencode; default $" + config.EnvAgentHome + " or the home directory (OpenCode: $XDG_CONFIG_HOME/opencode or ~/.config/opencode)"},
		{name: "--remove", description: "remove whatsapp-axi's hook or skill and nothing else"},
		{name: "--dry-run", description: "print the planned changes without writing anything"},
		jsonFlag,
	},
	minArgs:  1,
	maxArgs:  1,
	argsName: "hooks or skill",
	examples: []string{
		"whatsapp-axi setup hooks",
		"whatsapp-axi setup hooks --dry-run",
		"whatsapp-axi setup hooks --agent codex",
		"whatsapp-axi setup skill",
		"whatsapp-axi setup hooks --remove",
		"whatsapp-axi setup hooks --dir .",
	},
	run: (*App).runSetup,
}

// agentHome is where agent settings live: $WHATSAPP_AXI_AGENT_HOME, else
// the home directory.
func (a *App) agentHome() (string, error) {
	if v := strings.TrimSpace(a.Getenv(config.EnvAgentHome)); v != "" {
		return v, nil
	}
	home, err := a.HomeDir()
	if err != nil || home == "" {
		return "", contract.Errorf(contract.CodeUsage, "pass --dir DIR or set "+config.EnvAgentHome, "cannot determine the home directory")
	}
	return home, nil
}

// hookCommand is the managed hook's command: the bare name when the
// whatsapp-axi on PATH is this binary, otherwise this binary's path.
func (a *App) hookCommand() string {
	executable := a.hookExecutable()
	return agentsetup.HookCommand(executable, executable == agentsetup.BinaryName, runtime.GOOS)
}

func (a *App) hookExecutable() string {
	exe, err := a.Executable()
	if err != nil || exe == "" {
		return agentsetup.BinaryName
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	onPath := false
	if a.LookPath != nil {
		if p, err := a.LookPath(agentsetup.BinaryName); err == nil {
			if r, err := filepath.EvalSymlinks(p); err == nil {
				p = r
			}
			onPath = sameFile(p, exe)
		}
	}
	if onPath {
		return agentsetup.BinaryName
	}
	return exe
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ia, ib)
}

// setupAgents resolves --agent. Without it, install targets the agents
// whose config directory exists (Claude Code when none does) and removal
// targets every agent.
func (a *App) setupAgents(inv invocation, root string, remove bool) ([]agentsetup.Agent, error) {
	available := a.agentConfigs(inv.has("--dir"))
	v, ok := inv.flag("--agent")
	if !ok {
		var out []agentsetup.Agent
		for _, ag := range available {
			if remove || ag.Present(root) {
				out = append(out, ag)
			}
		}
		if len(out) == 0 {
			out = append(out, available[0])
		}
		return out, nil
	}
	seen := map[string]bool{}
	var out []agentsetup.Agent
	all := false
	for _, name := range strings.Split(v, ",") {
		name = strings.TrimSpace(name)
		if name == "all" {
			all = true
			continue
		}
		ag, ok := agentsetup.FindAgent(name)
		if ok {
			for _, candidate := range available {
				if candidate.Name == name {
					ag = candidate
					break
				}
			}
		}
		if !ok {
			return nil, usageErr("use --agent "+strings.Join(agentsetup.AgentNames(), ", ")+" or all (comma-separated)", "unknown agent %q", name)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, ag)
		}
	}
	if all {
		return available, nil
	}
	return out, nil
}

func (a *App) agentConfigs(project bool) []agentsetup.Agent {
	configHome := a.Getenv("XDG_CONFIG_HOME")
	if strings.TrimSpace(a.Getenv(config.EnvAgentHome)) != "" {
		configHome = ""
	}
	return agentsetup.AgentsAt(project, configHome)
}

type setupChangeJSON struct {
	Agent  string `json:"agent"`
	Item   string `json:"item"`
	Path   string `json:"path"`
	Change string `json:"change"`
}

type setupJSON struct {
	Setup       string            `json:"setup"`
	Root        string            `json:"root"`
	DryRun      bool              `json:"dry_run"`
	Remove      bool              `json:"remove"`
	HookCommand string            `json:"hook_command,omitempty"`
	Changes     []setupChangeJSON `json:"changes"`
	Written     int               `json:"written"`
	Help        []string          `json:"help"`
}

func (a *App) runSetup(_ context.Context, inv invocation) error {
	what := inv.args[0]
	if what != setupHooks && what != setupSkill {
		return usageErr("usage: "+setupUsage, "unknown setup target %q (want hooks or skill)", what)
	}
	remove, dryRun := inv.has("--remove"), inv.has("--dry-run")
	var root string
	if dir, ok := inv.flag("--dir"); ok {
		if strings.TrimSpace(dir) == "" {
			return usageErr("pass a directory, such as --dir .", "--dir needs a directory")
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return usageErr("pass a directory, such as --dir .", "invalid --dir %q", dir)
		}
		root = abs
	} else {
		var err error
		if root, err = a.agentHome(); err != nil {
			return err
		}
	}
	agents, err := a.setupAgents(inv, root, remove)
	if err != nil {
		return err
	}

	// Plan everything first, so a file that cannot be edited stops the run
	// before anything is written.
	res := setupJSON{Setup: what, Root: a.collapseHome(root), DryRun: dryRun, Remove: remove, Changes: []setupChangeJSON{}}
	var changes []agentsetup.Change
	if what == setupHooks && !remove {
		res.HookCommand = a.hookCommand()
	}
	for _, ag := range agents {
		var c agentsetup.Change
		var err error
		if what == setupHooks {
			if ag.Name == "opencode" {
				c, err = agentsetup.PlanOpenCodeHook(ag, root, a.hookExecutable(), remove)
			} else {
				c, err = agentsetup.PlanHook(ag, root, res.HookCommand, remove)
			}
		} else {
			c, err = agentsetup.PlanSkill(ag, root, skills.WhatsappAXI, remove)
		}
		if err != nil {
			return err
		}
		changes = append(changes, c)
		if what == setupHooks && !remove && ag.Name == "codex" {
			c, err := agentsetup.PlanCodexFeature(root)
			if err != nil {
				return err
			}
			changes = append(changes, c)
		}
	}
	pending := 0
	for _, c := range changes {
		if c.Action != agentsetup.ActionNone {
			pending++
		}
	}
	if !dryRun {
		for _, c := range changes {
			if c.Action == agentsetup.ActionNone {
				continue
			}
			if err := c.Apply(); err != nil {
				var ce *contract.Error
				if errors.As(err, &ce) && res.Written > 0 {
					e := *ce
					e.Help += "; " + strconv.Itoa(res.Written) + " earlier change(s) were written, and rerunning is safe"
					return &e
				}
				return err
			}
			res.Written++
		}
	}
	for _, c := range changes {
		res.Changes = append(res.Changes, setupChangeJSON{Agent: c.Agent, Item: c.Item, Path: a.collapseHome(c.Path), Change: c.Action})
	}
	res.Help = setupHints(what, remove, dryRun, pending, changes)
	if inv.json {
		return output.JSON(a.Out, res)
	}
	o := output.New(a.Out)
	o.Field(0, "setup", res.Setup)
	o.Field(0, "root", res.Root)
	o.Value(0, "dry_run", res.DryRun)
	if res.HookCommand != "" {
		o.Field(0, "hook_command", res.HookCommand)
	}
	rows := make([][]string, 0, len(res.Changes))
	for _, c := range res.Changes {
		rows = append(rows, []string{c.Agent, c.Item, c.Path, c.Change})
	}
	o.Table(0, "changes", []string{"agent", "item", "path", "change"}, rows)
	o.Value(0, "written", res.Written)
	o.Help(res.Help...)
	return o.Err()
}

func setupHints(what string, remove, dryRun bool, pending int, changes []agentsetup.Change) []string {
	var h []string
	switch {
	case pending == 0 && remove:
		h = append(h, "Nothing to remove: no whatsapp-axi "+what+" found")
	case pending == 0:
		h = append(h, "Already set up; nothing changed")
	case dryRun:
		h = append(h, "Dry run: nothing was written; rerun without --dry-run to apply "+strconv.Itoa(pending)+" change(s)")
	}
	for _, c := range changes {
		if c.Item == agentsetup.ItemHooksFeature && c.Action != agentsetup.ActionNone {
			h = append(h, "Codex runs hooks only with `[features] hooks = true` in config.toml, so setup enables it; it stays on after --remove")
		}
	}
	switch {
	case remove:
	case what == setupHooks:
		h = append(h,
			"New agent sessions start with `whatsapp-axi --brief`: wasync state and the chat count, never message text",
			"Ending a session runs `whatsapp-axi session-end`, which records command names and counts only, so the next brief can say what the last session did",
			"Undo with `whatsapp-axi setup hooks --remove`; `whatsapp-axi doctor` checks the hook")
	default:
		h = append(h,
			"Agents load the skill when a task is about WhatsApp; `whatsapp-axi setup hooks` adds a summary to every session instead",
			"Undo with `whatsapp-axi setup skill --remove`")
	}
	return h
}

// hookChecks are doctor's agent-integration checks: the session-start and
// session-end hooks in each agent's user settings, and the agent skill.
func (a *App) hookChecks() []doctorCheck {
	hooks := doctorCheck{Area: areaHooks, Name: "hooks", Source: sourceAXI}
	skill := doctorCheck{Area: areaHooks, Name: "skill", Source: sourceAXI}
	root, err := a.agentHome()
	if err != nil {
		hooks.Status, hooks.Detail = statusSkip, "no home directory to look for agent settings in"
		skill.Status, skill.Detail = statusSkip, hooks.Detail
		return []doctorCheck{hooks, skill}
	}
	cmd := a.hookCommand()
	agents := a.agentConfigs(false)
	var current, stale, broken, featureOff []string
	for _, ag := range agents {
		var st agentsetup.HookStatus
		if ag.Name == "opencode" {
			st = agentsetup.InspectOpenCodeHook(ag, root, a.hookExecutable())
		} else {
			st = agentsetup.InspectHook(ag, root, cmd)
		}
		where := ag.Label + " (" + a.collapseHome(st.Path) + ")"
		switch {
		case st.Err != nil:
			broken = append(broken, errMessage(st.Err))
		case st.Current:
			current = append(current, where)
			if ag.Name == "codex" {
				on, err := agentsetup.InspectCodexFeature(root)
				if err != nil {
					broken = append(broken, a.collapseHome(agentsetup.CodexConfigPath(root))+": "+errMessage(err))
				} else if !on {
					featureOff = append(featureOff, a.collapseHome(agentsetup.CodexConfigPath(root)))
				}
			}
		case st.Installed:
			stale = append(stale, where+" runs `"+output.Sanitize(st.Command)+"`")
		}
	}
	var skillCurrent, skillStale, skillBroken []string
	for _, ag := range agents {
		st := agentsetup.InspectSkill(ag, root, skills.WhatsappAXI)
		where := ag.Label + " (" + a.collapseHome(st.Path) + ")"
		switch {
		case st.Err != nil:
			skillBroken = append(skillBroken, errMessage(st.Err))
		case st.Current:
			skillCurrent = append(skillCurrent, where)
		case st.Installed:
			skillStale = append(skillStale, where)
		}
	}

	switch {
	case len(broken) > 0:
		hooks.Status, hooks.Fix = statusWarn, setupHooksFix
		hooks.Detail = strings.Join(broken, "; ") + "; fix the file by hand, then run the fix"
	case len(stale) > 0:
		hooks.Status, hooks.Fix = statusWarn, setupHooksFix
		hooks.Detail = "session hooks are missing or do not run this binary (`" + cmd + "`): " + strings.Join(stale, "; ")
	case len(featureOff) > 0:
		hooks.Status, hooks.Fix = statusWarn, setupHooksFix+" --agent codex"
		hooks.Detail = "Codex hook installed, but hooks are not enabled in " + strings.Join(featureOff, ", ")
	case len(current) > 0:
		hooks.Status = statusPass
		hooks.Detail = "session-start and session-end hooks run `" + cmd + "` for " + strings.Join(current, ", ")
	case len(skillCurrent)+len(skillStale) > 0:
		hooks.Status = statusSkip
		hooks.Detail = "no session hooks installed; the whatsapp-axi skill is installed instead"
	default:
		hooks.Status, hooks.Fix = statusWarn, setupHooksFix
		hooks.Detail = "not installed: agent sessions start without whatsapp-axi context (opt-in)"
	}
	switch {
	case len(skillBroken) > 0:
		skill.Status, skill.Fix = statusWarn, setupSkillFix
		skill.Detail = strings.Join(skillBroken, "; ") + "; move that file away, then run the fix"
	case len(skillStale) > 0:
		skill.Status, skill.Fix = statusWarn, setupSkillFix
		skill.Detail = "installed skill differs from this whatsapp-axi version: " + strings.Join(skillStale, ", ")
	case len(skillCurrent) > 0:
		skill.Status, skill.Detail = statusPass, "whatsapp-axi skill installed for "+strings.Join(skillCurrent, ", ")
	default:
		skill.Status, skill.Detail = statusSkip, "skill not installed (optional; `"+setupSkillFix+"` installs it)"
	}
	return []doctorCheck{hooks, skill}
}
