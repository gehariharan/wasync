# ADR-003: A tool server for apps without a terminal

Status: accepted, from the owner's answers on 2026-10-29 (tool server is `whatsapp-axi mcp`, with `wasync setup` for the human pieces). Amends ADR-001 D-1 in one narrow way; everything else in ADR-001 stays in force.

## Context

People who use the Codex desktop app have no terminal. They need WhatsApp to appear among Codex's tools, and they need a way to start first-time setup from a conversation. Codex runs a plugin's local tool server outside its command sandbox, so it can reach wasync's loopback API; a shell command run by the agent cannot, in the default sandbox.

## Decision

1. **`whatsapp-axi mcp` is the tool server.** It speaks the Model Context Protocol over standard input and output, opens no network port, and runs the same `whatsapp-axi` commands an agent would run in a shell. The agent credential, its scopes and the chat allowlist are enforced by wasync exactly as before (ADR-001 D-2). The server adds no policy of its own (D-3).
2. **The menu has no approving side.** The tools read, search, follow changes, reply, react, edit, ask for access to a chat, check that request, and start setup. There is no tool to approve a request, grant, create or revoke a credential, change the access mode, or read a secret, and none will be added without replacing this ADR. A test fixes the menu.
3. **One narrow amendment to ADR-001 D-1.** D-1 says AXI never starts `wasync serve`. The tool `whatsapp_start_setup` may run `wasync setup`, and only that, so a person can begin setup from the app. It is bounded:
   - It runs exactly `wasync setup` with no other arguments, found the same way AXI finds wasync for `wasync query`, started detached, with its output discarded. AXI does not read what it prints, so a link or credential never reaches the agent.
   - `wasync setup` opens the setup page itself. Linking WhatsApp, choosing chats, connecting an agent and installing a login item are done by the person on that page, and the last two end in the operating system's confirmation prompt (see the threat model).
   - At most one start per thirty seconds per tool-server process, so an agent cannot fill the screen with browser windows.
   - AXI still never connects to WhatsApp, never opens the archive or the session store and never takes `device.lock`; the `wasync` process it starts does those things, as it does when the person runs it.
   - The command-line `whatsapp-axi` program still never starts wasync. Only the tool server has this one tool.
4. **The plugin lives in this repository** (`plugins/whatsapp/`, with `.agents/plugins/marketplace.json` at the root) so it ships with the program version it launches (ADR-002). It is offered through a GitHub marketplace, not OpenAI's public directory.

## Consequences

- A prompt-injected agent can cause one setup page to open. It cannot link an account, choose chats, connect or widen an agent, or approve anything: those need the person at the computer.
- If `whatsapp-axi` is not on the PATH the Codex desktop app gives its tool server, the plugin cannot start. The setup skill tells the person how to fix it; a later change can pin a full path.
- The Windows paths (starting `wasync setup` detached, the login item) have not been run on a real Windows computer.
