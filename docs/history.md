# History and capture

This page explains what wasync stores, how it asks your phone for older messages, and the
limits of both. In short: wasync keeps only the chats you turn on, captures new messages while
it runs, and asks for older ones on a best-effort basis.

## What is captured

Only explicitly enabled chats are archived. You choose them on the web app's chat picker
(`wasync web`, then the ☑ icon), on the setup page, with `wasync chats enable`, or when you
approve an agent's request for a chat. Disabling a chat takes effect at
the database transaction boundary and retains existing records. Purge disables
capture and removes the chat's stored records and search entries.

The archive captures text, extended text, captions, sender/chat/message IDs,
original timestamps, whether the message was sent by you, reply IDs and safe
media metadata. By default it does not download attachment files; see [Media](media.md).
It never stores thumbnails, quoted message bodies, download URLs
or media decryption keys in the archive. Reactions retain emoji, target message identity, sender timestamp and removal
events. Unsupported types retain minimal type metadata. Reaction records are
observed events, not a reconstructed current reaction count.

Live and historical messages share the same allowlist and normalization rules.
Repeated deliveries are deduplicated by account, chat and WhatsApp message ID.
Delivered edits update the current text. Delivered revokes clear the body and
retain tombstones. A revoke arriving before the original prevents a subsequent
history delivery from restoring the body; edits arriving before originals are
held as pending content until the original arrives. Pending edits expire after
30 days, while revocation tombstones are retained.

View-once content is excluded. Ephemeral text is kept only when a usable expiry
can be determined, hidden immediately after expiry and cleared on maintenance
(every ten seconds) or restart. Ephemeral edits without an absolute deadline are
skipped rather than extending their retention from the edit time. Retention does
not remove copies in backups or guarantee forensic erasure of old SQLite pages.

Chat discovery metadata (chat ID and name) is cached for every chat so you can choose; message
bodies from chats you have not enabled are discarded. See
[Direct chats and contact names](cli.md#direct-chats-and-contact-names) for how direct chats are
discovered and named.

## Automatic historical download

Whenever you enable a chat, wasync automatically queues its historical download.
Chats already enabled when upgrading are queued on the next launch too.

1. Keep the chat enabled and wasync running (`wasync serve`, the setup page or the terminal UI).
   Keep your phone connected to the internet so it can respond; opening WhatsApp may help it
   process the request.
2. Received messages enter the archive automatically. A background worker checks the local
   queue every two seconds and asks the phone for the next 50 older messages while the
   returned cursor advances. It sends one request at a time, with a pause between pages.
3. To retry after a timeout or failure, use `wasync sync --chat CHAT_ID --history`,
   `POST /api/chats/{id}/history`, or **h** in the [terminal UI](tui.md#history-in-the-terminal-ui).
   When you approve an agent's request with **full history sync**, the same request is queued.

The request requires a genuine known message ID and timestamp. wasync uses its
oldest local message, or a metadata-only cursor previously supplied by WhatsApp. For an empty
chat with no known cursor, history waits for a known message. Once a new message is observed in
that chat, historical requests start automatically. A single newly captured message is enough
to bootstrap the first request.

Metadata-only history anchors (chat ID, message ID, timestamp and direction) survive restarts,
including for groups whose capture is disabled; their message bodies are never archived.
Direct-chat anchors are persisted only after capture is enabled; unselected direct chats retain
discovery metadata (chat ID/name) but no persistent per-message cursor. Selected chats' request
cursors and job states are also persisted so pagination can resume after restarting. Purging
clears the stored anchor for that chat; future WhatsApp deliveries may supply a new one.

Disabling a chat stops further requests and ignores its content on delivery.
Empty/repeated pages stop the attempt as **No older page returned (coverage
unknown)**. Failed sends and a two-minute response timeout are shown separately;
no response is not proof of an empty or complete history. Requests are protocol
messages addressed to your own linked device, not chat messages posted to the
selected chat. Live messages arrive through WhatsApp events; chat contents are not polled.

## History and recovery limits

Historical import is **best-effort**. Available history events are processed in
small transactions. Original timestamps are preserved; batch state and durable
chunk counts record progress. An interrupted batch is marked interrupted after
restart. Reported upstream progress, even 100%, does not mean the archive is
complete. Neither enabling nor reconnecting guarantees complete history or recovery of
downtime messages. Do not re-pair simply to obtain history: replay is not guaranteed.

The earliest versions of wasync counted the history WhatsApp delivered at pairing without
storing it. That history was discarded and cannot be imported from those counters. The
current capture path imports any history WhatsApp delivers again; it neither assumes nor
promises that the phone will replay the old batch.

The pinned library handles ordinary network reconnects and the special reconnect
after pairing. Its ordinary retry delay is currently linear. A custom exponential
supervisor remains future hardening work. Reconnects and periods when wasync was
stopped create uncertain account intervals; ending an interval means observation
resumed, not that all missed messages were recovered. Decryption failures for an
enabled chat also mark uncertainty. `wasync query status`, `query groups` and the terminal UI
report these potential gaps.

Selected live messages are written synchronously before our handler reports
success; the library's synchronous acknowledgement option is enabled. A database
write failure stops capture and is reported. This reduces the time between
delivery and a durable commit, but does not guarantee complete delivery or replay
after every failure. The library's optional decrypted-event buffer is not enabled
because it could retain plaintext for unselected chats.

## When the phone unlinks wasync

If the phone unlinks wasync (WhatsApp → Linked Devices → remove), WhatsApp
rejects the saved login from then on. Run `wasync login`, or press `c` in the
terminal UI. wasync notices the rejection, clears only the dead login from `session.db`
and shows a new QR code in the same run; `wasync login` also prints a note
saying the login was cleared. Your archive, chat choices and settings are not
touched, and no file needs to be moved by hand.

Other session errors do not clear the saved login. If WhatsApp reports
that the session was replaced by another client, that this client version is too
old, or that the account is temporarily restricted, fix that cause and restart
wasync.

## Pairing-time history options verified in upstream source

The pinned [whatsmeow](https://github.com/tulir/whatsmeow) implementation supports:

1. Pairing-time history notifications delivered by WhatsApp.
2. `BuildHistorySyncRequest` / `SendPeerMessage`: on-demand pages preceding a
   known message ID and timestamp, with 50 recommended per request.
3. `DownloadHistorySync` and `SendHistorySyncServerErrorReceipt`: download or retry
   a specific history notification already delivered; these do not list arbitrary
   server history or remove the cursor requirement.
4. The upstream `store.DeviceProps.RequireFullSync` pairing hint. To request it
   when linking a new device, start wasync with `--pairing-full-history` (for example
   `wasync --pairing-full-history setup`, or set `WASYNC_PAIRING_FULL_HISTORY=1`). It is an
   experimental best-effort request, not a completeness guarantee. It does not replay history
   for an existing link.

Pair with `wasync setup`, the terminal UI or an interactive `wasync login` to seed persistent
chat cursor metadata when WhatsApp delivers history; these can archive enabled chats during
pairing. The diagnostic forms of `login` (see [the command line](cli.md#diagnostic-login-and-status))
discard bodies even for already-enabled chats. Changing pairing hints
never automatically logs you out or deletes your existing archive.

## Archive schema

The archive migrates automatically when a newer wasync starts (`wasync serve`, the setup page or
the terminal UI); do not delete your existing data directory. Schema version 4 added cursor
metadata and version 5 persistent media policies. Read-only `query` commands never migrate, so
start the updated wasync once before querying an older archive with it. Group and direct chats
share the archive while preserving legacy `group_id` keys and group-only listing commands; use
`chat_id` and the `chats` commands for both.
