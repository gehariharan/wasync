package agentsetup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	if os.Getenv("WHATSAPP_AXI_PLUGIN_TEST_CHILD") != "1" {
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "session-end" {
		payload, _ := io.ReadAll(os.Stdin)
		f, err := os.OpenFile(os.Getenv("WHATSAPP_AXI_PLUGIN_ENDS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(f, string(payload))
		f.Close()
		os.Exit(0)
	}
	f, err := os.OpenFile(os.Getenv("WHATSAPP_AXI_PLUGIN_CALLS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(3)
	}
	fmt.Fprintln(f, "brief")
	f.Close()
	if os.Getenv("WHATSAPP_AXI_PLUGIN_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "private dependency failure")
		os.Exit(1)
	}
	if len(os.Args) != 2 || os.Args[1] != "--brief" {
		os.Exit(2)
	}
	fmt.Println("whatsapp-axi: test archive\nwasync: offline\nchats: 8")
	os.Exit(0)
}

func TestOpenCodePluginInvokesBriefAndInjectsOnce(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required to execute the native OpenCode plugin interface")
	}
	root := t.TempDir()
	plugin := filepath.Join(root, "whatsapp-axi.mjs")
	if err := os.WriteFile(plugin, OpenCodePlugin(os.Args[0]), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(root, "exercise.mjs")
	script := `import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { WhatsappAXI } from "./whatsapp-axi.mjs";
const hooks = await WhatsappAXI({ directory: process.cwd() });
const transform = hooks["experimental.chat.system.transform"];
const first = { system: ["existing system instructions"] };
await Promise.all([transform({sessionID:"one"}, first), transform({sessionID:"one"}, {system:[]})]);
assert.equal(first.system.length, 1);
assert.ok(first.system[0].startsWith("existing system instructions"));
assert.ok(first.system[0].includes("chats: 8"));
const prior = first.system[0];
await transform({sessionID:"one"}, first);
assert.equal(first.system[0], prior);
await transform({sessionID:"one"}, {system:["next turn"]});
const multiple = {system:["provider header", "other instructions", "primary instructions"]};
await transform({sessionID:"one"}, multiple);
assert.equal(multiple.system.length, 3);
assert.equal(multiple.system[0], "provider header");
assert.equal(multiple.system[1], "other instructions");
assert.ok(multiple.system[2].startsWith("primary instructions"));
assert.ok(multiple.system[2].includes("chats: 8"));
assert.equal((await readFile(process.env.WHATSAPP_AXI_PLUGIN_CALLS,"utf8")).trim().split("\n").length,1);
await transform({}, {system:[]});
process.env.WHATSAPP_AXI_PLUGIN_FAIL = "1";
const failure = {system:[]};
await transform({sessionID:"two"},failure);
assert.ok(failure.system[0].includes("whatsapp-axi doctor"));
assert.ok(!failure.system[0].includes("private dependency failure"));
assert.equal((await readFile(process.env.WHATSAPP_AXI_PLUGIN_CALLS,"utf8")).trim().split("\n").length,2);
console.log("OpenCode ambient context: per-session execution, in-place injection, deduplication and failure guidance passed");
`
	if err := os.WriteFile(runner, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, runner)
	cmd.Env = append(os.Environ(), "WHATSAPP_AXI_PLUGIN_TEST_CHILD=1", "WHATSAPP_AXI_PLUGIN_CALLS="+filepath.Join(root, "calls"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestOpenCodeInstallRepairRemoveAndOwnership(t *testing.T) {
	agent, _ := FindAgent("opencode")
	root := t.TempDir()
	original := []byte("export const AnotherPlugin = async () => ({});\n")
	neighbor := filepath.Join(root, agent.Dir, "plugins", "other.js")
	if err := os.MkdirAll(filepath.Dir(neighbor), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(neighbor, original, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, executable := range []string{"/one/whatsapp-axi", "/two/whatsapp-axi"} {
		plan, err := PlanOpenCodeHook(agent, root, executable, false)
		if err != nil || plan.Action == ActionNone {
			t.Fatalf("%+v %v", plan, err)
		}
		if err := plan.Apply(); err != nil {
			t.Fatal(err)
		}
		if status := InspectOpenCodeHook(agent, root, executable); !status.Current {
			t.Fatalf("%+v", status)
		}
		repeated, err := PlanOpenCodeHook(agent, root, executable, false)
		if err != nil || repeated.Action != ActionNone {
			t.Fatalf("%+v %v", repeated, err)
		}
	}
	plan, err := PlanOpenCodeHook(agent, root, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agent.HookPath(root)); !os.IsNotExist(err) {
		t.Fatalf("plugin still present: %v", err)
	}
	if got, err := os.ReadFile(neighbor); err != nil || string(got) != string(original) {
		t.Fatalf("neighbor changed: %v", err)
	}
	if err := os.WriteFile(agent.HookPath(root), original, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, remove := range []bool{false, true} {
		if _, err := PlanOpenCodeHook(agent, root, "/one/whatsapp-axi", remove); err == nil {
			t.Fatal("unowned plugin modified")
		}
	}
	for _, ag := range AgentsAt(true, "") {
		if ag.Name == "opencode" && !strings.Contains(ag.HookPath(root), filepath.Join(".opencode", "plugins")) {
			t.Fatal("project plugin path", ag.HookPath(root))
		}
	}
}

func TestOpenCodePluginRecordsCommandNamesAtSessionEnd(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required to execute the native OpenCode plugin interface")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "whatsapp-axi.mjs"), OpenCodePlugin(os.Args[0]), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { WhatsappAXI } from "./whatsapp-axi.mjs";
const hooks = await WhatsappAXI({ directory: process.cwd() });
const before = hooks["tool.execute.before"];
await before({ tool: "bash", sessionID: "one" }, { args: { command: "whatsapp-axi messages 999000111@s.whatsapp.net && whatsapp-axi search 'Dana Example'" } });
await before({ tool: "bash", sessionID: "one" }, { args: { command: "ls -la" } });
await before({ tool: "read", sessionID: "one" }, { args: { command: "whatsapp-axi chats" } });
await before({ tool: "bash", sessionID: "two" }, { args: { command: "/usr/local/bin/whatsapp-axi react X" } });
await before({ tool: "bash" }, { args: { command: "whatsapp-axi chats" } });
await before({ tool: "bash", sessionID: "one" }, {});
await hooks.event({ event: { type: "session.created", properties: { sessionID: "one" } } });
await hooks.event({ event: { type: "session.idle", properties: { sessionID: "none" } } });
await hooks.event({ event: { type: "session.deleted", properties: { info: { id: "one" } } } });
await hooks.event({ event: { type: "session.deleted", properties: { info: { id: "one" } } } });
const ends = (await readFile(process.env.WHATSAPP_AXI_PLUGIN_ENDS, "utf8")).trim().split("\n").map((l) => JSON.parse(l));
assert.deepEqual(ends, [{ cwd: process.cwd(), commands: ["messages", "search"] }]);
console.log("OpenCode session end: command names only, one record per ended session");
`
	if err := os.WriteFile(filepath.Join(root, "exercise.mjs"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "exercise.mjs")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "WHATSAPP_AXI_PLUGIN_TEST_CHILD=1", "WHATSAPP_AXI_PLUGIN_ENDS="+filepath.Join(root, "ends"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
