# The terminal UI

> **The terminal UI is being retired.** Chats, sent messages, agents and approvals are in the
> web app (`wasync web`), and `wasync serve` keeps syncing without a terminal screen. The
> terminal UI keeps working for now and says so when it closes; it will be removed in a later
> release.

wasync has a full-screen terminal UI, built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).
Running `wasync` with no command opens it, as do `wasync tui` and `wasync run`, and an
interactive `wasync login` opens it after pairing. While it is open it is the running wasync
that owns the data directory: it captures enabled chats, serves the local API and web app, and
handles approvals, just like `wasync serve`. Quitting stops capture and the local API and keeps
the device linked. Fullscreen operation has no three-minute command timeout; the `--timeout`
setting still applies to the scriptable commands.

```sh
wasync                     # open the terminal UI and capture enabled chats
wasync tui --offline       # browse the archive without connecting
wasync --no-api            # terminal UI with the API and command channel disabled
```

If no session exists, pairing happens inside the terminal UI. Open WhatsApp on your phone
and select **Linked Devices → Link a Device**. Resize your terminal if the QR code
needs more space. After authentication, chats are discovered with capture
disabled by default. Enable the chats you want using Space. Enabling automatically
queues a request for older messages from your phone (see [History](history.md)). Log lines go
to `wasync.log` in the data directory, including warnings and errors from the WhatsApp
encryption library, which never print to the terminal.

## Keys

| Key | Action |
| --- | --- |
| ↑ / ↓ or j / k | Move through chats or messages |
| Mouse wheel / click | Scroll / select a row; click a checkbox to toggle capture |
| PgUp / PgDn, Home / End | Jump through the list |
| Tab / Shift+Tab, or → / ← | In the chat list, switch between the Groups, Direct and All tabs (forward / back) |
| 1 / 2 / 3 | In the chat list, jump straight to Groups / Direct / All |
| Space | Enable or disable capture for the selected chat |
| A / N | Select all / none in the current tab, after confirming the count |
| Enter | Open the chat's archive; in messages, expand the full message |
| / | Filter chats; in the archive, search message text |
| e | Show enabled chats only |
| n / p or → / ← | Older / newer message page (in the message view) |
| R | Return to the latest message page |
| Ctrl+D / Ctrl+U | Scroll the selected message body |
| ↑ / ↓ in expanded message | Scroll the full message body |
| r | Refresh chat discovery |
| h | Queue/retry older history for the selected/open chat |
| m | On a chat, cycle its media policy `manual` → `auto` → `inherit`; on a message, download its attachment once |
| c | Connect or retry pairing |
| d | While browsing messages, clear that message's local archived content |
| D | Disable and purge the selected chat's archive, after confirmation |
| X | On your own archived message, request WhatsApp deletion for everyone |
| W | In the chat list, delete WhatsApp chat state for your account |
| M | Choose how agents get chats: ask me each time, or sync all chats (shown once after pairing) |
| L | Unlink the device and quit, after confirmation |
| Esc | Close details, return to chats, or clear the filter |
| ? | Show keyboard help |
| q or Ctrl+C | Quit; keep the device linked |

The cleanup and deletion keys (`d`, `D`, `X`, `W`) are explained in
[Cleanup and deletion](cleanup-and-deletion.md); media policies in [Media](media.md).

## The chat list

The tab strip at the top of the chat list always shows **Groups**, **Direct** and **All**
with the active one in brackets, how many chats each holds and how many are on
(`[ Groups 206 · 5 on ]  Direct 506 · 0 on`). Switch with **Tab** / **Shift+Tab**,
**←** / **→**, **1** / **2** / **3**, or a mouse click on a tab. Direct chats require
explicit selection.

The top **Select all** / **None** controls support mouse clicks or **A** / **N**.
Confirm the count before applying. They apply to the current tab, ignoring
name and enabled-only filters. The default Groups tab affects groups only.

Chat details show archived counts, last message time, observed history state and
the potential gaps while this chat was being captured (the account's stretches of lost
observation that overlap the time capture has been on for the chat, so chats turned on at
different times differ; 0 while capture is off). A group WhatsApp supplies no name for is
listed as `Unnamed group …` followed by the last six digits of its ID. An enabled chat
with no messages yet while its older history is queued or requested says so, and the
status line counts such chats ("history still arriving for N chats") and any messages
from enabled chats that could not be decrypted this session. Enabled chats appear first. The
selected chat stays selected when a refresh changes the ordering.

## Browsing messages

The message list uses keyset pagination; new arrivals do not shift an older page while you
browse it. Search uses a literal FTS5 phrase and is scoped to the open chat. The latest archive
page refreshes while you browse it. Older pages stay anchored. Full message detail works in
narrow terminals through Enter. Untrusted chat names, sender labels and message bodies are
escaped before terminal rendering.

Attachment details show MIME type, original filename/title when supplied, size, dimensions,
duration, document page count, voice-note/animation flags, download state and local file
location.

## History in the terminal UI

**Capture details** show `Older history queued`, `Requesting 50 older messages`, and
`Waiting for history from your phone` while wasync asks your phone for older messages. Press
**h** on a chat, or while browsing its messages, to queue a retry after a timeout or failure.
Do not confuse **n/p**, which browse local pages, with **h**, which asks the phone for more
history. For an empty chat with no known cursor, the UI says **Waiting for a known message**.
See [History](history.md) for how this works.

## Approvals in the terminal UI

Agent requests for chats, and cleanup or deletion requests, open as dialogs. A chat request
lets you pick the chat with ↑/↓ and the history depth with Tab; a destructive request shows the
exact action. Either way you type the exact confirmation shown in the dialog (for a destructive
request, the `DELETE CODE` it displays), then Enter. **n** or Esc rejects, and nothing is
granted. Where your
computer can show a system confirmation, it asks too. The same requests appear on the web app's
Approvals page; see [Agents](agents.md#approving-without-a-terminal).

## Logging out and re-pairing

**L**, then **y**, unlinks the device and exits; `wasync logout` from any folder does the same
through the running terminal UI, which then exits. **q** quits while keeping the pairing
session.

After logout, the terminal UI hides cached chats until the newly paired account is
authenticated. QR pairing uses a compact full-screen layout, so terminal space is
reserved for the QR rather than archive borders and controls. If the code still
does not fit, enlarge the terminal or reduce its font size. Archives are preserved
across logout and re-pairing; no database deletion is needed.
