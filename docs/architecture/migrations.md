# Archive migrations

The archive is a single SQLite file (`archive.db` in the data directory).
Its schema version is `PRAGMA user_version`; migrations are embedded in
`internal/storage/migrations/` and run in order, all in one transaction, when
the owner process (TUI or `serve`) opens the archive.

## Additive-only policy

Every migration must be additive so that an older release can still read and,
where declared, write an archive a newer release has migrated:

- Allowed: `CREATE TABLE`, `CREATE INDEX`, `CREATE TRIGGER`,
  `ALTER TABLE ... ADD COLUMN` (nullable or with a default), and `INSERT` or
  `UPDATE` that backfills new structures.
- Forbidden: `DROP TABLE`, `DROP INDEX`, `DROP VIEW`, `DROP TRIGGER`,
  `ALTER TABLE ... DROP COLUMN`, `ALTER TABLE ... RENAME`, and `DELETE FROM`.
- Do not change the meaning of an existing column or its allowed values.

`TestMigrationsAreAdditive` (`internal/storage/migration_safety_test.go`)
fails if an embedded migration contains a forbidden statement, and
`TestUpgradeFromEveryReleasedSchema` must cover every released schema.

## Automatic backup before migrating

When an existing archive has `user_version` between 1 and the binary's schema
minus one, the owner writes a backup before running any migration:

```
<archive>.pre-v<old version>-<UTC yyyymmddThhmmssZ>.bak
```

for example `archive.db.pre-v5-20261019T073015Z.bak`, in the same directory.

- The copy is made with `VACUUM INTO` on the live connection, so it is one
  consistent snapshot including committed WAL frames. It is compacted, so it
  is usually a little smaller than `archive.db` plus `archive.db-wal`.
- The file is created private first (0600 on macOS/Linux, an owner-and-SYSTEM
  DACL on Windows) and fsynced, together with its directory, before migrating.
- If the backup cannot be written (no disk space, a name collision, a
  permission error), migration does not start, the archive is left unchanged,
  and the open fails with `back up archive before migrating schema N to M`.
- Fresh archives (`user_version` 0), archives already at the binary's schema,
  and newer archives never get a backup.
- The three newest backups per archive are kept. Older files are deleted only
  when their names match the exact pattern above; anything else is left alone.

Disk use: each backup is a full copy of the message archive (media files are
stored separately and are not copied). Plan for free space of about one archive
size during an upgrade and up to three archive sizes for retained backups.
Backups contain the same private message content as the archive; delete them
when they are no longer needed.

## Restoring a backup (downgrade)

1. Stop wasync: quit the TUI and stop any `wasync serve` process or service.
2. In the data directory, copy the backup over the archive, for example
   `cp archive.db.pre-v5-20261019T073015Z.bak archive.db` (keep the backup).
3. Remove `archive.db-wal` and `archive.db-shm` if they exist; they belong to
   the newer archive and must not be replayed onto the restored copy.
4. Make sure `archive.db` is private (`chmod 600 archive.db` on macOS/Linux).
5. Reinstall the previous release and start it.

Messages captured after the backup was taken are not in the restored archive.
Change-journal consumers must resnapshot after a restore: a schema-5 backup is
migrated again with a new journal epoch, and a schema-6 backup has a journal
head behind their cursors; both cases return `cursor_expired` rather than
silently skipping changes.

## Forward compatibility: `schema_compat`

An older binary refuses an archive whose `user_version` is newer than it
supports, unless the archive declares that the older binary is safe. A
migration declares this with a single-row table:

```sql
CREATE TABLE IF NOT EXISTS schema_compat(
 id INTEGER PRIMARY KEY CHECK(id=1),
 min_reader INTEGER NOT NULL,
 min_writer INTEGER NOT NULL
);
INSERT INTO schema_compat(id,min_reader,min_writer) VALUES(1,5,6)
 ON CONFLICT(id) DO UPDATE SET min_reader=excluded.min_reader,min_writer=excluded.min_writer;
```

- `min_reader`: the lowest schema a binary must support to open the archive
  read-only (`wasync query`). Additive changes keep it at the oldest schema
  whose queries still return correct results.
- `min_writer`: the lowest schema a binary must support to open the archive as
  the owner and write to it without migrating. Raise it to the new version
  when older writers would break an invariant of the new schema (for example a
  change journal that every write must append to).
- When the table or its row is absent, a newer archive is refused exactly as
  before: the owner reports `archive schema N is newer than supported schema M`
  and `wasync query` reports `archive schema N is incompatible with query
  schema M`.
- An owner that accepts a newer archive never migrates, rewrites
  `user_version`, or backs it up.
- Every migration from the first one that creates `schema_compat` onward must
  set both values in the same migration, and the values may only increase.

Migration 006 (the change journal) is the first to create `schema_compat`, with
`min_reader=5` and `min_writer=6`: schema-5 queries remain correct, but a
schema-5 owner would write without appending to the journal.

Binaries before this check (schema 5 releases without it) ignore
`schema_compat` and always refuse newer archives, so the release containing it
must ship before the first new migration.

## Change journal (schema 6)

Migration 006 adds `change_journal`, `journal_meta` and `consumer_checkpoints`
(ADR-001 D-4). Every archive mutation appends identifier-only rows (never
message bodies) in its own transaction, so a rolled-back write leaves no
journal row:

| op | written by |
|---|---|
| `message.created` | a new original, or a placeholder (edit or revoke that arrived first) gaining its original |
| `message.updated` | an applied edit |
| `message.revoked` | an applied revoke |
| `message.expired` | `Expire` removing disappearing-message content |
| `message.purged` | a chat purge (one row per removed message) or a local clean of one message |
| `capture.enabled` / `capture.disabled` | a change of a chat's capture preference, including the disable done by a purge |

Duplicate deliveries change no row and journal nothing. Ops are change
notifications: consumers re-read the message by ref and delete their replica
when it is no longer returned. Entries older than 30 days are pruned by
`Expire`, which raises `journal_meta.low_water`; cursors behind it return
`cursor_expired`. `journal_meta.epoch` is random per archive, so cursors from
a recreated archive also return `cursor_expired`. Checkpoints are monotonic
per `(account, consumer)` and are committed only through the owner API.

Webhook delivery (`internal/webhook`) reads this journal but needs no migration:
each endpoint's position is kept in `webhook-state.json` (written only by the
owner), not in `consumer_checkpoints`. That lets the owner reset an endpoint to
the journal head when an archive is restored, which API checkpoints may not do
(they only move forward). After restoring a backup, webhook endpoints skip to
the head and record `webhook.cursor_reset` in `agent-audit.log`.
