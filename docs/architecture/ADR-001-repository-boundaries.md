# ADR-001: Repository boundaries, AXI transports and agent authorization

- **Status:** Accepted by Hari on 2026-10-19 (decisions 1–6 below). Implementation is gated on the baseline audit (B-00).
- **Superseded in part:** [ADR-002](ADR-002-monorepo.md) owns repository and release coordination; the process and authorization boundaries below remain in force.
- **Context source:** the platform handoff plan and the Phase 0 inspection ([inventory](inventory.md), [parity matrix](parity-matrix.md)) of WhatsappTuiCli `891ef40`, homebrew-tap `9bfb347`, openclaw/wacli `8c61e16` and kunchenguid/axi `6244245`.
- **Related:** [interface contract](axi-contract.md), [roadmap](roadmap.md), [threat model](threat-model.md).

## Context

wasync is the only component that pairs with WhatsApp, holds `device.lock`, owns the SQLite archive and enforces selective capture. A new agent-facing CLI, `whatsapp-axi`, is planned. The inspection found:

- The local API already separates a read bearer token (`api.token`) from HMAC-signed control requests (`send.token`) and human approval (`approve.token`) in `internal/api/server.go`.
- The read bearer token can also mutate state today: `POST /api/chats/{id}/enable|disable`, `enable-all|disable-all` and `/history` require only `api.token`. Any client holding the read token can change capture policy.
- Any process holding `send.token` can send to any chat, with no per-chat policy or audit.
- `wasync query` reads the archive read-only without the device lock (`internal/storage/readonly.go`).
- There is no change journal; message timestamps and row IDs are the only ordering available.

## Decisions

### D-1. Two read transports, nothing else

`whatsapp-axi` reads through exactly two transports:

1. **Local API** (`/api/v1` on the loopback address) when a wasync owner is running, authenticated with an AXI-specific scoped credential (D-2).
2. **`wasync query … --json`** as a subprocess for offline archive reads when no owner is listening.

AXI never connects to WhatsApp, never opens or writes the SQLite archive or the whatsmeow session store, never takes `device.lock`, never starts `wasync serve`, and never imports wasync's `internal/` packages. Every mutation goes through the API; the offline transport is read-only by construction (`mode=ro`, `_query_only`).

Transport selection is deterministic: probe `GET /api/v1/version`; if it answers with a compatible version, use the API for everything; otherwise use `wasync query` for reads and return `owner_offline` for anything that needs the owner. AXI reports which transport served each result.

### D-2. Authorization is enforced by wasync, not by AXI

CLI flags, AXI confirmations, agent skills and hook instructions are UX only. They are not a security boundary. The boundary is the wasync API:

- **Scoped agent clients.** The human creates an agent client in wasync (`wasync agents grant NAME --scope …`, interactive terminal or TUI only; no `--yes`, not available over the API). wasync stores a hashed credential and its scopes in a private file in the data dir and prints the credential once. AXI uses this credential, never `api.token`, `send.token` or `approve.token`.
- **Scopes:** `read.chats`, `read.messages`, `read.media`, `observe`, `send`, `react`, `edit.own`, `chat.state`, `group.admin`, `capture.manage`, `export`. `destructive` is not grantable: revoke, delete, purge and clean always go through the existing approval queue with typed human confirmation.
- **Constraints per grant:** chat allowlist (required for `send`, `react`, `edit.own`, `chat.state`, `group.admin`), rate limit, expiry. Enforced inside the handler before any whatsmeow call.
- **Audit:** every v1 mutation attempt (allowed or denied) is appended to a private audit log with client name, scope, chat JID, ref, outcome and request ID. No message bodies or tokens.
- **Legacy behaviour is preserved.** Existing `/api/*` routes and the three existing tokens keep working unchanged for the TUI, `wasync` CLI and current scripts. v1 routes accept only scoped agent credentials (or the legacy tokens with an explicit equivalent scope set, documented). Tightening the legacy read-token mutation is a separate, opt-in follow-up, not part of this change.
- **Offline transport boundary:** reading the archive via `wasync query` is equivalent to the OS user reading their own files. It is allowed only for reads, and AXI states in `doctor` that offline reads are not scoped.

### D-3. Business logic stays in the wasync core

The wasync core owns: WhatsApp protocol, capture policy, normalization, storage and migrations, search and context, identity resolution, the change journal and checkpoints, authorization, audit, sending and every other operation.

whatsapp-axi owns only: command parsing and composition, transport selection, output formatting (compact text, TOON if justified, `--json`), truncation, hints, discovery/help, `doctor` presentation, `setup hooks`, agent skills and benchmarks. Release ownership is defined in [ADR-002](ADR-002-monorepo.md).

Test of the rule: if a feature would need SQL, a whatsmeow call, a policy decision or persistent state other than AXI's own config, it belongs in wasync behind an API endpoint and a `wasync query` equivalent.

### D-4. `observe` is a first-class command on a transactional journal

- wasync adds a `change_journal` table (additive migration 006) written in the same SQLite transaction as every archive mutation: message created/updated/revoked/expired/purged and capture enabled/disabled.
- Each entry has a monotonic `sequence` (INTEGER PRIMARY KEY AUTOINCREMENT), `account_jid`, `chat_jid`, `message_id`, `op`, `op_version`, `committed_at`. No message bodies; consumers fetch content by ref under `read.messages`.
- Cursors are opaque and encode `(account_jid, journal_epoch, sequence)`. `journal_epoch` changes if the archive is recreated, so a stale cursor fails with `cursor_expired` instead of silently skipping. Timestamps and message row IDs are never cursors.
- **Account-aware checkpoints:** wasync stores named consumer checkpoints keyed by `(account_jid, consumer)`. Advancing a checkpoint is a write and goes through the API under the `observe` scope. Offline `observe` can read the journal through `wasync query changes` but cannot commit a checkpoint; AXI says so.
- `whatsapp-axi observe [--after CURSOR | --consumer NAME] [--chat JID] [--limit N] [--follow]` prints bounded batches with the next cursor and an explicit commit step (`--commit` or `observe ack`).
- Retention: journal rows older than the retention window are pruned; a cursor behind the low-water mark returns `cursor_expired` with a resnapshot hint.

### D-5. `whatsapp-axi doctor`

`doctor` checks and reports with pass/warn/fail and repair guidance; see
[doctor usage](../../axi/README.md#usage):

1. Installation: both binaries found, versions, install source (Homebrew cask, ZIP, other), PATH.
2. Data dir: located, private permissions, schema version supported.
3. Pairing: whether wasync has a paired session (from wasync, never by opening the session store).
4. Service: owner running, API reachable, deep health state, last event observed.
5. Permissions: AXI credential present and valid, granted scopes vs scopes each AXI command needs.
6. Compatibility: API version and feature list vs the [AXI compatibility matrix](../../axi/internal/compat/compatibility.json), journal available.
7. Agent integration: hooks and skill installed and pointing at the current binary.

wasync provides the facts (`GET /api/v1/diagnostics` plus offline `wasync query doctor --json`); AXI only formats them (D-3).

### D-6. Ordering

Release ordering is superseded by [ADR-002](ADR-002-monorepo.md); see the [common release guide](../releasing.md). Optional webhook delivery (W-05) shipped after the first AXI release as a human-managed, identifier-only feed over the change journal; see the [README](../../README.md#webhooks) and the [threat model](threat-model.md).

## Consequences

- One new additive migration (006: journal + checkpoints) is required before the AXI release, so the migration-safety work (W-07: backup before migrate, read-only acceptance of newer additive schemas) moves onto the critical path.
- AXI's first release is read + observe only. Mutating AXI commands wait for the scoped authorization layer (W-09) and per-operation scopes.
- Two read transports mean every AXI read command needs a `wasync query` equivalent; the backlog pairs each new v1 endpoint with one.
- Repository creation and any release stay blocked until the baseline audit (B-00) passes and Hari says go.

## Rejected alternatives

- AXI reading SQLite directly: couples AXI to schema changes and bypasses authorization.
- AXI holding `send.token`: unscoped, unaudited sending.
- Enforcing send consent in AXI or in an agent skill: not a security boundary.
- Timestamp or row-ID based change cursors: break under late history, edits and purges.
- Webhooks before AXI: adds outbound data paths before the journal and authorization are proven.
