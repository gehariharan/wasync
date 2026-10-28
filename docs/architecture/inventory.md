# wasync inventory

Source: Phase 0 inspection of WhatsappTuiCli `891ef40` and openclaw/wacli `8c61e16` (2026-10-19).


### Source-to-responsibility map

| Path | Responsibility |
|---|---|
| `cmd/wasync/main.go` | Entry point |
| `internal/cli/` | Command dispatch (`cli.go`), help catalog (`help.go`), read-only `query` (`query.go`), `serve`, `sync`, `send`, `media`, `clean`/`delete` requests (`destructive.go`), owner delegation (`targets_owner.go`) |
| `internal/tui/` | Bubble Tea TUI: chat browser, capture toggles, media, approval prompts |
| `internal/whatsapp/` | whatsmeow client, pairing, capture pipeline (`capture.go`), normalization (`normalize.go`: text, image, video, audio, document, sticker, reaction, edit, revoke), history worker (`history.go`), media download/policy, send, remote delete |
| `internal/storage/` | SQLite archive, embedded migrations 001–005, consent-checked writes, FTS5, expiry, purge, read-only opener |
| `internal/api/` | Loopback REST API, token auth, signed control requests, HMAC-scoped cursors |
| `internal/approval/` | Human approval queue for destructive actions |
| `internal/query/` | JSON DTOs shared by CLI and API |
| `internal/processlock/` | Single-owner `device.lock` |
| `internal/localfs/` | Private file/dir permissions (Unix modes, Windows ACLs) |
| `internal/config/`, `internal/buildinfo/` | Data dir resolution, injected version |
| `scripts/`, `packaging/` | Packaging, signing, notarization, cask generation, tap publish, smoke tests |
| `website/` | Cloudflare worker site |

### Features

- **Pairing and ownership:** QR pairing, saved session, logout; one owner per data dir via `device.lock`; TUI or `serve` is the owner, other commands delegate to it over the local API.
- **Selective capture:** per-chat opt-in (`groups.sync_enabled`), rechecked inside each write transaction; direct chats discovered best-effort and named from whatsmeow's contact store (address-book, push, business name; refreshed on connect and on contact events).
- **History:** anchor-based best-effort history jobs per chat (`history_jobs`, `history_anchors`), batch accounting, gap recording (`sync_gaps`, `capture_runs`).
- **Mutations:** edits, revokes (tombstones), expiry of disappearing messages, local purge.
- **Media:** per-chat policy auto/manual/inherit, one-time download via live owner, streamed attachments.
- **Send:** text and file (image/document) with signed requests.
- **Read surfaces:** `wasync query groups|chats|messages|search|status` (offline JSON), REST API reads, `help --json`. Later additions: `query version|message|thread|changes|checkpoint|doctor`, `--v1`, `query chats --match`, `query search --context`; human-only `wasync agents grant|list|revoke`.

### Database migrations (`PRAGMA user_version`, current = 5)

| # | File | Adds |
|---|---|---|
| 1 | `001_groups.sql` | `accounts`, `app_state`, `groups` |
| 2 | `002_capture.sql` | `messages`, FTS5 + triggers, `sync_state`, `history_batches`, `capture_runs`, `sync_gaps`, `archived_messages` view |
| 3 | `003_history_jobs.sql` | `history_jobs` (+ backfill rows for enabled chats) |
| 4 | `004_history_anchors.sql` | `history_anchors` |
| 5 | `005_media_preferences.sql` | `media_preferences` |

All migrations run in one transaction (`sqlite.go` `migrate`). None are destructive.

### REST API (loopback only, `127.0.0.1:8787` default)

| Route | Auth | Purpose |
|---|---|---|
| `GET /health` | none | archive ping |
| `GET /api/status` | bearer | connection, counts, history flag |
| `GET /api/chats`, `/api/groups` | bearer | chat summaries |
| `GET /api/chats/{id}`, `/api/groups/{id}` | bearer | one chat |
| `GET /api/chats/{id}/messages` (+groups alias) | bearer | paginated messages, filters `from`,`to`,`sender_id`,`limit≤200`,`cursor` |
| `GET /api/messages/search?q=` | bearer | FTS phrase search, same filters |
| `GET /api/messages/{id}` | bearer | one message by **local row id** |
| `POST /api/chats/{id}/enable|disable`, `enable-all|disable-all` | bearer | capture preference |
| `POST /api/chats/{id}/history` | bearer | queue history |
| `POST /api/send`, `/api/sync`, `/api/media/policy`, `/api/media/download` | signed (send.token) | control |
| `POST /api/actions`, `GET /api/actions/{id}` | signed | destructive request/status |
| `POST /api/actions/{id}/approve` | signed (approve.token) | human approval |

### Release workflows

Release automation has changed since this Phase 0 inspection. See the
[common release guide](../releasing.md) for the current workflows and publication
safeguards.
