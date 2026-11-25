# Webhooks

Push a small notification to your own HTTPS service whenever the archive changes,
instead of polling the change feed. Webhooks are off until a human registers an
endpoint, and they never carry message text or media.

```sh
wasync webhooks add crm --url https://hooks.example.com/wasync --chat 123456789-123@g.us
wasync webhooks test crm        # signed ping; prints the HTTP outcome
wasync webhooks list
wasync webhooks deadletter crm  # events that could not be delivered
wasync webhooks remove crm
```

`add` prints a signing secret (`whsec_...`) once and asks you to type the endpoint
name; `list`, `remove`, `test` and `deadletter` need an interactive terminal too.
There is no `--yes`, no agent scope for webhooks and no API route that manages
them. The secret is also stored in `webhooks.json`, readable only by your OS
user, because HMAC signing needs the raw key. Delivery runs inside the running
wasync (`serve`, the setup page or the terminal UI), picks up changes to `webhooks.json`
within seconds, and stops when that wasync stops. `deadletter --clear` empties the
dead-letter list.

Each event is one JSON POST built from a change-journal entry:

```json
{"id":"evt_9cc5fde8_32996e716007dc74d42fb048efd85e96_42","type":"change",
 "endpoint":"9cc5fde8","epoch":"32996e71...","sequence":42,"cursor":"c1_...",
 "op":"message.created","op_version":1,"chat_id":"123456789-123@g.us",
 "message_id":"3EB0...","ref":"m1_...","committed_at":"2026-10-20T12:00:00Z"}
```

The receiver reads the message itself with an agent credential that has
`read.messages` (`GET /api/v1/messages/{ref}`); the message may already be gone.
Chat IDs contain phone numbers or group IDs, so use `--chat` (repeatable) and
`--op` to limit what an endpoint sees. Operations are `message.created`,
`message.updated`, `message.revoked`, `message.expired`, `message.purged`,
`capture.enabled` and `capture.disabled` (default: all).

Verify every request: `Wasync-Webhook-Signature` is `v1=` plus the lowercase hex
HMAC-SHA256, keyed with the secret, of the `Wasync-Webhook-Timestamp` value (Unix
seconds), a `.`, and the raw request body. Reject timestamps more than five
minutes from your clock and compare signatures in constant time. Every attempt is
signed afresh, so a captured request cannot be replayed later.

```python
import hmac, hashlib, time
def valid(secret, timestamp, signature, body: bytes) -> bool:
    if abs(time.time() - int(timestamp)) > 300:
        return False
    want = "v1=" + hmac.new(secret.encode(), timestamp.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, signature)
```

Delivery is at-least-once and in journal order per endpoint. Dedupe on the event
`id` (also sent as `Wasync-Webhook-Id`); it is stable across retries and restarts.
Answer with any 2xx to acknowledge. A 5xx, 408, 429 or network error is retried
after 5 seconds, 30 seconds, 2 minutes and 10 minutes; any other answer, or the
fifth failure, moves the event to the dead-letter list and delivery continues
with the next event. If the dead-letter file cannot be written, the event is held
and retried every 10 minutes rather than dropped. Endpoints are independent, so a failing one does not delay
another. A new endpoint receives changes from the moment the owner first sees it,
not history. If the archive is restored from a backup or the journal position
becomes unreadable, the endpoint skips to the journal head and records
`webhook.cursor_reset` in `agent-audit.log`; use the changes feed to resnapshot.

Destinations are restricted to the public internet. The URL must be `https` with
a hostname (no credentials, fragment or local names). Whenever wasync connects it
resolves the name once, refuses the whole answer if any address is loopback,
private, link-local (including the cloud metadata address), carrier-grade NAT,
multicast, reserved or an IPv4-embedding IPv6 range, and connects to the address
it checked, so DNS rebinding cannot redirect a request. Redirects are never
followed and proxy environment variables are ignored. Certificates must verify
(TLS 1.2 or later). Keep your receiver reachable from the internet or use a
tunnel; webhooks to `localhost` are intentionally unsupported.

Deliveries, retries, dead letters and endpoint changes are written to
`agent-audit.log` with identifiers and outcomes only. The dead-letter list
(`webhook-deadletter.jsonl`) holds the same kind of entries. Neither file ever
contains message text or secrets.
