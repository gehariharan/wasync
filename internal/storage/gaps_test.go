package storage

import (
	"context"
	"testing"
)

// The same account-wide number used to be shown for every chat. Each chat's
// gap count is now the account's gaps that overlap the time capture has been on
// for that chat; the account-wide count stays available separately.
func TestGapCountIsPerChatAndAccountGapCountIsNot(t *testing.T) {
	store, account := captureStore(t)
	ctx := context.Background()
	chats := []Group{{ID: "early@g.us", Name: "Early"}, {ID: "middle@g.us", Name: "Middle"}, {ID: "late@g.us", Name: "Late"}, {ID: "off@g.us", Name: "Off"}}
	if err := store.SaveGroups(ctx, account, chats); err != nil {
		t.Fatal(err)
	}
	// Three stretches when wasync was not observing: two closed, one still open.
	for _, gap := range [][2]any{{1000, 2000}, {5000, 6000}, {9000, nil}} {
		if _, err := store.db.ExecContext(ctx, "INSERT INTO sync_gaps(account_id,started_at,ended_at,reason) VALUES(?,?,?,'test')", account, gap[0], gap[1]); err != nil {
			t.Fatal(err)
		}
	}
	// Capture turned on at different times: before every gap, during the second, after it.
	for id, enabledAt := range map[string]int64{"early@g.us": 500, "middle@g.us": 5500, "late@g.us": 7000} {
		if _, err := store.db.ExecContext(ctx, "UPDATE groups SET sync_enabled=1,enabled_at=? WHERE account_id=? AND group_id=?", enabledAt, account, id); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := store.GroupSummaries(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"early@g.us": 3, "middle@g.us": 2, "late@g.us": 1, "off@g.us": 0}
	for _, g := range groups {
		if g.AccountGapCount != 3 {
			t.Errorf("%s: account-wide gaps %d, want 3 for every chat", g.ID, g.AccountGapCount)
		}
		if w, ok := want[g.ID]; ok && g.GapCount != w {
			t.Errorf("%s: per-chat gaps %d, want %d", g.ID, g.GapCount, w)
		}
	}
	// Turning capture off ends the chat's exposure to gaps.
	if err = store.SetEnabled(ctx, account, []string{"early@g.us"}, false); err != nil {
		t.Fatal(err)
	}
	groups, _ = store.GroupSummaries(ctx, account)
	for _, g := range groups {
		if g.ID == "early@g.us" && g.GapCount != 0 {
			t.Fatalf("a chat with capture off still reports %d gaps", g.GapCount)
		}
	}
}
