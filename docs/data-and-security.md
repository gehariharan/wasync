# Data, files and security

Everything wasync keeps stays in one private folder on your computer. This page lists what is
in it, which credentials protect what, and what wasync does and does not protect against. The
[threat model](architecture/threat-model.md) goes deeper.

## The data directory

All commands use the same private data directory, normally:

- macOS: `~/Library/Application Support/wasync`
- Linux: `$XDG_DATA_HOME/wasync` or `~/.local/share/wasync`
- Windows Setup/ZIP: `%LOCALAPPDATA%\wasync`
- Windows Microsoft Store build: `%USERPROFILE%\.wasync` (see
  [Store archive location and migration](windows.md#store-archive-location-and-migration))

The **Storage** section of Settings says where it is, and **Show folder** opens it in
Finder, File Explorer or your file manager.

Override it using `WASYNC_DATA_DIR` or the global `--data-dir` flag; flags win.
Global flags precede the command.

### Moving from an older version's folder

Versions before this one kept the data in `%APPDATA%\wasync` on Windows, which Windows copies
between computers on managed profiles, and in `$XDG_CONFIG_HOME/wasync` (`~/.config/wasync`) on
Linux, which is meant for settings. The first time wasync starts after the update (`wasync
setup`, `wasync serve`, the login item, the terminal UI or a command such as `wasync status`), it
moves the data to the new folder by itself; until then, read-only queries and agents keep reading
the old one:

1. It waits until no other wasync uses the old folder, then holds it so none can start.
2. It copies everything to a temporary folder beside the new one and checks the copy: every
   file and folder is there with its size, nothing changed in the old folder meanwhile, and each
   SQLite database passes `PRAGMA integrity_check`.
3. Only then does the copy become the new folder. The old one is renamed to
   `wasync.moved-DATE` with a note, `MOVED-TO-NEW-FOLDER.txt`, saying where the data went, and
   the new folder gets a note, `OLD-FOLDER-KEPT.txt`, saying where the old one is. On Windows it
   goes to `%LOCALAPPDATA%\wasync.moved-DATE`, so it no longer roams between computers; if it
   cannot be moved there, it stays beside where it was and wasync says so. On Linux it stays
   beside where it was. Delete it once everything works.

If anything fails, wasync says why, removes the copy and keeps using the old folder as it was;
the next start tries again. A login item or script that still passes the old folder with
`--data-dir` (or `WASYNC_DATA_DIR`) keeps working: naming either default folder means the one in
use. Any other `--data-dir` or `WASYNC_DATA_DIR` is never moved. macOS and the Microsoft Store
edition keep their folders, so nothing moves there.

```sh
wasync --data-dir "$PWD/.local/wasync" serve
WASYNC_DATA_DIR="$PWD/.local/wasync" wasync serve
```

The directory is created with 0700 permissions, and managed files with 0600 (a private ACL for
your user and SYSTEM on Windows). Shared existing directories and symlink data files are
rejected. Use a dedicated location owned by your OS user with trusted parent directories. No
credentials, QR payloads or message bodies are written to logs. Filesystem permissions do not
encrypt data at rest.

| File | Purpose |
| --- | --- |
| `session.db` | Whatsmeow credentials and protocol state; protect as a secret |
| `archive.db` | Accounts, chats, messages, FTS5, sync progress, history jobs, gap records, the change journal, and when each chat was last active (a time only, also for chats that are not synced, so Choose chats can list recent chats first) |
| `api.token` | Archive API bearer token; keep private |
| `send.token` | Separate send/action-request credential; keep private |
| `approve.token` | Approval credential (on macOS kept in the login Keychain instead); never give it to agents |
| `agents.json` | Agent credentials (hashes only), their scopes and chats (for a connected agent, kept equal to the synced chats) |
| `agent-connections.json` | Apps connected from the setup page: which credential each got, the settings file wasync changed, its backup, the entry wasync wrote and any entry it replaced, so Disconnect undoes exactly that |
| `codex-marketplace/` | The WhatsApp plugin as a local Codex marketplace, written from the copy built into wasync when Codex is connected; removed again on Disconnect |
| `access-mode.json` | Only in data folders from earlier releases, for the removed access mode; no longer read, and safe to delete |
| `settings.json` | Everyday settings: "For my AI agents only" or "Also read my chats here", whether photos, videos and files are saved, and whether the setup steps are finished and the furthest one reached, so an unfinished setup opens where it was left |
| `pending-approvals.json` | Requests waiting for a person, so a restart does not lose them |
| `send-lists.json` | Approved phone-number lists for agents, their pacing, the numbers each list dropped after a STOP (so its agent's list status can name them), and for up to 30 days who a removed list already messaged |
| `send-record.json` | Kept for good: when each number first and last got a list message, and the numbers that replied STOP (numbers and times only, never message text) |
| `open-sending.json` | Whether open sending is on, and until when |
| `tailnet.json` | The tailnet name and Tailscale login allowed to open the web app from other devices ([Tailscale](tailscale.md)); absent when off |
| `tailnet-devices.json` | Devices set up to approve from the tailnet: each passkey's public key, name and when it was added and last used ([Tailscale](tailscale.md)); never a secret; removed by `wasync tailnet off` |
| `sent-log.jsonl` | Every send by you or an agent: who, which chat, when, message ID and outcome, never the text |
| `sent-log-backfill.done` | Empty; says earlier list messages were already added to their chats from the sent log, so that runs only once |
| `agent-audit.log` | Agent requests, grants, approvals, reads that only an approved list allowed, and webhook deliveries: identifiers and outcomes only |
| `webhooks.json` | Webhook endpoints and their signing secrets; keep private |
| `webhook-state.json` | Per-endpoint delivery position (journal sequence and retry timing) |
| `webhook-deadletter.jsonl` | Events webhooks gave up on: identifiers and outcomes only |
| `media/`, `media-refs/` | Downloaded attachments and their download keys (see [Media](media.md)) |
| `avatars/` | Small profile pictures of chats, fetched only when a wasync page shows that chat, and asked about again at most once a day |
| `wasync.log` | Structured lifecycle logs from the terminal UI and interactive login, including warnings and errors from the WhatsApp encryption library, which never print to the terminal |
| `service.log` | Output of the macOS login item's `wasync serve` |
| `desktop.log` | The [wasync app](desktop.md)'s messages and what the `wasync` it started says for a person |
| `remove.pending` | Says wasync is stopping to be removed, so the wasync app quits; cleared once the removal is done or given up ([Removing wasync](install.md#removing-wasync)) |
| `remove.log` | What **Settings > Remove wasync** did, when you kept your data |
| `first-open.done` | Says the wasync app's First open screen was seen |
| `device.lock` | Process ownership lock, retained after exit |
| `owner.json` | Who holds the lock right now (command, PID, start time, how it was started, API address); removed when that wasync exits, and ignored when it names a process that no longer runs |
| `*-wal`, `*-shm` | SQLite WAL sidecars when present |

Only one wasync process owns a data directory at a time; see
[the command line](cli.md#one-running-wasync-owns-the-data). The OS releases the lock after
crashes. Never delete the lock file to bypass an active process or run copies of the same device
credentials concurrently.

## Backups

Both databases use WAL, foreign keys, a busy timeout and synchronous FULL. Stop
wasync (`wasync stop`) before a file-copy backup or use SQLite's backup API. Copying only the
live `.db` file can omit WAL contents. Keep session files, archives and backups private
and outside Git. Installers and upgrades never touch this directory.

## Credentials and the Keychain

| Credential | Grants | Who holds it |
| --- | --- | --- |
| `api.token` | Archive queries and capture selection over the [local API](api.md) | You and your own scripts |
| `send.token` | Explicit sending, sync requests, media control and queuing cleanup requests | The `wasync` CLI |
| `approve.token` | Carrying out a request after a person's confirmation; stopping and unlinking through the local API | The running wasync and the interactive CLI |
| Agent credentials (`wsa_…`) | Only the scopes and chats you granted, through `/api/v1` | Agents, via `wasync-axi` (see [Agents](agents.md)) |

None of these is printed in command output (an agent credential is printed once only if you
decline to save it for wasync-axi). The default loopback channel is local HTTP, not transport
encryption. A holder of `send.token` can request sending any regular local file the owner
process can read; it is a privileged credential, not a read-only API key. Agents should receive
only their own scoped credential; filesystem access as your OS user is not a security boundary
against a malicious agent.

On macOS and Windows the running wasync also asks you to confirm every approval
with Touch ID or your password (macOS) or Windows Hello or your password (Windows), whichever
way the approval arrived; see [Agents](agents.md#approving-without-a-terminal). Where a computer
has no such prompt (Linux, or a session with no screen), the typed phrase is the only check and
is enforced by the interactive CLI and terminal UI, not by the HTTP endpoint.

On macOS `approve.token` is kept in your login Keychain instead of a file (an existing file is
moved there when wasync next starts), so another program that asks for it makes macOS ask for
your password; Windows and Linux keep the private file. If you cancel or deny the Keychain
prompt when wasync starts by itself at login, it stops without restarting and shows one
notification: approvals stay off until you run wasync again. A shell outside your desktop
session, such as SSH, cannot open the Keychain at all, so commands that need the approval
credential (approving, `wasync logout`) fail there; `wasync stop` still works, because it then
asks your own wasync process to stop as Ctrl-C would. There is one Keychain item per data
directory (named `approve:` plus a short hash of its path, service `wasync`), so a wasync
started with a temporary `--data-dir` leaves an item behind. `wasync keychain` lists them
without reading any secret, and `wasync keychain prune --apply` removes the ones whose recorded
folder is gone, always keeping the item for the data directory you are using; without
`--apply` it only shows the plan. Items made before wasync recorded the folder are
listed as "unknown folder - not removed" and are never deleted, because prune cannot
tell whether a running wasync still uses them; to remove one, open Keychain Access,
search for wasync and delete the named item. If macOS refuses to let this build of wasync edit
an item an earlier build made (error -25244), wasync replaces the item once; if it cannot, it
says which item to delete in Keychain Access instead of asking again.

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

## The web app is local

The web app (`wasync web`, `wasync setup`, `wasync approvals`) is served by your running wasync
on its loopback address. It opens through a one-time link that works for two minutes and is
never printed. A signed-in page stays signed in while you use it (30 minutes after the last
page, at most 12 hours); after that, or after wasync restarts, **Sign in** on the page asks your
computer to confirm (Touch ID, your password or Windows Hello), or run `wasync web` again. The
page shows message text, so open it only on a computer you trust.

If you turn it on, your other devices on your Tailscale network can open it too, through
`tailscale serve`; see [Opening the web app from your other computers](tailscale.md).

wasync asks GitHub for its newest release only when you press **Check for updates** in
Settings; the request carries no credential and nothing about you. Installing an update
needs your confirmation on the computer running wasync, and the download must match its
published checksum and signature first; see
[Updating from the web app](install.md#updating-from-the-web-app).

## Linked-device identity

New pairings identify the client as **wasync (macOS)**, **wasync (Windows)** or
**wasync (Linux)** with the desktop platform type. WhatsApp chooses the linked-device
icon and final label; there is no supported custom icon upload. This is an
unofficial client, not an official browser or verified WhatsApp application.
Existing linked devices may keep their old label until you explicitly logout and
pair again. Updating does not automatically logout or delete an archive.

## Using an unofficial client

wasync is an unofficial WhatsApp client built on [whatsmeow](https://github.com/tulir/whatsmeow):
protocol compatibility, WhatsApp terms and account restriction risks apply. Obtain appropriate
permission before archiving or sharing conversations. wasync posts to conversations only when
asked: `send`, `reply`, `react` and `edit` (and the web app's message box) act on your command,
or on an agent credential you granted the matching scope, and deletions are never sent
automatically. There is no cloud service and no AI processing in wasync itself; the web app and
API run only on your computer. Attachments of synced chats download by default; see
[Media](media.md) to keep text only.
