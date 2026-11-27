package storage

import (
	"context"
	"testing"
	"time"
)

func TestPickerChatsUseTheNewestOfArchivedAndReportedActivity(t *testing.T) {
	store, account := captureStore(t)
	ctx := context.Background()
	archived := time.Unix(1700000000, 0).UTC()
	mustApply(t, store, account, original("m1", "kept"))
	reported := archived.Add(time.Hour)
	if err := store.SaveChatActivity(ctx, account, map[string]time.Time{"one@g.us": archived.Add(-time.Hour), "two@g.us": reported, "unknown@g.us": reported, "zero@g.us": {}}); err != nil {
		t.Fatal(err)
	}
	// An older report never moves a time back.
	if err := store.SaveChatActivity(ctx, account, map[string]time.Time{"two@g.us": archived}); err != nil {
		t.Fatal(err)
	}
	chats, err := store.PickerChats(ctx, account)
	if err != nil || len(chats) != 2 {
		t.Fatalf("chats: %+v %v", chats, err)
	}
	got := map[string]PickerChat{}
	for _, c := range chats {
		got[c.ID] = c
	}
	if c := got["one@g.us"]; !c.Enabled || c.Name != "Enabled" || c.LastActive == nil || !c.LastActive.Equal(archived) {
		t.Fatalf("synced chat: %+v", c)
	}
	if c := got["two@g.us"]; c.Enabled || c.LastActive == nil || !c.LastActive.Equal(reported) {
		t.Fatalf("unsynced chat: %+v", c)
	}
	if _, ok, err := store.PickerChat(ctx, account, "unknown@g.us"); ok || err != nil {
		t.Fatalf("activity alone made a chat known: %v %v", ok, err)
	}
	if c, ok, err := store.PickerChat(ctx, account, "two@g.us"); !ok || err != nil || c.Name != "Disabled" {
		t.Fatalf("one chat: %+v %v %v", c, ok, err)
	}
}
