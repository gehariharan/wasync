-- sent_by names the agent that sent one of the owner's outgoing messages
-- through wasync; it is empty for everything else. Schema 5 readers stay
-- correct (they ignore it), and a schema 6 writer only leaves it empty.
ALTER TABLE messages ADD COLUMN sent_by TEXT NOT NULL DEFAULT '';

INSERT INTO schema_compat(id,min_reader,min_writer) VALUES(1,5,6)
 ON CONFLICT(id) DO UPDATE SET min_reader=excluded.min_reader,min_writer=excluded.min_writer;
