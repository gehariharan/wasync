package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRepliesAndStopsAreReadFromDirectChatsOnly(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	account, err := s.UseAccount(ctx, "me@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	pn, lid, group := "15551230000@s.whatsapp.net", "999@lid", "120363000000000001@g.us"
	if err = s.SaveGroups(ctx, account, []Group{{ID: pn, Name: "A"}, {ID: lid, Name: "B"}, {ID: group, Name: "G"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, account, []string{pn, lid, group}, true); err != nil {
		t.Fatal(err)
	}
	if last, err := s.LastIncoming(ctx, account, pn); err != nil || !last.IsZero() {
		t.Fatalf("no replies yet: %v %v", last, err)
	}
	at := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	msgs := []Message{
		{GroupID: pn, MessageID: "m1", SenderID: "me@s.whatsapp.net", Timestamp: at.Add(time.Second), Type: "text", Text: "STOP", FromMe: true},
		{GroupID: group, MessageID: "m2", SenderID: "15551230001@s.whatsapp.net", Timestamp: at, Type: "text", Text: "stop"},
		{GroupID: lid, MessageID: "m3", SenderID: lid, SenderAlt: "15551230002:3@s.whatsapp.net", Timestamp: at, Type: "text", Text: " Stop\n"},
		{GroupID: pn, MessageID: "m4", SenderID: pn, Timestamp: at, Type: "text", Text: "please stop sending"},
	}
	if _, err = s.Apply(ctx, account, msgs); err != nil {
		t.Fatal(err)
	}
	stops, err := s.StopSenders(ctx, account)
	if err != nil || len(stops) != 1 || !stops["15551230002@s.whatsapp.net"] {
		t.Fatalf("stops: %v %v", stops, err)
	}
	if last, err := s.LastIncoming(ctx, account, pn); err != nil || !last.Equal(at) {
		t.Fatalf("last reply: %v %v", last, err)
	}
	if last, err := s.LastIncoming(ctx, account, "15551230002@s.whatsapp.net"); err != nil || !last.Equal(at) {
		t.Fatalf("a reply keyed by its hidden address: %v %v", last, err)
	}
	if last, err := s.LastIncoming(ctx, account, "15551230001@s.whatsapp.net"); err != nil || !last.IsZero() {
		t.Fatalf("a group message is not a reply: %v %v", last, err)
	}
}
