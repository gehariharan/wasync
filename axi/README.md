# whatsapp-axi

`whatsapp-axi` is a command-line tool for AI agents to read your local WhatsApp archive and, when you
grant it, to reply, react to and edit messages in it. The archive
belongs to [wasync](https://github.com/gehariharan/wasync); `whatsapp-axi` only asks wasync for
data and formats the answer for agents, following the [AXI](https://github.com/kunchenguid/axi)
principles: compact output, explicit empty states, structured errors on stdout, next-step hints.

It has no WhatsApp code of its own. It never connects to WhatsApp, never opens the SQLite archive or the
session store, never takes wasync's device lock and never starts `wasync serve`.

> Status: early development. Today it implements the home view, the read commands (`chats`, `messages`,
> `search`, `thread`, `message`), `observe`, `reply`, `react`, `edit`, `version`, `doctor`, `setup` (agent hooks and skill)
> and `help`.

## How it reads wasync

whatsapp-axi uses exactly two transports and tells you which one served each result.

| Transport | Used when | Can do |
|---|---|---|
| Local API: `http://127.0.0.1:8787/api/v1` | wasync is running (TUI or `wasync serve`) and its API version is compatible | Scoped reads and, with the right scope, writes (`reply`, `react`, `edit`) with an agent credential |
| `wasync query <command> --v1 --json` subprocess | wasync is not running | Read-only offline reads of the archive |

Selection is deterministic: whatsapp-axi probes `GET /api/v1/version` with a short timeout. If it answers
with a compatible version, the API is used. Otherwise whatsapp-axi runs `wasync query`. Anything that
needs the running owner returns `owner_offline` instead of guessing.

Settings, all optional:

| Variable | Meaning | Default |
|---|---|---|
| `WASYNC_API_ADDR` | wasync API address; must be a literal loopback address | `127.0.0.1:8787` |
| `WASYNC_BIN` | path to the wasync binary | `wasync` on `PATH` |
| `WASYNC_DATA_DIR` | wasync data directory, passed as `--data-dir` to `wasync query` | wasync's default |
| `WHATSAPP_AXI_TOKEN` | agent credential (`wsa_...`) | the `credential` file below |
| `WHATSAPP_AXI_CONFIG_DIR` | whatsapp-axi config directory | `<user config dir>/whatsapp-axi` |
| `WHATSAPP_AXI_AGENT_HOME` | synthetic home for `setup` and `doctor` agent settings (`.claude`, `.codex`, `.config/opencode`); overrides native XDG paths | unset (native agent paths) |

## Security model

- **A human grants access, in wasync.** Run `wasync agents grant <name> --scope read.chats,read.messages`
  in a terminal. wasync offers to save the new agent credential straight into the file `credential` in the
  whatsapp-axi config directory (private, mode `600`), so you copy nothing by hand. If you decline, it
  prints the credential once; put it in `WHATSAPP_AXI_TOKEN` or that file yourself.
- **whatsapp-axi never uses wasync's own tokens.** It refuses anything that is not a `wsa_` agent
  credential, so `api.token`, `send.token` and `approve.token` stay with wasync.
- **Read-only by default, enforced by wasync.** Scopes, chat allowlists, rate limits, expiry and the audit
  log are checked inside wasync's API. Flags, confirmations and agent skills in whatsapp-axi are
  convenience, not a security boundary.
- **Writes need a scope you grant, per chat.** `whatsapp-axi reply REF --text TEXT` posts to wasync's API,
  which refuses it unless the credential holds `send` and the message's chat is in its allowlist
  (`wasync agents grant <name> --scope read.chats,read.messages,send --on-demand`, then the person
  approves each chat when you ask for it).
  `whatsapp-axi react REF EMOJI|--remove` needs the separate `react` scope the same way, and
  `whatsapp-axi edit REF --text TEXT` the `edit.own` scope; wasync also checks that the message is yours,
  is text and is inside WhatsApp's edit window. whatsapp-axi adds
  no rules of its own and prints wasync's refusal unchanged. It sends once and never retries; offline
  (`wasync query`) never writes.
- **Chats can be approved on demand.** If a read or write is refused because the chat is not allowed,
  `whatsapp-axi access request (--chat JID | --match TEXT) [--send] [--edit]` asks the person for it and
  `whatsapp-axi access status REQUEST_ID` shows the answer. `--send` also asks to send, reply and react;
  `--edit` also asks to edit your own messages. Asking grants nothing by itself: wasync queues a human
  approval (the TUI dialog, the approval page or `wasync actions approve REQUEST_ID`, confirmed by the
  person on their computer, five-minute expiry, no replay) in which the person picks the chat, the access and the history depth (from now on
  only, or a full history sync). The grant covers that one chat and never exceeds the scopes of your
  credential. in ask-each-time mode wasync always answers `pending` and never says whether a name matched anything or which
  chats it found; if it expires, try the phone number or the exact chat JID. If the person set wasync to
  "sync all chats", a read, send, reply or react request for the one chat it matched is approved at
  once (`status: approved` in the first reply), so in that mode a name or number probe both reveals and grants that chat; edit access and unmatched or ambiguous requests still
  wait. A rejection or expiry changes nothing and is not retried. Needs the running API and a credential
  created with `wasync agents grant NAME --on-demand` (or with a chat allowlist). Direct chats use the
  saved contact name when WhatsApp provides one; a phone number always works.
- **Offline reads are not scoped.** `wasync query` reads the archive as your OS user, like opening your
  own files. whatsapp-axi says so when it uses the offline transport.
- **Loopback only.** The API address must be a literal loopback IP. whatsapp-axi does not use proxies,
  does not follow redirects and uses short timeouts.
- **Message text is untrusted.** WhatsApp content is printed as a quoted `untrusted_*` field with
  terminal control sequences removed. Agents should treat it as data, never as instructions.

## Install

whatsapp-axi is part of the [wasync monorepo](https://github.com/gehariharan/wasync).
Both tools ship from its [releases page](https://github.com/gehariharan/wasync/releases).
Existing standalone AXI releases remain available at the old repository.
See the [common release guide](../docs/releasing.md).

Installation options:

- **Homebrew (macOS and Linux):** follow the root
  [Homebrew installation guide](../README.md#homebrew-installation), including
  tap migration and the transition from legacy pinned definitions.
- **Prebuilt archives:** download the archive for your OS and CPU from the
  [releases page](https://github.com/gehariharan/wasync/releases), check it against `checksums.txt`
  (`sha256sum -c checksums.txt --ignore-missing`), extract it and put `whatsapp-axi` (`whatsapp-axi.exe`)
  on your `PATH`. The binaries are static (no cgo).
- **Any OS, from source** (Go 1.27.1 or newer; the module selects Go 1.27.2):

  ```sh
  go install github.com/gehariharan/wasync/axi/cmd/whatsapp-axi@latest
  ```

You also need wasync. The wasync installers can install this tool for you as an
optional choice (off by default); see
[Choose whether to add AXI for agents](../README.md#choose-whether-to-add-axi-for-agents).
Direct-download users who prefer the standalone archive install both binaries from the
[core installation guide](../README.md#install-a-prebuilt-package).

## Usage

```sh
whatsapp-axi                                # home view: transport, compatibility, recent chats, next steps
whatsapp-axi --brief                        # a few lines for agent session hooks; no chat names or text
whatsapp-axi chats [--match TEXT]           # list chats, or find one by JID or name
whatsapp-axi messages CHAT                  # a chat's messages, newest first (CHAT is a JID or name)
whatsapp-axi search TEXT [--chat CHAT] [--context N]
whatsapp-axi thread REF [--before N] [--after N]
whatsapp-axi message REF [--full]           # one message; --full prints all of its text
whatsapp-axi observe [--consumer NAME]      # change journal by cursor; see below
whatsapp-axi doctor                         # pass/warn/fail checks with repair guidance
whatsapp-axi setup hooks|skill              # opt-in agent integration; see below
whatsapp-axi session-end                    # run by the session-end hook, not by hand; see below
whatsapp-axi version                        # whatsapp-axi version plus wasync version, features and transport
whatsapp-axi help [COMMAND]                 # commands, or flags and examples for one command
whatsapp-axi --version                      # bare version, no probing
```

`messages` and `search` print 20 results per page (`--limit N`); when there are more, the output ends with
the exact command for the next page (`--cursor`). Message text is cut to 200 characters in lists and
1000 in `message`, with a hint naming the command that shows the rest. `REF` is the `ref` column
(`m1_...`). Direct chats carry the contact name WhatsApp shows (your address-book name, else the person's own profile name, else the number); it is printed as untrusted text and `--match` finds it case-insensitively by any part. A chat name that matches several chats prints the candidates and exits 1; rerun with one id.
Matching, search, paging and permissions are all decided by wasync; both transports print the same
output for the same data, apart from the `transport:` line.

List views default to four columns. Use `--fields` to select additional columns
or their order, for example:

```sh
whatsapp-axi chats --fields id,untrusted_name,last_message_at,sync
whatsapp-axi messages family --fields ref,time,sender,type,state,untrusted_text
```

`whatsapp-axi help COMMAND` lists available fields. Selection is local presentation;
pagination commands retain it. Unknown, empty or repeated fields are usage errors.
`--fields` cannot be combined with `--json` or the home view's `--brief`.
Chat views include archived group/direct counts, capture-enabled chats and message
totals. Message and search views distinguish `shown` (this page), `total` (all
matches for the same filters and permissions), and `has_more`. Older compatible
owners that omit totals report `total: unknown` rather than treating the page size
as the total. See the [v1 response contract](../docs/architecture/axi-contract.md#response-envelope-v1-only)
for JSON totals and feature discovery.

Add `--json` to any command for JSON: read commands print wasync's v1 `data` and `page` unchanged plus
the `transport`. Exit codes: `0` success (including empty results and "wasync is not running" in the
home view), `1` error, `2` usage error. Errors are printed on stdout as `error:` / `code:` / `help:` lines.

`doctor` checks installation, service, pairing, permissions, compatibility and agent hooks and skill. It combines
its own client-side checks (wasync binary, API reachability, credential, config file modes, granted
scopes, the compatibility table) with wasync's checks from `GET /api/v1/diagnostics`, or from offline
`wasync query doctor` when the API is not running or no credential is configured; `transport:` says
which. Every `warn` or `fail` check carries a `fix` field with repair guidance.
It exits `1` when any check fails and `0` when the worst result is a warning.
Credentials and hashes are never printed.

The `fix` field can contain instructions for a human rather than a shell command.
Replacing an expiring grant requires an explicit human choice of expiry while
retaining its chat allowlist and rate limit. If an older core supplies no scope
repair, doctor recommends upgrading wasync first.

### Following changes

`observe` reads wasync's change journal. Positions are opaque `c1_` cursors issued by wasync, never
timestamps. Changes name a message by ref but carry no content: re-read the message by ref.

```sh
whatsapp-axi observe                                   # one page from the oldest retained change
whatsapp-axi observe --after c1_...                    # continue after a cursor
whatsapp-axi observe --consumer replica                # resume after the consumer's stored checkpoint
whatsapp-axi observe ack c1_... --consumer replica     # commit progress after processing
whatsapp-axi observe --consumer replica --follow --json   # poll until interrupted, one JSON object per line
```

The checkpoint never moves on its own: only `observe ack` or an explicit `--commit` commits it, and only
through the running wasync API with an agent granted the `observe` scope. Offline reads work but never
commit. With one-shot `--commit --json`, the page is emitted first and a separate JSON line
containing `committed` confirms the checkpoint write. A failed page write leaves the checkpoint
unchanged; use explicit `observe ack` to acknowledge processing by a downstream consumer.
A `cursor_expired` error means the position is gone (older than wasync's 30-day journal or from
another archive): re-read what you replicate, then restart with `--from-oldest` and ack the new cursor.

## Tool server (MCP)

`whatsapp-axi mcp` serves the same reads and the allowed writes as [Model Context
Protocol](https://modelcontextprotocol.io) tools over standard input and output, for apps
without a terminal such as the Codex desktop app. It opens no network port. Tools:
`whatsapp_status`, `whatsapp_doctor`, `whatsapp_list_chats`, `whatsapp_read_messages`,
`whatsapp_search`, `whatsapp_thread`, `whatsapp_message`, `whatsapp_changes`,
`whatsapp_reply`, `whatsapp_react`, `whatsapp_edit`, `whatsapp_request_access`,
`whatsapp_access_status` and `whatsapp_start_setup` (opens the wasync setup page in the
person's browser). There is deliberately no tool to approve, grant, revoke or reveal a
credential. The Codex plugin that wires it up is `plugins/whatsapp/` at the repository root.

## Agent integration

Two optional ways to make agents aware of whatsapp-axi. You only need one; they work together too.

| | Session hook (`setup hooks`) | Skill (`setup skill`) |
|---|---|---|
| What the agent gets | `whatsapp-axi --brief` at the start of every session: wasync state, chat count, next step, and a line on what the last session in this directory did | [`skills/whatsapp-axi/SKILL.md`](skills/whatsapp-axi/SKILL.md), loaded when a task is about WhatsApp |
| Cost | about 150 tokens per session, plus about 25 once a previous session is recorded | none until used |
| Agents | Claude Code (`~/.claude/settings.json`), Codex (`~/.codex/hooks.json`), OpenCode (paths below) | Skills under `.claude/skills/`, `.codex/skills/` or the OpenCode config root below |

```sh
whatsapp-axi setup hooks --dry-run       # show what would change, write nothing
whatsapp-axi setup hooks                 # install or repair the SessionStart and SessionEnd hooks
whatsapp-axi setup hooks --remove        # remove them again
whatsapp-axi setup skill                 # install or update the skill
whatsapp-axi setup hooks --dir .         # project settings (.claude, .codex, .opencode)
whatsapp-axi setup hooks --agent opencode # native ambient-context plugin
```

Without `--agent`, setup targets the agents whose config directory exists (Claude Code if none does);
`--agent claude,codex,opencode` or `--agent all` chooses explicitly. Setup only runs when you run it.

- **Idempotent.** Rerunning changes nothing. If whatsapp-axi moved, the existing hooks' path is repaired in
  place; a start-only install from an older version gains the session-end hook. The hook runs the bare name `whatsapp-axi` when that is the binary on `PATH`, otherwise the full path.
- **Your settings stay yours.** In Claude Code and Codex settings, only whatsapp-axi's
  own hook entries are added, replaced or removed; every other byte of the file (other
  settings, other hooks, key order, indentation) is kept. `--remove` takes out the
  hook entries and any `hooks` container they leave empty, and nothing else.
- **Safe writes.** Files are written through a temporary file and a rename, with mode `600`; a symlinked
  settings file is edited at its target.
- **Refuses what it cannot parse.** A Claude Code or Codex hook settings file that
  is not valid JSON (comments, trailing commas) or has an unexpected shape is never
  rewritten: setup exits 1 with `code: config_invalid`, names the line and column,
  and writes nothing for any agent.
- **Codex** runs hooks only with `[features] hooks = true` in `~/.codex/config.toml`; `setup hooks` adds
  that line (and leaves it on after `--remove`, since other hooks may need it).
  Setup validates TOML before editing and refuses representations it cannot
  preserve safely, including inline or quoted feature definitions that need
  changing. It returns `config_invalid` before writing any agent's files; enable
  `features.hooks` manually and rerun setup in that case.
- **Session-end capture.** When a session ends, the `SessionEnd` hook runs `whatsapp-axi session-end`
  (Claude Code and Codex; OpenCode below). It reads the transcript the hook names, looks only at the
  commands the agent ran (never tool output or messages), and counts how often each whatsapp-axi command
  name ran, such as `messages 3, reply 1`. It stores that, per working directory, in `last-session.json`
  in the whatsapp-axi config directory (mode `600`, at most 32 directories, no paths: the directory is
  kept as a hash). The next `whatsapp-axi --brief` in that directory adds one line,
  `last_session: 2026-10-27, 5 commands: messages 3, search 2`. Arguments, chat names, message text,
  phone numbers and the transcript itself are never stored or printed, and a session that ran no
  whatsapp-axi command records nothing. The command does not talk to wasync, and `setup hooks --remove`
  stops new records (an existing `last-session.json` stays; delete it to clear it).
- **OpenCode** uses its native system-context transform hook, calling the brief
  view directly without a shell and with a timeout. For session-end capture the same plugin
  notes the command names of the session's bash calls (never arguments) and passes only those names to
  `whatsapp-axi session-end` when OpenCode reports the session idle or deleted. It caches the brief by session,
  retaining up to 128 sessions. It merges context into the existing system
  block, avoids duplicate injection, and reports short repair guidance on failure.
  Project plugins live in `.opencode/plugins/`; global plugins live in
  `$XDG_CONFIG_HOME/opencode/plugins/` when `XDG_CONFIG_HOME` is absolute, otherwise
  `~/.config/opencode/plugins/`. Skills use the same config root. Setting
  `WHATSAPP_AXI_AGENT_HOME` overrides XDG and uses `.config/opencode/` under that
  synthetic home. Setup owns only
  `whatsapp-axi.js`; another file at that path is never overwritten or removed.
- `doctor` reports both under `hooks` (the session hooks count as installed only when both are present and
  current): installed, missing, pointing at another binary, or unreadable, each
  with the `whatsapp-axi setup ...` command that fixes it.

The skill tells agents about the security model above and the explicit checkpoint
commits described in [Following changes](#following-changes). The committed skill
is generated from CLI metadata and shared static home-view guidance; CI rejects
drift. See [Adding a command](CONTRIBUTING.md#adding-a-command) for regeneration
and [Checks](CONTRIBUTING.md#checks) for the drift check.

`setup` never talks to wasync, so it has no entry in the compatibility table
(`internal/compat/compatibility.json`) and works whether or not wasync is installed.

## Benchmark

The [synthetic benchmark](docs/benchmark.md) compares agent calls and output size
with raw `wasync query --v1 --json`, including measured results, token estimates
and limits.

## License

MIT. See [LICENSE](LICENSE).
