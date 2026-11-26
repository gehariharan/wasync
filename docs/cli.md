# The wasync command line

Everything wasync does can be driven from the `wasync` command. This page covers the commands in
more depth than the [README](../README.md#command-line). The built-in help is always the
authoritative reference for the installed version.

## Finding commands

Help never opens the archive or connects to WhatsApp. It is written to stdout and exits
successfully:

```sh
wasync help                      # global flags and command overview
wasync help query messages        # detailed help for any command path
wasync media policy --help        # equivalent command-specific help
wasync help --json                # versioned command catalog, usage and notes, for agents
```

Global flags such as `--data-dir`, `--timeout`, `--api-addr` and `--no-api` must come before the
command.

## One running wasync owns the data

Only one wasync process owns a data directory and its WhatsApp connection: `wasync serve`, the
`wasync setup` page, the [terminal UI](tui.md) (`wasync`, `wasync tui`, `wasync run`), or the
login item. Any other command, run from any folder, either works through that owner or says
exactly which wasync it is (its kind, PID, how it was started, the data directory and its API
address) and the command that stops it, `wasync stop`. `status`, `logout`, `groups`, `chats`,
`sync`, `send` and the other owner commands go through the running owner over its authenticated
loopback channel; `login`, `serve`, `setup`, `tui` and `run` cannot start a second one. `query`
commands read the archive directly and work with or without an owner. Never delete the lock
file to bypass an active process or run copies of the same device credentials concurrently.

Use the same `--data-dir` and `--api-addr` as the owning process. `--no-api` disables that
cooperation; stop the owner with `wasync stop` before running a command that needs the device
lock. The commands find the owner from `owner.json`, so they work even when the owner was
started with a different `--api-addr`.

`wasync stop` stops the owner (a `serve` started by hand or by the login item, the setup page,
the terminal UI or `run`) cleanly and waits until the data directory is free. It asks the owner
through its local API, or, when the owner runs without the API, asks its process to shut down as
Ctrl-C would (on Windows, end the process it names). It does not unlink the phone and does not
remove the login item (`wasync service uninstall` does). Nothing restarts it by itself until
your next login; `wasync service restart` starts the login item's wasync again at once, after a
stop or an upgrade, and also moves a wasync running in a terminal to the background.

To unlink from the CLI, run `wasync logout` from any folder. While an owner is running, logout
goes through it: it says which owner it used, unlinks the device, and that owner then stops,
because it cannot capture without a linked phone. Run `wasync setup` or `wasync login`
afterwards to pair a new phone; nothing needs to be killed. Unlinking and stopping are signed
with the approval credential, not the sending credential, so something that can only send
messages cannot unlink the phone or stop capture. Logout preserves archived data and reports a
failed remote unlink as an error.

## Working without a browser or terminal UI

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
when WhatsApp supplies their metadata; they are disabled by default. See
[History and capture](history.md).

`wasync groups enable --all` and `wasync groups disable --all` act on all cached groups; newly
discovered groups remain disabled. `wasync chats list|enable|disable` does the same for group
and direct chats.

### Diagnostic login and status

In an interactive terminal, plain `wasync login` pairs (or reuses the saved session), caches
your groups and then opens the terminal UI, which captures enabled chats. `--select=false` or
`--observe` keep diagnostic output instead; without a terminal, `login` always prints the group
table and exits. Log lines go to `wasync.log` in the data directory during an interactive login.

```sh
wasync login --select=false
wasync groups list
wasync groups list --refresh --json
wasync groups enable '123456789@g.us'
wasync groups disable '123456789@g.us'
wasync status
wasync --timeout 5m login --select=false --observe 60s
wasync logout
```

Cached list and preference commands open no WhatsApp connection. `--refresh`
connects only to refresh discovery. `status` inspects local state (through the running owner
when there is one); it cannot tell whether the saved device was revoked remotely. Observation
counts all received events but discards bodies, even for enabled chats.

## Reading the archive (agent CLI queries)

`query` commands are read-only JSON interfaces:

```sh
wasync query groups --enabled
wasync query chats --enabled
wasync query messages --chat '123456789@g.us' --limit 50
wasync query search "meeting" --chat '123456789@g.us'
wasync query messages --chat '123456789@g.us' \
  --since 2026-10-01T00:00:00Z --until 2026-10-02T00:00:00Z \
  --sender '123456789@lid'
wasync query status
wasync query --help
```

`--group` is a legacy alias for `--chat`; do not combine them. Newer queries emit the versioned v1
envelope (`--v1` opts existing queries in):
`query version`, `query message --ref REF`, `query thread --ref REF [--before N]
[--after N]`, `query chats --v1 --match QUERY`, `query search PHRASE --v1
--context N`, `query changes [--after CURSOR] [--limit N]`, `query checkpoint
--consumer NAME` and `query doctor`. `wasync help COMMAND` documents each one.

All query results are JSON on stdout by default; `--json` is also accepted for
explicit agent invocations. Diagnostics go to stderr. Exit codes are 0 for
success (including empty results), 1 for errors and 130 for cancellation. For v1
envelopes and matching totals, see the [interface contract](architecture/axi-contract.md#response-envelope-v1-only).
Legacy group results contain `account_id` and `items`. Legacy message/search
results also contain `next_cursor`. Pass a nonempty cursor as `--cursor 'VALUE'` with the same filters
to retrieve the next page. Page size is 1–200, default 50; newest messages come
first, with stable ordering for equal timestamps. Cursors for the CLI and API
are separate formats. CLI cursors contain no secrets and are scoped to the
current account and filters; they are not an authorization mechanism.

Search uses literal phrase matching with SQLite FTS5. A chat is required for
`messages` and optional for `search`. `--since` is inclusive and `--until` is
exclusive; timestamps are RFC3339. Query output uses the same message and group
fields as the API. Expired and pending messages are excluded; revoked messages
have empty bodies. Disabled chats retain query access to their existing archive.

Queries open the existing archive in SQLite read-only mode, using normal WAL
coordination to see ongoing commits. They acquire no device lock, open no
WhatsApp connection, require no API/token, run no migrations, and create no
credentials. Missing or incompatible archives produce an actionable error;
start the matching version of wasync (`wasync serve` or the terminal UI) once to create or
migrate the archive. `--data-dir` and `WASYNC_DATA_DIR` select the same archive as other commands:

```sh
wasync --data-dir /absolute/path/to/private/wasync query groups
```

Each group in `query groups` carries `potential_account_gaps` (every gap the account has
had, the same for all chats) and `potential_chat_gaps` (only those that overlap the time
capture has been on for that chat). `query status` reports persisted archive counts,
capture timestamps, history observations and potential gaps. Its `connection` is always `unknown`: an
unfinished capture run or recent heartbeat cannot prove that capture is currently
running or connected. Querying works with capture stopped, but the archive only
updates while capture is active. Pagination reads the live archive rather than a
frozen snapshot and is not a complete replication feed.

## Direct chats and contact names

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
part of a name), the web app, the terminal UI, the local API and `wasync-axi`. Names are
personal data: they are stored in the local archive only, never logged, and
`wasync-axi` prints them as untrusted. A contact you have not saved and who has
never messaged you keeps its number as its name.

```sh
wasync chats list
wasync chats enable '15551234567@s.whatsapp.net'
wasync query chats --enabled
wasync query messages --chat '15551234567@s.whatsapp.net'
wasync query search 'meeting' --chat '15551234567@s.whatsapp.net'
```

## Sending, replying, reacting and editing

wasync posts to a conversation only when asked: each `send`, `reply`, `react` or `edit`
invocation acts once, as you. (Agents use the same operations through `wasync-axi`; see
[Agents](agents.md#what-agents-can-do).) You can also write from the web app's Chats page.

```sh
# Each invocation explicitly sends one conversation message.
wasync send --to '123456789@g.us' --text 'Hello'
wasync send --to '15551234567@s.whatsapp.net' --text 'https://example.com'
wasync send --to '123456789@g.us' --file ./photo.png --type image --text 'Caption'
wasync send --to '15551234567@s.whatsapp.net' --file ./report.pdf --type document
wasync send --to '15551234567@s.whatsapp.net' --file ./explainer.mp4 --type video --text 'Caption'
```

Sending supports text/URLs, JPEG/PNG images, videos and documents. WhatsApp determines
which sizes and formats it accepts; wasync adds no fixed file-size cap for images and
documents (see [Media](media.md#media-size)). The default attachment type is document. Rich
link-preview generation is not implemented.

`--type video` sends a clip that plays in the chat, with `--text` as its caption. It accepts
an MP4 file with H.264 video and, when it has sound, AAC audio, up to 64 MiB, the most
WhatsApp's linked devices send as a playable video; anything else is refused before anything
is sent, with a message saying what to change (for example
`ffmpeg -i IN -c:v libx264 -c:a aac -movflags +faststart OUT.mp4`). Send a larger or
different file with `--type document`, where it arrives as a file. wasync reads the clip's
length and shape from the file itself. Its preview picture is a frame taken with `ffmpeg`
when it is installed (it is optional, looked up on `PATH` and in the usual Homebrew and system
folders), otherwise a plain dark frame; the video plays the same either way.
Output is JSON with `result.status` and `result.message_id`. A server acknowledgement
is not proof of delivery or reading. An unknown outcome must be checked in WhatsApp
before retrying. wasync does not add an application retry loop; whatsmeow may retry
the same protocol frame using its existing message ID.

When an owner runs, `send` reuses its authenticated local API. Supply the same `--api-addr`
if you changed the default. With `--no-api`, stop the owner before sending. When no owner runs,
`send` acquires the device lock, reuses the paired session and closes it after the request. No
test sends real messages automatically. Read-only `query` commands never send messages.

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
Quoted text is copied into the reply only when the target is a text message.

### Reactions

```sh
wasync react --chat '123456789@g.us' --message-id 'ABCDEF123' --emoji '👍'
wasync react --chat '123456789@g.us' --message-id 'ABCDEF123' --remove
```

`react` sets your reaction on an archived message, or clears it with `--remove`;
give exactly one of `--emoji` and `--remove`. WhatsApp keeps one reaction per person
per message, so a new emoji replaces the old one. `--emoji` must be a single emoji
(words, plain digits and ASCII punctuation are refused). The target rules, owner reuse
and one-attempt behaviour are the same as for `reply`. Your own sent reactions are archived
like incoming ones, including removals.

### Editing your own messages

```sh
wasync edit --chat '123456789@g.us' --message-id 'ABCDEF123' --text 'Meet at 7, not 6'
```

`edit` replaces the text of a message you sent. wasync refuses, before connecting or
sending anything, a message that is someone else's, is not plain text (media captions
cannot be edited), was deleted, or was first sent more than WhatsApp's edit window ago
(about 20 minutes, counted from the original send; later edits do not extend it). The
error says which rule applied. Your edit is archived like one made on your phone, so
`edited_at` and the new text appear in queries.

The signed owner routes these commands use are listed in [Local API](api.md#signed-owner-routes).

## Cleanup, deletion and media

- `wasync clean` and `wasync delete` only queue requests that a person approves; see
  [Cleanup and deletion](cleanup-and-deletion.md).
- `wasync media policy` and `wasync media download` control attachment downloads; see
  [Media](media.md).
