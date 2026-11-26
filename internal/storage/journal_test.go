package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

type entry struct{ op, chat, message string }

func changes(t *testing.T, s *Store, account, after int64) []entry {
	t.Helper()
	p, err := s.Changes(context.Background(), account, after, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]entry, 0, len(p.Changes))
	for i, c := range p.Changes {
		if c.OpVersion != 1 || c.AccountID != account || i > 0 && c.Sequence <= p.Changes[i-1].Sequence {
			t.Fatalf("bad entry %+v", c)
		}
		out = append(out, entry{c.Op, c.ChatID, c.MessageID})
	}
	return out
}

func head(t *testing.T, s *Store) int64 {
	t.Helper()
	info, err := s.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.Head
}

func wantChanges(t *testing.T, got []entry, want ...entry) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("journal\n got %v\nwant %v", got, want)
	}
}

func TestJournalRecordsCaptureMutationsOnce(t *testing.T) {
	ctx := context.Background()
	s, id := captureStore(t)
	wantChanges(t, changes(t, s, id, -1), entry{OpCaptureEnabled, "one@g.us", ""})
	start := head(t, s)

	// Live first, then late history: an older message arriving later gets a
	// later sequence (commit order, never timestamp order); the history copy
	// of the live message is a duplicate and journals nothing.
	live := original("live", "live text")
	mustApply(t, s, id, live)
	late := original("late", "older history")
	late.Timestamp = live.Timestamp.Add(-time.Hour)
	late.Source = "history"
	dup := live
	dup.Source = "history"
	if _, err := s.ApplyHistory(ctx, id, 0, []Message{late, dup}); err != nil {
		t.Fatal(err)
	}
	// Duplicate delivery of both again.
	mustApply(t, s, id, live, late)
	wantChanges(t, changes(t, s, id, start),
		entry{OpMessageCreated, "one@g.us", "live"},
		entry{OpMessageCreated, "one@g.us", "late"})

	// Edit and revoke before the original: the placeholder journals the edit
	// or revoke, the original journals created, replays journal nothing.
	start = head(t, s)
	edit := Message{GroupID: "one@g.us", MessageID: "e", Kind: "edit", Text: "edited", Type: "text", MutationAt: time.Unix(1700000100, 0), Source: "live"}
	revoke := Message{GroupID: "one@g.us", MessageID: "r", Kind: "delete", MutationAt: time.Unix(1700000100, 0), Source: "live"}
	mustApply(t, s, id, edit, revoke)
	mustApply(t, s, id, original("e", "first"), original("r", "secret"))
	mustApply(t, s, id, edit, revoke, original("e", "first"), original("r", "secret"))
	// A later edit of an existing message and a revoke of a live one.
	edit2 := edit
	edit2.MutationAt = edit.MutationAt.Add(time.Minute)
	revokeLive := Message{GroupID: "one@g.us", MessageID: "live", Kind: "delete", MutationAt: time.Unix(1700000200, 0), Source: "live"}
	mustApply(t, s, id, edit2, revokeLive, revokeLive)
	wantChanges(t, changes(t, s, id, start),
		entry{OpMessageUpdated, "one@g.us", "e"},
		entry{OpMessageRevoked, "one@g.us", "r"},
		entry{OpMessageCreated, "one@g.us", "e"},
		entry{OpMessageCreated, "one@g.us", "r"},
		entry{OpMessageUpdated, "one@g.us", "e"},
		entry{OpMessageRevoked, "one@g.us", "live"})
	// Content is never in the journal.
	var bodies int
	s.db.QueryRow(`SELECT count(*) FROM change_journal WHERE chat_id LIKE '%text%' OR message_id LIKE '%edited%' OR op LIKE '%secret%'`).Scan(&bodies)
	if bodies != 0 {
		t.Fatal("journal contains content")
	}
	// Disabled chats journal nothing.
	start = head(t, s)
	other := original("x", "not captured")
	other.GroupID = "two@g.us"
	mustApply(t, s, id, other)
	wantChanges(t, changes(t, s, id, start))
}

func TestJournalCaptureTogglePurgeAndClear(t *testing.T) {
	ctx := context.Background()
	s, id := captureStore(t)
	mustApply(t, s, id, original("1", "a"), original("2", "b"))
	mustApply(t, s, id, Message{GroupID: "one@g.us", MessageID: "pending", Kind: "edit", Text: "x", Type: "text", MutationAt: time.Unix(1700000100, 0), Source: "live"})
	start := head(t, s)
	// Unchanged values journal nothing; changes journal once.
	if err := s.SetEnabled(ctx, id, []string{"one@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(ctx, id, []string{"one@g.us", "two@g.us"}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(ctx, id, []string{"one@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearMessage(ctx, id, "one@g.us", "2"); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeGroup(ctx, id, "one@g.us"); err != nil {
		t.Fatal(err)
	}
	// Purging a disabled chat journals only the removed rows (none here).
	if err := s.PurgeGroup(ctx, id, "two@g.us"); err != nil {
		t.Fatal(err)
	}
	wantChanges(t, changes(t, s, id, start),
		entry{OpCaptureDisabled, "one@g.us", ""},
		entry{OpCaptureEnabled, "one@g.us", ""},
		entry{OpMessagePurged, "one@g.us", "2"},
		entry{OpMessagePurged, "one@g.us", "1"},
		entry{OpMessagePurged, "one@g.us", "2"},
		entry{OpMessagePurged, "one@g.us", "pending"},
		entry{OpCaptureDisabled, "one@g.us", ""})
}

func TestJournalExpiryAndRetention(t *testing.T) {
	ctx := context.Background()
	s, id := captureStore(t)
	future := time.Now().Add(time.Hour)
	m := original("vanish", "disappearing")
	m.ExpiresAt = &future
	mustApply(t, s, id, m, original("keep", "kept"))
	if _, err := s.db.Exec(`UPDATE messages SET expires_at=1 WHERE message_id='vanish'`); err != nil {
		t.Fatal(err)
	}
	start := head(t, s)
	for i := 0; i < 2; i++ {
		if err := s.Expire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	wantChanges(t, changes(t, s, id, start), entry{OpMessageExpired, "one@g.us", "vanish"})

	// Entries older than the retention window are pruned and raise low-water.
	old := time.Now().Add(-JournalRetention - time.Hour).UnixMilli()
	if _, err := s.db.Exec(`UPDATE change_journal SET committed_at=? WHERE sequence<=?`, old, start); err != nil {
		t.Fatal(err)
	}
	if err := s.Expire(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := s.Journal(ctx)
	if err != nil || info.LowWater != start || info.Head != start+1 {
		t.Fatalf("journal after prune %+v %v", info, err)
	}
	if _, err = s.Changes(ctx, id, start-1, 10); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("cursor behind low-water: %v", err)
	}
	if _, err = s.Changes(ctx, id, 0, 10); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("start cursor behind low-water: %v", err)
	}
	wantChanges(t, changes(t, s, id, -1), entry{OpMessageExpired, "one@g.us", "vanish"})
	wantChanges(t, changes(t, s, id, start), entry{OpMessageExpired, "one@g.us", "vanish"})
	// A position ahead of the journal (for example a restored backup) expires too.
	if _, err = s.Changes(ctx, id, info.Head+1, 10); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("cursor beyond head: %v", err)
	}
	if _, err = s.SetCheckpoint(ctx, id, "c", info.Epoch, start-1); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("checkpoint behind low-water: %v", err)
	}
	// Retention never lowers low-water, and pruning everything keeps the head.
	if _, err = s.db.Exec(`UPDATE change_journal SET committed_at=?`, old); err != nil {
		t.Fatal(err)
	}
	if err = s.Expire(ctx); err != nil {
		t.Fatal(err)
	}
	if info, err = s.Journal(ctx); err != nil || info.LowWater != start+1 || info.Head != start+1 {
		t.Fatalf("journal after full prune %+v %v", info, err)
	}
	p, err := s.Changes(ctx, id, -1, 10)
	if err != nil || len(p.Changes) != 0 || p.Next != start+1 || p.HasMore {
		t.Fatalf("empty journal page %+v %v", p, err)
	}
}

func TestJournalPagination(t *testing.T) {
	ctx := context.Background()
	s, id := captureStore(t)
	for i := 0; i < 5; i++ {
		mustApply(t, s, id, original(fmt.Sprint(i), "x"))
	}
	var seen []int64
	after := int64(-1)
	for {
		p, err := s.Changes(ctx, id, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range p.Changes {
			seen = append(seen, c.Sequence)
		}
		if p.Next < after || len(p.Changes) > 2 {
			t.Fatalf("page %+v", p)
		}
		after = p.Next
		if !p.HasMore {
			break
		}
	}
	if len(seen) != 6 || !slices.IsSorted(seen) {
		t.Fatalf("pages returned %v", seen)
	}
	for _, limit := range []int{0, 1001} {
		if _, err := s.Changes(ctx, id, -1, limit); err == nil {
			t.Fatalf("limit %d accepted", limit)
		}
	}
}

func TestJournalRollsBackWithFailedWrite(t *testing.T) {
	s, id := captureStore(t)
	start := head(t, s)
	// A failure after the first message was written and journaled.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_boom BEFORE INSERT ON messages WHEN NEW.message_id='boom' BEGIN SELECT RAISE(ABORT,'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), id, []Message{original("ok", "x"), original("boom", "y")}); err == nil {
		t.Fatal("apply succeeded")
	}
	wantChanges(t, changes(t, s, id, start))
	if p := page(t, s, id, MessageQuery{}); len(p.Items) != 0 {
		t.Fatalf("message committed without its journal entry: %+v", p.Items)
	}
	// A failing journal write rolls back the message.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_journal BEFORE INSERT ON change_journal BEGIN SELECT RAISE(ABORT,'journal'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), id, []Message{original("ok", "x")}); err == nil || !strings.Contains(err.Error(), "journal") {
		t.Fatalf("apply without journal: %v", err)
	}
	if err := s.SetEnabled(context.Background(), id, []string{"two@g.us"}, true); err == nil {
		t.Fatal("capture toggled without journal")
	}
	if err := s.PurgeGroup(context.Background(), id, "one@g.us"); err == nil {
		t.Fatal("purge without journal")
	}
	if p := page(t, s, id, MessageQuery{}); len(p.Items) != 0 {
		t.Fatalf("message committed without its journal entry: %+v", p.Items)
	}
	if enabled, _ := s.IsEnabled(context.Background(), id, "two@g.us"); enabled {
		t.Fatal("capture toggle committed without journal")
	}
	if enabled, _ := s.IsEnabled(context.Background(), id, "one@g.us"); !enabled {
		t.Fatal("purge committed without journal")
	}
}

func TestJournalAndCheckpointsAreAccountScoped(t *testing.T) {
	ctx := context.Background()
	s, first := captureStore(t)
	second, err := s.UseAccount(ctx, "two@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveGroups(ctx, second, []Group{{ID: "one@g.us", Name: "Same JID"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, second, []string{"one@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	mustApply(t, s, first, original("a", "first account"))
	mustApply(t, s, second, original("b", "second account"))
	wantChanges(t, changes(t, s, first, -1), entry{OpCaptureEnabled, "one@g.us", ""}, entry{OpMessageCreated, "one@g.us", "a"})
	wantChanges(t, changes(t, s, second, -1), entry{OpCaptureEnabled, "one@g.us", ""}, entry{OpMessageCreated, "one@g.us", "b"})

	info, err := s.Journal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetCheckpoint(ctx, first, "replica", info.Epoch, info.Head); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.Checkpoint(ctx, second, "replica"); err != nil || found {
		t.Fatalf("checkpoint leaked across accounts: %v %v", found, err)
	}
	if _, err = s.SetCheckpoint(ctx, second, "replica", info.Epoch, 1); err != nil {
		t.Fatal(err)
	}
	cp, found, err := s.Checkpoint(ctx, first, "replica")
	if err != nil || !found || cp.Sequence != info.Head || cp.Epoch != info.Epoch || cp.Consumer != "replica" || cp.UpdatedAt.IsZero() {
		t.Fatalf("checkpoint %+v %v %v", cp, found, err)
	}
}

func TestCheckpointIsMonotonicAndEpochBound(t *testing.T) {
	ctx := context.Background()
	s, id := captureStore(t)
	mustApply(t, s, id, original("1", "x"), original("2", "y"))
	info, err := s.Journal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(info.Epoch) {
		t.Fatalf("epoch %q", info.Epoch)
	}
	if epoch, err := s.JournalEpoch(ctx); err != nil || epoch != info.Epoch {
		t.Fatalf("epoch accessor %q %v", epoch, err)
	}
	if _, err = s.SetCheckpoint(ctx, id, "c", info.Epoch, info.Head); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetCheckpoint(ctx, id, "c", info.Epoch, info.Head); err != nil {
		t.Fatalf("idempotent commit: %v", err)
	}
	if _, err = s.SetCheckpoint(ctx, id, "c", info.Epoch, info.Head-1); !errors.Is(err, ErrCheckpointBackwards) {
		t.Fatalf("moved backwards: %v", err)
	}
	if _, err = s.SetCheckpoint(ctx, id, "c", info.Epoch, info.Head+1); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("committed beyond head: %v", err)
	}
	if _, err = s.SetCheckpoint(ctx, id, "c", strings.Repeat("0", 32), info.Head); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("committed another epoch: %v", err)
	}
	if cp, _, _ := s.Checkpoint(ctx, id, "c"); cp.Sequence != info.Head {
		t.Fatalf("checkpoint changed to %d", cp.Sequence)
	}
	// A new archive has a different epoch.
	other, _ := captureStore(t)
	otherInfo, err := other.Journal(ctx)
	if err != nil || otherInfo.Epoch == info.Epoch {
		t.Fatalf("epochs %q %q %v", otherInfo.Epoch, info.Epoch, err)
	}
}

func TestReadOnlyStoreCannotWriteCheckpoints(t *testing.T) {
	ctx := context.Background()
	dir := privateTestDir(t)
	path := dir + "/archive.db"
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UseAccount(ctx, "one@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	info, err := ro.Journal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ro.Changes(ctx, id, -1, 10); err != nil {
		t.Fatalf("read-only changes: %v", err)
	}
	if _, err = ro.SetCheckpoint(ctx, id, "c", info.Epoch, 0); err == nil {
		t.Fatal("read-only store wrote a checkpoint")
	}
	if _, found, err := s.Checkpoint(ctx, id, "c"); err != nil || found {
		t.Fatalf("checkpoint stored: %v %v", found, err)
	}
}

func TestUpgradeFromSchema5CreatesJournal(t *testing.T) {
	ctx := context.Background()
	path := buildArchive(t, 5)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := backupFiles(t, path); len(got) != 1 || !strings.Contains(got[0], ".pre-v5-") {
		t.Fatalf("backups %v", got)
	}
	if v := userVersion(t, s.db); v != SchemaVersion {
		t.Fatalf("schema %d", v)
	}
	compat, err := readSchemaCompat(ctx, s.db)
	if err != nil || compat == nil || compat.MinReader != 5 || compat.MinWriter != 6 {
		t.Fatalf("schema_compat %+v %v", compat, err)
	}
	info, err := s.Journal(ctx)
	if err != nil || len(info.Epoch) != 32 || info.LowWater != 0 || info.Head != 0 {
		t.Fatalf("journal %+v %v", info, err)
	}
	// Existing rows are kept and are not back-filled into the journal.
	a, err := s.Account(ctx)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	if p, err := s.Messages(ctx, a.ID, "", MessageQuery{Search: "searchable"}); err != nil || len(p.Items) != 1 {
		t.Fatalf("messages lost: %+v %v", p.Items, err)
	}
	wantChanges(t, changes(t, s, a.ID, -1))
	mustApply(t, s, a.ID, original("new", "after upgrade"))
	wantChanges(t, changes(t, s, a.ID, -1), entry{OpMessageCreated, "one@g.us", "new"})
}

// TestSchema5ReaderCompatibility simulates a schema-5 binary (one that
// understands schema_compat) opening a migrated schema-6 archive: queries
// work, but owning the archive is refused because writes must journal.
func TestSchema5ReaderCompatibility(t *testing.T) {
	ctx := context.Background()
	path := buildArchive(t, 5)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	previous := schemaVersion
	schemaVersion = 5
	t.Cleanup(func() { schemaVersion = previous })
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("schema-5 reader refused: %v", err)
	}
	defer ro.Close()
	if p, err := ro.Messages(ctx, 1, "", MessageQuery{Search: "searchable"}); err != nil || len(p.Items) != 1 {
		t.Fatalf("schema-5 query: %+v %v", p.Items, err)
	}
	if _, err = Open(ctx, path); err == nil || !strings.Contains(err.Error(), "requires writer schema 6") {
		t.Fatalf("schema-5 writer accepted: %v", err)
	}
}
