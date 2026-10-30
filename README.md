# wasync

A personal, local WhatsApp chat archive with a **full-screen terminal UI**,
written in Go using [whatsmeow](https://github.com/tulir/whatsmeow) and
[Bubble Tea](https://github.com/charmbracelet/bubbletea).

The executable is named **`wasync`**: launch it for the terminal UI, or use
**`wasync query …`** for read-only JSON queries from local agents. Sending is
separate and always explicit: `send`, `reply`, `react` and `edit`.

This monorepo also contains **whatsapp-axi**, the agent-facing CLI in
[`axi/`](axi/). Both binaries, their documentation, release automation, and
Homebrew definitions are maintained here. wasync owns the WhatsApp connection
and archive; AXI reads it through the local API or `wasync query`. Reading is open to
any credential with a read scope. Replies, reactions and edits need a scope a person
granted, per chat; AXI never deletes anything.

For development see [CONTRIBUTING.md](CONTRIBUTING.md). For releases see
[docs/releasing.md](docs/releasing.md); for the one-time tap migration see
[Homebrew installation](#homebrew-installation).

## Let your agent set this up

If you use the **Codex desktop app** on macOS or Windows, you can ask Codex to install wasync
and set up WhatsApp for you. Paste this into a Codex conversation:

> Set up WhatsApp for me with wasync from https://github.com/gehariharan/wasync. Follow the
> "Let your agent set this up" section of its README: install wasync and whatsapp-axi on this
> computer, add the WhatsApp plugin, then start the setup page. Tell me whenever I need to do
> something on my own computer.

What Codex runs for you, and why it will ask your permission first (Codex's default settings
block the network and writing outside your project, so it must ask before each command):

| Computer | Install (no administrator password) | Add the plugin |
| --- | --- | --- |
| macOS | `brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git`, then `brew trust --cask gehariharan/tap/wasync` if your Homebrew has `brew trust`, then `brew install --cask gehariharan/tap/wasync` and `brew install gehariharan/tap/whatsapp-axi` | `codex plugin marketplace add gehariharan/wasync`, then `codex plugin add whatsapp@wasync` |
| Windows | `Invoke-WebRequest https://raw.githubusercontent.com/gehariharan/wasync/main/scripts/install-windows.ps1 -OutFile $env:TEMP\install-wasync.ps1` then `powershell -ExecutionPolicy Bypass -File $env:TEMP\install-wasync.ps1` | the same two `codex plugin` commands |

The Windows script finds the newest release, checks the download against the release's
published SHA-256 checksum, and runs Setup quietly for your account with the PATH entry and
`whatsapp-axi` (`-NoAxi` leaves `whatsapp-axi` out, `-DryRun` shows what it would do). It
changes no agent settings. Homebrew's `whatsapp-axi` builds from source, so that step takes a
few minutes.

After installing, **fully quit and reopen the Codex app** so it sees the new PATH. Then ask
Codex to "set up WhatsApp". It opens the setup page; the steps only you can do are:

1. scan the QR code with WhatsApp on your phone (*Linked devices → Link a device*);
2. choose which chats wasync may keep;
3. press **Connect** for the agent and confirm with Touch ID, your password or Windows Hello;
4. later, approve each chat Codex asks for, the same way.

No tool Codex has can approve anything, create a credential or read one (see
[ADR-003](docs/architecture/ADR-003-mcp-tool-server.md)). If Codex says it has no WhatsApp tools
after the restart, the plugin could not start `whatsapp-axi`: check `whatsapp-axi --version` in
a terminal and that the Codex app was started after the install.

> **Windows:** continuous integration runs the installer, the `whatsapp-axi mcp` tool server
> and `wasync serve` on a Windows runner, but the Codex desktop app, Windows Hello, the
> password prompt and the setup page in a real browser have not yet been exercised on a real
> Windows PC by the author.

## Install a prebuilt package

End users do not need Go, Xcode Command Line Tools, or a C compiler. The native
release workflow produces these downloads:

| Platform | Download | Installation |
| --- | --- | --- |
| macOS 15+, Apple Silicon | `wasync_VERSION_darwin_arm64.pkg` | Open the package and follow Installer |
| macOS 15+, Intel | `wasync_VERSION_darwin_amd64.pkg` | Open the package and follow Installer |
| Linux x86-64, glibc 2.35+ | `wasync_VERSION_linux_amd64.tar.gz` | Extract and run `./install.sh` |
| Windows 10/11 x64 | `wasync_VERSION_windows_amd64-setup.exe` or `.zip` | Per-user Setup or portable ZIP |
| Linux ARM64, glibc 2.35+ | `wasync_VERSION_linux_arm64.tar.gz` | Extract and run `./install.sh` |

### Choose whether to add AXI for agents

wasync is the app and engine for people. **`whatsapp-axi`** ("AXI for agents") is a
separate, small command that AI agents such as Claude Code, Codex and OpenCode use
to work with your local wasync archive. You do not need it to use wasync, so every
installer asks for it as a choice, off unless you say yes. Both programs always come
from the same release and have the same version.

| Where you install from | How you choose AXI for agents |
| --- | --- |
| macOS `.pkg` | On the installer's **Customize** step, tick **AXI for agents (whatsapp-axi)**. It starts unticked. |
| Linux archive | `./install.sh` asks and the default is no. Skip the question with `--with-axi` or `--without-axi`. |
| Windows Setup | Tick **Also install whatsapp-axi** on the tasks page (unticked by default). For a silent install add `/MERGETASKS="installaxi"`. |
| Windows ZIP | The ZIP already contains `whatsapp-axi.exe` next to `wasync.exe`; extracting installs and configures nothing. |
| Homebrew | `brew install gehariharan/tap/whatsapp-axi` (see [Homebrew installation](#homebrew-installation)). |
| Microsoft Store | Not offered: the Store edition is wasync only (see [the MSIX section](#microsoft-store-msix-distribution)). |

Installing AXI for agents never changes your agents' settings. To let your agents
learn about it, you run (or accept an offer to run) these two commands; add
`--dry-run` to either to preview what it would write:

```sh
whatsapp-axi setup skill   # adds the whatsapp-axi skill to your agents
whatsapp-axi setup hooks   # adds whatsapp-axi's session hooks to your agents
```

The Linux script and Windows Setup offer these as separate steps, each off by default
(Linux: `--axi-skill` and `--axi-hooks` say yes without asking; Windows: two unticked
boxes on the last page, never run by a silent install). The macOS installer cannot run
commands as you, so it only reminds you on its last page.

The release workflow builds native Intel/Apple Silicon macOS packages and
Linux and Windows packages. Manual builds can remain drafts; publishing also updates the
Homebrew tap and runs the [installation checks](docs/releasing.md#validation). Downloadable
versions appear on the
[GitHub Releases page](https://github.com/gehariharan/wasync/releases).
Source code and prebuilt downloads are public. No GitHub account is needed to install.

On macOS the package installs `/usr/local/bin/wasync` and dependency notices under
`/usr/local/share/wasync`; if you tick **AXI for agents** it also installs
`/usr/local/bin/whatsapp-axi`. It asks for administrator approval. After installation,
open Terminal and run:

```sh
wasync version
wasync
# From another terminal, for agents:
wasync query groups --enabled
```

If an earlier manual or Homebrew installation is still on PATH, check
`command -v wasync`. This package installs `/usr/local/bin/wasync`; use that
explicit path to verify it, and keep only one installation method active.

Starting with `0.1.0-preview.5`, macOS executables use Developer ID Application
signatures and Apple notarization. Installers use Developer ID Installer
signatures and include validated stapled notarization tickets. Older previews
were unsigned/ad hoc. No installer removes quarantine or bypasses Gatekeeper.

Linux users can extract the matching archive and install without administrator
access. The archive holds `wasync`, `whatsapp-axi` and `install.sh`; extracting it
installs nothing:

```sh
tar -xzf wasync_VERSION_linux_amd64.tar.gz
./install.sh                # installs wasync to ~/.local/bin; asks about AXI for agents (default no)
./install.sh --with-axi     # or: install both without being asked
./install.sh --without-axi  # or: install wasync only without being asked
export PATH="$HOME/.local/bin:$PATH"
wasync version
```

`install.sh --bin-dir DIR` installs somewhere else. Without a terminal and without
`--with-axi`, the script installs wasync only. To install by hand instead, copy
`wasync` (and, if you want it, `whatsapp-axi`) with `install -m 755` into a directory on
your PATH.

The `checksums.txt` release asset contains SHA-256 hashes of each download. On
Linux use `sha256sum --ignore-missing -c checksums.txt` in the download directory;
on macOS compare `shasum -a 256 FILE.pkg` with its line in `checksums.txt`.
Quit capture before upgrading. Installers only replace program files; archives,
allowlists and pairing state remain in the per-user data directory. The package
rejects the wrong CPU architecture, unsupported macOS versions and an existing
`/usr/local/bin/wasync` symlink owned by another installation method.

### Homebrew installation

The source repository is also the Homebrew tap. Existing users should run the
custom-remote command below once to migrate from the former standalone tap.

Linux Homebrew installs both tools from source:

```sh
brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git
# On Homebrew versions with tap trust, authorize the core dependency:
if brew help trust >/dev/null 2>&1; then brew trust --formula gehariharan/tap/wasync; fi
brew install gehariharan/tap/whatsapp-axi
```

On macOS, install the signed core cask below. AXI for agents is your choice there:
add it with `brew install gehariharan/tap/whatsapp-axi` (the cask prints this
reminder after installing). The standalone `whatsapp-axi` formula stays separate on
purpose, so the agent tool can be installed, upgraded or removed on its own. The
combined release's core source formula is Linux-only. The source formulas use Homebrew's Go and allow
Go to download the project-selected toolchain when needed.

Each release generates `wasync.rb`, a Homebrew **cask** that installs the
prebuilt native macOS executable and verifies its archive checksum. It performs
no source build. The public tap downloads signed release assets without GitHub
authentication:

```sh
brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git
# On Homebrew versions with tap trust:
brew trust --cask gehariharan/tap/wasync
brew install --cask gehariharan/tap/wasync
```

After migrating the tap, upgrade an existing Mac installation with:

```sh
brew update
brew upgrade --cask gehariharan/tap/wasync
wasync version
```

If AXI is installed, upgrade it with `brew upgrade gehariharan/tap/whatsapp-axi`.
Homebrew upgrades preserve the local archive, capture preferences and pairing.

Releases from `0.1.0-preview.5` onward are Developer ID signed and notarized.
Homebrew's quarantine behavior is preserved; no cask hook disables Gatekeeper.
The repository is public. Existing manual launchers may
precede the Homebrew binary; check `command -v wasync` and use
`/usr/local/bin/wasync` on Intel or `/opt/homebrew/bin/wasync` on Apple Silicon to
select the installed release explicitly.

## Set up an agent, start to finish

This is the whole first-run path for letting an agent such as Claude Code read
(and, if you choose, reply in) your chats. No step needs a chat ID or a hand-made
credential file. To do the pairing and the agent connection from a browser page
instead of a terminal, see [Setting up without a terminal](#setting-up-without-a-terminal).

1. **Install** wasync and, when asked, **AXI for agents** (see
   [Install a prebuilt package](#install-a-prebuilt-package)).
2. **Pair your phone.** Run `wasync` and scan the QR code, then let the first
   sync finish. Leave wasync running (the TUI, or `wasync serve` for a headless
   service) whenever an agent should use it.
3. **Create the agent's credential.** In a terminal, pick any name:

   ```sh
   wasync agents grant helper --scope read.chats,read.messages,observe,send,react,edit.own --on-demand
   ```

   `--on-demand` starts the agent with no chats at all; it asks for each chat by
   name and you decide. Type the agent's name to confirm. wasync then asks
   **"Save this credential for whatsapp-axi now, at …? [Y/n]"**. Press Enter: it writes
   the credential into whatsapp-axi's private credential file (owner-only, in
   whatsapp-axi's config folder) and does not print it. If you say no, it prints
   the credential once so you can put it in `WHATSAPP_AXI_TOKEN` or your agent's
   own secret store. wasync never replaces a different saved credential without
   asking.
4. **Check the agent can reach wasync:** `whatsapp-axi doctor`.
5. **The agent asks for a chat by name**, for example
   `whatsapp-axi access request --match "Family" --send`. It is told the request is
   pending whatever matched.
6. **You approve it.** Open the request in the TUI, or run `wasync actions list`
   and `wasync actions approve REQUEST_ID`. You choose the chat when several
   match, the history depth, and see whether it may only read or also send. Only
   then can the agent use that chat.

If you would rather decide up front, `--chat "Family"` takes a chat name from
your archive instead of a raw ID: wasync shows the matching chat with its ID and
asks you to confirm, and refuses ambiguous or unknown names. The two options
combine. Revoke an agent any time with `wasync agents revoke helper`.

## Build and launch (development)

Requires macOS, Linux, or Windows x64, Go 1.27.1 or newer (the project selects Go 1.27.2), and a
C compiler. On macOS, install the Xcode Command Line Tools if needed.

```sh
git clone https://github.com/gehariharan/wasync.git
cd wasync
go build -tags sqlite_fts5 -o bin/wasync ./cmd/wasync
./bin/wasync
```

**Launching without a command opens the TUI and starts capture for groups you
already enabled.** Your Phase 1 session and group preferences are reused. The
archive migrates automatically; do not delete your existing data directory.

If no session exists, pairing happens inside the TUI. Open WhatsApp on your phone
and select **Linked Devices → Link a Device**. Resize your terminal if the QR code
needs more space. After authentication, groups are discovered with capture
disabled by default. Enable the groups you want using Space. Enabling automatically
queues a request for older messages from your phone.

Keep the TUI or `wasync serve` running while you want to capture messages. Quitting
stops capture and the local API, and leaves linked-device credentials available
for the next launch. The TUI and headless server expose an authenticated loopback
HTTP API by default.

## TUI controls

| Key | Action |
| --- | --- |
| ↑ / ↓ or j / k | Move through groups or messages |
| Mouse wheel / click | Scroll / select a row; click a group checkbox to toggle capture |
| PgUp / PgDn, Home / End | Jump through the list |
| Space | Enable or disable capture for the selected group |
| Enter | Open the group's archive; in messages, expand the full message |
| / | Filter groups; in the archive, search message text |
| e | Show enabled groups only |
| n / p or → / ← | Older / newer message page |
| R | Return to the latest message page |
| Ctrl+D / Ctrl+U | Scroll the selected message body |
| ↑ / ↓ in expanded message | Scroll the full message body |
| r | Refresh group discovery |
| h | Queue/retry older history for the selected/open group |
| c | Connect or retry pairing |
| D | Disable and purge the selected group's archive, after confirmation |
| M | Choose how agents get chats: ask me each time, or sync all chats (shown once after pairing) |
| L | Unlink the device and quit, after confirmation |
| Esc | Close details, return to groups, or clear the group filter |
| ? | Show keyboard help |
| q or Ctrl+C | Quit; keep the device linked |

Group details show archived counts, last message time, observed history state and
the potential gaps while this chat was being captured (the account's stretches of lost
observation that overlap the time capture has been on for the chat, so chats turned on at
different times differ; 0 while capture is off). Enabled groups appear first. The selected group stays
selected when a refresh changes the ordering. The message list uses keyset
pagination; new arrivals do not shift an older page while you browse it. Search
uses a literal FTS5 phrase and is scoped to the open group.

The latest archive page refreshes while you browse it. Older pages stay anchored.
Full message detail works in narrow terminals through Enter. Untrusted group
names, sender labels and message bodies are escaped before terminal rendering.

Browse the archive without connecting:

```sh
./bin/wasync tui --offline
```

`wasync tui` and `wasync run` both open the live TUI. Fullscreen operation has no
three-minute command timeout. The `--timeout` setting still applies to the
scriptable diagnostic commands below.

## Automatic historical download

Whenever you enable a group, wasync automatically queues its historical download.
Groups already enabled when upgrading are queued on the next launch too.

1. Quit the running older binary with **q**, rebuild, and launch `./bin/wasync`.
2. Keep the group enabled and the TUI online. Keep your phone connected to the
   internet so it can respond; opening WhatsApp may help it process the request.
3. Watch **Capture details** for `Older history queued`, `Requesting 50 older
   messages`, and `Waiting for history from your phone`.
4. Received messages enter the archive automatically. The worker asks for the next
   50 older messages while the returned cursor advances. It sends one request at a
   time, with a pause between pages.
5. Press **h** on the group, or while browsing its messages, to queue a retry after
   a timeout or failure. Do not confuse **n/p**, which browse local pages, with
   **h**, which asks the phone for more history.

The request requires a genuine known message ID and timestamp. Wasync uses its
oldest local message, or a metadata-only cursor previously supplied by WhatsApp. For an empty group with no known cursor, the UI says **Waiting for a
known message**. Once a new message is observed in that group, historical requests
start automatically. A single newly captured message is enough to bootstrap the
first request.

Only message ID, group ID, direction and timestamp cursor metadata may be kept
in the local cursor table for unselected groups; their message bodies are never
archived. These cursors survive restarts. Selected groups' request cursors and job
states are also persisted so pagination can resume after restarting.

Disabling a group stops further requests and ignores its content on delivery.
Empty/repeated pages stop the attempt as **No older page returned (coverage
unknown)**. Failed sends and a two-minute response timeout are shown separately;
no response is not proof of an empty or complete history. Requests are protocol
messages addressed to your own linked device, not chat messages posted to the
selected group.

## What is captured

Only explicitly enabled groups are archived. Disabling a group takes effect at
the database transaction boundary and retains existing records. Purge disables
capture and removes the group's stored records and search entries.

The archive captures text, extended text, captions, sender/group/message IDs,
original timestamps, whether the message was sent by you, reply IDs and safe
media metadata. By default it does not download attachment files. Optional media downloads are
described below. It never stores thumbnails, quoted message bodies, download URLs
or media decryption keys in the archive. Reactions retain emoji, target message identity, sender timestamp and removal
events. Unsupported types retain minimal type metadata. Reaction records are
observed events, not a reconstructed current reaction count.

Live and historical messages share the same allowlist and normalization rules.
Repeated deliveries are deduplicated by account, group and WhatsApp message ID.
Delivered edits update the current text. Delivered revokes clear the body and
retain tombstones. A revoke arriving before the original prevents a subsequent
history delivery from restoring the body; edits arriving before originals are
held as pending content until the original arrives. Pending edits expire after
30 days, while revocation tombstones are retained.

View-once content is excluded. Ephemeral text is kept only when a usable expiry
can be determined, hidden immediately after expiry and cleared on maintenance
(every ten seconds) or restart. Ephemeral edits without an absolute deadline are
skipped rather than extending their retention from the edit time. Retention does
not remove copies in backups or guarantee forensic erasure of old SQLite pages.

## History and recovery limits

Historical import is **best-effort**. Available history events are processed in
small transactions. Original timestamps are preserved; batch state and durable
chunk counts record progress. An interrupted batch is marked interrupted after
restart. Reported upstream progress, even 100%, does not mean the archive is
complete.

**The history observed during Phase 1 was discarded.** It cannot be imported from
those counters. The new capture path imports any history WhatsApp delivers again;
it neither assumes nor promises that the phone will replay the old batch.
Enabling requests history automatically, but a historical backfill is not guaranteed.

The pinned library handles ordinary network reconnects and the special reconnect
after pairing. Its ordinary retry delay is currently linear. A custom exponential
supervisor remains future hardening work. Reconnects and periods when wasync was
stopped create uncertain account intervals; ending an interval means observation
resumed, not that all missed messages were recovered. Decryption failures for an
enabled group also mark uncertainty.

Selected live messages are written synchronously before our handler reports
success; the library's synchronous acknowledgement option is enabled. A database
write failure stops capture and appears in the TUI. This reduces the time between
delivery and a durable commit, but does not guarantee complete delivery or replay
after every failure. The library's optional decrypted-event buffer is not enabled
because it could retain plaintext for unselected chats.

After a terminal session error, such as being unlinked from the phone, restart
wasync to establish a fresh client and re-pair if needed.

## Pull data into your own database

The archive is local SQLite. You can query it while wasync runs using a read-only
connection (macOS default path shown):

```sh
sqlite3 -readonly "$HOME/Library/Application Support/wasync/archive.db" \
  'SELECT account_id, group_id, message_id, text_content FROM archived_messages LIMIT 20;'
```

A stable view exposes current records:

```sql
SELECT account_id, group_id, message_id, sender_id, timestamp,
       message_type, text_content, edited_at, deleted_at, received_at
FROM archived_messages
ORDER BY timestamp, id;
```

Timestamps are UTC Unix milliseconds. `archived_messages` excludes unmaterialized
pending edits and expired messages. Revoked messages retain their identities and
have empty bodies. Filter by `account_id` and `group_id`; read the database in
read-only mode and keep read transactions short. Never modify schema or archive
tables from external applications.

For a separate DB, use a consistent snapshot and upsert by
`(account_id, group_id, message_id)`, with deletion-aware snapshot reconciliation.
A message-time cursor alone misses edits, late history and purges. A transactional
replication change feed remains later work. The local REST API below provides
read access to the same archive.

## Agent CLI queries

**`wasync` opens the TUI; `wasync query …` reads the archive and exits.** The
examples below use the repository executable; if it is installed on your PATH,
use `wasync` in place of `./bin/wasync`. To install the built executable for
commands from any directory:

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 bin/wasync "$HOME/.local/bin/wasync"
export PATH="$HOME/.local/bin:$PATH"
```

Add the PATH export to your shell configuration if needed. Reinstall the binary
after rebuilding.

Agents can discover every command without opening the archive or connecting to
WhatsApp. Help is written to stdout and exits successfully:

```sh
wasync help --json                # versioned command catalog, usage and notes
wasync help query messages        # detailed help for any command path
wasync media policy --help        # equivalent command-specific help
wasync help                      # global flags and command overview
```

Global flags such as `--data-dir` and `--timeout` must precede the command.
`query` commands are read-only JSON interfaces; `send`, `reply`, `react` and `edit`
explicitly send a message, reaction or edit,
and `clean` / `delete` only queue requests requiring a human's interactive approval.
Per-chat `media policy` is persistent; `media download` requests one attachment
without changing that policy.

Keep the TUI capturing in one terminal and query from another:

```sh
./bin/wasync query groups --enabled
./bin/wasync query messages --group '123456789@g.us' --limit 50
./bin/wasync query search "meeting" --group '123456789@g.us'
./bin/wasync query messages --group '123456789@g.us' \
  --since 2026-10-01T00:00:00Z --until 2026-10-02T00:00:00Z \
  --sender '123456789@lid'
./bin/wasync query status
./bin/wasync query --help
```

Newer queries emit the versioned v1 envelope (`--v1` opts existing queries in):
`query version`, `query message --ref REF`, `query thread --ref REF [--before N]
[--after N]`, `query chats --v1 --match QUERY`, `query search PHRASE --v1
--context N`, `query changes [--after CURSOR] [--limit N]`, `query checkpoint
--consumer NAME` and `query doctor`. Scoped API credentials for agents are
managed with `wasync agents grant|list|revoke`, which a human must run in an
interactive terminal (no `--yes`); `--on-demand` lets an agent ask for chats
that you approve one at a time, see
[just-in-time access](#letting-an-agent-ask-for-chats-just-in-time-access).
`wasync help COMMAND` documents each one.

All query results are JSON on stdout by default; `--json` is also accepted for
explicit agent invocations. Diagnostics go to stderr. Exit codes are 0 for
success (including empty results), 1 for errors and 130 for cancellation. For v1
envelopes and matching totals, see the [interface contract](docs/architecture/axi-contract.md#response-envelope-v1-only).
Legacy group results contain `account_id` and `items`. Legacy message/search
results also contain `next_cursor`. Pass a nonempty cursor as `--cursor 'VALUE'` with the same filters
to retrieve the next page. Page size is 1–200, default 50; newest messages come
first, with stable ordering for equal timestamps. Cursors for the CLI and API
are separate formats. CLI cursors contain no secrets and are scoped to the
current account and filters; they are not an authorization mechanism.

Search uses literal phrase matching with SQLite FTS5. `--group` is required for
`messages` and optional for `search`. `--since` is inclusive and `--until` is
exclusive; timestamps are RFC3339. Query output uses the same message and group
fields as the API. Expired and pending messages are excluded; revoked messages
have empty bodies. Disabled groups retain query access to their existing archive.

Queries open the existing archive in SQLite read-only mode, using normal WAL
coordination to see ongoing commits. They acquire no device lock, open no
WhatsApp connection, require no API/token, run no migrations, and create no
credentials. Missing or incompatible archives produce an actionable error;
start the appropriate version of the TUI to create or migrate the archive.
`--data-dir` and `WASYNC_DATA_DIR` select the same archive as other commands:

```sh
./bin/wasync --data-dir /absolute/path/to/private/wasync query groups
```

Each group in `query groups` carries `potential_account_gaps` (every gap the account has
had, the same for all chats) and `potential_chat_gaps` (only those that overlap the time
capture has been on for that chat). `query status` reports persisted archive counts,
capture timestamps, history observations and potential gaps. Its `connection` is always `unknown`: an
unfinished capture run or recent heartbeat cannot prove that capture is currently
running or connected. Querying works with capture stopped, but the archive only
updates while capture is active. Pagination reads the live archive rather than a
frozen snapshot and is not a complete replication feed.

To use the TUI and agent queries without an HTTP listener:

```sh
./bin/wasync --no-api
# In another terminal:
./bin/wasync query groups --enabled
```

## Webhooks

Push a small notification to your own HTTPS service whenever the archive changes,
instead of polling the change feed. Webhooks are off until a human registers an
endpoint, and they never carry message text or media.

```sh
wasync webhooks add crm --url https://hooks.example.com/wasync --chat 123456789-123@g.us
wasync webhooks test crm        # signed ping; prints the HTTP outcome
wasync webhooks list
wasync webhooks deadletter crm  # events that could not be delivered
wasync webhooks remove crm
```

`add` prints a signing secret (`whsec_...`) once and asks you to type the endpoint
name; `list`, `remove`, `test` and `deadletter` need an interactive terminal too.
There is no `--yes`, no agent scope for webhooks and no API route that manages
them. The secret is also stored in `webhooks.json`, readable only by your OS
user, because HMAC signing needs the raw key. Delivery runs inside the owner
(`tui` or `serve`), picks up changes to `webhooks.json` within seconds, and stops
when the owner stops.

Each event is one JSON POST built from a change-journal entry:

```json
{"id":"evt_9cc5fde8_32996e716007dc74d42fb048efd85e96_42","type":"change",
 "endpoint":"9cc5fde8","epoch":"32996e71...","sequence":42,"cursor":"c1_...",
 "op":"message.created","op_version":1,"chat_id":"123456789-123@g.us",
 "message_id":"3EB0...","ref":"m1_...","committed_at":"2026-10-20T12:00:00Z"}
```

The receiver reads the message itself with an agent credential that has
`read.messages` (`GET /api/v1/messages/{ref}`); the message may already be gone.
Chat IDs contain phone numbers or group IDs, so use `--chat` (repeatable) and
`--op` (for example `message.created`) to limit what an endpoint sees.

Verify every request: `Wasync-Webhook-Signature` is `v1=` plus the lowercase hex
HMAC-SHA256, keyed with the secret, of the `Wasync-Webhook-Timestamp` value (Unix
seconds), a `.`, and the raw request body. Reject timestamps more than five
minutes from your clock and compare signatures in constant time. Every attempt is
signed afresh, so a captured request cannot be replayed later.

```python
import hmac, hashlib, time
def valid(secret, timestamp, signature, body: bytes) -> bool:
    if abs(time.time() - int(timestamp)) > 300:
        return False
    want = "v1=" + hmac.new(secret.encode(), timestamp.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, signature)
```

Delivery is at-least-once and in journal order per endpoint. Dedupe on the event
`id` (also sent as `Wasync-Webhook-Id`); it is stable across retries and restarts.
Answer with any 2xx to acknowledge. A 5xx, 408, 429 or network error is retried
after 5 seconds, 30 seconds, 2 minutes and 10 minutes; any other answer, or the
fifth failure, moves the event to the dead-letter list and delivery continues
with the next event. If the dead-letter file cannot be written, the event is held
and retried every 10 minutes rather than dropped. Endpoints are independent, so a failing one does not delay
another. A new endpoint receives changes from the moment the owner first sees it,
not history. If the archive is restored from a backup or the journal position
becomes unreadable, the endpoint skips to the journal head and records
`webhook.cursor_reset` in `agent-audit.log`; use the changes feed to resnapshot.

Destinations are restricted to the public internet. The URL must be `https` with
a hostname (no credentials, fragment or local names). Whenever wasync connects it
resolves the name once, refuses the whole answer if any address is loopback,
private, link-local (including the cloud metadata address), carrier-grade NAT,
multicast, reserved or an IPv4-embedding IPv6 range, and connects to the address
it checked, so DNS rebinding cannot redirect a request. Redirects are never
followed and proxy environment variables are ignored. Certificates must verify
(TLS 1.2 or later). Keep your receiver reachable from the internet or use a
tunnel; webhooks to `localhost` are intentionally unsupported.

Deliveries, retries, dead letters and endpoint changes are written to
`agent-audit.log` with identifiers and outcomes only. The dead-letter list
(`webhook-deadletter.jsonl`) holds the same kind of entries. Neither file ever
contains message text or secrets.

## Local REST API

Restart the rebuilt executable to enable the API alongside the TUI:

```sh
./bin/wasync
```

The default address is **http://127.0.0.1:8787**. The TUI header shows the actual
address. Use the numeric address as shown; other Host headers are rejected.
All `/api/*` routes require a bearer token. `/health` is a content-free readiness
check and does not require authentication. The token is generated on first API
startup and reused across restarts, with file permissions 0600.

In another terminal on macOS:

```sh
WASYNC_TOKEN=$(cat "$HOME/Library/Application Support/wasync/api.token")
curl --fail -H "Authorization: Bearer $WASYNC_TOKEN" \
  http://127.0.0.1:8787/api/status
curl --fail -H "Authorization: Bearer $WASYNC_TOKEN" \
  http://127.0.0.1:8787/api/groups
curl --fail -H "Authorization: Bearer $WASYNC_TOKEN" \
  'http://127.0.0.1:8787/api/groups/123456789@g.us/messages?limit=50'
curl --fail -G -H "Authorization: Bearer $WASYNC_TOKEN" \
  --data-urlencode 'q=meeting' \
  http://127.0.0.1:8787/api/messages/search
```

Replace the group ID with one from `/api/groups`. On Linux or with a custom data
directory, read `api.token` from that directory instead. Never put the token in a
URL, commit it, or share it. To rotate it, stop wasync, remove only `api.token`,
and restart. Existing cursors become invalid after rotation.

| Method | Route | Result |
| --- | --- | --- |
| GET | `/health` | Archive readiness (not WhatsApp connectivity) |
| GET | `/api/status` | Connection, counts, attention flag and history uncertainty |
| GET | `/api/groups` | Current account's groups and capture/history state |
| GET | `/api/groups/{id}` | One group |
| POST | `/api/groups/{id}/enable` | Enable capture and queue available history |
| POST | `/api/groups/{id}/disable` | Stop capture; retain archived messages |
| POST | `/api/groups/{id}/history` | Retry best-effort history for an enabled group |
| GET | `/api/groups/{id}/messages` | Newest messages first, with pagination |
| GET | `/api/messages/search?q=keyword` | Literal phrase search across the current account |
| GET | `/api/messages/{id}` | One message by its local numeric row ID |

Message list and search responses contain `account_id`, `items` and
`next_cursor`. Pass the returned cursor as `cursor` to retrieve older messages;
an empty cursor means the end of currently stored results. The cursor is signed
and tied to the account and filters. Keep filters unchanged between pages.
Page size defaults to 50; `limit` accepts 1–200.

Both query routes accept `sender_id`, `from` (inclusive), and `to` (exclusive).
Dates use RFC3339, for example `2026-10-01T00:00:00Z`; URL-encode timezone `+`
signs. Search also accepts `group_id`. Response timestamps are RFC3339 UTC;
SQLite timestamps remain Unix milliseconds. Search uses SQLite FTS5. Expired
messages are excluded and revoked messages have empty bodies, just as in the TUI.
Disabling capture preserves the archive and query access. Reads are scoped to the
current account; pairing another account does not expose the old archive via API.

Query results are live archive reads. Pagination is not a frozen snapshot;
new history, edits, expiry and purge can change results. This is not a complete
replication feed. For another database, use snapshot reconciliation and the
composite identity described above; do not use message timestamps as a change log.

### Headless mode and configuration

Quit the TUI before starting another process for the same data directory:

```sh
./bin/wasync serve                    # Capture + API; requires an existing paired session
./bin/wasync serve --offline          # API for existing archive, no WhatsApp connection
./bin/wasync --api-addr 127.0.0.1:8788 # TUI + API on another local port
./bin/wasync --no-api                 # TUI with the API disabled
```

`serve` runs until Ctrl-C or SIGTERM and shuts down the API before closing the
archive. `--timeout` does not limit service lifetime. `WASYNC_API_ADDR` supplies
the default bind address; `--api-addr` overrides it. Only literal IPv4/IPv6
loopback addresses are accepted. API responses use `Cache-Control: no-store`;
foreign browser origins are rejected and no CORS access is enabled. Keep the API
local for this MVP. All routes share the running process's archive and device
owner, without creating a second WhatsApp session.

## Files, configuration and privacy

All commands use the same private data directory, normally:

- macOS: `~/Library/Application Support/wasync`
- Linux: `$XDG_CONFIG_HOME/wasync` or `~/.config/wasync`
- Windows EXE/ZIP: `%APPDATA%\wasync`
- Windows Microsoft Store build: `%USERPROFILE%\.wasync` (see
  [Store archive location and migration](#store-archive-location-and-migration))

Override it using `WASYNC_DATA_DIR` or the global `--data-dir` flag; flags win.
Global flags precede the command.

```sh
./bin/wasync --data-dir "$PWD/.local/wasync"
WASYNC_DATA_DIR="$PWD/.local/wasync" ./bin/wasync
```

The directory is created with 0700 permissions, and managed files with 0600.
Shared existing directories and symlink data files are rejected. Use a dedicated
location owned by your OS user with trusted parent directories. No credentials,
QR payloads or message bodies are written to logs. Filesystem permissions do not
encrypt data at rest.

| File | Purpose |
| --- | --- |
| `session.db` | Whatsmeow credentials and protocol state; protect as a secret |
| `archive.db` | Accounts, groups, messages, FTS5, sync progress, history jobs and gap records |
| `api.token` | Archive API bearer token; keep private |
| `send.token` | Separate send/action-request credential; keep private |
| `webhooks.json` | Webhook endpoints and their signing secrets; keep private |
| `webhook-state.json` | Per-endpoint delivery position (journal sequence and retry timing) |
| `webhook-deadletter.jsonl` | Events webhooks gave up on: identifiers and outcomes only |
| `wasync.log` | Structured lifecycle logs from TUI sessions |
| `device.lock` | Process ownership lock, retained after exit |
| `*-wal`, `*-shm` | SQLite WAL sidecars when present |

Only one wasync process owns a data directory. Manage groups in the running TUI
or through its API. `query` commands can read the archive concurrently; quit the
owner before using other CLI commands against that directory. The explicit
`send` command can reuse the running owner through its authenticated local API. The OS releases the
lock after crashes. Never delete the lock file to bypass an active process or run
copies of the same device credentials concurrently.

Both databases use WAL, foreign keys, a busy timeout and synchronous FULL. Stop
wasync before a file-copy backup or use SQLite's backup API. Copying only the live
`.db` file can omit WAL contents. Keep session files, archives and backups private
and outside Git.

## Scriptable diagnostics

These commands remain useful for setup and scripting. They do not start capture.
In an interactive terminal, plain `wasync login` is different: after pairing or
reusing the saved session it caches your groups and opens the TUI, which captures
enabled groups like running `wasync` with no command. `--select=false` or
`--observe` keep the diagnostic output below; without a terminal, `login` always
prints the group table and exits. Log lines go to `wasync.log` in the data
directory during an interactive login.

```sh
./bin/wasync login --select=false
./bin/wasync groups list
./bin/wasync groups list --refresh --json
./bin/wasync groups enable '123456789@g.us'
./bin/wasync groups disable '123456789@g.us'
./bin/wasync status
./bin/wasync --timeout 5m login --select=false --observe 60s
./bin/wasync logout
```

Cached list and preference commands open no WhatsApp connection. `--refresh`
connects only to refresh discovery. `status` inspects local state; it cannot tell
whether the saved device was revoked remotely. Observation counts all received
events but discards bodies. Logout asks WhatsApp to unlink the device, preserves
archived data, and reports a failed remote unlink as an error.

## Validation

```sh
go test -tags sqlite_fts5 ./...
go test -race -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
```

Tests use synthetic events and temporary files. They cover Phase 1 migration,
consent checks at commit time, live/history deduplication, account isolation,
original timestamps, out-of-order edits/revokes, search index updates, expiry,
purge, pagination, interrupted batch state, process locks, private files,
terminal controls, filtering, keyboard actions, narrow terminal rendering, automatic
history queueing, cursor encoding, response correlation, pagination, timeout and
cancellation behavior. API tests cover authentication, loopback binding, origin checks,
account isolation, signed pagination, date/sender filters, concurrent reads during
capture, edited/revoked/expired/purged content, and headless shutdown. CLI query
tests cover reads while the capture lock is held, active WAL visibility, pagination,
filters, account isolation, missing archives, read-only enforcement, schema checks,
and default TUI dispatch. History request tests use an injected sender; they do not
send protocol requests to a real account.

Manual account checks:

1. Launch wasync and confirm it reuses your paired session and enabled group.
2. Have someone send a text in an enabled and a disabled group. Only the enabled
   group's message should appear in the archive. No conversation messages are sent unless you explicitly invoke `wasync send`.
3. Toggle capture with Space; confirm subsequent messages follow the preference.
4. Browse with Enter, search with /, and scroll long messages with Enter then ↑/↓.
5. Restart and verify the archive and preferences persist without duplicate rows.
6. Interrupt and restore the network; verify the UI reports reconnection and
   uncertainty. History may or may not be delivered.
7. Test purge on disposable archived data; confirm the group is disabled and its
   messages disappear from browsing and search.

Real account delivery and historical completeness cannot be established by the
synthetic tests. Do not re-pair simply to obtain history without understanding
that replay is not guaranteed.

This is an unofficial WhatsApp client: protocol compatibility, WhatsApp terms and
account restriction risks apply. Obtain appropriate permission before archiving
or sharing conversations. wasync does post to conversations, but only when asked: `send`,
`reply`, `react` and `edit` act on your command, or on an agent credential you granted
the matching scope, and deletions are never sent automatically. There are no features
for cloud services, AI processing or an application web frontend. Attachment downloads
are opt-in.
The original design and risk review are saved in
[the HTML design artifact](.lavish/whatsapp-sync-design.html).

## Maintainer release packaging

See [the release guide](docs/releasing.md) for coordinated core/AXI releases,
local packaging, signing-runner operation, manual signing and publication safeguards.

## License

wasync is open source under the [MIT license](LICENSE). Dependencies retain
their own licenses; prebuilt archives and installers include third-party notices.


## Windows installer (preview)

The release workflow builds Windows 10/11 x64 using a native Windows runner,
CGO SQLite with FTS5, and a statically linked MinGW runtime. Downloads:

- `wasync_VERSION_windows_amd64-setup.exe`: per-user Inno Setup installer.
- `wasync_VERSION_windows_amd64.zip`: portable `wasync.exe`, `whatsapp-axi.exe` and license notices.

Run Setup, then open **wasync** from the Start menu or open a new terminal and
run `wasync`. The installer offers a user PATH entry and, as a separate unticked
task, **AXI for agents** (`whatsapp-axi.exe`; keep the PATH entry ticked so agents
can find it). If you pick it, the last page offers two more unticked boxes that run
`whatsapp-axi setup skill` and `whatsapp-axi setup hooks` as you; a silent install
never runs them. Silent examples:
`wasync_VERSION_windows_amd64-setup.exe /VERYSILENT /MERGETASKS="installaxi"` adds
AXI for agents, and omitting `installaxi` leaves it out. No administrator access is
needed. If you set up your agents, run `whatsapp-axi setup hooks --remove` and
`whatsapp-axi setup skill --remove` before uninstalling. Quit wasync before upgrading. Uninstall removes program files and its
PATH entry; it preserves `%APPDATA%\wasync`, including the archive and device
session. The portable ZIP uses the same data directory unless `--data-dir` is set.
Local data uses a private Windows ACL for your user and SYSTEM; concurrent capture
uses an exclusive Windows file lock. Native Windows ARM64 is not yet packaged.

**Windows previews are unsigned while Microsoft Artifact Signing is being set up.**
Signing for macOS remains unchanged. Windows release signing is a separate step
and is not yet configured; signing credentials are never bundled in downloads.

Maintainers can build using Go, Python 3, MinGW GCC and Inno Setup 6:

```powershell
$env:CC = 'C:/msys64/ucrt64/bin/gcc.exe'
$env:PATH = 'C:/msys64/ucrt64/bin;' + $env:PATH
./scripts/package-windows.ps1 -Version 0.1.0-preview.1
```

## Bulk selection, logout, and historical sync

- The top **Select all** / **None** controls support mouse clicks or **A** / **N**.
  Confirm the count before applying. They apply to the current category, ignoring
  name and enabled-only filters. The default Groups category affects groups only.
- **Tab** switches Groups / Direct / All. Direct chats require explicit selection.
- `wasync groups enable --all` and `wasync groups disable --all` provide the same
  operations from the CLI. Newly discovered groups remain disabled.
- **L**, then **y**, unlinks the device and exits; `wasync logout` does the same
  after quitting the TUI. **q** quits while keeping the pairing session.
- Enabling queues history immediately. A background worker checks the local queue
  every two seconds, requesting pages of 50 from the primary phone when online.
  Live messages arrive through WhatsApp events; chat contents are not polled.
- Metadata-only history anchors (group ID, message ID, timestamp and direction)
  now survive restarts, including for groups whose capture is disabled. Direct-chat anchors are persisted
  only after capture is enabled; unselected direct chats retain discovery metadata
  (chat ID/name) but no persistent per-message cursor. Message
  bodies from disabled groups are still discarded. Purging clears the stored
  anchor for that group; future WhatsApp deliveries may supply a new one.
- With no known message anchor, history waits for WhatsApp to supply one. Keep the
  primary phone online; **h** retries a failed history request. Neither enabling
  nor reconnecting guarantees complete history or recovery of downtime messages.

Schema version 4 adds cursor metadata; version 5 adds persistent media policies.
Start the updated TUI once to migrate an older archive before running read-only queries with the updated executable.
Group and direct chats share the archive while preserving legacy `group_id` keys
and group-only listing commands; use `chat_id` and the `chats` commands for both.


## Direct chats and explicit sending

Direct chats are discovered from WhatsApp history and live events; there is no
promise of a complete server-side inbox listing. Metadata is cached with capture
disabled. Phone-number JIDs (`...@s.whatsapp.net`) and privacy/LID JIDs (`...@lid`)
may represent the same person and are retained as supplied by WhatsApp; do not
assume they have already been merged.

A direct chat's name is the one WhatsApp would show: your address-book full name,
then first name, then the person's own profile (push) name, then a business name,
then the number. Names come from whatsmeow's contact store, which WhatsApp fills
through the linked-device contact sync, and from contact, push-name and
business-name events; `@lid` chats are looked up through whatsmeow's LID-to-phone
mapping. They are refreshed on every connect and when a contact changes (a failed
refresh never stops message capture; it is retried on the next such event), and appear
in `query chats` (including `--v1 --match`, which is case-insensitive and matches
part of a name), the terminal UI, the local API and `whatsapp-axi`. Names are
personal data: they are stored in the local archive only, never logged, and
`whatsapp-axi` prints them as untrusted. A contact you have not saved and who has
never messaged you keeps its number as its name.

```sh
wasync chats list
wasync chats enable '15551234567@s.whatsapp.net'
wasync query chats --enabled
wasync query messages --chat '15551234567@s.whatsapp.net'
wasync query search 'meeting' --chat '15551234567@s.whatsapp.net'

# Each invocation explicitly sends one conversation message.
wasync send --to '123456789@g.us' --text 'Hello'
wasync send --to '15551234567@s.whatsapp.net' --text 'https://example.com'
wasync send --to '123456789@g.us' --file ./photo.png --type image --text 'Caption'
wasync send --to '15551234567@s.whatsapp.net' --file ./report.pdf --type document
```

Sending supports text/URLs, JPEG/PNG images and documents. WhatsApp determines
which sizes and formats it accepts; wasync adds no fixed file-size cap. The default
attachment type is document. Rich link-preview generation is not implemented.
Output is JSON with `result.status` and `result.message_id`. A server acknowledgement
is not proof of delivery or reading. An unknown outcome must be checked in WhatsApp
before retrying. wasync does not add an application retry loop; whatsmeow may retry
the same protocol frame using its existing message ID.

When the TUI or service owns the data directory, `send` reuses its authenticated
local API. Supply the same `--api-addr` if you changed the default. With `--no-api`,
quit capture before sending. When no owner runs, `send` acquires the device lock,
reuses the paired session and closes it after the request. No test sends real
messages automatically. Read-only `query` commands never send messages.

### Replies

`wasync reply` quotes an archived message and can mention people. The target
must already be in the local archive of that chat (a disabled or purged chat
cannot be replied into), and it must not be deleted or a reaction.

```sh
wasync reply --chat '123456789@g.us' --message-id 'ABCDEF123' --text 'On my way'
wasync reply --chat '123456789@g.us' --message-id 'ABCDEF123' \
  --text 'Can you check this, @15551234567?' --mention '15551234567@s.whatsapp.net'
```

Each `--mention` (repeatable, up to 20) is a user JID, and the text must contain
`@NUMBER` for it, not followed by another letter or digit, so a mention is never
invisible. Like `send`, `reply` reuses a
running owner or connects with the saved session, sends exactly once and prints
`result.status`, `result.message_id` and `result.target_message_id` as JSON.
The owner route is the signed `POST /api/reply` with `{ "chat", "message_id",
"text", "mentions" }`, using the same `send.token` signature as `/api/send`.
Quoted text is copied into the reply only when the target is a text message.

Agents reply through `POST /api/v1/messages/{ref}/reply` with `{ "text",
"mentions" }`. That needs an agent credential holding the `send` scope and the
target's chat approved for it (`wasync agents grant NAME --scope send
--on-demand`, then approve the chat when the agent asks); wasync records the attempt, the chat and the ref, never the text, in
the audit log. Errors use the v1 envelope: `target_unavailable` (deleted or a
reaction), `not_found` (not archived or another account), `owner_offline`
(wasync is not connected) and `send_failed`, whose outcome is unknown and must
not be retried automatically. `whatsapp-axi reply REF --text TEXT` calls it.

### Reactions

```sh
wasync react --chat '123456789@g.us' --message-id 'ABCDEF123' --emoji '👍'
wasync react --chat '123456789@g.us' --message-id 'ABCDEF123' --remove
```

`react` sets your reaction on an archived message, or clears it with `--remove`;
give exactly one of `--emoji` and `--remove`. WhatsApp keeps one reaction per person
per message, so a new emoji replaces the old one. `--emoji` must be a single emoji
(words, plain digits and ASCII punctuation are refused). The target rules, owner reuse
and one-attempt behaviour are the same as for `reply`; the signed owner route is
`POST /api/react` with `{ "chat", "message_id", "emoji" }` or `"remove": true`.
Agents use `PUT /api/v1/messages/{ref}/reaction` with `{ "emoji" }` and `DELETE` on the
same path to clear it. Both need the `react` scope (separate from `send`) and the
chat in the credential's allowlist, and are audited by scope, chat and ref.
`whatsapp-axi react REF EMOJI` and `whatsapp-axi react REF --remove` call them. Your
own sent reactions are archived like incoming ones, including removals.

### Editing your own messages

```sh
wasync edit --chat '123456789@g.us' --message-id 'ABCDEF123' --text 'Meet at 7, not 6'
```

`edit` replaces the text of a message you sent. wasync refuses, before connecting or
sending anything, a message that is someone else's, is not plain text (media captions
cannot be edited), was deleted, or was first sent more than WhatsApp's edit window ago
(about 20 minutes, counted from the original send; later edits do not extend it). The
error says which rule applied. The signed owner route is `POST /api/edit` with
`{ "chat", "message_id", "text" }`. Agents use `POST /api/v1/messages/{ref}/edit` with
`{ "text" }`, which needs the `edit.own` scope and the chat in the credential's
allowlist; refusals use the codes `not_editable` and `edit_window_expired`, and an
unknown outcome is `send_failed`, never retried automatically. `whatsapp-axi edit REF
--text TEXT` calls it. Your edit is archived like one made on your phone, so
`edited_at` and the new text appear in queries.

REST aliases `/api/chats`, `/api/chats/{id}`, and the matching
`/enable`, `/disable`, `/history`, `/messages` routes support both chat types.
Existing `/api/groups` lists groups only. Explicit sending uses authenticated
`POST /api/send` with JSON `{ "to": "CHAT_ID", "text": "Hello" }`; optional
`file` is an absolute local path and `type` is `image` or `document`. The separate `send.token` permits explicit sends, including reading the requested
local attachment. The archive `api.token` cannot send. CLI commands sign requests
with the send credential, bind the signature to the method/path/body and reject
replays; they do not transmit the credential. Send, sync and action routes accept
only signed requests (headers `X-Wasync-Time`, `X-Wasync-Nonce`, `X-Wasync-Signature`);
a plaintext bearer token is rejected there. Keep the token private.

## Optional media and document downloads

The global flag below enables automatic downloads for chats whose saved policy is
`inherit`; per-chat policies and one-time downloads are covered in
[Per-chat media policies](#per-chat-media-policies-and-one-time-downloads).

```sh
wasync --download-media
# Headless:
wasync --download-media serve
# Equivalent environment setting:
WASYNC_DOWNLOAD_MEDIA=1 wasync
```

Images, documents, videos, audio/voice notes and ordinary WebP stickers are
eligible in enabled chats. Lottie stickers retain metadata only. Downloads run in
a bounded background queue and
use whatsmeow's authenticated decryption and integrity checking. Files are streamed to disk without a wasync-specific size cap and stored under the private `media/ACCOUNT/CHAT_HASH/` directory.
Sender filenames are never used as paths. Attachment metadata gains
`download_state`, `local_path` and `relative_path`, visible in message queries.
`relative_path` is authoritative relative to the current data directory’s `media/`
root, so moving your archive preserves file identity; `local_path` records the
absolute path at download time. States such as
`failed`, `queue_full` and `invalid_metadata` describe incomplete attempts; downloads
are best effort and require available WhatsApp media. Missing files are not
retrieved merely by searching the local archive.

Download keys/references for eligible attachments are kept in the separate private
`media-refs/` credential store so one-time downloads can work after a restart. They
are never added to archive metadata or query responses. These files are plaintext
credentials protected by filesystem permissions/ACLs, like the device session;
keep them private. View-once and disappearing attachments are excluded. Old
metadata-only rows from earlier versions still require WhatsApp to deliver the
original media metadata again. Purging a chat in the TUI removes
its local download directory. Observed deletions remove matching downloaded files.
Local media files are plaintext and are never opened automatically.

## Pairing-time history options verified in upstream source

The pinned whatsmeow implementation supports:

1. Pairing-time history notifications delivered by WhatsApp.
2. `BuildHistorySyncRequest` / `SendPeerMessage`: on-demand pages preceding a
   known message ID and timestamp, with 50 recommended per request.
3. `DownloadHistorySync` and `SendHistorySyncServerErrorReceipt`: download or retry
   a specific history notification already delivered; these do not list arbitrary
   server history or remove the cursor requirement.
4. The upstream `store.DeviceProps.RequireFullSync` pairing hint. To request it
   when linking a new device, launch `wasync --pairing-full-history` (or set
   `WASYNC_PAIRING_FULL_HISTORY=1`). It is an experimental best-effort request,
   not a completeness guarantee. It does not replay history for an existing link.

Pair using the TUI or `wasync login` to seed persistent group cursor metadata
when WhatsApp delivers history. The diagnostic forms of `login` (see
[Scriptable diagnostics](#scriptable-diagnostics)) discard bodies even for
already-enabled chats; the TUI, including the one an interactive `login` opens,
can archive enabled chats during pairing. Changing pairing hints
never automatically logs you out or deletes your existing archive.

## Encryption at rest

**wasync does not encrypt `archive.db`, `session.db`, SQLite sidecars, backups or
downloaded media.** WhatsApp's end-to-end transport encryption ends before local
archiving. Private Unix permissions / Windows ACLs restrict other OS users; they
do not isolate data from software running as your own user. FileVault / BitLocker
can protect powered-off disks when enabled.

An encrypted-database edition requires an encrypted SQLite implementation,
OS-backed or passphrase-based key management, and explicit migration of both the
archive and linked-device store. That upgrade is not included in these installers.
Do not treat a protected directory or a separate session database as encryption.


## Confirmed local cleanup and WhatsApp deletion

In the TUI:

- **d**, while browsing messages: clear that message's local archived content and
  download, retaining a tombstone so history replay cannot restore its body.
- **D**, in the chat list: disable capture and purge that chat's local archive and
  downloaded files. Re-enabling later can import history again.
- **X**, on your own archived message: request WhatsApp deletion for everyone,
  subject to WhatsApp permissions/time limits. Local content is cleared after
  WhatsApp acknowledges the revocation. Other participants' messages cannot be
  revoked through this implementation.
- **W**, in the chat list: delete WhatsApp chat state for your account through the
  last locally known message. This does not leave a group or delete participants'
  copies. The local archive is retained.

Each operation shows an explicit scope and target confirmation. Local cleanup
never sends a WhatsApp deletion operation. Backups and filesystem remnants may
retain copies; cleanup is not a secure erase guarantee.

Agent commands **only request** destructive operations:

```sh
wasync clean --chat '123456789@g.us' --message-id MESSAGE_ID
wasync clean --chat '123456789@g.us' --all
wasync delete --chat '123456789@g.us' --scope everyone --message-id MY_MESSAGE_ID
wasync delete --chat '123456789@g.us' --scope my-whatsapp-chat
wasync actions status REQUEST_ID
```

The TUI must be running with its command channel enabled. Requests return JSON
`pending_human_confirmation`; submitting a request changes no archived or WhatsApp
messages. The TUI displays the exact action and requires typing `DELETE CODE`
shown in the dialog, then Enter. **n** rejects. There is no `--yes`. Alternatively, an interactive terminal can run
`wasync actions approve REQUEST_ID`, inspect the exact scope and target, and type
the displayed confirmation. Requests expire after five minutes and are
discarded when the owning process exits. Approval cannot be replayed. Inspect request status
before retrying; `failed_or_unknown` can include an ambiguous remote outcome.

Pending requests use authenticated `POST /api/actions` and status is available
through `GET /api/actions/{id}`, using the separate send credential. The headless
service supports interactive CLI approval through a separate private `approve.token`.
The query and send credentials cannot approve a request. Agents should receive
only the query credential; filesystem access as your OS user is not a security
boundary against a malicious agent.

## Using WhatsApp from the Codex desktop app

This repository is also a Codex plugin marketplace. In the Codex app (or with the command line):

```sh
codex plugin marketplace add gehariharan/wasync
codex plugin add whatsapp@wasync
```

The plugin gives Codex `whatsapp_*` tools served by `whatsapp-axi mcp` (read, search, follow changes, and reply, react and edit only where you allow it, plus asking for access to a chat and starting setup). Ask Codex to "set up WhatsApp": it opens the setup page below, you scan the QR code and choose chats, and later every chat Codex asks for is approved by you on your own computer (unless you set wasync to sync all chats, where asking grants it). No tool can approve, grant or reveal a credential. See [ADR-003](docs/architecture/ADR-003-mcp-tool-server.md) and the threat model.

It needs `wasync` and `whatsapp-axi` installed (see [Choose whether to add AXI for agents](#choose-whether-to-add-axi-for-agents)) with `whatsapp-axi` on the PATH the Codex app uses. [Let your agent set this up](#let-your-agent-set-this-up) has the install commands for macOS and Windows and the steps only you can do.

## Setting up without a terminal

```sh
wasync setup
```

`wasync setup` starts wasync (like `wasync serve`) and opens a page in your browser
that walks through the first-time steps that used to need a terminal:

1. **Link WhatsApp.** The page shows the QR code as an image; scan it from WhatsApp
   on your phone under *Linked devices → Link a device*. The code refreshes by itself;
   if it expires unscanned, the page offers **Get a new code**.
2. **Choose chats.** Tick the chats wasync should keep on this computer. Agents see none
   of them until you connect one.
3. **Connect an agent.** Pick a name. The agent may read chats and messages, only those
   you approve, until you revoke it (`wasync agents grant` offers other access and
   expiry). The credential is created and saved straight to where whatsapp-axi reads
   it; it is never shown. The agent starts with no chats, and each one it asks for
   appears under **Approvals**.
4. **Start at login.** Optionally add a login item (a macOS LaunchAgent, or a Windows
   startup item) that runs `wasync serve` for your account only, with no administrator
   rights. It takes effect the next time you log in. The same choice is available as
   `wasync service install|uninstall|status`.

Steps 3 and 4 widen what runs on your behalf, so each asks your computer to confirm
(Touch ID or password on macOS, Windows Hello or password on Windows), exactly like an
approval. Where a computer cannot show that prompt the page says so and shows the
terminal command instead. `wasync setup` never prints the link it opens, and if wasync
is already running it opens the setup page of the running copy.

> **Windows:** the login item, the Windows Hello and password prompts and the
> notification have not yet been run on a real Windows computer by the author.

## Approving without a terminal

You can approve what agents ask for, and choose between asking each time and
syncing all chats, without typing in a terminal:

```sh
wasync approvals          # opens the approval page in your browser
```

With wasync running (the TUI or `wasync serve`), this opens a page listing the
requests agents are waiting on. For each one you see the agent, the candidate chats
and the history depth, and you press **Approve**. wasync then asks **your computer**
to confirm: Touch ID or your account password on macOS, Windows Hello on Windows.
The operating-system prompt names exactly what you are approving, in words wasync
wrote from its own records, never the agent's. Nothing on the page, in a
notification, or in a signed request can approve on its own.

A desktop notification tells you when an agent is waiting. It names only the agent
and carries no code or button; run `wasync approvals` to review.

The same confirmation applies when you approve in the TUI or with
`wasync actions approve`: after you type the phrase, your computer asks too. If
your computer cannot show that prompt, wasync says so when it starts, the page
only lists requests, and approvals stay in the terminal. The link `wasync
approvals` opens works once and only for two minutes. The threat model explains
what this does and does not protect:
[docs/architecture/threat-model.md](docs/architecture/threat-model.md).

Creating or revoking an agent credential (`wasync agents grant`, `agents revoke`)
asks for the same confirmation after you type the agent's name.

> **Windows:** wasync asks for Windows Hello first and, if Hello is not set up,
> for your account password in the standard Windows security dialog. These prompts
> and the notification have not yet been run on a real Windows computer by the
> author. They build and are covered by the Windows checks, but treat them as
> untested until you have tried an approval.

## Letting an agent ask for chats (just-in-time access)

You do not have to pick an agent's chats up front. Create the credential with
`--on-demand`; it then starts with no chats at all:

```sh
wasync agents grant helper --scope read.chats,read.messages,send --on-demand
```

After you confirm, wasync offers to save the credential straight into
whatsapp-axi's credential file, so there is nothing to copy by hand (see
[Set up an agent, start to finish](#set-up-an-agent-start-to-finish)).

When the agent needs a chat it cannot use, its refusal points at
`whatsapp-axi access request --chat JID` for that exact chat; an agent can also ask
by name with `--match NAME` (a contact or group name, or a phone number). `--send`
also asks to send, reply and react; `--edit` also asks to edit its own messages.
wasync resolves the request to concrete candidate chats that only you see and
queues a pending request with the same rules as the destructive actions above:
five-minute expiry, no replay, a person's confirmation, no `--yes`.
The agent is always told "pending", whether or not anything matched, so it
cannot use a request to find out which chats you have. A request that matched
nothing shows as "no chat matched" in the TUI and `wasync actions list`; you can
reject it, it cannot be approved, and it expires. Direct chats are named after
the saved contact when WhatsApp provides one; a phone number always works.

You see the request in the TUI or on the approval page (`wasync approvals`), or
run `wasync actions approve REQUEST_ID` in a terminal. A headless `wasync serve` has no dialog, so it prints (and logs) one
line per new request naming only the agent and the request ID, never a chat or
number; run `wasync actions list` in a terminal to see what each pending request
asks for, then `wasync actions approve REQUEST_ID`. Before confirming you choose:

- the chat, when several match;
- the history depth: **from now on only** (new messages, nothing older is
  requested) or a **full history sync** (also asks your phone for the chat's
  older messages, best effort, like the TUI's `h`);
- you see whether the agent asked to read only, to also send/reply/react, or
  also to edit its own messages.

On approval wasync turns on capture for that chat, applies the history choice,
and extends that agent to that one chat with only the access you saw: read
never implies send, and the agent can never gain a scope its credential does
not already hold. Rejecting or letting the request expire changes nothing.
Approved chats show in `wasync agents list`; to take them away, revoke and
re-grant the agent. Pre-approving chats with `--chat`, or in the TUI, still
works and combines with `--on-demand`.

### Ask each time, or sync all chats

One setting decides how requests are answered. After you pair, the TUI asks once
(and `M` there, or `wasync access mode`, changes it later):

- **Ask me each time** (default): every request waits for you, as above.
- **Sync all chats**: turns on capture for every chat now, with the history
  depth you pick (from now on only, or a full history sync), and from then on an
  agent's request for the one chat it matched is approved at once, without asking
  you. **In this mode agents can read, send, reply and react in any chat without
  asking, and can discover any chat by name or number: a matching request is granted at once, so a name probe both reveals and grants that chat (ask-each-time reveals nothing).** Editing their own messages (`--edit`) and deleting are never
  automatic. Each automatic approval is still recorded as an approval request
  and in the audit log (`access_grant_auto`), the agent's own rate limits still
  apply, and a request that matched nothing or several chats still waits for you.
  Chats WhatsApp shows you later are enabled when you run `wasync access mode
  all` again, or when an agent first asks for them.

```sh
wasync access mode                          # show the current mode
wasync access mode all --history now        # sync all; typed SYNC ALL, interactive terminal
wasync access mode ask                      # back to ask each time
```

Changing the mode needs a running wasync owner (the TUI or `serve`) and either
an interactive terminal with a typed confirmation or the approval page (see
"Approving without a terminal" above); choosing sync all also asks your computer
to confirm where it can. No agent credential can reach it.
Switching back to ask-each-time stops future automatic approvals but does not
revoke access already given; revoke the agent to remove that.

## Re-pairing screen

After logout, the TUI hides cached groups until the newly paired account is
authenticated. QR pairing uses a compact full-screen layout, so terminal space is
reserved for the QR rather than archive borders and controls. If the code still
does not fit, enlarge the terminal or reduce its font size. Archives are preserved
across logout and re-pairing; no database deletion is needed.

Download cleanup failures (for example, a file open in another Windows program)
do not stop message capture. A background sweep retries removal of orphaned
managed downloads at startup and every thirty seconds. Unrecognized files placed
in the media directory are not swept. Close other programs holding downloaded
files if cleanup remains pending.


## Complete CLI workflow (TUI optional)

```sh
wasync login --select=false
wasync groups list
wasync groups enable GROUP_ID
# Or explicitly opt in to all currently discovered groups:
wasync groups enable --all
wasync sync --wait 30s
wasync sync --chat GROUP_ID --history --wait 60s
wasync query messages --chat GROUP_ID
wasync query search --chat GROUP_ID "keyword"
wasync serve
```

`sync` connects for a bounded observation window (0–60 seconds), refreshes group
metadata, captures enabled chats, and returns JSON. `--history` queues best-effort
older-message requests; enabling a chat already queues its initial history.
History requires a known message anchor and may remain pending after the command
ends; use `serve` for continuous capture and history processing. This is event
observation, not a complete cloud download or a guarantee that the latest server
message or every downtime message has arrived. Direct chats become discoverable
when WhatsApp supplies their metadata; they are disabled by default.

While `serve` or the TUI is running, CLI selection, listing, sending and `sync`
reuse its authenticated loopback command channel. Use the same `--data-dir` and
`--api-addr` as the owning process. `--no-api` disables that cooperation; quit the
owner before running a command that needs the device lock. Read-only `query`
commands read the archive directly and remain available without a server.
To unlink from the CLI, stop the owner and run `wasync logout`.

Destructive CLI requests can be approved without opening the TUI:

```sh
wasync clean --chat CHAT_ID --all
wasync actions status REQUEST_ID
wasync actions approve REQUEST_ID  # interactive typed confirmation required
```

`api.token` grants archive queries and capture selection; `send.token` separately
grants explicit sending, sync requests and queuing cleanup requests;
`approve.token` authorizes execution after the interactive CLI confirmation.
Keep these files private. None are printed in command output. The default
loopback channel is local HTTP, not transport encryption.

The Windows installer owns only the PATH entry it adds. A preexisting equivalent
entry is preserved on uninstall. Close wasync before upgrading or uninstalling.

On macOS and Windows the running wasync also asks you to confirm every approval
with Touch ID or your password (macOS) or Windows Hello or your password (Windows), whichever way
the approval arrived; see "Approving without a terminal" above. Where a computer
has no such prompt (Linux, or a session with no screen), the typed phrase is the
only check and is enforced by the interactive CLI/TUI, not by the HTTP endpoint.
Do not give `approve.token` to agents. On macOS it is kept in your login Keychain
instead of a file (an existing file is moved there when wasync next starts), so another
program that asks for it makes macOS ask for your password; Windows and Linux keep
the private file. If you cancel or deny the Keychain prompt when wasync starts by
itself at login, it stops without restarting and shows one notification: approvals stay
off until you run wasync again. A holder of `send.token` can request sending any regular local file
the owner process can read; it is a privileged credential, not a read-only API key.
Download `queue_full`/`failed` states have no automatic retry; another delivery of
valid WhatsApp media metadata is required to attempt them again. Sending an
attachment to an enabled chat with downloads enabled may also download your own
upload. Purge removes content and disables capture; discovery rows (chat ID/name)
remain for the selection interface and may be refreshed by later WhatsApp events.

## Microsoft Store MSIX distribution

The Store edition is an x64 desktop console application with a `wasync.exe` app
execution alias and the same optional TUI/CLI. **It contains wasync only, not
AXI for agents.** A Store package cannot ask the user a question at install time, so
bundling `whatsapp-axi` would install it for everyone, which is not a choice. A
separate Store add-on that people could pick would be a new Partner Center product
and a new certification, which only the Store account owner can do. Store users who
want it can take `whatsapp-axi.exe` from the Windows ZIP on the
[Releases page](https://github.com/gehariharan/wasync/releases); with the Store
execution alias on PATH, it finds `wasync` the same way it does for other installs.
That Store-plus-AXI combination has not been tested on a real Store installation. Windows 10 build 19041 or newer is
required. Store publication is a separate certification process; building a
package does not create a Store listing or guarantee approval.

For Windows Server, this manifest targets Server 2022 or newer with Desktop
Experience; native installation still needs validation on your server image.
Server 2019 is below this package's minimum OS version and does not support the
execution alias extension. For Server Core or unattended deployments, use the
portable ZIP distribution. Server sideloading requires a trusted signed MSIX;
the unsigned Store submission output cannot be installed directly. See
[Microsoft's Server guidance](https://learn.microsoft.com/en-us/windows/msix/msix-server-2019).

The Microsoft Store signs MSIX submissions after certification. No Azure signing
subscription, commercial certificate, PFX or hardware token is required for this
route. Our existing GitHub EXE/ZIP downloads keep their existing signing status.
Unsigned Store build artifacts are submission inputs, not public sideload installers.

### Reserve the product and configure its identity

1. Register and verify a developer account at <https://storedeveloper.microsoft.com>.
2. Reserve the MSIX product name **wasync**. Open **Product identity** and copy the
   exact case-sensitive `Package/Identity/Name`, `Package/Identity/Publisher`, and
   `PublisherDisplayName`. These values are public package metadata, not passwords.
3. Set repository variables `WASYNC_STORE_IDENTITY_NAME`, `WASYNC_STORE_PUBLISHER`,
   and `WASYNC_STORE_PUBLISHER_DISPLAY_NAME` to those values.
4. Run **Build Microsoft Store submission** in GitHub Actions. Supply an application
   version such as `0.1.0-store.1` and a separate increasing Store version such as
   `1.0.1.0`. Store versions use four integers, the first is nonzero, all are at most
   65535, and the last is zero. A later Store update must have a higher version.
5. Download the `store-submission-VERSION` artifact. It contains an unsigned `.msix`,
   `.msixupload` upload container, checksums and `submission-info.json`. Verify the
   identity receipt against Partner Center before uploading the package.
6. Use the draft copy and certification notes in
   [packaging/store/listing.json](packaging/store/listing.json). Provide real,
   privacy-safe screenshots of the running application, complete age ratings and
   the `runFullTrust` justification, run Windows App Certification Kit, then submit
   for Microsoft review. No Partner Center credentials are stored in this repo.

The workflow refuses missing identity values and the CI-only test identity. It
produces an artifact for manual review/upload; it does not automatically publish
or claim certification. Native CI separately packages an isolated test identity,
uses a short-lived test certificate on its disposable runner, and exercises app
alias activation, local search, package upgrade and retained data after uninstall.
Its test packages are labelled **not for Store** and must never be submitted.

For a native Windows maintainer build with the Windows SDK installed:

```powershell
./scripts/package-msix.ps1 -Version 0.1.0-store.1 -PackageVersion 1.0.1.0 `
  -IdentityName 'COPY_FROM_PARTNER_CENTER' `
  -Publisher 'CN=COPY_FROM_PARTNER_CENTER' `
  -PublisherDisplayName 'COPY_FROM_PARTNER_CENTER'
```

### Store archive location and migration

The Store build defaults to `%USERPROFILE%\.wasync`, a private directory outside
MSIX-managed app data. Upgrades and uninstall retain the archive, linked-device
session, allowlist and downloads. This avoids requesting the additional restricted
capability for disabling MSIX filesystem virtualization. `runFullTrust` remains
necessary for this desktop application and requires Store review.

The EXE/ZIP build keeps `%APPDATA%\wasync`. These are separate installations;
there is no automatic session copy or background login. Before switching, stop
both versions and back up the entire old data directory, including SQLite
sidecars, session database and media. Copy it to `%USERPROFILE%\.wasync` while
both apps are stopped, then start the Store edition and verify `wasync status`.
Copying a live WAL database or running two device sessions can lose data or
cause conflicts. `WASYNC_DATA_DIR`/`--data-dir` can override the location; use a
private external directory if data must survive MSIX uninstall. Remove an old
EXE installation or its PATH entry when switching so the intended command wins.

### Store privacy policy

wasync is a local, personal-use WhatsApp client. The developer operates no data
collection backend and does not collect analytics or receive your archived chats,
credentials, searches or downloaded attachments. Your account connects to
WhatsApp servers through the unofficial whatsmeow linked-device implementation;
authentication, protocol acknowledgements, synchronization requests, and messages
you explicitly send are exchanged with WhatsApp under its own service policies.

You choose which chats have content captured. Chat discovery metadata is cached
for selection; disabled chats' message bodies are discarded. Enabled chats can
store sender identifiers, timestamps, message text and attachment metadata.
Downloading attachment files (images, documents, video, audio, stickers) is opt-in. Search and the optional authenticated
loopback API operate on your computer. Nothing is sent to an AI service.

The archive, session and downloaded files are plaintext with restrictive OS
permissions; software running as your user can access them. Shared or exported
copies are your responsibility. Local cleanup and WhatsApp remote deletion have
different scopes and require explicit confirmation. Uninstall intentionally
retains the Store data directory; stop the application and remove that directory
if you want to delete the remaining local archive and pairing credentials. Use
`wasync logout` before removal to unlink the device. Questions can be raised via
our public GitHub issue tracker; do not post private messages or credentials there.


## Linked-device identity and rich attachment details

New pairings identify the client as **wasync (macOS)**, **wasync (Windows)** or
**wasync (Linux)** with the desktop platform type. WhatsApp chooses the linked-device
icon and final label; there is no supported custom icon upload. This is an
unofficial client, not an official browser or verified WhatsApp application.
Existing linked devices may keep their old label until you explicitly logout and
pair again. Updating does not automatically logout or delete an archive.

The TUI attachment details show MIME type, original filename/title when supplied,
size, dimensions, duration, document page count, voice-note/animation flags,
download state and local file location. The same fields are available in query/API
`media_metadata`. Sender filenames are display metadata only: downloads use hashed
names and a fixed safe MIME-to-extension mapping. Unknown types use `.bin`.
Documents such as PDF, Word, Excel and PowerPoint are retained as files, not parsed
or indexed internally; text search covers message text and attachment captions, not
PDF/OCR/document contents or metadata-only filenames/titles. Files are never opened or executed automatically.

Media downloads are selected by saved per-chat policies or explicit one-message
requests. `--download-media` / `WASYNC_DOWNLOAD_MEDIA=1` enables automatic downloads
only for chats whose policy is `inherit`. The allowlist, expiry/view-once exclusions,
deletion cleanup and best-effort history limits also apply to video, voice notes,
audio and stickers. Installing an update does not recover previously discarded
media download keys; WhatsApp must deliver the original attachment metadata again.


## Media size and download selection

Wasync has no fixed 25 MiB sending or download limit. Uploads use whatsmeow's
streaming encryption/upload API; downloads write to disk and verify the message's
advertised length plus upstream cryptographic integrity. WhatsApp's current
service/type limits and media availability still apply; removing an application
cap does not make WhatsApp accept an unsupported file. Cancellation and normal
network/protocol failures remain possible. Large transfers need disk space for
temporary encrypted data in addition to their final file.

Download selection currently has two layers:

- Enable capture for the groups/direct chats you want to archive.
- Use a saved per-chat policy, a one-time message request, or
  `--download-media` / `WASYNC_DOWNLOAD_MEDIA=1` for chats using `inherit`.
  With no global flag and no per-chat override, attachments remain available
  for manual download.

Per-chat media policies and one-time downloads are described below. Disabling
chat capture stops its downloads too. Starting
with media downloads enabled does not recover discarded download keys from old
metadata-only records; the original media metadata must be delivered again.

For a long explicit upload, increase the CLI's command timeout, for example:

```sh
wasync --timeout 30m send --to CHAT_ID --file ./large-report.pdf
```

The running owner's authenticated send route honors client cancellation rather
than imposing the ordinary short query timeout. Only an explicit send invocation
uploads a file; enabling downloads never sends a conversation message.


## Per-chat media policies and one-time downloads

**Chat policies are saved configuration.** They survive restarts and are isolated
by WhatsApp account. Capture selection remains independent and must be enabled.

| Policy | Behavior |
| --- | --- |
| `manual` | No automatic file downloads, even with the global flag. Request individual attachments. |
| `auto` | Automatically download newly delivered eligible attachments in this chat, without the global flag. |
| `inherit` (default) | Follow the launch-time `--download-media` / `WASYNC_DOWNLOAD_MEDIA` setting. With that setting off, download manually. |

```sh
wasync media policy --chat CHAT_ID --mode manual
wasync media policy --chat CHAT_ID --mode auto
wasync media policy --chat CHAT_ID --mode inherit
wasync media policy --chat CHAT_ID
```

Policy changes work with or without a running owner. They do not change capture
selection, remove downloaded files, or bulk-download old attachments. The global
flag is a setting for that process launch, not a preference written into every chat.
An environment variable remains effective for as long as you keep it configured.

**A per-message request is one-time**, and changes no policy:

```sh
# Keep the TUI or headless owner running:
wasync serve
# In another terminal:
wasync media download --chat CHAT_ID --message-id WHATSAPP_MESSAGE_ID
wasync query messages --chat CHAT_ID
```

The command returns `queued` or `already_downloaded`; queued is not a success
receipt for the transfer. Inspect `media_metadata.download_state` for `available`,
`queued`, `downloading`, `downloaded`, `failed`, `queue_full` or `interrupted`.
Repeated active requests are deduplicated. Re-request an interrupted/failed file
to retry. Commands reuse the live owner's signed local channel and `send.token`;
read-only query commands never trigger downloads. Media-control endpoints require
`send.token` and reject `api.token`.
With `--no-api`, use the owner's TUI instead. Offline mode can edit policies and
browse the archive, but cannot download from WhatsApp.

In the TUI, **m** on a chat cycles `manual` → `auto` → `inherit`; **m** on a message
requests its attachment once. Message details show the download state and local
path. **R** refreshes the latest message page.

For newly captured eligible media, the private `media-refs/ACCOUNT/CHAT_HASH/`
store retains only the download path/key, hashes, type/size and opaque identities.
It contains credentials, not searchable message text. Archive/API queries never
return these secrets. Include this directory only in private backups; it is not a
file to share with agents or other archive consumers. Same-user filesystem access
is not isolated by directory permissions, and these credentials are not encrypted
at rest. References can outlive remote WhatsApp media availability.

Clear/purge/revoke removes corresponding references; background cleanup retries
orphaned references after interruptions. Disabled capture preserves existing
references and downloaded data, but downloading requires re-enabling capture.
View-once, disappearing and edited attachment references are excluded; missing
references require redelivery of valid original attachment metadata. This update
does not reconstruct keys discarded by older versions or guarantee that remote
media still exists. No fixed 25 MiB file-size cap is reintroduced.

Media policies survive a local chat purge, so re-enabling capture restores the
previous media behavior. Logout preserves references with the archived data;
clear/purge the corresponding local data to remove them. Remote media availability
may expire independently of either local policy or pairing status.
