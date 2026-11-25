# wasync

wasync keeps a private archive of the WhatsApp chats you choose, on your own computer, and lets
you and the AI agents you trust read it, and send messages only where you allow.

It links to your WhatsApp account as a linked device (like WhatsApp on a desktop), stores the
chats you pick in a local SQLite database, and keeps capturing new messages while it runs. You
use it from a local web app in your browser, from the command line, or through an HTTP API on
your own computer. Agents such as Claude Code, Codex and OpenCode use it through a small
companion command, **`wasync-axi`**. In the normal setup an agent starts with no chats: it asks
for one, and you approve it on your own computer. Nothing goes to a cloud service, agents can
never delete anything, and every cleanup or deletion needs a person's confirmation.

wasync is written in Go on top of [whatsmeow](https://github.com/tulir/whatsmeow). It is an
unofficial client; see [Using an unofficial client](docs/data-and-security.md#using-an-unofficial-client).

## Ways to use it

| You want to | Use | Details |
| --- | --- | --- |
| Link WhatsApp, pick chats, read and write messages, approve agents | The web app: `wasync setup` the first time, then `wasync web` | [The web app](#the-web-app) |
| Keep wasync syncing in the background | `wasync serve`, or start it at login with `wasync service install` | [Running in the background](#running-in-the-background) |
| Script it from a terminal | `wasync query …`, `wasync send …` and the other commands | [Command line](#command-line), [docs/cli.md](docs/cli.md) |
| Let an AI agent read (and, if you allow, write) | `wasync-axi`, or the Codex plugin | [Agents](#agents), [docs/agents.md](docs/agents.md) |
| Connect your own program | The local HTTP API and webhooks | [Local API and webhooks](#local-api-and-webhooks) |
| Browse in a terminal | The terminal UI (`wasync`), which is being retired | [Terminal UI](#terminal-ui) |

## Install

### Install a prebuilt package

Prebuilt downloads are on the [Releases page](https://github.com/gehariharan/wasync/releases).
You do not need Go or a compiler.

| Platform | Download | How |
| --- | --- | --- |
| macOS 15+ (Apple Silicon or Intel) | `wasync_VERSION_darwin_arm64.pkg` / `_amd64.pkg` | Open the package and follow Installer |
| Linux x86-64 or ARM64, glibc 2.35+ | `wasync_VERSION_linux_amd64.tar.gz` / `_arm64.tar.gz` | Extract and run `./install.sh` |
| Windows 10/11 x64 | `wasync_VERSION_windows_amd64-setup.exe` or `.zip` | Per-user Setup or portable ZIP ([Windows](docs/windows.md)) |

Each release has a `checksums.txt` with SHA-256 hashes. Upgrades keep your archive, chat choices
and pairing. Platform details, signing, checksums and the Microsoft Store edition are in
[docs/install.md](docs/install.md) and [docs/windows.md](docs/windows.md).

### Choose whether to add AXI for agents

`wasync-axi` ("AXI for agents") is the command agents use to work with your archive. You only
need it if you want an agent to use wasync, so the installers offer it as a choice that is off
by default: a checkbox in the macOS and Windows installers, `./install.sh --with-axi` on Linux,
and a separate Homebrew formula. (The Windows ZIP always includes it; the Microsoft Store edition
does not.) Both programs come from the same release. Installing it never
changes your agents' settings; to tell your agents about it, run `wasync-axi setup skill` and
`wasync-axi setup hooks` (add `--dry-run` to preview). It was previously called `whatsapp-axi`,
which still works as an alias. See [docs/install.md](docs/install.md#axi-for-agents-wasync-axi).

### Homebrew installation

The repository is also the Homebrew tap. On macOS, install the signed app and, if you want it,
the agent command:

```sh
brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git
brew trust --cask gehariharan/tap/wasync   # only on Homebrew versions with tap trust
brew install --cask gehariharan/tap/wasync
brew install gehariharan/tap/wasync-axi    # optional: AXI for agents
```

On Linux, Homebrew builds both from source:

```sh
brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git
if brew help trust >/dev/null 2>&1; then brew trust --formula gehariharan/tap/wasync; fi
brew install gehariharan/tap/wasync-axi
```

Existing users of the former standalone tap should run the `brew tap --custom-remote` command
once to migrate. Upgrading, trust and PATH details are in [docs/install.md](docs/install.md#homebrew).

## Pair your phone

```sh
wasync setup
```

`wasync setup` starts wasync and opens a setup page in your browser:

1. **Link WhatsApp.** Scan the QR code with WhatsApp on your phone (*Linked devices → Link a
   device*). The code refreshes by itself; if it expires, the page offers **Get a new code**.
2. **Choose chats.** Tick the chats wasync should keep on this computer. Nothing else is
   archived, and agents see none of them until you connect one and approve what it may read.
3. **Connect an agent** (optional). Pick a name; wasync creates a read-only credential that starts
   with no chats and saves it straight to where `wasync-axi` reads it, never showing it.
4. **Start at login** (optional, macOS and Windows). Adds a login item for your account only,
   with no administrator rights, that runs `wasync serve`.

Steps 3 and 4 widen what runs on your behalf, so each asks your computer to confirm (Touch ID or
password on macOS, Windows Hello or password on Windows). Where a computer cannot show that
prompt, the page shows the terminal command instead. `setup` keeps running as the active wasync
until you press Ctrl-C; if wasync is already running, it opens that copy's setup page.

wasync only captures while it is running. Once you enable a chat, wasync also asks your phone for
its older messages, best effort. See [History and capture](docs/history.md) for what is stored
and the limits.

## The web app

```sh
wasync web
```

With wasync running, `wasync web` opens the web app in your browser through a one-time link
that works for two minutes and is never printed. It has five pages:

- **Chats**: your synced chats and their messages, side by side. A small robot marks chats an
  agent can read. Write to an open chat from the box under its messages, or start a message to
  a new number with the pencil icon; messages you send go out once, as you. Use the ☑ icon to
  choose which chats sync, and the chat header's **Downloads** setting to choose how
  attachments download ([Media](docs/media.md)).
- **Sent**: what was sent from this computer, by you or by an agent.
- **Agents**: each agent's access; allow or stop sending, approve a paced list of phone numbers
  for one agent, or switch on open sending for an hour.
- **Approvals**: requests agents are waiting on (also `wasync approvals`), and the choice
  between asking you each time and syncing all chats.
- **Set up**: the setup page above.

Anything that gives an agent more power asks your computer to confirm, and the operating-system
prompt names exactly what you are approving. Stopping things never needs confirmation. The page
shows message text, so open it only on a computer you trust. Details:
[docs/agents.md](docs/agents.md).

To open it from your other computers on your Tailscale network, turn on `wasync tailnet allow`
and share it with `tailscale serve`: see [docs/tailscale.md](docs/tailscale.md).

## Running in the background

`wasync setup` and `wasync serve` keep wasync running without a window: capturing chats, serving
the web app and the local API, and handling approvals. Only one wasync runs per data folder, and
every other command works through it.

```sh
wasync serve                 # run in the foreground until Ctrl-C (needs a linked phone)
wasync service install       # start wasync serve when you log in (macOS, Windows)
wasync service status
wasync status                # pairing state and chats, from the running wasync
wasync stop                  # stop the running wasync cleanly
wasync logout                # unlink this computer from WhatsApp; the archive is kept
```

`wasync stop` does not unlink the phone or remove the login item (`wasync service uninstall`
does). After `logout`, run `wasync setup` again to link a phone. See
[docs/cli.md](docs/cli.md#one-running-wasync-owns-the-data).

## Agents

An agent never talks to WhatsApp directly. It uses a credential you create, which carries only
the scopes you chose (read, send, react, edit its own messages, and so on), and wasync checks
every request. Reading needs a read scope; replies, reactions and edits need a scope you granted,
per chat. No agent scope can delete anything. Create agents with `--on-demand` (as below, and as
the setup page does) so they start with no chats; a read-only agent granted without
`--on-demand` or `--chat` can read every archived chat.

### Set up an agent, start to finish

1. **Install** wasync and AXI for agents (see [Install](#install)).
2. **Pair your phone** with `wasync setup`, and keep wasync running (`wasync serve` or the login
   item) whenever an agent should use it.
3. **Create the agent's credential**, either with **Connect** on the setup page (read only), or
   in a terminal, with any name:

   ```sh
   wasync agents grant helper --scope read.chats,read.messages,observe,send,react,edit.own --on-demand
   ```

   `--on-demand` starts the agent with no chats at all. Type the agent's name to confirm. wasync
   then offers to save the credential straight into wasync-axi's private credential file; if you
   decline, it prints it once for `WASYNC_AXI_TOKEN` or your agent's secret store.
4. **Check the agent can reach wasync:** `wasync-axi doctor`.
5. **The agent asks for a chat by name**, for example
   `wasync-axi access request --match "Family" --send`. It is told the request is pending,
   whatever matched.
6. **You approve it** on the Approvals page (`wasync approvals`), or with `wasync actions list`
   and `wasync actions approve REQUEST_ID` in a terminal. You choose the chat when several
   match and the history depth, and see whether it may only read or also send. Only then can
   the agent use that chat.

Revoke an agent any time with `wasync agents revoke helper`. Pre-approving chats with
`--chat "Family"`, switching to "sync all chats", approved number lists and open sending are
covered in [docs/agents.md](docs/agents.md). The agent command itself is documented in
[axi/README.md](axi/README.md).

### Let your agent set this up

If you use the **Codex desktop app** on macOS or Windows, you can ask Codex to install wasync
and set up WhatsApp for you. Paste this into a Codex conversation (the same words on both):

> Set up WhatsApp for me with wasync from https://github.com/gehariharan/wasync. Follow the
> "Let your agent set this up" section of its README: install wasync and wasync-axi on this
> computer, add the WhatsApp plugin, then start the setup page by running `wasync setup`
> through its full installed path, outside the sandbox, as a background process that keeps
> running, and ask for my approval to run it outside the sandbox. Tell me whenever I need to do
> something on my own computer, and tell me to restart Codex only at the very end.

Codex then installs wasync and `wasync-axi`, adds the plugin
(`codex plugin marketplace add gehariharan/wasync`, then `codex plugin add whatsapp@wasync`) and
starts the setup page, asking your approval for each step. You scan the QR code, choose chats,
press **Connect** for the agent, and later approve each chat Codex asks for. Restart Codex last
so it loads the WhatsApp tools. No tool Codex has can approve anything, create a credential or
read one. **The exact commands for each platform, which Codex follows, are in
[docs/codex.md](docs/codex.md).**

## Command line

`wasync help` lists every command, and `wasync help COMMAND` explains one; neither connects to
anything. Global flags such as `--data-dir` go before the command.

### Agent CLI queries

`wasync query …` reads the archive and prints JSON. It never sends, never changes the archive,
and works whether or not wasync is running:

```sh
wasync query chats --enabled
wasync query messages --chat '123456789@g.us' --limit 50
wasync query search "meeting" --chat '123456789@g.us'
wasync query status
```

Sending is always an explicit command that acts once, as you:

```sh
wasync send --to '123456789@g.us' --text 'Hello'
wasync reply --chat '123456789@g.us' --message-id 'ABCDEF123' --text 'On my way'
wasync react --chat '123456789@g.us' --message-id 'ABCDEF123' --emoji '👍'
wasync edit --chat '123456789@g.us' --message-id 'ABCDEF123' --text 'Meet at 7, not 6'
```

Chat selection (`wasync chats`), one-off syncs (`wasync sync`), media downloads (`wasync media`)
and cleanup or deletion requests (`wasync clean`, `wasync delete`, which a person must approve)
are covered in [docs/cli.md](docs/cli.md), [docs/media.md](docs/media.md) and
[docs/cleanup-and-deletion.md](docs/cleanup-and-deletion.md).

## Local API and webhooks

While wasync runs, it serves an HTTP API on `http://127.0.0.1:8787` (loopback only; change it
with `--api-addr`). Your own scripts can read the archive with the bearer token in `api.token`;
agents use the scoped `/api/v1` with their own credential. Webhooks push a small,
identifier-only notification to your HTTPS service whenever the archive changes; a person
registers each endpoint with `wasync webhooks add`. You can also read the SQLite archive
directly, read-only. See [docs/api.md](docs/api.md) and [docs/webhooks.md](docs/webhooks.md).

## Terminal UI

Running `wasync` with no command opens a full-screen terminal UI that captures chats and lets
you browse them, toggle capture and approve requests with the keyboard. **It is being retired**:
the web app and `wasync serve` now do everything it does, and it will be removed in a later
release. Its keys and screens are documented in [docs/tui.md](docs/tui.md).

## Your data

Everything lives in one private folder: `~/Library/Application Support/wasync` on macOS,
`~/.config/wasync` (or `$XDG_CONFIG_HOME/wasync`) on Linux, and `%APPDATA%\wasync` on Windows
(`%USERPROFILE%\.wasync` for the Microsoft Store edition). Override it with `--data-dir` or
`WASYNC_DATA_DIR`. It holds the archive, the WhatsApp session, credentials and settings, with
owner-only permissions. wasync does **not** encrypt them at rest, so use full-disk encryption and
keep backups private. See [docs/data-and-security.md](docs/data-and-security.md).

<a id="store-privacy-policy"></a>

## Privacy policy

wasync is a local, personal-use WhatsApp client. The developer operates no data
collection backend and does not collect analytics or receive your archived chats,
credentials, searches or downloaded attachments. Your account connects to
WhatsApp servers through the unofficial whatsmeow linked-device implementation;
authentication, protocol acknowledgements, synchronization requests, and messages
you explicitly send are exchanged with WhatsApp under its own service policies.

You choose which chats have content captured. Chat discovery metadata is cached
for selection; disabled chats' message bodies are discarded. Enabled chats can
store sender identifiers, timestamps, message text and attachment metadata.
Downloading attachment files (images, documents, video, audio, stickers) is opt-in. Search,
the web app and the authenticated loopback API operate on your computer. wasync sends nothing to
an AI service; an agent you connect yourself reads only what you grant it.

The archive, session and downloaded files are plaintext with restrictive OS
permissions; software running as your user can access them. Shared or exported
copies are your responsibility. Local cleanup and WhatsApp remote deletion have
different scopes and require explicit confirmation. Uninstalling keeps the data
directory; stop the application and remove that directory
if you want to delete the remaining local archive and pairing credentials. Use
`wasync logout` before removal to unlink the device. Questions can be raised via
our public GitHub issue tracker; do not post private messages or credentials there.

## Documentation

- [Installing wasync](docs/install.md) and [wasync on Windows](docs/windows.md), including the Microsoft Store edition
- [Agents: access, approvals and sending](docs/agents.md) and [using wasync from Codex](docs/codex.md)
- [The command line](docs/cli.md)
- [Local API](docs/api.md) and [webhooks](docs/webhooks.md)
- [Opening the web app from your other computers](docs/tailscale.md) with Tailscale
- [History and capture](docs/history.md): what is stored, older messages and their limits
- [Media and attachments](docs/media.md)
- [Cleanup and deletion](docs/cleanup-and-deletion.md)
- [Data, files and security](docs/data-and-security.md)
- [The terminal UI](docs/tui.md)
- [wasync-axi, the agent command](axi/README.md)
- Design records: [architecture decisions and threat model](docs/architecture/)

## Development

This monorepo holds both `wasync` and `wasync-axi` (in [`axi/`](axi/)), their release automation
and Homebrew definitions. To build from source:

```sh
git clone https://github.com/gehariharan/wasync.git
cd wasync
go build -tags sqlite_fts5 -o bin/wasync ./cmd/wasync
bash scripts/check.sh
```

See [docs/development.md](docs/development.md), [CONTRIBUTING.md](CONTRIBUTING.md) and the
[release guide](docs/releasing.md).

## License

wasync is open source under the [MIT license](LICENSE). Dependencies retain
their own licenses; prebuilt archives and installers include third-party notices.
