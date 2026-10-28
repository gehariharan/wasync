package agentsetup

import (
	"bytes"
	"encoding/json"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

const (
	openCodeMarkerPrefix = "// Managed by whatsapp-axi: OpenCode "
	openCodeMarker       = openCodeMarkerPrefix + "ambient context v2\n"
)

// OpenCodePlugin uses the native system-transform hook for session-start
// context and, for session-end capture, notes which whatsapp-axi commands the
// session's bash calls ran. Only the contentless brief view is read, and only
// command names reach `whatsapp-axi session-end`, never arguments; the
// executable is invoked directly, without a shell.
func OpenCodePlugin(executable string) []byte {
	binary, _ := json.Marshal(executable)
	return []byte(openCodeMarker + `import { execFile } from "node:child_process";

const binary = ` + string(binary) + `;
const marker = "[whatsapp-axi ambient context]";
const invocation = /(?:^|[\s"'\/\\;&|(])whatsapp-axi(?:\.exe)?["']?\s+([a-z][a-z-]*)/g;

export const WhatsappAXI = async ({ directory }) => {
  const sessions = new Map();
  const used = new Map();
  const readBrief = () => new Promise((resolve) => {
    execFile(binary, ["--brief"], {
      cwd: directory, timeout: 10000, maxBuffer: 8192, windowsHide: true,
    }, (error, stdout) => {
      resolve(error
        ? "whatsapp-axi: ambient context unavailable\nhelp: Run whatsapp-axi doctor"
        : stdout.trim());
    });
  });
  const recordEnd = (names) => new Promise((resolve) => {
    const child = execFile(binary, ["session-end"], {
      cwd: directory, timeout: 10000, maxBuffer: 8192, windowsHide: true,
    }, () => resolve());
    child.stdin.on("error", () => {});
    child.stdin.end(JSON.stringify({ cwd: directory, commands: names }));
  });
  return {
    "tool.execute.before": async (input, output) => {
      const command = output && output.args && output.args.command;
      if (input.tool !== "bash" || !input.sessionID || typeof command !== "string") return;
      const names = Array.from(command.matchAll(invocation), (m) => m[1]);
      if (!names.length) return;
      if (!used.has(input.sessionID)) {
        if (used.size >= 128) used.delete(used.keys().next().value);
        used.set(input.sessionID, []);
      }
      const list = used.get(input.sessionID);
      for (const name of names) if (list.length < 1000) list.push(name);
    },
    event: async ({ event }) => {
      if (!event || (event.type !== "session.idle" && event.type !== "session.deleted")) return;
      const props = event.properties || {};
      const id = props.sessionID || (props.info && props.info.id);
      const names = used.get(id);
      if (!names) return;
      if (event.type === "session.deleted") used.delete(id);
      await recordEnd(names);
    },
    "experimental.chat.system.transform": async (input, output) => {
      if (!input.sessionID || output.system.some((part) => part.includes(marker))) return;
      if (!sessions.has(input.sessionID)) {
        if (sessions.size >= 128) sessions.delete(sessions.keys().next().value);
        sessions.set(input.sessionID, readBrief());
      }
      const brief = await sessions.get(input.sessionID);
      if (!brief) return;
      const context = marker + "\n" + brief;
      if (output.system.length) output.system[output.system.length - 1] += "\n\n" + context;
      else output.system.push(context);
    },
  };
};
`)
}

func openCodeFile(agent Agent, root string) (file, error) {
	f, err := readFile(agent.HookPath(root))
	if err == nil && f.Exists && !bytes.HasPrefix(f.Data, []byte(openCodeMarkerPrefix)) {
		return f, &contract.Error{Code: CodeConfigInvalid, Message: f.Path + " exists but is not the managed whatsapp-axi plugin", Help: "move the file away, then rerun setup; whatsapp-axi only edits its own plugin"}
	}
	return f, err
}

func PlanOpenCodeHook(agent Agent, root, executable string, remove bool) (Change, error) {
	c := Change{Agent: agent.Name, Item: ItemHook, Path: agent.HookPath(root), Action: ActionNone}
	f, err := openCodeFile(agent, root)
	if err != nil {
		return c, err
	}
	c.target = f.Target
	switch {
	case remove && f.Exists:
		c.Action, c.delete = ActionRemove, true
	case remove:
	case !f.Exists:
		c.Action, c.data = ActionAdd, OpenCodePlugin(executable)
	case !bytes.Equal(f.Data, OpenCodePlugin(executable)):
		c.Action, c.data = ActionUpdate, OpenCodePlugin(executable)
	}
	return c, nil
}

func InspectOpenCodeHook(agent Agent, root, executable string) HookStatus {
	s := HookStatus{Path: agent.HookPath(root)}
	f, err := openCodeFile(agent, root)
	if err != nil {
		s.Err = err
		return s
	}
	s.Exists, s.Installed = f.Exists, f.Exists
	if f.Exists {
		s.Current = bytes.Equal(f.Data, OpenCodePlugin(executable))
		s.Command = "managed whatsapp-axi plugin"
	}
	return s
}
