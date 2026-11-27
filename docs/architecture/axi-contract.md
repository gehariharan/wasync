# wasync ↔ wasync-axi interface contract


Locked by ADR-001 ([ADR-001](ADR-001-repository-boundaries.md)). Summary of what each side must provide:

### Transports (ADR D-1)

| Transport | When | Allowed |
|---|---|---|
| Local API `/api/v1` with a scoped agent credential | owner running and version compatible | reads, `observe`, checkpoint commits, and the writes the grant allows (reply, react, edit own; never deletes) |
| `wasync query … --json` subprocess | no owner listening | reads and journal reads only; never checkpoint commits or mutations |

AXI never touches WhatsApp, the SQLite archive, the session store or `device.lock`, and never imports `internal/`. Each AXI result states which transport served it.

### Authorization (ADR D-2)

- Scoped agent clients created only by a human in wasync (`wasync agents grant|list|revoke`, interactive terminal or TUI).
- Scopes: `read.chats`, `read.messages`, `read.media`, `observe`, `send`, `react`, `edit.own`, `chat.state`, `group.admin`, `capture.manage`, `export`. Destructive operations are not grantable and stay on the human approval queue.
- Chat allowlists, rate limits and expiry enforced in wasync handlers. Audit log of every v1 mutation attempt without bodies or tokens.
- `GET /api/v1/whoami` returns the client name, scopes and constraints so AXI and `doctor` can explain a denial (`error_code: forbidden`). Human repair guidance follows the shared policy in [`agentauth.RegrantCommand`](../../internal/agentauth/regrant.go).
- Legacy `/api/*` routes and tokens unchanged.

### Version handshake

`GET /api/v1/version` (no auth, loopback only, no content) and offline `wasync query version --json`:

The response shape is defined by [`query.VersionInfo`](../../internal/query/v1.go).
Read the returned `features` array for the owner's registered capabilities rather
than relying on a copied feature list.

The embedded [AXI compatibility matrix](../../axi/internal/compat/compatibility.json)
owns the minimum API version and required features per command; a missing feature
disables that command with `wasync_too_old` and an upgrade hint.

### Response envelope (v1 only)

Success: `{"request_id", "data", "page": {"next_cursor", "has_more"}}`
Error: `{"request_id", "error_code", "error", "help", "state"}`

Error codes: `unauthorized`, `forbidden`, `not_found`, `invalid_argument`, `invalid_cursor`, `cursor_expired`, `chat_ambiguous`, `owner_offline`, `no_account`, `archive_unavailable`, `schema_incompatible`, `rate_limited`, `target_unavailable`, `not_editable`, `edit_window_expired`, `send_failed`, `internal`. The `wasync query --v1` mode emits the same envelope so AXI parses both transports identically.

Message-list and search responses add optional `data.total`, advertised by the
`messages.total` feature. The archive owner counts all matching messages for the
current account, chat, sender, time and search filters, restricted to the API
client's chat allowlist. Offline reads are unscoped. The count ignores cursor and
limit; search context neighbors do not count as hits. It is independent of the
page's visible item count and `has_more`, including when authorization leaves an
empty page. Older compatible owners may omit it, which means unknown, not zero.
Legacy unversioned responses are unchanged. The schema is owned by
[`query.V1MessageList`](../../internal/query/v1_message.go), and matching filters
are shared by reads and counts in [`storage`](../../internal/storage/messages.go).

### Identifiers

- Message ref: opaque `m1_<base64url(account_jid \x00 chat_jid \x00 message_id)>`, account-scoped, stable across restarts, purge and re-capture, no migration.
- Chat id: WhatsApp JID. Name lookup returns `chat_ambiguous` with candidates rather than guessing.
- Change cursor: opaque `(account_jid, journal_epoch, sequence)`. Never a timestamp or row ID.
- UTC RFC 3339 at the boundary.

### Endpoints and their offline equivalents

| v1 endpoint | `wasync query` equivalent | AXI command | Scope |
|---|---|---|---|
| `GET /api/v1/version` | `query version` | handshake, `doctor` | none |
| `GET /api/v1/whoami` | – (offline is unscoped) | `doctor`, denials | any |
| `GET /api/v1/diagnostics` | `query doctor` | `doctor` | any |
| `GET /api/v1/health/deep` | `query status --v1` | `status`, home view | `read.chats` |
| `GET /api/v1/chats[?match=]` | `query chats --v1 [--match]` | `chats` | `read.chats` |
| `GET /api/v1/chats/{jid}/messages` | `query messages --v1` | `messages CHAT` | `read.messages` |
| `GET /api/v1/messages/search?context=` | `query search --v1 --context` | `search` | `read.messages` |
| `GET /api/v1/messages/{ref}` | `query message --ref` | `message` | `read.messages` |
| `GET /api/v1/messages/{ref}/thread` | `query thread --ref` | `thread` | `read.messages` |
| `GET /api/v1/changes?after=&limit=` | `query changes --after` | `observe` | `observe` |
| `GET/PUT /api/v1/checkpoints/{consumer}` | read only | `observe --consumer`, `observe ack` | `observe` |
| `POST /api/v1/messages` | – (owner only) | `send --chat CHAT --text`, `send --to NUMBER --text`, `send ... --video FILE` | `send`, chat allowlist; or `send` and a number on the agent's approved list; or `send` while open sending is on (feature `write.send.to` for `to`; feature `write.send.video` for a body carrying `video` bytes and, when wasync-axi took one with ffmpeg, a `thumbnail` preview frame (a JPEG of at most 64 KiB and 320 pixels on each side), both base64) |
| `GET /api/v1/send-list` | – (owner only) | `whatsapp-axi` (home view) | any credential; its own list only (feature `write.send.list`) |
| `POST /api/v1/messages/{ref}/reply` | – (owner only) | `reply` | `send`, chat allowlist |
| `PUT/DELETE /api/v1/messages/{ref}/reaction` | – (owner only) | `react REF EMOJI`, `react REF --remove` | `react`, chat allowlist |
| `POST /api/v1/messages/{ref}/edit` | – (owner only) | `edit REF --text` | `edit.own`, chat allowlist |

`POST /api/v1/messages` (feature `write.send`) sends a new message, not a quote, to a chat the credential's allowlist already holds; a chat or number the agent was not given is refused with `forbidden`, never turned into a grant, unless the person has switched on open sending (one hour, set from the web app after the system confirmation, with a cap of 15 messages and 5 new numbers an hour; past a cap the answer is `rate_limited`), or the number is on the list the person approved for this agent (feature `write.send.list`; set on the Agents page after the system confirmation, optionally with an end date; per agent, the owner's chosen 5 to 20 numbers messaged for the first time an hour, spaced at random intervals (each gap half to one and a half times the average), and three times that in list messages, one message per number until it replies, past which the answer is `rate_limited` with `Retry-After` when the wait is known; a number that replies STOP, unsubscribe, opt out or the like (any case or punctuation) after wasync's first list message to it is dropped from every list for good; earlier messages from that number never count). `GET /api/v1/send-list` returns whether the credential has a list, whether the person paused it (`paused`: while true every list send answers `forbidden`), the allowance left this hour, and `stopped_numbers`, the phone-number addresses its list dropped after they replied STOP (no message text), never the other numbers. Every send, by the owner or an agent, is also noted in a private sent log (`sent-log.jsonl`: who, which chat, when, the WhatsApp message ID and the outcome, never the text) that the web app's Sent page reads.

The read routes above (`read.chats` and `read.messages` only) also let an agent with an approved list read the chats of the numbers on its list in force that a list has messaged, and the hidden-ID (`@lid`) chat wasync links to such a number, as if they were on its allowlist: those chats appear in `GET /api/v1/chats` and in searches over every chat, and a read that names one only the list allows, and a chat list or search over every chat that returns one, is audited as `read_list`, one entry per such chat it names or returns, and is refused when the audit log cannot be written. Other numbers on the list, numbers dropped after a STOP, other agents' numbers, media and the change feed stay refused, and the access ends with the list. See [Approved numbers](../agents.md#approved-numbers).

Writes (features `write.reply`, `write.react`, `write.edit`) act on a message wasync has archived, are authorized
and audited before they run (the audit entry holds scope, chat and ref, never text),
and send once. `target_unavailable` (409) means the target was deleted or is a
reaction; `owner_offline` (503) means wasync is not connected to WhatsApp;
`not_editable` (409) means the message is not the account's own text message, and
`edit_window_expired` (409) that it was first sent longer ago than WhatsApp's edit
window; both are checked before anything is sent. `send_failed` (502) means the outcome is unknown and must not be retried
automatically. A ref that does not decode names no chat and is refused with
`forbidden` before the handler runs.

### Agent send requests (feature `write.send.request`)

A connected agent's credential covers every chat synced in wasync (`synced_chats: true` in `agents.json`;
its chat list is kept equal to the synced chats), so it reads, replies and reacts there with no per-chat
request. Only a send to a chat that is not synced, or to a new number, needs the person, one message at a time.

| Endpoint | AXI command | Authorization |
|---|---|---|
| `POST /api/v1/send-requests` body `{"chat":JID,"text":TEXT}` or `{"to":PHONE,"text":TEXT}` | `send-request --chat JID --text TEXT`, `send-request --to PHONE --text TEXT` | an agent credential holding `send` |
| `GET /api/v1/send-requests/{id}` | `send-request status ID` | the credential that made the request; any other is `404 not_found` |

Exactly one of `chat` and `to`; `text` is required, text only, at most 4096 bytes. A request sends nothing
by itself: it returns `202` with `request_id`, `status: pending`, `chat`, `new_number`, `expires_at` and
`approve_with` (the command the person can run). The person sees the agent, the chat and the full text and
approves with the computer's own check (Touch ID, Windows Hello or a passkey) in wasync; on approval wasync
itself sends exactly that text once. Asking again with the same chat and text while one is pending returns
the same request. `200` with `status: already_allowed` means `POST /api/v1/messages` would take that
send right now: the chat is one the agent may use, or the agent's approved list (for a listed number)
or else open sending would admit it. When that list or open sending would refuse it (a paused list, a
number that has not replied yet, an hourly limit used up), the request is queued for the person instead. `403 forbidden` without `send`, `429 rate_limited` when the agent already has 5
requests waiting, `503 owner_offline` when no owner runs. Status is `pending`, `sent` (with `message_id`),
`declined`, `expired` or `failed`; `expires_at` is present only while pending. Requests expire after
30 minutes and then change nothing.

A send with `POST /api/v1/messages` to a chat the agent may not use still fails with `403 forbidden`; its
help tells the agent to raise a send request (`wasync-axi send-request ...`).

Per-chat access requests are removed: `POST /api/v1/access-requests` and `GET /api/v1/access-requests/{id}`
answer `410` with `invalid_argument` and a help line, the feature `access.request` is no longer listed,
and wasync-axi no longer has an `access` command (an old invocation is a usage error that points at
`send-request`).

### Message content safety

Message text is data in a typed field. AXI marks it as untrusted content in output and never turns it into hints or instructions.

### Agent session hooks (AXI principle 7)

`wasync-axi setup hooks` is the only command that writes agent settings. It installs a session-start hook (`wasync-axi --brief`, a few lines of wasync state with no chat names or message text) and a session-end hook (`wasync-axi session-end`) for Claude Code, Codex and OpenCode, and `--remove` takes both out again.

The session-end hook is session capture without a wasync call and without content. It reads the hook's JSON from stdin, and from the transcript it names (or, for OpenCode, from the command names the plugin passes) it keeps only the wasync-axi command names an agent ran and how many times. It stores one record per working directory in `last-session.json` in the AXI config directory: a time, and names with counts. It never stores or prints arguments, chat names, message text, phone numbers, paths or transcript text, because message content is untrusted and arguments can carry it. The next `--brief` in that directory shows the record as one `last_session` line. The record is local and advisory; nothing reads it as an instruction.
