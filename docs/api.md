# Local API

While wasync runs (`wasync serve`, the `wasync setup` page or the terminal UI), it serves an
authenticated HTTP API on a loopback address only. This page covers the archive API, the signed
owner routes the CLI uses, headless settings, and reading the archive from your own database.
Agents use the scoped `/api/v1` instead; it is documented in the
[interface contract](architecture/axi-contract.md) and [Agents](agents.md). For push
notifications, see [Webhooks](webhooks.md).

## Starting the API

```sh
wasync serve                          # capture + API; needs a paired session (wasync setup)
wasync serve --offline                # API for the existing archive, no WhatsApp connection
wasync --api-addr 127.0.0.1:8788 serve # serve on another local port
wasync --no-api                       # terminal UI with the API disabled
```

The default address is **http://127.0.0.1:8787**. Use the numeric address as shown; other Host
headers are rejected (the one exception, only for the web app, is the tailnet name you allow
with `wasync tailnet allow`; see [Tailscale](tailscale.md)). `WASYNC_API_ADDR` supplies the default bind address; `--api-addr`
overrides it. Only literal IPv4/IPv6 loopback addresses are accepted. `serve --offline` cannot
be combined with `--no-api`.

`serve` runs until Ctrl-C, SIGTERM or `wasync stop`, and shuts down the API before closing the
archive. `--timeout` does not limit service lifetime. API responses use `Cache-Control: no-store`;
foreign browser origins are rejected and no CORS access is enabled. All routes share the running
process's archive and device owner, without creating a second WhatsApp session. The API is
local HTTP, not transport encryption, and is meant to stay on this computer.

## Archive API (`api.token`)

All `/api/*` routes require a bearer token. `/health` is a content-free readiness
check and does not require authentication. The token is generated on first API
startup and reused across restarts, with file permissions 0600.

In another terminal on macOS:

```sh
WASYNC_TOKEN=$(cat "$HOME/Library/Application Support/wasync/api.token")
curl --fail -H "Authorization: Bearer $WASYNC_TOKEN" \
  http://127.0.0.1:8787/api/status
curl --fail -H "Authorization: Bearer $WASYNC_TOKEN" \
  http://127.0.0.1:8787/api/chats
curl --fail -H "Authorization: Bearer $WASYNC_TOKEN" \
  'http://127.0.0.1:8787/api/chats/123456789@g.us/messages?limit=50'
curl --fail -G -H "Authorization: Bearer $WASYNC_TOKEN" \
  --data-urlencode 'q=meeting' \
  http://127.0.0.1:8787/api/messages/search
```

Replace the chat ID with one from `/api/chats`. On Linux, Windows or with a custom data
directory, read `api.token` from that directory instead (see
[Data, files and security](data-and-security.md)). Never put the token in a
URL, commit it, or share it. To rotate it, stop wasync, remove only `api.token`,
and restart. Existing cursors become invalid after rotation.

| Method | Route | Result |
| --- | --- | --- |
| GET | `/health` | Archive readiness (not WhatsApp connectivity) |
| GET | `/api/status` | Connection, counts, attention flag and history uncertainty |
| GET | `/api/chats` | Current account's group and direct chats with capture/history state |
| GET | `/api/chats/{id}` | One chat |
| POST | `/api/chats/{id}/enable` | Enable capture and queue available history |
| POST | `/api/chats/{id}/disable` | Stop capture; retain archived messages |
| POST | `/api/chats/{id}/history` | Retry best-effort history for an enabled chat |
| GET | `/api/chats/{id}/messages` | Newest messages first, with pagination |
| GET | `/api/messages/search?q=keyword` | Literal phrase search across the current account |
| GET | `/api/messages/{id}` | One message by its local numeric row ID |

The older `/api/groups`, `/api/groups/{id}` and matching `/enable`, `/disable`, `/history` and
`/messages` routes still work and list groups only.

Message list and search responses contain `account_id`, `items` and
`next_cursor`. Pass the returned cursor as `cursor` to retrieve older messages;
an empty cursor means the end of currently stored results. The cursor is signed
and tied to the account and filters. Keep filters unchanged between pages.
Page size defaults to 50; `limit` accepts 1–200.

Both query routes accept `sender_id`, `from` (inclusive), and `to` (exclusive).
Dates use RFC3339, for example `2026-10-01T00:00:00Z`; URL-encode timezone `+`
signs. Search also accepts `group_id`. Response timestamps are RFC3339 UTC;
SQLite timestamps remain Unix milliseconds. Search uses SQLite FTS5. Expired
messages are excluded and revoked messages have empty bodies.
Disabling capture preserves the archive and query access. Reads are scoped to the
current account; pairing another account does not expose the old archive via API.

Query results are live archive reads. Pagination is not a frozen snapshot;
new history, edits, expiry and purge can change results. This is not a complete
replication feed. For another database, use snapshot reconciliation and the
composite identity described [below](#pull-data-into-your-own-database); do not use message
timestamps as a change log.

## Signed owner routes

The `wasync` CLI talks to the running owner through routes that need the separate
`send.token` (or, for approving, stopping and unlinking, the approval credential). The archive
`api.token` cannot send. CLI commands sign requests with the credential, bind the signature to
the method/path/body and reject replays; they do not transmit the credential. These routes
accept only signed requests (headers `X-Wasync-Time`, `X-Wasync-Nonce`, `X-Wasync-Signature`);
a plaintext bearer token is rejected there.

| Route | Body | Used by |
| --- | --- | --- |
| `POST /api/send` | `{ "to": "CHAT_ID", "text": "Hello" }`; optional `file` (an absolute local path), `type` (`image`, `video` or `document`) and, for a video, `thumbnail` (the preview frame `wasync send` took with ffmpeg: a JPEG of at most 64 KiB and 320 pixels on each side, base64; not a user option) | `wasync send` |
| `POST /api/reply` | `{ "chat", "message_id", "text", "mentions" }` | `wasync reply` |
| `POST /api/react` | `{ "chat", "message_id", "emoji" }` or `"remove": true` | `wasync react` |
| `POST /api/edit` | `{ "chat", "message_id", "text" }` | `wasync edit` |
| `POST /api/media/policy`, `POST /api/media/download` | | `wasync media` |
| `POST /api/sync` | | `wasync sync` |
| `POST /api/actions`, `GET /api/actions/{id}` | | `wasync clean`, `wasync delete`, `wasync actions status` |
| `GET /api/agent-requests`, `POST /api/agent-requests/{id}/approve`, `POST /api/agent-requests/{id}/decline`, `POST /api/agent-requests/decline-all` | | The messages agents ask to send ([send requests](agents.md#sending-to-other-chats-and-new-numbers)), oldest first with `expires_at`, for a desktop app. Approving needs the approval credential and the system confirmation, one request at a time; listing and declining need `send.token` |

The wasync app's settings (its First open flag and its start-at-login item) are read
and changed on signed-in pages, under `/ui/desktop`; see
[The wasync app](desktop.md#settings-the-setup-screens-use).

A holder of `send.token` can request sending any regular local file the owner process can read;
it is a privileged credential, not a read-only API key. See
[Data, files and security](data-and-security.md#credentials-and-the-keychain) for all three
credentials.

## Web app update routes

The web app's update routes are for the owner's signed-in browser session only (the
one-time link or **Sign in**); the archive token, the signed owner credentials and agent
credentials are all refused. `POST` routes need the page's form token as the `csrf` form
field or the `X-Wasync-CSRF` header. They answer with JSON.

| Route | What it does |
| --- | --- |
| `GET /ui/update/progress` | Where updates stand: `current`, `channel` (`kind`, `one_click`, `note`), `state` (`idle`, `checking`, `up-to-date`, `available`, `downloading`, `verifying`, `installing` or `failed`), `checked_at` (when the last check finished), `latest` (`version`, `page`, `published_at`, `asset`, `size`), `can_install`, `note`, `progress` (`done`, `total` bytes), `error` and `last_result` (how the last installed update ended: `from`, `to`, `ok`, `error`, `restart`, `finished`; kept across the restart while it still describes the running version) |
| `POST /ui/update/check` | Asks GitHub for the newest published release now and returns the same object |
| `POST /ui/update/start` | Asks the person to confirm with the system prompt, then downloads, checks and installs the update and restarts wasync; `202` with the object, `409` when there is nothing to install, `403` when not confirmed or asked from another device, `503` without a system prompt |

See [Updating from the web app](install.md#updating-from-the-web-app) for what each kind of
install does.

## Web app data (`/ui/api`)

wasync's own pages read and change a few things through `/ui/api`. These routes need the web
app's browser session, like every `/ui` page, and every change needs that session's form token
in a `csrf` form field; no token above opens them. They are for wasync's pages, not for scripts.

| Route | What it does |
| --- | --- |
| `GET /ui/api/chats` | One page of Choose chats. `tab` is `all`, `groups`, `people`, `unnamed` (the direct chats WhatsApp gives no name for, folded under People as one `unnamed` entry while nothing is searched) or `selected` (the chats that sync). `q` searches names and phone numbers. Chats come most recently active first, then A to Z; `offset` and `limit` (50 by default, at most 200) page them, and `next_offset` is null on the last page. Tab counts follow the search, and `select_all` counts the chats matching the tab and search that do not sync yet. |
| `POST /ui/api/chats/sync` | Saves one change at once: `chat` (one or more), `sync=on` or `off`, and optionally `history=yes` or `no` (asking the phone for older messages of a chat that starts syncing; the default is yes, or no in agent-only mode). `all=1` with `sync=off` stops syncing every chat. With `tab` and `q`, the answer also carries that page's new `select_all` count. |
| `GET`, `POST /ui/api/settings` | `use` (`agents` for "For my AI agents only", or `chats`) and `save_media` (`on` or `off`, the "Also save photos, videos and files" box, on by default). |
| `GET /ui/avatar?c=CHAT` | The chat's small profile picture, fetched from WhatsApp only when a page asks for it, at most one request every half second, and cached for a day; otherwise its initials as an image. |

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
have empty bodies. Group and direct chats share the archive; the `group_id` column holds the
chat ID for both. Filter by `account_id` and `group_id`; read the database in
read-only mode and keep read transactions short. Never modify schema or archive
tables from external applications.

For a separate DB, use a consistent snapshot and upsert by
`(account_id, group_id, message_id)`, with deletion-aware snapshot reconciliation.
A message-time cursor alone misses edits, late history and purges. To follow changes, use the
change journal instead: `wasync query changes`, `wasync-axi observe`, or
[webhooks](webhooks.md).
