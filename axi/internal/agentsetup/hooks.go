package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

// The managed session-start hook runs `whatsapp-axi --brief`, the compact
// home view, and the managed session-end hook runs `whatsapp-axi
// session-end`, which records command names and counts; both have a timeout.
// A hook entry is whatsapp-axi's when its command runs a binary named
// whatsapp-axi with HookFlag (start) or EndWord (end), wherever that binary
// lives, so setup can repair the path after a reinstall.
const (
	BinaryName  = "whatsapp-axi"
	HookFlag    = "--brief"
	EndWord     = "session-end"
	HookTimeout = 10 // seconds
	SkillName   = "whatsapp-axi"
)

// hookKind is one managed hook event: its key in the agent's settings and
// the argument that marks a command as whatsapp-axi's.
type hookKind struct{ event, arg string }

var (
	startKind = hookKind{event: "SessionStart", arg: HookFlag}
	endKind   = hookKind{event: "SessionEnd", arg: EndWord}
)

// Agent is an agent tool whose session hooks and skills setup manages.
// Config paths are absolute or relative to a root: the user's home directory,
// or a project directory for project-scoped settings.
type Agent struct {
	Name     string // flag value
	Label    string // display name
	Dir      string // absolute config directory or path under the root
	HookFile string // hook settings file inside Dir
}

// Agents lists the supported agents in display order.
var Agents = []Agent{
	{Name: "claude", Label: "Claude Code", Dir: ".claude", HookFile: "settings.json"},
	{Name: "codex", Label: "Codex", Dir: ".codex", HookFile: "hooks.json"},
	{Name: "opencode", Label: "OpenCode", Dir: ".config/opencode", HookFile: "plugins/whatsapp-axi.js"},
}

// AgentsAt distinguishes OpenCode's global config from project-local config.
func AgentsAt(project bool, configHome string) []Agent {
	agents := append([]Agent(nil), Agents...)
	for i := range agents {
		if agents[i].Name == "opencode" {
			if project {
				agents[i].Dir = ".opencode"
			} else if filepath.IsAbs(configHome) {
				agents[i].Dir = filepath.Join(configHome, "opencode")
			}
		}
	}
	return agents
}

// AgentNames returns the agent flag values.
func AgentNames() []string {
	out := make([]string, len(Agents))
	for i, a := range Agents {
		out[i] = a.Name
	}
	return out
}

// FindAgent returns the agent named name.
func FindAgent(name string) (Agent, bool) {
	for _, a := range Agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// HookPath respects native absolute config paths as well as project roots.
func (a Agent) HookPath(root string) string { return filepath.Join(a.configDir(root), a.HookFile) }

// SkillPath uses the same config root as the hook.
func (a Agent) SkillPath(root string) string {
	return filepath.Join(a.configDir(root), "skills", SkillName, "SKILL.md")
}

// Present reports whether the agent's resolved config directory exists.
func (a Agent) Present(root string) bool {
	info, err := os.Stat(a.configDir(root))
	return err == nil && info.IsDir()
}

func (a Agent) configDir(root string) string {
	if filepath.IsAbs(a.Dir) {
		return a.Dir
	}
	return filepath.Join(root, a.Dir)
}

// CodexConfigPath is Codex's config.toml under root; Codex runs hooks only
// with `[features] hooks = true` there.
func CodexConfigPath(root string) string { return filepath.Join(root, ".codex", "config.toml") }

// Change actions.
const (
	ActionAdd    = "add"
	ActionUpdate = "update"
	ActionNone   = "none"
	ActionRemove = "remove"
)

// Items a change applies to.
const (
	ItemHook         = "session_hooks"
	ItemHooksFeature = "hooks_feature"
	ItemSkill        = "skill"
)

// Change is one planned file change. Apply performs it; a change with
// ActionNone writes nothing.
type Change struct {
	Agent  string `json:"agent"`
	Item   string `json:"item"`
	Path   string `json:"path"`
	Action string `json:"change"`

	target string
	data   []byte
	delete bool
}

// Apply writes the change. It is a no-op for ActionNone.
func (c Change) Apply() error {
	switch {
	case c.Action == ActionNone:
		return nil
	case c.delete:
		if err := os.Remove(c.target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ioErr(c.Path, "cannot remove", err)
		}
		// Remove the skill's own directory when it is now empty.
		_ = os.Remove(filepath.Dir(c.target))
		return nil
	}
	return writeAtomic(c.target, c.data)
}

// HookCommand is the managed hook's command line. onPath means the
// whatsapp-axi found on PATH is exe itself, so the portable bare name is
// used; otherwise the absolute path is quoted for the agent's shell.
func HookCommand(exe string, onPath bool, goos string) string {
	return hookCommandFor(startKind, exe, onPath, goos)
}

// EndCommand is the session-end hook's command line for a start command from
// HookCommand: the same program, run as `session-end`.
func EndCommand(command string) string {
	return strings.TrimSuffix(command, " "+HookFlag) + " " + EndWord
}

func hookCommandFor(kind hookKind, exe string, onPath bool, goos string) string {
	if onPath || exe == "" {
		return BinaryName + " " + kind.arg
	}
	if goos == "windows" {
		// Forward slashes work in cmd.exe, PowerShell and Git Bash alike.
		p := strings.ReplaceAll(exe, `\`, "/")
		if strings.ContainsAny(p, " &()^%!'\"") {
			p = `"` + p + `"`
		}
		return p + " " + kind.arg
	}
	return shellQuote(exe) + " " + kind.arg
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9._/~:@%+=-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// IsManagedCommand reports whether a hook command runs whatsapp-axi's
// session-start view: its program is named whatsapp-axi (any directory,
// optional .exe) and its arguments include HookFlag. The session-end hook is
// recognized the same way by EndWord.
func IsManagedCommand(cmd string) bool { return isManagedCommand(startKind, cmd) }

func isManagedCommand(kind hookKind, cmd string) bool {
	prog, rest := firstWord(strings.TrimSpace(cmd))
	base := prog
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if strings.EqualFold(filepath.Ext(base), ".exe") {
		base = base[:len(base)-4]
	}
	if base != BinaryName {
		return false
	}
	for _, f := range strings.Fields(rest) {
		if f == kind.arg {
			return true
		}
	}
	return false
}

// firstWord splits off the first shell word, honouring single and double
// quotes. A backslash escapes only a space or quote, so Windows paths keep
// their separators.
func firstWord(s string) (word, rest string) {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == '"' && c == '\\' && i+1 < len(s) && s[i+1] == '"':
			i++
			b.WriteByte('"')
		case quote != 0:
			b.WriteByte(c)
		case c == '\'' || c == '"':
			quote = c
		case c == '\\' && i+1 < len(s) && strings.IndexByte(" '\"", s[i+1]) >= 0:
			i++
			b.WriteByte(s[i])
		case c == ' ' || c == '\t':
			return b.String(), s[i:]
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), ""
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type hookGroup struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

func desiredEntry(command string) hookEntry {
	return hookEntry{Type: "command", Command: command, Timeout: HookTimeout}
}

// hookLoc is a managed hook: group g of the event's list, entry h.
type hookLoc struct {
	g, h    int
	command string
	entry   *node
}

// hookTree is the parsed path to the event's list (SessionStart or
// SessionEnd). hooks or ss is nil when absent.
type hookTree struct {
	root, hooks, ss *node
	managed         []hookLoc
}

// parseHooks parses settings and locates the managed hooks. It refuses
// shapes it cannot edit without guessing.
func parseHooks(path string, src []byte, kind hookKind) (hookTree, error) {
	var t hookTree
	if !json.Valid(src) {
		return t, invalidJSON(path, src)
	}
	root, err := parseJSON(src)
	if err != nil {
		return t, invalidJSON(path, src)
	}
	t.root = root
	if root.kind != '{' {
		return t, shapeErr(path, "its top level is not a JSON object")
	}
	if t.hooks, _ = root.get("hooks"); t.hooks == nil {
		return t, nil
	}
	if t.hooks.kind != '{' {
		return t, shapeErr(path, `"hooks" is not an object`)
	}
	if t.ss, _ = t.hooks.get(kind.event); t.ss == nil {
		return t, nil
	}
	if t.ss.kind != '[' {
		return t, shapeErr(path, `"hooks.`+kind.event+`" is not an array`)
	}
	for gi, g := range t.ss.elems {
		if g.kind != '{' {
			continue
		}
		list, _ := g.get("hooks")
		if list == nil || list.kind != '[' {
			continue
		}
		for hi, h := range list.elems {
			if h.kind != '{' {
				continue
			}
			c, _ := h.get("command")
			if c == nil || c.kind != '"' {
				continue
			}
			var cmd string
			if json.Unmarshal(c.raw(src), &cmd) == nil && isManagedCommand(kind, cmd) {
				t.managed = append(t.managed, hookLoc{g: gi, h: hi, command: cmd, entry: h})
			}
		}
	}
	return t, nil
}

// isCurrent reports whether a managed entry is exactly the desired one.
func isCurrent(src []byte, entry *node, command string) bool {
	var got map[string]any
	if json.Unmarshal(entry.raw(src), &got) != nil {
		return false
	}
	return got["type"] == "command" && got["command"] == command && got["timeout"] == float64(HookTimeout)
}

// removeHook deletes one managed entry, then each container it leaves
// empty: its group, the event's list, and hooks.
func removeHook(src []byte, t hookTree, loc hookLoc, kind hookKind) []byte {
	e := newEditor(src)
	group := t.ss.elems[loc.g]
	list, _ := group.get("hooks")
	switch {
	case len(list.elems) > 1:
		return e.removeElem(list, loc.h)
	case len(t.ss.elems) > 1:
		return e.removeElem(t.ss, loc.g)
	case len(t.hooks.members) > 1:
		_, i := t.hooks.get(kind.event)
		return e.removeMember(t.hooks, i)
	default:
		_, i := t.root.get("hooks")
		return e.removeMember(t.root, i)
	}
}

// installHook returns src with exactly one current managed hook. Extra
// managed entries are removed and a stale one is replaced in place.
func installHook(path string, src []byte, command string, kind hookKind) ([]byte, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		src = []byte("{}\n")
	}
	for range 64 {
		t, err := parseHooks(path, src, kind)
		if err != nil {
			return nil, err
		}
		e := newEditor(src)
		switch {
		case t.hooks == nil:
			return e.addMember(t.root, "hooks", map[string][]hookGroup{kind.event: {{Hooks: []hookEntry{desiredEntry(command)}}}})
		case t.ss == nil:
			return e.addMember(t.hooks, kind.event, []hookGroup{{Hooks: []hookEntry{desiredEntry(command)}}})
		case len(t.managed) == 0:
			return e.addElem(t.ss, hookGroup{Hooks: []hookEntry{desiredEntry(command)}})
		case len(t.managed) > 1:
			src = removeHook(src, t, t.managed[len(t.managed)-1], kind)
		case isCurrent(src, t.managed[0].entry, command):
			return src, nil
		default:
			return e.replace(t.managed[0].entry, desiredEntry(command))
		}
	}
	return nil, shapeErr(path, "it holds too many whatsapp-axi hooks")
}

// uninstallHook returns src without any managed hook.
func uninstallHook(path string, src []byte, kind hookKind) ([]byte, error) {
	for range 64 {
		t, err := parseHooks(path, src, kind)
		if err != nil {
			return nil, err
		}
		if len(t.managed) == 0 {
			return src, nil
		}
		src = removeHook(src, t, t.managed[0], kind)
	}
	return nil, shapeErr(path, "it holds too many whatsapp-axi hooks")
}

// PlanHook plans installing (or, with remove, removing) the managed
// session-start hook, and the session-end hook that goes with it (command is
// the start command; EndCommand derives the end one), in agent's settings
// under root. Both land in one change because they share a file. Nothing is
// written.
func PlanHook(agent Agent, root, command string, remove bool) (Change, error) {
	path := agent.HookPath(root)
	c := Change{Agent: agent.Name, Item: ItemHook, Path: path, Action: ActionNone}
	f, err := readFile(path)
	if err != nil {
		return c, err
	}
	c.target = f.Target
	commands := map[hookKind]string{startKind: command, endKind: EndCommand(command)}
	if remove {
		if !f.Exists || len(bytes.TrimSpace(f.Data)) == 0 {
			return c, nil
		}
		out := f.Data
		for _, kind := range []hookKind{startKind, endKind} {
			if out, err = uninstallHook(path, out, kind); err != nil {
				return c, err
			}
		}
		if !bytes.Equal(out, f.Data) {
			c.Action, c.data = ActionRemove, out
		}
		return c, nil
	}
	out := f.Data
	for _, kind := range []hookKind{startKind, endKind} {
		if out, err = installHook(path, out, commands[kind], kind); err != nil {
			return c, err
		}
	}
	if !f.Exists || len(bytes.TrimSpace(f.Data)) == 0 {
		if !bytes.HasSuffix(out, []byte("\n")) {
			out = append(out, '\n')
		}
		c.Action, c.data = ActionAdd, out
		return c, nil
	}
	if bytes.Equal(out, f.Data) {
		return c, nil
	}
	c.Action, c.data = ActionUpdate, out
	managed := 0
	for _, kind := range []hookKind{startKind, endKind} {
		if t, err := parseHooks(path, f.Data, kind); err == nil {
			managed += len(t.managed)
		}
	}
	if managed == 0 {
		c.Action = ActionAdd
	}
	return c, nil
}

// HookStatus is what InspectHook found.
type HookStatus struct {
	Path      string
	Exists    bool
	Installed bool   // a managed hook is present
	Current   bool   // the session-start and session-end hooks are exactly the desired ones
	Command   string // the first managed session-start hook's command, else the session-end one's
	Err       error  // the file could not be read or parsed
}

// InspectHook reports the managed hooks in agent's settings under root.
// Current needs both the session-start and the session-end hook.
func InspectHook(agent Agent, root, command string) HookStatus {
	s := HookStatus{Path: agent.HookPath(root)}
	f, err := readFile(s.Path)
	if err != nil {
		s.Err = err
		return s
	}
	s.Exists = f.Exists
	if !f.Exists || len(bytes.TrimSpace(f.Data)) == 0 {
		return s
	}
	s.Current = true
	for _, kind := range []hookKind{startKind, endKind} {
		t, err := parseHooks(s.Path, f.Data, kind)
		if err != nil {
			s.Err = err
			return s
		}
		want := command
		if kind == endKind {
			want = EndCommand(command)
		}
		if len(t.managed) > 0 {
			s.Installed = true
			if s.Command == "" {
				s.Command = t.managed[0].command
			}
		}
		if len(t.managed) != 1 || !isCurrent(f.Data, t.managed[0].entry, want) {
			s.Current = false
		}
	}
	return s
}

// invalidJSON explains where a settings file stops being JSON.
func invalidJSON(path string, src []byte) error {
	where := ""
	var se *json.SyntaxError
	var v any
	if err := json.Unmarshal(src, &v); errors.As(err, &se) {
		line, col := lineCol(src, int(se.Offset))
		where = " (line " + strconv.Itoa(line) + ", column " + strconv.Itoa(col) + ": " + se.Error() + ")"
	}
	return &contract.Error{Code: CodeConfigInvalid, Message: path + " is not valid JSON" + where,
		Help: "fix the JSON by hand (comments and trailing commas are not allowed) or move the file away, then rerun; whatsapp-axi never rewrites a file it cannot parse"}
}

func shapeErr(path, why string) error {
	return &contract.Error{Code: CodeConfigInvalid, Message: path + " cannot be edited safely: " + why,
		Help: "fix the file by hand or move it away, then rerun; whatsapp-axi never rewrites settings it does not understand"}
}

func lineCol(src []byte, offset int) (int, int) {
	if offset > len(src) {
		offset = len(src)
	}
	line, col := 1, 1
	for _, c := range src[:max(offset-1, 0)] {
		if c == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return line, col
}
