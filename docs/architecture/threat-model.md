# Threat model: agent access to wasync

Scope: the local wasync owner (TUI or `serve`), its loopback API, the `wasync query` CLI, and agent clients such as whatsapp-axi. See [ADR-001](ADR-001-repository-boundaries.md).

## Assets

- WhatsApp pairing credentials (whatsmeow session store)
- The SQLite archive: message bodies, media, chat membership, capture preferences
- Ability to act as the user on WhatsApp: send, react, edit, revoke, change chat or group state
- Legacy tokens (`api.token`, `send.token`, `approve.token`) and agent credentials

## Trust boundaries

| Boundary | Enforcement |
|---|---|
| Other machines → API | Literal loopback address only (`api.ValidateAddr`); Host and Origin checks against DNS rebinding and browsers |
| Other local users → data dir | Private file and directory permissions (`internal/localfs`, Unix modes and Windows ACLs) |
| Agent → wasync | Scoped agent credential checked by wasync on every `/api/v1` request; scopes, chat allowlist, rate limit, expiry; audit log |
| Agent → destructive actions | Not grantable; human approval queue with typed confirmation |
| Same OS user → archive | Not a boundary. `wasync query` offline reads are equivalent to the user reading their own files and are read-only |
| WhatsApp content → agent | Message text and contact or chat names are untrusted data; never rendered as instructions or hints |

## Threats and mitigations

| Threat | Mitigation |
|---|---|
| Agent instructed by message content (prompt injection) to send or delete | Writes need explicit scopes and allowlisted chats granted by a human in wasync; destructive actions are never grantable; AXI renders content as delimited data |
| Agent or skill passes flags to bypass confirmation | Flags are UX only; wasync enforces scopes server-side |
| Leaked agent credential | Hash-only storage, per-client revoke and expiry, rate limits, audit trail |
| Agent credential replayed on legacy routes, or legacy token on v1 | The two credential types are isolated; each route family accepts only its own |
| Legacy read token mutating capture policy (existing behaviour) | Recorded in the baseline audit; unchanged for compatibility; agent credentials do not inherit it |
| Duplicate WhatsApp session | Only wasync pairs; `device.lock`; AXI never connects to WhatsApp |
| Data loss on upgrade | Additive-only migrations, automatic pre-migration backup, schema upgrade tests from every released schema |
| Contact names in logs, or hostile names reaching an agent | Direct-chat names (address-book, push and business names) come from whatsmeow's contact store and events and are stored only in the local archive; they are never written to logs or diagnostics. Names are trimmed of control characters and capped at 256 characters when stored, shown in `whatsapp-axi` as sanitized `untrusted_` values, and visible to an agent only through the same `read.chats` scope and chat allowlist as the chat itself. Push names are chosen by other people, so a name can impersonate a contact or carry instruction-like text; agents must treat it as data, and a name is never an authorization input (allowlists use chat IDs). Residual risk: an agent with `read.chats` learns the owner's saved contact names for the chats it may see |
| Secrets or bodies in logs | Audit and diagnostics record identifiers and outcomes only |
| Change feed skipping events | Transactional journal with sequence cursors and an epoch; no timestamp cursors |
| Webhook SSRF | Endpoints are registered only by a human (interactive terminal, typed confirmation; no scope, no API route). HTTPS only. The dialer resolves each name once, refuses the whole answer if any address is loopback, private, link-local (incl. cloud metadata), carrier-grade NAT, multicast, reserved or an IPv4-embedding IPv6 range, and connects to the vetted address, so DNS rebinding cannot change the target. Redirects are never followed and proxy variables are ignored |
| Webhook data egress | Opt-in per endpoint; events carry identifiers only (journal sequence, op, chat ID, message ID, message ref), never bodies or media, so a receiver must hold a scoped read credential to see content. Endpoints can be limited by chat and operation. Residual risk: chat IDs (phone numbers, group IDs) and the fact and timing of activity reach the receiver |
| Forged or replayed webhook requests | HMAC-SHA256 over timestamp and body with a per-endpoint secret, a fresh timestamp on every attempt, a stable event ID for deduplication. Receivers must enforce the five-minute window. The secret is stored in `webhooks.json` (0600) because signing needs it; same trust level as `api.token` |
| Lost or duplicated webhook events | Per-endpoint checkpoint in `webhook-state.json`, advanced only after a 2xx or a recorded dead letter; at-least-once, in order. A position that can no longer be served (restored archive, retention) resets to the journal head and is audited |
