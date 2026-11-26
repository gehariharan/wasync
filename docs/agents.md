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
confirm there. The web app's **Set up** page can create a read-only agent without a terminal
(see below).

Scopes are `read.chats`, `read.messages`, `read.media`, `observe`, `send`, `react`, `edit.own`,
`chat.state`, `group.admin`, `capture.manage` and `export`; without `--scope` an agent gets
`read.chats,read.messages`. Which chats an agent sees depends on how you create it: with
`--on-demand` it starts with none and asks for each one; with `--chat` it gets the chats you
list; with neither, a read-only agent can read every archived chat (the Agents page marks it
"reads every chat"). The write scopes `send`, `react`, `edit.own`, `chat.state`, `group.admin`
and `capture.manage` always need a chat list. There is no destructive scope: cleaning, deleting, revoking and
purging always need a person's approval. `--rate` sets requests per minute (default 60) and
`--expires` an expiry (none by default). `wasync help agents grant` has the full reference.

After you confirm, wasync offers to save the credential straight into wasync-axi's private
credential file (owner-only, in wasync-axi's config folder), so there is nothing to copy by
hand. If you decline, it prints the credential once so you can put it in `WASYNC_AXI_TOKEN` or
your agent's own secret store. wasync stores only its hash, never replaces a different saved
credential without asking, and never accepts its own `api.token`, `send.token` or
`approve.token` as an agent credential. Revoke an agent any time with `wasync agents revoke NAME`.

## Letting an agent ask for chats (just-in-time access)

You do not have to pick an agent's chats up front. Create the credential with
`--on-demand`; it then starts with no chats at all:

```sh
wasync agents grant helper --scope read.chats,read.messages,send --on-demand
```

When the agent needs a chat it cannot use, its refusal points at
`wasync-axi access request --chat JID` for that exact chat; an agent can also ask
by name with `--match NAME` (a contact or group name, or a phone number). `--send`
also asks to send, reply and react; `--edit` also asks to edit its own messages.
wasync resolves the request to concrete candidate chats that only you see and
queues a pending request with the same rules as other approvals:
30-minute expiry, no replay, a person's confirmation, no `--yes`. Pending requests are
kept in a private file (`pending-approvals.json`), so restarting wasync does not lose
one that is still waiting.
The agent is always told "pending", whether or not anything matched, so it
cannot use a request to find out which chats you have. A request that matched
nothing shows as "no chat matched" on the Approvals page, in the terminal UI and in
`wasync actions list`; you can reject it, it cannot be approved, and it expires. Direct
chats are named after the saved contact when WhatsApp provides one; a phone number always works.

You decide on the web app's **Approvals** page (`wasync approvals`), in the
[terminal UI](tui.md), or with `wasync actions approve REQUEST_ID` in a terminal. A headless
`wasync serve` prints (and logs) one line per new request naming only the agent and the request
ID, never a chat or number; `wasync actions list` shows what each pending request asks for.
Before confirming you choose:

- the chat, when several match;
- the history depth: **from now on only** (new messages, nothing older is
  requested) or a **full history sync** (also asks your phone for the chat's
  older messages, best effort; see [History](history.md));
- you see whether the agent asked to read only, to also send/reply/react, or
  also to edit its own messages.

On approval wasync turns on capture for that chat, applies the history choice,
and extends that agent to that one chat with only the access you saw: read
never implies send, and the agent can never gain a scope its credential does
not already hold. Rejecting or letting the request expire changes nothing.
Approved chats show in `wasync agents list`; to take them away, revoke and
re-grant the agent.

If you would rather decide up front, `--chat "Family"` pre-approves a chat by the name in
your archive instead of a raw ID: wasync shows the matching chat with its ID and
asks you to confirm, and refuses ambiguous or unknown names. A raw chat ID also works.
`--chat` and `--on-demand` combine.

## Ask each time, or sync all chats

One setting decides how requests are answered. Change it on the Approvals page, with
`wasync access mode`, or with `M` in the terminal UI (which also asks once after pairing):

- **Ask me each time** (default): every request waits for you, as above.
- **Sync all chats**: turns on capture for every chat now, with the history
  depth you pick (from now on only, or a full history sync), and from then on an
  agent's request for the one chat it matched is approved at once, without asking
  you. **In this mode agents can read, send, reply and react in any chat without
  asking, and can discover any chat by name or number: a matching request is granted
  at once, so a name probe both reveals and grants that chat (ask-each-time reveals
  nothing).** Editing their own messages (`--edit`) and deleting are never
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

Changing the mode needs wasync running (`wasync serve`, `wasync setup` or the terminal UI) and
either an interactive terminal with a typed confirmation or the Approvals page; choosing sync
all also asks your computer to confirm where it can. No agent credential can reach it.
Switching back to ask-each-time stops future automatic approvals but does not
revoke access already given; revoke the agent to remove that.

## Approving without a terminal

```sh
wasync approvals          # opens the Approvals page in your browser
wasync web                # opens the web app (its Approvals link is the same page)
```

With wasync running, the Approvals page lists the requests agents are waiting on, including
[cleanup and deletion requests](cleanup-and-deletion.md). For each one you see the agent, the
candidate chats and the history depth, and you press **Approve**. wasync then asks **your
computer** to confirm: Touch ID or your account password on macOS, Windows Hello or your
password on Windows. The operating-system prompt names exactly what you are approving, in words
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

`wasync web`, then **Agents**, shows every agent with its scopes and how many chats it has so
far. From there you can:

- **Allow sending** for an agent that asks for chats one at a time (adds the `send` scope after
  your system confirmation), or **Stop sending** (no confirmation needed). It still sends only
  in chats you approved for sending, on its approved numbers, or while open sending is on.
- Approve a list of **approved numbers** for one agent (below).
- Switch **open sending** on for an hour (below).

On a computer with no system confirmation the page is read-only for anything that gives an agent
more power; stopping things still works.

### Approved numbers

To let one agent message a set of phone numbers without approving each chat, approve a list for
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
**Pause** stops a list's sends and keeps everything else; **Resume…** asks you to confirm.
Removing a list needs no confirmation. The one-message-until-reply rule belongs to the number,
not the agent: wasync remembers for good when any agent's list last messaged each number, so no
list sends it another message until it replies to that one, even when two agents' lists hold the
same number, after a list is removed, or when the same numbers are approved for another agent.

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

Whatever wasync sends is also kept in its chat as your outgoing message, so the chat moves to the
top of **Chats**; a message an agent sent is marked there with the agent's name, as on the Sent
page. Sending to a chat you never chose to sync turns syncing on for it; a chat you turned off
stays off and keeps nothing. Releases before this kept a message only when its chat was already
syncing, so the first start of this one adds those earlier messages to chats that sync now,
once, from the sent log: they show at the time they were sent, without their text, which was
never stored.

## What agents can do

Agents read through `wasync-axi` (or `/api/v1`) and write only with the matching scope and an
approved chat. Each write is attempted once and never retried automatically; an unknown outcome
must be checked in WhatsApp first.

| Agent command | API route | Needs |
| --- | --- | --- |
| `wasync-axi send --chat CHAT_JID --text TEXT` or `--to PHONE_NUMBER` | `POST /api/v1/messages` with `{ "chat", "text" }` or `{ "to", "text" }` | `send`, and a chat approved for sending, a number on the agent's approved list, or open sending |
| `wasync-axi send (--chat CHAT_JID \| --to PHONE_NUMBER) --video FILE.mp4 [--text CAPTION]` | the same route with `"video"` (the MP4's bytes, base64) and, when one was taken, `"thumbnail"` (the preview frame, base64) | exactly as for text: one message under the same scope, approvals, list pacing, open-sending caps and rate limit |
| `wasync-axi reply REF --text TEXT [--mentions JID,JID]` | `POST /api/v1/messages/{ref}/reply` with `{ "text", "mentions" }` | `send` and the chat approved |
| `wasync-axi react REF EMOJI` / `--remove` | `PUT` / `DELETE /api/v1/messages/{ref}/reaction` | `react` and the chat approved |
| `wasync-axi edit REF --text TEXT` | `POST /api/v1/messages/{ref}/edit` with `{ "text" }` | `edit.own` and the chat approved |

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
