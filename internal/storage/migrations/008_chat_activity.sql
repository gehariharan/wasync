-- chat_activity remembers when each chat was last active, as WhatsApp reports
-- it, including chats that are not synced, so the chat picker can list the most
-- recent chats first. It holds one time per chat, never message content.
-- Older readers ignore the table and an older writer only leaves it stale, so
-- both compatibility values stay where migration 007 left them.
CREATE TABLE IF NOT EXISTS chat_activity(
 account_id INTEGER NOT NULL,
 chat_id TEXT NOT NULL,
 last_active_at INTEGER NOT NULL,
 PRIMARY KEY(account_id,chat_id)
);

INSERT INTO schema_compat(id,min_reader,min_writer) VALUES(1,5,6)
 ON CONFLICT(id) DO UPDATE SET min_reader=excluded.min_reader,min_writer=excluded.min_writer;
