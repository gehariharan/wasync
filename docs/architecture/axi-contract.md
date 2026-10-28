# wasync ↔ whatsapp-axi interface contract


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
| `POST /api/v1/messages/{ref}/reply` | – (owner only) | `reply` | `send`, chat allowlist |
| `PUT/DELETE /api/v1/messages/{ref}/reaction` | – (owner only) | `react REF EMOJI`, `react REF --remove` | `react`, chat allowlist |
| `POST /api/v1/messages/{ref}/edit` | – (owner only) | `edit REF --text` | `edit.own`, chat allowlist |

Writes (features `write.reply`, `write.react`, `write.edit`) act on a message wasync has archived, are authorized
and audited before they run (the audit entry holds scope, chat and ref, never text),
and send once. `target_unavailable` (409) means the target was deleted or is a
reaction; `owner_offline` (503) means wasync is not connected to WhatsApp;
`not_editable` (409) means the message is not the account's own text message, and
`edit_window_expired` (409) that it was first sent longer ago than WhatsApp's edit
window; both are checked before anything is sent. `send_failed` (502) means the outcome is unknown and must not be retried
automatically. A ref that does not decode names no chat and is refused with
`forbidden` before the handler runs.

### Access requests (feature `access.request`)

| Endpoint | AXI command | Authorization |
|---|---|---|
| `POST /api/v1/access-requests` body `{"chat":JID}` or `{"match":TEXT}`, optional `"send":true`, `"edit":true` | `access request` | any active agent credential that is on-demand or has a chat allowlist; rate limited |
| `GET /api/v1/access-requests/{id}` | `access status` | the credential that made the request |

A request grants nothing by itself. wasync resolves `chat`/`match` (JID, phone number, or name using the same
tiers as `chats?match=`, which includes the saved contact name of direct chats) to at most ten candidate chats
across the whole archive and queues a `grant-chat-access` request in the human approval queue (five-minute
expiry, no replay, one request per agent and query, at most three unanswered per agent). It returns `202` with
the request ID, the access asked for (`read`, `send`, `edit`; read is always included) and `approve_with`, the
command the person runs. The agent never receives the candidates, and the reply is the same whether zero, one or
several chats matched: a request that matched nothing is queued with no candidates, shown to the person as "no
chat matched", cannot be approved and simply stays `pending` until it is rejected or expires, exactly like a
request nobody has answered. Status is `pending`, `approved` (with `chat` and `history`), `rejected`,
`expired` or `failed`. `already_allowed` (200) means a JID the agent already may use needs no approval.
`400` for an unrestricted credential (nothing to approve) or a malformed body, `403` when the credential lacks a
read scope, or lacks every scope of a requested send/edit family, `429` when too many requests wait or the rate
limit is hit, `503` when no owner is running.

On approval, `read` covers the read-family scopes the credential holds, `send` adds `send` and `react` where
held, and `edit` adds `edit.own` where held; other scopes are never requestable.

When the owner chose "sync all chats" (`wasync access mode all`, stored in `access-mode.json`), a request without
`edit` that matched exactly one chat is approved at once: the same request is recorded, the chosen history depth
is applied, the audit log records `access_grant_auto`, and the first reply is `200` with `status: approved`.
That is the only case where an agent can tell a match from none: in sync-all mode agents can discover and access any
chat by name or number, because a matching request is granted at once, so a name probe both reveals and grants that
chat. Ask-each-time mode reveals nothing. Requests with `edit`, no match or several matches still wait for a person, and destructive
actions are never automatic. The mode is changed only through `POST /api/access/mode`, signed with the
interactive approval credential; no `/api/v1` route and no agent credential reaches it.

### Message content safety

Message text is data in a typed field. AXI marks it as untrusted content in output and never turns it into hints or instructions.
