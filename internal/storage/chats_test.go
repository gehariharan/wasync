package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectChatRequiresOptInAndDownloadUpdatesRespectConsent(t *testing.T) {
	store, account := captureStore(t)
	ctx := context.Background()
	chat := Group{ID: "555@lid", Name: "Direct"}
	if err := store.SaveDiscoveredChat(ctx, account, chat); err != nil {
		t.Fatal(err)
	}
	message := original("direct", "private text")
	message.GroupID = chat.ID
	message.Type = "image"
	message.Media = `{"mime":"image/png"}`
	result, err := store.Apply(ctx, account, []Message{message})
	if err != nil || result.Stored != 0 {
		t.Fatalf("unselected direct archived: %+v %v", result, err)
	}
	if err = store.SetEnabled(ctx, account, []string{chat.ID}, true); err != nil {
		t.Fatal(err)
	}
	result, err = store.Apply(ctx, account, []Message{message, message})
	if err != nil || result.Stored != 1 {
		t.Fatalf("direct dedup: %+v %v", result, err)
	}
	if ok, err := store.SetMediaDownload(ctx, account, chat.ID, message.MessageID, "downloaded", "private/file"); err != nil || !ok {
		t.Fatalf("download update: %v %v", ok, err)
	}
	if err = store.SetEnabled(ctx, account, []string{chat.ID}, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.SetMediaDownload(ctx, account, chat.ID, message.MessageID, "downloaded", "other/file"); err != nil || ok {
		t.Fatal("download committed after disable")
	}
}
func TestHistoryAnchorSurvivesRestartAndDoesNotMoveBackward(t *testing.T) {
	store, account := captureStore(t)
	ctx := context.Background()
	first := HistoryAnchor{ID: "newest", Timestamp: time.Now().Truncate(time.Millisecond), FromMe: true}
	if err := store.SaveHistoryAnchors(ctx, account, map[string]HistoryAnchor{"one@g.us": first}); err != nil {
		t.Fatal(err)
	}
	older := first
	older.ID = "older"
	older.Timestamp = older.Timestamp.Add(-time.Hour)
	if err := store.SaveHistoryAnchors(ctx, account, map[string]HistoryAnchor{"one@g.us": older}); err != nil {
		t.Fatal(err)
	}
	var path string
	var n int
	var dbName string
	if err := store.db.QueryRow("PRAGMA database_list").Scan(&n, &dbName, &path); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := reader.KnownHistoryAnchor(ctx, account, "one@g.us")
	if err != nil || got.ID != first.ID || !got.Timestamp.Equal(first.Timestamp) {
		t.Fatalf("cursor not durable: %+v %v", got, err)
	}
}

func TestLocalCleanupTombstonePreventsReplay(t *testing.T) {
	store, account := captureStore(t)
	ctx := context.Background()
	message := original("clean", "sensitive cleanup text")
	mustApply(t, store, account, message)
	if err := store.ClearMessage(ctx, account, message.GroupID, message.MessageID); err != nil {
		t.Fatal(err)
	}
	mustApply(t, store, account, message)
	page, err := store.Messages(ctx, account, message.GroupID, MessageQuery{Search: "sensitive", Limit: 5})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("history replay restored locally cleared text")
	}
	got, err := store.MessageByWhatsAppID(ctx, account, message.GroupID, message.MessageID)
	if err != nil || got.DeletedAt == nil || got.Text != "" {
		t.Fatal("cleanup tombstone missing")
	}
}

func TestLegacyMediaReferenceSurvivesWindowsPathRelocation(t *testing.T) {
	store, account := captureStore(t)
	ctx := context.Background()
	key := "1/" + strings.Repeat("a", 64) + "/" + strings.Repeat("b", 64) + "-" + strings.Repeat("c", 64) + ".png"
	metadata, err := json.Marshal(map[string]string{"local_path": `C:\Users\Synthetic\archive\media\` + strings.ReplaceAll(key, "/", `\`), "download_state": "downloaded"})
	if err != nil {
		t.Fatal(err)
	}
	message := original("legacy", "image")
	message.Type = "image"
	message.Media = string(metadata)
	mustApply(t, store, account, message)
	moved := filepath.Join("moved-directory", "media", filepath.FromSlash(key))
	if ok, err := store.MediaReferenced(ctx, moved); err != nil || !ok {
		t.Fatalf("legacy moved reference lost: %v %v", ok, err)
	}
	if err = store.ClearMessage(ctx, account, message.GroupID, message.MessageID); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.MediaReferenced(ctx, moved); err != nil || ok {
		t.Fatal("cleared media still referenced", err)
	}
}

func TestSaveDirectNamesOnlyRenamesKnownDirectChats(t *testing.T) {
	store, account := captureStore(t)
	ctx := context.Background()
	if err := store.SaveDiscoveredChat(ctx, account, Group{ID: "111@s.whatsapp.net", Name: "111@s.whatsapp.net"}); err != nil {
		t.Fatal(err)
	}
	err := store.SaveDirectNames(ctx, account, map[string]string{
		"111@s.whatsapp.net": "Synthetic Contact",
		"999@s.whatsapp.net": "Not In Archive",
		"one@g.us":           "Group Must Keep Its Name",
		"222@lid":            "",
	})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := store.Groups(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	if names["111@s.whatsapp.net"] != "Synthetic Contact" || names["one@g.us"] != "Enabled" {
		t.Fatalf("unexpected names: %v", names)
	}
	if _, ok := names["999@s.whatsapp.net"]; ok || len(names) != 3 {
		t.Fatalf("a chat was created: %v", names)
	}
}
