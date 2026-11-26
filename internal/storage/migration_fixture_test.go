package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// buildArchive writes an archive exactly as an older release left it: the first
// version migrations, that user_version, and synthetic rows for those tables.
func buildArchive(t *testing.T, version int) string {
	t.Helper()
	path := filepath.Join(privateTestDir(t), fmt.Sprintf("schema-%d.db", version))
	if err := Prepare(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`INSERT INTO accounts(id,jid,created_at) VALUES(1,'owner@lid',1)`,
		`INSERT INTO app_state(id,account_id) VALUES(1,1)`,
		`INSERT INTO groups(account_id,group_id,name,sync_enabled,created_at,updated_at) VALUES(1,'one@g.us','First',1,1,1),(1,'two@g.us','Second',0,1,1)`,
	}
	if version >= 2 {
		statements = append(statements,
			`INSERT INTO messages(account_id,group_id,message_id,sender_id,timestamp,message_type,text_content,source,has_original,received_at) VALUES(1,'one@g.us','m1','alice@lid',1759320000000,'text','legacy searchable body','live',1,1759320000000)`,
			`INSERT INTO messages(account_id,group_id,message_id,source,received_at,deleted_at) VALUES(1,'one@g.us','m0','live',1759320000000,1759320000000)`,
			`INSERT INTO sync_gaps(account_id,started_at,reason) VALUES(1,1,'restart')`)
	}
	if version >= 4 {
		statements = append(statements, `INSERT INTO history_anchors(account_id,group_id,message_id,timestamp,is_from_me) VALUES(1,'one@g.us','m1',1759320000000,0)`)
	}
	if version >= 5 {
		statements = append(statements, `INSERT INTO media_preferences(account_id,group_id,mode) VALUES(1,'one@g.us','manual')`)
	}
	for i := 0; i < version; i++ {
		if _, err = db.Exec(migrations[i]); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			for _, s := range statements[:3] {
				if _, err = db.Exec(s); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, s := range statements[3:] {
		if _, err = db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
		t.Fatal(err)
	}
	return path
}

// releasedSchemas is the newest schema shipped in a release; every schema from
// 1 to it must upgrade to the current SchemaVersion.
const releasedSchemas = 6

// TestUpgradeFromEveryReleasedSchema is the baseline for archive preservation:
// every schema an installed release can have upgrades without losing rows.
func TestUpgradeFromEveryReleasedSchema(t *testing.T) {
	ctx := context.Background()
	for version := 1; version <= releasedSchemas; version++ {
		t.Run(fmt.Sprintf("schema-%d", version), func(t *testing.T) {
			path := buildArchive(t, version)
			if version < SchemaVersion {
				if _, err := OpenReadOnly(ctx, path); err == nil || !strings.Contains(err.Error(), "incompatible") {
					t.Fatalf("read-only query accepted schema %d before migration: %v", version, err)
				}
			}
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			wantBackups := 1
			if version == SchemaVersion {
				wantBackups = 0
			}
			if got := backupFiles(t, path); len(got) != wantBackups {
				t.Fatalf("schema %d upgrade left backups %v, want %d", version, got, wantBackups)
			}
			var current int
			if err = s.db.QueryRow("PRAGMA user_version").Scan(&current); err != nil || current != SchemaVersion {
				t.Fatalf("schema %d migrated to %d: %v", version, current, err)
			}
			account, err := s.Account(ctx)
			if err != nil || account == nil || account.JID != "owner@lid" {
				t.Fatalf("account lost: %+v %v", account, err)
			}
			groups, err := s.Groups(ctx, account.ID)
			if err != nil || len(groups) != 2 || !groups[0].Enabled || groups[1].Enabled {
				t.Fatalf("capture preferences changed: %+v %v", groups, err)
			}
			wantPolicy := "inherit"
			if version >= 5 {
				wantPolicy = "manual"
			}
			if groups[0].MediaPolicy != wantPolicy {
				t.Fatalf("media policy %q, want %q", groups[0].MediaPolicy, wantPolicy)
			}
			if version >= 2 {
				page, err := s.Messages(ctx, account.ID, "", MessageQuery{Search: "searchable", Limit: 10})
				if err != nil || len(page.Items) != 1 || page.Items[0].MessageID != "m1" || page.Items[0].SentBy != "" {
					t.Fatalf("archived message not searchable after upgrade: %+v %v", page.Items, err)
				}
				var tombstones int
				s.db.QueryRow("SELECT count(*) FROM messages WHERE deleted_at IS NOT NULL").Scan(&tombstones)
				if tombstones != 1 {
					t.Fatal("tombstone lost")
				}
			}
			var jobs int
			s.db.QueryRow("SELECT count(*) FROM history_jobs WHERE group_id='one@g.us'").Scan(&jobs)
			if version < 3 && jobs != 1 {
				t.Fatal("history job not backfilled for enabled chat")
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			ro, err := OpenReadOnly(ctx, path)
			if err != nil {
				t.Fatalf("read-only open after upgrade: %v", err)
			}
			ro.Close()
		})
	}
}
