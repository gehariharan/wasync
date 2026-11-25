package cli

import (
	"context"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/output"
)

type flagJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type commandJSON struct {
	Name        string     `json:"name"`
	Usage       string     `json:"usage"`
	Description string     `json:"description"`
	Flags       []flagJSON `json:"flags"`
	Examples    []string   `json:"examples"`
}

func toJSON(c *command) commandJSON {
	out := commandJSON{Name: displayName(c), Usage: c.usage, Description: c.description, Flags: []flagJSON{}, Examples: c.examples}
	for _, f := range c.flags {
		out.Flags = append(out.Flags, flagJSON{flagLabel(f), f.description})
	}
	return out
}

func (a *App) runHelp(_ context.Context, inv invocation) error {
	if len(inv.args) == 1 {
		name := inv.args[0]
		if name == "wasync-axi" || name == "home" {
			name = ""
		}
		c := findCommand(name)
		if c == nil {
			return usageErr("valid commands: wasync-axi, "+strings.Join(commandNames(), ", "), "unknown command %q", inv.args[0])
		}
		return a.commandHelp(c, inv.json)
	}
	if inv.json {
		list := make([]commandJSON, 0, len(commands))
		for _, c := range commands {
			list = append(list, toJSON(c))
		}
		return output.JSON(a.Out, map[string]any{"commands": list})
	}
	o := output.New(a.Out)
	rows := make([][]string, 0, len(commands))
	for _, c := range commands {
		rows = append(rows, []string{displayName(c), c.usage, c.description})
	}
	o.Table(0, "commands", []string{"name", "usage", "description"}, rows)
	o.Help("Run `wasync-axi help <command>` for flags and examples")
	return o.Err()
}

// commandHelp prints the concise reference for one command.
func (a *App) commandHelp(c *command, jsonMode bool) error {
	if jsonMode {
		return output.JSON(a.Out, toJSON(c))
	}
	o := output.New(a.Out)
	o.Field(0, "usage", c.usage)
	o.Field(0, "description", c.description)
	rows := make([][]string, 0, len(c.flags)+1)
	for _, f := range c.flags {
		rows = append(rows, []string{flagLabel(f), f.description})
	}
	rows = append(rows, []string{"--help", "show this reference"})
	o.Table(0, "flags", []string{"flag", "description"}, rows)
	o.List(0, "examples", c.examples)
	return o.Err()
}

// flagLabel is "--name" or "--name VALUE".
func flagLabel(f flagSpec) string {
	if f.value == "" {
		return f.name
	}
	return f.name + " " + f.value
}
