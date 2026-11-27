package storage

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDiscoveryPreservesAllowlistAndAccountIsolation(t *testing.T) {
	ctx := context.Background()
	name := "archive ?# test.db"
	if runtime.GOOS == "windows" {
		name = "archive # test.db" // '?' is not a legal Windows filename character
	}
	path := filepath.Join(privateTestDir(t), name)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.UseAccount(ctx, "one@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	group := Group{ID: "123@g.us", Name: "Before"}
	if err = s.SaveGroups(ctx, account, []Group{group}); err != nil {
		t.Fatal(err)
	}
	groups, _ := s.Groups(ctx, account)
	if len(groups) != 1 || groups[0].Enabled {
		t.Fatal("discovery must default to disabled")
	}
	if err = s.SetEnabled(ctx, account, []string{group.ID}, true); err != nil {
		t.Fatal(err)
	}
	group.Name = "After"
	group.Enabled = false
	if err = s.SaveGroups(ctx, account, []Group{group, group}); err != nil {
		t.Fatal(err)
	}
	groups, err = s.Groups(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || !groups[0].Enabled || groups[0].Name != "After" {
		t.Fatalf("refresh changed allowlist or duplicated metadata: %+v", groups)
	}
	other, err := s.UseAccount(ctx, "two@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveGroups(ctx, other, []Group{group}); err != nil {
		t.Fatal(err)
	}
	groups, _ = s.Groups(ctx, other)
	if len(groups) != 1 || groups[0].Enabled {
		t.Fatal("allowlist crossed accounts")
	}
	again, err := s.UseAccount(ctx, "one@s.whatsapp.net")
	if err != nil || again != account {
		t.Fatalf("account identity changed on re-pair: %d %v", again, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	groups, _ = s.Groups(ctx, account)
	if len(groups) != 1 || !groups[0].Enabled {
		t.Fatal("preferences did not survive restart")
	}
	a, err := s.Account(ctx)
	if err != nil || a == nil || a.ID != account || a.LastDiscovery == nil {
		t.Fatalf("active account not persisted: %+v %v", a, err)
	}
}
func TestEnableBatchRollsBackOnUnknownGroup(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(privateTestDir(t), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, _ := s.UseAccount(ctx, "one@s.whatsapp.net")
	if err = s.SaveGroups(ctx, id, []Group{{ID: "123@g.us", Name: "test"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, id, []string{"123@g.us", "missing@g.us"}, true); err == nil {
		t.Fatal("expected unknown group error")
	}
	groups, _ := s.Groups(ctx, id)
	if groups[0].Enabled {
		t.Fatal("partially committed invalid selection")
	}
}
func TestMigrationRejectsNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTestDir(t), "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// A future schema that needs a newer writer raises min_writer with it.
	if _, err = s.db.Exec("UPDATE schema_compat SET min_reader=99,min_writer=99"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(ctx, path); err == nil {
		s.Close()
		t.Fatal("accepted future schema")
	}
}
func TestSQLiteSettingsAndFTS5(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(privateTestDir(t), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var journal string
	var fk, syncMode int
	if err = s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	s.db.QueryRow("PRAGMA synchronous").Scan(&syncMode)
	if journal != "wal" || fk != 1 || syncMode != 2 {
		t.Fatalf("unsafe settings %s %d %d", journal, fk, syncMode)
	}
	// The documented build must include FTS5 before later phases depend on it.
	if _, err = s.db.Exec("CREATE VIRTUAL TABLE temp.search_probe USING fts5(text)"); err != nil {
		t.Fatalf("build with -tags sqlite_fts5: %v", err)
	}
	if _, err = s.db.Exec("INSERT INTO temp.search_probe(text) VALUES('hello searchable archive')"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM temp.search_probe WHERE search_probe MATCH 'searchable'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("FTS5 query failed: %d %v", count, err)
	}
}

// Capture switched on for an agent's approved list is marked as such until the
// person enables the chat; a chat the person already syncs is never marked.
func TestListCaptureIsMarkedUntilThePersonEnablesTheChat(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(privateTestDir(t), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, _ := s.UseAccount(ctx, "one@s.whatsapp.net")
	if err = s.SaveGroups(ctx, id, []Group{{ID: "list@s.whatsapp.net", Name: "list"}, {ID: "mine@g.us", Name: "mine"}}); err != nil {
		t.Fatal(err)
	}
	state := func() map[string]Group {
		t.Helper()
		groups, err := s.Groups(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		summaries, err := s.GroupSummaries(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]Group{}
		for i, g := range groups {
			out[g.ID] = g
			for _, sg := range summaries {
				if sg.ID == g.ID && (sg.Enabled != g.Enabled || sg.ListCapture != g.ListCapture) {
					t.Fatalf("Groups and GroupSummaries disagree on %s: %+v %+v", g.ID, groups[i], sg)
				}
			}
		}
		return out
	}
	if err = s.SetEnabled(ctx, id, []string{"mine@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []string{"list@s.whatsapp.net", "mine@g.us"} {
		if err = s.EnableListCapture(ctx, id, chat); err != nil {
			t.Fatal(err)
		}
	}
	got := state()
	if g := got["list@s.whatsapp.net"]; !g.Enabled || !g.ListCapture {
		t.Fatalf("a chat switched on for a list: %+v", g)
	}
	if g := got["mine@g.us"]; !g.Enabled || g.ListCapture {
		t.Fatalf("a chat the person synced became list capture: %+v", g)
	}
	if err = s.EnableCapture(ctx, id, "list@s.whatsapp.net", false); err != nil {
		t.Fatal(err)
	}
	if g := state()["list@s.whatsapp.net"]; !g.Enabled || g.ListCapture {
		t.Fatalf("the person enabled it but it is still list capture: %+v", g)
	}
	if err = s.SetEnabled(ctx, id, []string{"list@s.whatsapp.net"}, false); err != nil {
		t.Fatal(err)
	}
	if err = s.EnableListCapture(ctx, id, "list@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, id, []string{"list@s.whatsapp.net"}, true); err != nil {
		t.Fatal(err)
	}
	if g := state()["list@s.whatsapp.net"]; !g.Enabled || g.ListCapture {
		t.Fatalf("enabling it from the TUI or CLI kept it as list capture: %+v", g)
	}
}

// An archive from a release before list capture was recorded, with a synced
// chat, is marked once; a new archive never is.
func TestListCaptureBackfillRunsOnceAndOnlyOnUpgradedArchives(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(privateTestDir(t), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if pending, err := fresh.ListCaptureBackfillPending(ctx); err != nil || pending {
		t.Fatalf("a new archive asks for the backfill: %v %v", pending, err)
	}
	id, _ := fresh.UseAccount(ctx, "one@s.whatsapp.net")
	if err = fresh.SaveGroups(ctx, id, []Group{{ID: "mine@s.whatsapp.net", Name: "mine"}}); err != nil {
		t.Fatal(err)
	}
	if err = fresh.SetEnabled(ctx, id, []string{"mine@s.whatsapp.net"}, true); err != nil {
		t.Fatal(err)
	}
	if err = fresh.BackfillListCapture(ctx, id, []string{"mine@s.whatsapp.net"}); err != nil {
		t.Fatal(err)
	}
	if groups, _ := fresh.Groups(ctx, id); !groups[0].PersonSynced() {
		t.Fatalf("a new archive's synced chat was marked: %+v", groups[0])
	}

	upgraded, err := Open(ctx, buildArchive(t, 7)) // one@g.us synced, two@g.us not
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if pending, err := upgraded.ListCaptureBackfillPending(ctx); err != nil || !pending {
		t.Fatalf("an upgraded archive with a synced chat does not ask for the backfill: %v %v", pending, err)
	}
	if err = upgraded.BackfillListCapture(ctx, 1, []string{"one@g.us", "two@g.us"}); err != nil {
		t.Fatal(err)
	}
	state := func() map[string]Group {
		groups, err := upgraded.Groups(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]Group{}
		for _, g := range groups {
			out[g.ID] = g
		}
		return out
	}
	if got := state(); !got["one@g.us"].ListCapture || got["two@g.us"].ListCapture {
		t.Fatalf("backfill marks: %+v", got)
	}
	if pending, err := upgraded.ListCaptureBackfillPending(ctx); err != nil || pending {
		t.Fatalf("the backfill is still pending after it ran: %v %v", pending, err)
	}
	// The person syncs it again; a later run marks nothing.
	if err = upgraded.SetEnabled(ctx, 1, []string{"one@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	if err = upgraded.BackfillListCapture(ctx, 1, []string{"one@g.us"}); err != nil {
		t.Fatal(err)
	}
	if got := state(); !got["one@g.us"].PersonSynced() {
		t.Fatalf("the backfill ran twice: %+v", got["one@g.us"])
	}
}
