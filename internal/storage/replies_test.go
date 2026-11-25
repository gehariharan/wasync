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
	listed := []string{pn, "15551230001@s.whatsapp.net", "15551230002@s.whatsapp.net"}
	stops, err := s.StopSenders(ctx, account, listed)
	if err != nil || len(stops) != 1 || !stops["15551230002@s.whatsapp.net"] {
		t.Fatalf("stops: %v %v", stops, err)
	}
	if stops, err = s.StopSenders(ctx, account, listed[:2]); err != nil || len(stops) != 0 {
		t.Fatalf("a STOP from a number that was not asked about: %v %v", stops, err)
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

func TestReplyLookupsReadOnlyTheHiddenChatsOfTheAskedNumbers(t *testing.T) {
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
	listed, mine, theirs, quiet := "15551230000@s.whatsapp.net", "111@lid", "222@lid", "333@lid"
	if err = s.SaveGroups(ctx, account, []Group{{ID: mine, Name: "A"}, {ID: theirs, Name: "B"}, {ID: quiet, Name: "C"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, account, []string{mine, theirs, quiet}, true); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	msgs := []Message{
		// An older STOP without an alternate sender still belongs to the person of that chat.
		{GroupID: mine, MessageID: "m1", SenderID: mine, Timestamp: at, Type: "text", Text: "STOP"},
		{GroupID: mine, MessageID: "m2", SenderID: mine, SenderAlt: "15551230000:2@s.whatsapp.net", Timestamp: at.Add(time.Second), Type: "text", Text: "hello"},
		{GroupID: theirs, MessageID: "m3", SenderID: theirs, SenderAlt: "15559990000@s.whatsapp.net", Timestamp: at.Add(2 * time.Second), Type: "text", Text: "STOP"},
		{GroupID: quiet, MessageID: "m4", SenderID: quiet, Timestamp: at, Type: "text", Text: "hi"},
	}
	if _, err = s.Apply(ctx, account, msgs); err != nil {
		t.Fatal(err)
	}
	person, err := s.personChats(ctx, account, []string{listed})
	if err != nil || len(person) != 2 || person[listed] != listed || person[mine] != listed {
		t.Fatalf("only the listed number's own chats are read: %v %v", person, err)
	}
	stops, err := s.StopSenders(ctx, account, []string{listed})
	if err != nil || len(stops) != 1 || !stops[listed] {
		t.Fatalf("stops: %v %v", stops, err)
	}
	if last, err := s.LastIncoming(ctx, account, listed); err != nil || !last.Equal(at.Add(time.Second)) {
		t.Fatalf("last reply is the listed person's, not another hidden chat's: %v %v", last, err)
	}
}
