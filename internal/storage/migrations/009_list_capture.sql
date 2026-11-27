-- list_capture marks a chat whose capture was switched on only so an agent's
-- approved list can see replies, not by the person. Such a chat is not shared
-- with agents that use every synced chat. The person enabling the chat clears
-- it. Schema 5 readers stay correct (they ignore it), but a schema 6, 7 or 8
-- writer would enable a chat without clearing it, so writers need schema 9.
ALTER TABLE groups ADD COLUMN list_capture INTEGER NOT NULL DEFAULT 0;

-- An archive that already synced chats may hold chats an earlier release
-- switched on for an agent's approved list without saying so. Its row asks the
-- owner to mark those once (BackfillListCapture); a new archive gets none.
CREATE TABLE list_capture_backfill(id INTEGER PRIMARY KEY CHECK(id=1));
INSERT INTO list_capture_backfill(id) SELECT 1 WHERE EXISTS(SELECT 1 FROM groups WHERE sync_enabled=1);

INSERT INTO schema_compat(id,min_reader,min_writer) VALUES(1,5,9)
 ON CONFLICT(id) DO UPDATE SET min_reader=excluded.min_reader,min_writer=excluded.min_writer;
