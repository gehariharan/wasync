// Package storage owns the local SQLite archive and its versioned migrations.
package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"github.com/gehariharan/wasync/internal/localfs"
	_ "github.com/mattn/go-sqlite3"
	"net/url"
	"path/filepath"
	"runtime"
	"time"
)

//go:embed migrations/001_groups.sql
var migration1 string

//go:embed migrations/002_capture.sql
var migration2 string

//go:embed migrations/003_history_jobs.sql
var migration3 string

//go:embed migrations/004_history_anchors.sql
var migration4 string

//go:embed migrations/005_media_preferences.sql
var migration5 string

//go:embed migrations/006_change_journal.sql
var migration6 string

// migrations lists every embedded migration in order; migration i+1 sets
// user_version to i+1. New migrations must follow docs/architecture/migrations.md.
var migrations = []string{migration1, migration2, migration3, migration4, migration5, migration6}

// schemaVersion is the newest archive schema this binary writes.
var schemaVersion = len(migrations)

// SchemaVersion is the archive schema this build migrates to and queries.
const SchemaVersion = 6

type Store struct{ db *sql.DB }
type Group struct {
	ID                   string
	Name                 string
	Enabled              bool
	MediaPolicy          string
	MessageCount         int64
	LastMessage          *time.Time
	HistoryState         string
	GapCount             int64
	HistoryRequest       string
	HistoryRequestError  string
	HistoryBatchMessages int
}
type Account struct {
	ID            int64
	JID           string
	LastDiscovery *time.Time
}

// DSN uses URL encoding so paths containing ?, #, or spaces are literal filenames.
func DSN(path string) string {
	u := url.URL{Scheme: "file", Path: sqlitePath(path)}
	q := u.Query()
	q.Set("_foreign_keys", "on")
	q.Set("_busy_timeout", "5000")
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "FULL")
	u.RawQuery = q.Encode()
	return u.String()
}

func Prepare(path string) error {
	if err := localfs.EnsureFile(path); err != nil {
		return err
	}
	return localfs.CheckSidecars(path)
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := Prepare(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", DSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err = db.PingContext(ctx); err == nil {
		err = s.migrate(ctx, path)
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open archive: %w", err)
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) migrate(ctx context.Context, path string) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		compat, err := readSchemaCompat(ctx, s.db)
		if err != nil {
			return err
		}
		if compat == nil {
			return fmt.Errorf("archive schema %d is newer than supported schema %d", version, schemaVersion)
		}
		if schemaVersion < compat.MinWriter {
			return fmt.Errorf("archive schema %d is newer than supported schema %d and requires writer schema %d", version, schemaVersion, compat.MinWriter)
		}
		// A newer additive schema that declares this binary a safe writer
		// opens as-is; never downgrade or rewrite it.
		return nil
	}
	if version >= 1 && version < schemaVersion {
		if _, err := backupArchive(ctx, s.db, path, version); err != nil {
			return fmt.Errorf("back up archive before migrating schema %d to %d (archive left unchanged): %w", version, schemaVersion, err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current != version {
		return fmt.Errorf("archive schema changed from %d to %d while opening; retry", version, current)
	}
	for i := version; i < len(migrations); i++ {
		if _, err = tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) UseAccount(ctx context.Context, jid string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO accounts(jid,created_at) VALUES(?,?)
 ON CONFLICT(jid) DO UPDATE SET jid=excluded.jid RETURNING id`, jid, time.Now().UnixMilli()).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO app_state(id,account_id) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET account_id=excluded.account_id`, id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
func (s *Store) Account(ctx context.Context) (*Account, error) {
	var a Account
	var last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT a.id,a.jid,a.last_discovery_at FROM accounts a JOIN app_state s ON s.account_id=a.id WHERE s.id=1`).Scan(&a.ID, &a.JID, &last)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if last.Valid {
		t := time.UnixMilli(last.Int64).UTC()
		a.LastDiscovery = &t
	}
	return &a, nil
}
func (s *Store) SaveGroups(ctx context.Context, account int64, groups []Group) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, g := range groups {
		_, err = tx.ExecContext(ctx, `INSERT INTO groups(account_id,group_id,name,created_at,updated_at) VALUES(?,?,?,?,?)
  ON CONFLICT(account_id,group_id) DO UPDATE SET name=excluded.name,updated_at=excluded.updated_at`, account, g.ID, g.Name, now, now)
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE accounts SET last_discovery_at=? WHERE id=?", now, account); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Groups(ctx context.Context, account int64) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id,name,sync_enabled,COALESCE((SELECT mode FROM media_preferences p WHERE p.account_id=groups.account_id AND p.group_id=groups.group_id),'inherit') FROM groups WHERE account_id=? ORDER BY name COLLATE NOCASE,group_id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make([]Group, 0)
	for rows.Next() {
		var g Group
		if err = rows.Scan(&g.ID, &g.Name, &g.Enabled, &g.MediaPolicy); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}
func (s *Store) SetEnabled(ctx context.Context, account int64, ids []string, enabled bool) error {
	return s.setEnabled(ctx, account, ids, enabled, historyOnEnable)
}

// historyMode says when enabling capture queues a request for older messages.
type historyMode int

const (
	historyOnEnable historyMode = iota // only when capture was off (the TUI and CLI default)
	historyNever                       // capture from now on only
	historyAlways                      // also for an already enabled chat, unless a request is in flight
)

// EnableCapture turns capture on for one cached chat. With withHistory it also
// queues a request for older messages (unless one is already in flight), the
// same as enabling the chat in the TUI; without it capture starts from now on
// and no history is requested.
func (s *Store) EnableCapture(ctx context.Context, account int64, chat string, withHistory bool) error {
	mode := historyNever
	if withHistory {
		mode = historyAlways
	}
	return s.setEnabled(ctx, account, []string{chat}, true, mode)
}

func (s *Store) setEnabled(ctx context.Context, account int64, ids []string, enabled bool, history historyMode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		var wasEnabled bool
		if err = tx.QueryRowContext(ctx, "SELECT sync_enabled FROM groups WHERE account_id=? AND group_id=?", account, id).Scan(&wasEnabled); err != nil {
			return fmt.Errorf("group %q is not in the cached group list; refresh discovery first", id)
		}
		result, e := tx.ExecContext(ctx, `UPDATE groups SET sync_enabled=?,enabled_at=CASE WHEN ? AND sync_enabled=0 THEN ? ELSE enabled_at END,updated_at=? WHERE account_id=? AND group_id=?`, enabled, enabled, time.Now().UnixMilli(), time.Now().UnixMilli(), account, id)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return fmt.Errorf("group %q is not in the cached group list; refresh discovery first", id)
		}
		if enabled != wasEnabled {
			op := OpCaptureDisabled
			if enabled {
				op = OpCaptureEnabled
			}
			if err = journal(ctx, tx, account, id, "", op, time.Now().UnixMilli()); err != nil {
				return err
			}
		}
		queueHistory := enabled && !wasEnabled && history != historyNever
		if enabled && wasEnabled && history == historyAlways {
			var state string
			if e := tx.QueryRowContext(ctx, "SELECT state FROM history_jobs WHERE account_id=? AND group_id=?", account, id).Scan(&state); e != nil && e != sql.ErrNoRows {
				return e
			}
			queueHistory = state != "sending" && state != "waiting"
		}
		if queueHistory {
			if _, err = tx.ExecContext(ctx, `INSERT INTO history_jobs(account_id,group_id,updated_at) VALUES(?,?,?)
   ON CONFLICT(account_id,group_id) DO UPDATE SET state='queued',request_id='',requested_at=NULL,last_error='',updated_at=excluded.updated_at`, account, id, time.Now().UnixMilli()); err != nil {
				return err
			}
		} else if !enabled {
			if _, err = tx.ExecContext(ctx, "UPDATE history_jobs SET state='disabled',request_id='',updated_at=? WHERE account_id=? AND group_id=?", time.Now().UnixMilli(), account, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Ping checks archive readiness without reading message content.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// SQLite file URIs require a slash before Windows drive letters.
func sqlitePath(path string) string {
	if runtime.GOOS == "windows" {
		path = filepath.ToSlash(path)
		if filepath.VolumeName(path) != "" {
			path = "/" + path
		}
	}
	return path
}
