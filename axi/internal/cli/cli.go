// Package cli parses whatsapp-axi arguments and runs commands. Exit codes:
// 0 success (including "wasync not running" states), 1 error (and a doctor
// report with a failed check), 2 usage.
package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gehariharan/wasync/axi/internal/buildinfo"
	"github.com/gehariharan/wasync/axi/internal/compat"
	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/output"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Description is the one-sentence description shown in the home view.
const Description = "Agent-friendly access to your local WhatsApp archive through wasync: reads by default, writes only with a scope a human granted"

// App holds the process dependencies so tests can replace them.
type App struct {
	Out          io.Writer
	Getenv       config.Getenv
	Executable   func() (string, error)
	HomeDir      func() (string, error)
	LookPath     func(file string) (string, error) // PATH lookup for the hook command
	Stdin        io.Reader                         // the session-end hook's payload; nil when stdin is a terminal
	Getwd        func() (string, error)            // the directory the session record is kept for
	ProbeTimeout time.Duration                     // API probe; default 750ms
	QueryTimeout time.Duration                     // offline version probe; default 10s
	Compat       *compat.Matrix
}

// New returns an App wired to the real process.
func New(out io.Writer) *App {
	return &App{Out: out, Getenv: os.Getenv, Executable: os.Executable, HomeDir: os.UserHomeDir, LookPath: exec.LookPath, Stdin: pipedStdin(), Getwd: os.Getwd, Compat: compat.Default()}
}

// pipedStdin is stdin unless it is a terminal, where reading a hook payload
// would only hang.
func pipedStdin() io.Reader {
	if info, err := os.Stdin.Stat(); err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	return os.Stdin
}

// IsVersionFlag reports whether arg is one of the version flags.
func IsVersionFlag(arg string) bool { return arg == "--version" || arg == "-v" || arg == "-V" }

// FastPath handles a bare `--version`, `-v` or `-V` without probing any
// transport. It returns false for every other argument list.
func FastPath(args []string, w io.Writer) bool {
	if len(args) != 1 || !IsVersionFlag(args[0]) {
		return false
	}
	_, _ = io.WriteString(w, buildinfo.Version+"\n")
	return true
}

type flagSpec struct {
	name        string
	value       string // placeholder such as "N" when the flag takes a value
	description string
}

type command struct {
	name        string // "" is the home view
	usage       string
	description string
	flags       []flagSpec
	minArgs     int
	maxArgs     int
	argsName    string
	examples    []string
	run         func(a *App, ctx context.Context, inv invocation) error
}

type invocation struct {
	json  bool
	args  []string
	flags map[string]string // value flags by name (with "--"); boolean flags map to "true"
}

// flag returns the value of a flag and whether it was given.
func (inv invocation) flag(name string) (string, bool) {
	v, ok := inv.flags[name]
	return v, ok
}

// has reports whether a boolean flag was given.
func (inv invocation) has(name string) bool {
	_, ok := inv.flags[name]
	return ok
}

var jsonFlag = flagSpec{name: "--json", description: "print JSON instead of text"}

// commands lists implemented commands only. It is filled in init because
// the help command reads it.
var commands []*command

func init() {
	commands = []*command{
		{
			name:        "",
			usage:       "whatsapp-axi [--brief] [--fields LIST] [--json]",
			description: "Home view: this binary, the wasync transport in use, compatibility, recent chats and next steps",
			flags:       []flagSpec{briefFlag, listFieldsFlag(chatFields, defaultChatFields), jsonFlag},
			examples:    []string{"whatsapp-axi", "whatsapp-axi --brief", "whatsapp-axi --json"},
			run:         (*App).runHome,
		},
		chatsCommand,
		messagesCommand,
		searchCommand,
		threadCommand,
		messageCommand,
		replyCommand,
		reactCommand,
		editCommand,
		accessCommand,
		{
			name:        "version",
			usage:       "whatsapp-axi version [--json]",
			description: "Show the whatsapp-axi version and the detected wasync version, transport and compatibility",
			flags:       []flagSpec{jsonFlag},
			examples:    []string{"whatsapp-axi version", "whatsapp-axi version --json", "whatsapp-axi --version"},
			run:         (*App).runVersion,
		},
		{
			name:        "doctor",
			usage:       "whatsapp-axi doctor [--json]",
			description: "Check installation, pairing, service, permissions, compatibility and hooks; each problem comes with repair guidance",
			flags:       []flagSpec{jsonFlag},
			examples:    []string{"whatsapp-axi doctor", "whatsapp-axi doctor --json"},
			run:         (*App).runDoctor,
		},
		{
			name:        "help",
			usage:       "whatsapp-axi help [COMMAND] [--json]",
			description: "List commands, or show flags and examples for one command",
			flags:       []flagSpec{jsonFlag},
			maxArgs:     1,
			argsName:    "COMMAND",
			examples:    []string{"whatsapp-axi help", "whatsapp-axi help version", "whatsapp-axi help --json"},
			run:         (*App).runHelp,
		},
		observeCommand(),
		setupCommand,
		sessionEndCommand,
	}
}

func findCommand(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	return nil
}

func commandNames() []string {
	var out []string
	for _, c := range commands {
		if c.name != "" {
			out = append(out, c.name)
		}
	}
	return out
}

func displayName(c *command) string {
	if c.name == "" {
		return "whatsapp-axi"
	}
	return c.name
}

func usageErr(help, format string, args ...any) *contract.Error {
	return contract.Errorf(contract.CodeUsage, help, format, args...)
}

// Run executes args and returns the exit code. All output, including
// errors, goes to a.Out.
func (a *App) Run(ctx context.Context, args []string) int {
	if FastPath(args, a.Out) {
		return ExitOK
	}
	if a.Compat == nil {
		a.Compat = compat.Default()
	}
	cmd, rest, jsonMode, err := a.resolve(args)
	if err != nil {
		_ = output.Error(a.Out, err, jsonMode)
		return ExitUsage
	}
	inv, wantHelp, err := parseFlags(cmd, rest)
	if err != nil {
		_ = output.Error(a.Out, err, inv.json)
		return ExitUsage
	}
	if wantHelp {
		if err := a.commandHelp(cmd, inv.json); err != nil {
			return ExitError
		}
		return ExitOK
	}
	if err := cmd.run(a, ctx, inv); err != nil {
		if errors.Is(err, errReported) {
			return ExitError
		}
		var ce *contract.Error
		_ = writeError(a.Out, err, inv.json)
		if errors.As(err, &ce) && ce.Code == contract.CodeUsage {
			return ExitUsage
		}
		return ExitError
	}
	return ExitOK
}

func (a *App) resolve(args []string) (*command, []string, bool, error) {
	jsonMode := containsJSON(args)
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return findCommand(""), args, jsonMode, nil
	}
	if c := findCommand(args[0]); c != nil {
		return c, args[1:], jsonMode, nil
	}
	return nil, nil, jsonMode, usageErr("valid commands: "+strings.Join(commandNames(), ", ")+"; run `whatsapp-axi help`", "unknown command %q", args[0])
}

func containsJSON(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--json" {
			return true
		}
	}
	return false
}

func (c *command) findFlag(name string) *flagSpec {
	for i := range c.flags {
		if c.flags[i].name == name {
			return &c.flags[i]
		}
	}
	return nil
}

// parseFlags validates rest against the command's own flags. Unknown flags
// and unexpected arguments are usage errors that list the valid flags.
// Value flags accept `--flag value` and `--flag=value`; after `--` every
// argument is positional, so a search text may start with "-".
func parseFlags(c *command, rest []string) (invocation, bool, error) {
	inv := invocation{flags: map[string]string{}}
	inv.json = containsJSON(rest)
	wantHelp := false
	positionalOnly := false
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		switch {
		case positionalOnly || arg == "-" || !strings.HasPrefix(arg, "-"):
			if len(inv.args) >= c.maxArgs {
				return inv, false, usageErr("usage: "+c.usage, "unexpected argument %q for `%s`", arg, displayName(c))
			}
			inv.args = append(inv.args, arg)
		case arg == "--":
			positionalOnly = true
		case IsVersionFlag(arg):
			return inv, false, usageErr("whatsapp-axi --version", "%s takes no other arguments", arg)
		case arg == "--help" || arg == "-h":
			wantHelp = true
		case arg == "--json":
			inv.json = true
		default:
			name, value, hasValue := strings.Cut(arg, "=")
			f := c.findFlag(name)
			if f == nil {
				if name == "--json" {
					return inv, false, usageErr("use --json without a value", "--json takes no value")
				}
				return inv, false, usageErr(validFlags(c), "unknown flag %s for `%s`", name, displayName(c))
			}
			switch {
			case f.value == "" && hasValue:
				return inv, false, usageErr("use "+name+" without a value", "%s takes no value", name)
			case f.value == "":
				value = "true"
			case !hasValue:
				if i+1 >= len(rest) {
					return inv, false, usageErr("usage: "+c.usage, "%s needs a value (%s %s)", name, name, f.value)
				}
				i++
				value = rest[i]
			}
			inv.flags[name] = value
		}
	}
	if !wantHelp && len(inv.args) < c.minArgs {
		return inv, false, usageErr("usage: "+c.usage, "`%s` needs %s", displayName(c), c.argsName)
	}
	return inv, wantHelp, nil
}

func validFlags(c *command) string {
	names := make([]string, 0, len(c.flags))
	for _, f := range c.flags {
		names = append(names, f.name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "`" + displayName(c) + "` takes no flags (--help always allowed)"
	}
	return "valid flags for `" + displayName(c) + "`: " + strings.Join(names, ", ") + " (--help always allowed)"
}

// binPath is the current executable with the home directory collapsed to ~.
func (a *App) binPath() string {
	exe, err := a.Executable()
	if err != nil || exe == "" {
		return "whatsapp-axi"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return a.collapseHome(exe)
}

func (a *App) collapseHome(p string) string {
	home, err := a.HomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + string(filepath.Separator) + p[len(home)+1:]
	}
	return p
}
