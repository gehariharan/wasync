# wasync / wacli feature parity

Source: Phase 0 inspection of WhatsappTuiCli `891ef40` and openclaw/wacli `8c61e16` (2026-10-19).


Legend: ✅ have, 🟡 partial, ❌ missing. Permission class is what an agent would need. "Mig" = needs an additive migration. Milestone uses handoff §6 order (F=foundations, M=messaging, Md=media, G=chat/group, B=broader, D=downstream).

| Capability | wasync | wacli reference | Mig | Permission | Milestone |
|---|---|---|---|---|---|
| QR pairing, logout | ✅ `whatsapp/client.go` | `auth.go` | – | admin (human) | – |
| Single-owner lock | ✅ `processlock` | `internal/lock` | – | – | – |
| Owner delegation of writes | ✅ HTTP API | Unix socket `send_ipc.go` | – | – | – |
| Selective per-chat capture | ✅ (wacli has none) | – | – | capture.manage | – |
| Read-only global mode | 🟡 `query` is read-only | `--read-only` `root.go:195` | – | – | F |
| Health / reconnect detail | 🟡 connection string only | stale-keepalive reconnect `app/sync.go`, heartbeat | – | read | F (W-02b) |
| History coverage report | 🟡 per-chat state + gaps | `history coverage` | – | read | F |
| On-demand history backfill | ✅ anchor jobs | `app/backfill.go` (retry next anchor) | – | capture.manage | F |
| LID↔PN identity mapping | 🟡 stores `sender_alt` only | `session_resolver.go`, `lid_migration.go` | yes | read | F |
| Contacts list/search | ❌ | `contacts.go` | yes | read | G |
| Search context windows | ❌ | `messages_read.go:190`, `store/messages.go:569` | – | read | F (W-03b) |
| Search ranking / snippets | ❌ | bm25 + snippet `store/search.go` | maybe | read | F |
| Stable message refs | ❌ (row id) | rowid cursors | – | read | F (W-03a) |
| Name → chat resolution with disambiguation | ❌ | `internal/resolve` | – | read | F (W-03c) |
| Multi-account | 🟡 one per data dir | `config.yaml` accounts | – | – | F (ADR only) |
| Reply/quote capture | ✅ `reply_to_message_id` | – | – | read | – |
| Send text / file | ✅ | `send.go`, `send_file.go` | – | send | – |
| Send reply/quote, mentions | ✅ `whatsapp/write_reply.go` | `send.go:158` | – | send | M (W-06) |
| Reactions (capture) | ✅ event rows | – | – | read | – |
| Send reaction | ✅ `whatsapp/write_react.go` | `send_react_cmd.go` | – | react | M (W-06) |
| Edits / revokes capture | ✅ | `sync_edits.go` | – | read | – |
| Edit own message | ✅ `whatsapp/write_edit.go` | `messages_edit.go` | – | edit.own | M (W-06) |
| Revoke own message | ✅ via approval | `messages_delete.go` | – | destructive | – |
| Forward | ❌ | `messages_forward.go` | – | send | M |
| Delivery/read receipts | ❌ | `message_receipts.go`, `read_receipts.go` | yes | read / chat.state | M |
| Media download | ✅ policy + one-shot | `media.go`, verified cache | – | media.read | – |
| Media re-upload retry | ❌ | `media_retry.go` | – | media.read | Md |
| Voice / sticker / location / poll send | ❌ | `send_*` | – | send | Md/B |
| Groups: info, participants | ❌ | `groups_*` | yes | read | G |
| Groups: admin, invite links | ❌ | `groups_admin.go`, `groups_invite_join.go` | – | group.admin | G |
| Chat state mute/pin/archive/read | ❌ | `chats_state.go` | yes | chat.state | G |
| Polls / locations capture | ❌ | `sync_polls.go`, `store/locations.go` | yes | read | B |
| Channels, status, presence, calls | ❌ | `newsletters.go`, `presence.go`, `calls.go` | yes | read | B |
| Change journal / pull feed | ❌ | `store/changes.go` | yes | read | D (W-04) |
| Agent chat access on demand (human-approved, per chat, read/send separate, history depth) | ✅ `api/v1_access.go`, `approval`, `agentauth` | – | `access` | access.request (not a scope; approval-gated) | M (JIT) |
| Webhooks (HMAC, SSRF guard) | ✅ `internal/webhook`, human-managed, identifier-only events on the change journal | `app/webhook.go` | – | admin (human only, not grantable) | D (W-05) |
| Storage limits | ❌ | `storage_limits.go` | – | admin | B |
| Local purge / clean | ✅ via approval | `messages purge` | – | destructive | – |
| Disappearing-message expiry | ✅ `Expire` | – | – | – | – |
| TUI | ✅ (wacli has none) | – | – | – | – |
| Signed/notarized Mac releases, Homebrew, Windows installer | ✅ | – | – | – | – |

Not adopting from wacli: macOS Contacts import, business button/list messages, its migration list or tables wholesale, direct SQLite reads by companion tools. Any port records upstream SHA `8c61e16` + path and the MIT notice (© 2026 Peter Steinberger).
