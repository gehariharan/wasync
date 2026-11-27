# Agents: access, approvals and sending

An agent (Claude Code, Codex, OpenCode or your own program) never talks to WhatsApp itself. It
uses a scoped credential you create, through the `wasync-axi` command or the local `/api/v1`, and
wasync checks every request against what you allowed. This page explains how that works. The
[README](../README.md#agents) has the short version, and the
[wasync-axi README](../axi/README.md) documents the agent command itself.

## Credentials and scopes

`wasync agents grant|list|revoke` manages agent credentials. A human must run `grant` and `revoke`
in an interactive terminal and type the agent's name; there is no `--yes`, and none of these
commands is available over the API. On a computer with a system confirmation (Touch ID or
password on macOS, Windows Hello or the account password on Windows), wasync also asks you to
confirm there. The web app's **Set up** page can connect an agent without a terminal
(see below).

Scopes are `read.chats`, `read.messages`, `read.media`, `observe`, `send`, `react`, `edit.own`,
`chat.state`, `group.admin`, `capture.manage` and `export`; without `--scope` an agent gets
`read.chats,read.messages`. Which chats an agent sees depends on how you create it: with
`--synced-chats` it uses every chat you sync in wasync (below); with `--chat` it gets the chats
you list; with neither, a read-only agent can read every archived chat (the Agents page marks it
"reads every chat"). An agent with [approved numbers](#approved-numbers) can also read the
chats of the numbers its list has messaged. The write scopes `send`, `react`, `edit.own`, `chat.state`, `group.admin`
and `capture.manage` always need a chat list. There is no destructive scope: cleaning, deleting, revoking and
purging always need a person's approval. `--rate` sets requests per minute (default 60) and
`--expires` an expiry (none by default). `wasync help agents grant` has the full reference.

After you confirm, wasync offers to save the credential straight into wasync-axi's private
credential file (owner-only, in wasync-axi's config folder), so there is nothing to copy by
hand. If you decline, it prints the credential once so you can put it in `WASYNC_AXI_TOKEN` or
your agent's own secret store. wasync stores only its hash, never replaces a different saved
credential without asking, and never accepts its own `api.token`, `send.token` or
`approve.token` as an agent credential. Revoke an agent any time with `wasync agents revoke NAME`.

## Which chats an agent uses

A connected agent uses every chat you sync in wasync, now and later. Press **Connect** on the
web app's **Set up** page, or run:

```sh
wasync agents grant helper --scope read.chats,read.messages,send,react --synced-chats
```

The agent can then read, reply and react in each synced chat without asking you. Connect gives
exactly those four scopes and no expiry. Editing its own messages (`edit.own`) is off unless you
add that scope in a terminal; other scopes and an expiry also need `wasync agents grant`.

wasync keeps the agent's chat list equal to your synced chats by itself: before each request from
the agent, when wasync starts, and when the Agents page opens. Each change is recorded in the
audit log as `synced_chats`. To widen or narrow what the agent can use, sync or unsync a chat:
in the chat picker on the Set up page, with `wasync chats enable` or `wasync chats disable`, or in
the [terminal UI](tui.md). The agent cannot sync a chat itself.

If you would rather fix the list, `--chat "Family"` pre-approves a chat by the name in your
archive instead of a raw ID: wasync shows the matching chat with its ID and asks you to confirm,
and refuses ambiguous or unknown names. A raw chat ID also works. `--chat` and `--synced-chats`
cannot be combined.

Credentials created with `--on-demand` in earlier releases keep exactly the chats you approved for
them and are never widened; requests they had waiting were dropped. The Agents page marks them
"chat(s) approved earlier". To move one to synced chats, revoke it and connect it again.
`--on-demand` itself is now refused, with a pointer to `--synced-chats`.

## Sending to other chats and new numbers

An agent asks you before it sends to a chat that is not synced, or to a phone number it may not
message. It runs `wasync-axi send-request --chat JID --text TEXT` (or `--to PHONE`), which uses
`POST /api/v1/send-requests`; through MCP the tool is `whatsapp_request_send`. Nothing is sent
yet: the agent gets a request ID and checks the answer with `wasync-axi send-request status ID`.
A request carries text only, up to 4096 bytes. One agent can have at most 5 waiting, asking
again with the same chat and text returns the same request, and each request lapses after 30
minutes and then changes nothing. Waiting requests are kept in a private file
(`pending-approvals.json`), so restarting wasync does not lose one.

You answer each message on its own, on the web app's **Approvals** page (`wasync approvals`), in
the [terminal UI](tui.md), or with `wasync actions approve REQUEST_ID`, where you type `SEND`
followed by the last six characters of the ID. You see the agent, the chat or number, and the
whole message. Approving sends exactly that message once, from wasync, and gives the agent no
other access. There is no way to approve several at once. **Not now** declines one; **Not now to
all** declines every waiting agent request. A headless `wasync serve` prints (and logs) one line
per new request naming only the agent and the request ID, never the chat, number or text;
`wasync actions list` shows what each one asks to send.

To let an agent message many numbers without approving each message, use an
[approved number list](#approved-numbers) or [open sending](#open-sending).

## Approving without a terminal

```sh
wasync approvals          # opens the Approvals page in your browser
wasync web                # opens the web app (its Approvals link is the same page)
```

With wasync running, the Approvals page lists the messages agents are asking to send and,
separately, your own [cleanup and deletion requests](cleanup-and-deletion.md). For a send
request you see the agent, the chat or number and the whole message, and you press
**Approve**. wasync then asks **your computer** to confirm: Touch ID or your account password on macOS, Windows Hello or your
password on Windows. From another device on your tailnet, that device's passkey asks instead
(see [Tailscale](tailscale.md)). The operating-system prompt names exactly what you are approving, in words
wasync wrote from its own records, never the agent's. Nothing on the page, in a notification,
or in a signed request can approve on its own. The link `wasync approvals` opens works once and
only for two minutes, and is never printed.

A desktop notification tells you when an agent is waiting. It names only the agent
and carries no code or button; run `wasync approvals` to review.

The same confirmation applies when you approve in the terminal UI or with
`wasync actions approve`: after you type the phrase, your computer asks too. If
your computer cannot show that prompt (Linux, or a session with no screen), wasync says so
when it starts, the page only lists requests, and approvals stay in the terminal, where the
typed phrase is the only check. Creating or revoking an agent credential
(`wasync agents grant`, `agents revoke`) asks for the same confirmation after you type the
agent's name. The [threat model](architecture/threat-model.md) explains what this does and
does not protect.

On macOS the approval credential (`approve.token`) is kept in your login Keychain; see
[Data, files and security](data-and-security.md#credentials-and-the-keychain).

> **Windows:** wasync asks for Windows Hello first and, if Hello is not set up,
> for your account password in the standard Windows security dialog. These prompts
> and the notification have not yet been run on a real Windows computer by the
> author. They build and are covered by the Windows checks, but treat them as
> untested until you have tried an approval.

## The Agents page

`wasync web`, then **Agents**, shows every agent with its scopes and its chats: for a connected
agent, every synced chat and how many there are now. From there you can:

- **Allow sending** for an agent that has a chat list but no `send` scope (adds the scope after
  your system confirmation), or **Stop sending** (no confirmation needed). It still sends only
  in its own chats, on its approved numbers, or while open sending is on, and asks you for
  anything else.
- Approve a list of **approved numbers** for one agent (below).
- Switch **open sending** on for an hour (below).

On a computer with no system confirmation the page is read-only for anything that gives an agent
more power; stopping things still works.

### Approved numbers

To let one agent message a set of phone numbers without approving each message, approve a list for
it under **Approved numbers**: paste the numbers or choose a .txt or .csv file, one number with
its country code per line (up to 1,000), and pick how many new numbers an hour (5 to 20) and
when the list ends (in 1, 7 or 30 days, or never). wasync asks you to confirm, showing the count
and the first numbers. Approving again replaces that agent's list. The agent may then message
those numbers only: first messages are spaced at random through the hour, up to the pace you
picked (and three times that in messages), and one message per number until that number
replies; a number that asks to stop is removed from every agent's list and is never listed again,
even if you approve it later. Only replies that arrive after wasync's first list message to that
number count, so older chat with that person never removes it. That counts any reply with the word "stop",
"unsubscribe", "opt out", "remove me" or "take me off", in any case and with any punctuation
("Stop.", "STOP!", "please stop", "stop messaging me"), and a reply that is only "cancel",
"end", "quit" or "revoke". A word that merely contains "stop", such as "nonstop", does not count. wasync archives a listed
number's chat from its first list message on, so that message shows in the chat on the Chats page and wasync
can see the replies, including a listed contact's replies that WhatsApp delivers in a separate
hidden-ID chat, unless you already turned archiving of that hidden-ID chat on or off yourself.
A chat wasync archives only for a list is not one you synced: other agents that use every synced
chat do not get it, and **Choose chats** shows it unticked until you tick it yourself.
Releases before this one did not keep that difference, so when an archive that already had synced
chats is upgraded, wasync, once and as soon as your phone is linked, treats the synced chat of
every number an approved list holds or ever messaged as one it archives only for a list. That includes a chat you synced yourself whose number is also on a
list: tick it again in **Choose chats** to share it with your agents. A list contact's separate
hidden-ID chat that an earlier release followed is not recognised this way.

**Pause** stops a list's sends and keeps everything else; **Resume…** asks you to confirm.
Removing a list needs no confirmation. The one-message-until-reply rule belongs to the number,
not the agent: wasync remembers for good when any agent's list last messaged each number, so no
list sends it another message until it replies to that one, even when two agents' lists hold the
same number, after a list is removed, or when the same numbers are approved for another agent.

The agent may also read those chats, so it can see the replies and any STOP without you
syncing each chat: with the `read.messages` and `read.chats` scopes it already holds, it may
read the chat of every number on its list that a list has messaged and the hidden-ID chat wasync
links to that number. It cannot read a number on its list that no list has messaged yet, a
number its list dropped after a STOP, another agent's numbers, or any other chat, and a list
never adds a scope, media or the change feed. A number counts as messaged once any agent's list
messaged it, the same rule the sending follows. Reading stops when you remove the list, when it
ends, when the number replies STOP, or when you approve it again without that number; a paused
list can still be read. So that the agent can record an opt-out, `GET /api/v1/send-list` names
the numbers its list dropped after a STOP, with no message text. The agent reads the whole
archived chat with that person, not only what came after its first message, so list only
numbers whose chat you are happy for the agent to read. Each read that names such a chat, and
each chat list or search over every chat that returns one, is recorded in the audit log as
`read_list`, one entry per such chat, and the Chats page marks those chats as readable by an
agent.

### Open sending

Switching open sending on asks you to confirm with Touch ID, your password or Windows Hello,
and lasts one hour: any agent that already holds the `send` scope may then message any chat or
phone number, up to 15 messages and 5 new numbers an hour in total. Each message is marked on
the Sent page. Turning it off needs no confirmation. On a computer with no system confirmation
open sending cannot be switched on.

### The Sent page and log

Every send, by you or by an agent, is noted in `sent-log.jsonl` in the data folder (who, which
chat, when, the WhatsApp message ID and the outcome, never the text) and listed on the web app's
**Sent** page, which reads the text back from your archive. "Sent" means WhatsApp accepted the
message, not that it was delivered or read.

A message wasync sends is kept in its chat as your outgoing message only when that chat is
archived; a message an agent sent is marked there with the agent's name, as on the Sent page. A
send to an approved-list number switches that number's chat on before the message goes, even if
you turned it off, so the message is kept in that chat and the chat moves to the top of **Chats**.
Other sends never switch a chat on. Releases before this switched a listed number's chat on only
after the first message went, so the first start of this one adds those earlier list messages to
their chats, once, from the sent log: they show at the time they were sent, without their text,
which was never stored. If WhatsApp later delivers the real copy, it replaces the one added.

## What agents can do

Agents read through `wasync-axi` (or `/api/v1`) and write only with the matching scope and a
chat they may use (for a connected agent, a synced chat). Each write is attempted once and never retried automatically; an unknown outcome
must be checked in WhatsApp first.

| Agent command | API route | Needs |
| --- | --- | --- |
| `wasync-axi send --chat CHAT_JID --text TEXT` or `--to PHONE_NUMBER` | `POST /api/v1/messages` with `{ "chat", "text" }` or `{ "to", "text" }` | `send`, and a chat the agent may use, a number on the agent's approved list, or open sending |
| `wasync-axi send (--chat CHAT_JID \| --to PHONE_NUMBER) --video FILE.mp4 [--text CAPTION]` | the same route with `"video"` (the MP4's bytes, base64) and, when one was taken, `"thumbnail"` (the preview frame, base64) | exactly as for text: one message under the same scope, approvals, list pacing, open-sending caps and rate limit |
| `wasync-axi reply REF --text TEXT [--mentions JID,JID]` | `POST /api/v1/messages/{ref}/reply` with `{ "text", "mentions" }` | `send` and a chat the agent may use |
| `wasync-axi react REF EMOJI` / `--remove` | `PUT` / `DELETE /api/v1/messages/{ref}/reaction` | `react` and a chat the agent may use |
| `wasync-axi edit REF --text TEXT` | `POST /api/v1/messages/{ref}/edit` with `{ "text" }` | `edit.own` and a chat the agent may use |
| `wasync-axi send-request (--chat CHAT_JID \| --to PHONE_NUMBER) --text TEXT`, `send-request status ID` | `POST /api/v1/send-requests`, `GET /api/v1/send-requests/{id}` | `send`; nothing is sent until you approve that one message ([above](#sending-to-other-chats-and-new-numbers)) |

wasync records each attempt with its scope, chat and ref, never the text, in the audit log
(`agent-audit.log`). Errors use the v1 envelope: `target_unavailable` (deleted or a reaction),
`not_found` (not archived or another account), `owner_offline` (wasync is not connected),
`not_editable` and `edit_window_expired` for edits, and `send_failed`, whose outcome is unknown
and must not be retried automatically. The rules each write follows (quoting, mentions, single
emoji, WhatsApp's edit window) are the same as for the [command line](cli.md#sending-replying-reacting-and-editing).

An agent sends a video as its bytes, not as a path, so wasync never opens a file on an agent's
behalf. `wasync-axi send --video` reads the file with the agent's own permissions and takes a
preview frame with `ffmpeg` when that is installed on the agent's side; wasync itself never runs
`ffmpeg` on what an agent sends. It accepts the frame only as a JPEG of at most 64 KiB and 320
pixels on each side, passed on unchanged, and a video with no frame gets a plain preview. wasync checks the video (MP4, H.264 video, AAC audio, at most 64 MiB) before the send
counts against any pacing and refuses anything else with `invalid_argument`. A wasync that can
take videos lists the feature `write.send.video`.
The full agent interface is in the [interface contract](architecture/axi-contract.md).
