-- list_capture marks a chat whose capture was switched on only so an agent's
-- approved list can see replies, not by the person. Such a chat is not shared
-- with agents that use every synced chat. The person enabling the chat clears
-- it. Schema 5 readers stay correct (they ignore it), and a schema 6, 7 or 8
-- writer only leaves it as it is.
ALTER TABLE groups ADD COLUMN list_capture INTEGER NOT NULL DEFAULT 0;

INSERT INTO schema_compat(id,min_reader,min_writer) VALUES(1,5,6)
 ON CONFLICT(id) DO UPDATE SET min_reader=excluded.min_reader,min_writer=excluded.min_writer;
