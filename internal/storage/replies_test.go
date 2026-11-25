package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// sinceAll asks about every chat from at on.
func sinceAll(chats []string, at time.Time) map[string]time.Time {
	out := map[string]time.Time{}
	for _, c := range chats {
		out[c] = at
	}
	return out
}

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
		{GroupID: pn, MessageID: "m4", SenderID: pn, Timestamp: at, Type: "text", Text: "nonstop deals?"},
	}
	if _, err = s.Apply(ctx, account, msgs); err != nil {
		t.Fatal(err)
	}
	listed := []string{pn, "15551230001@s.whatsapp.net", "15551230002@s.whatsapp.net"}
	stops, err := s.StopSenders(ctx, account, sinceAll(listed, at))
	if err != nil || len(stops) != 1 || !stops["15551230002@s.whatsapp.net"] {
		t.Fatalf("stops: %v %v", stops, err)
	}
	if stops, err = s.StopSenders(ctx, account, sinceAll(listed[:2], at)); err != nil || len(stops) != 0 {
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
	stops, err := s.StopSenders(ctx, account, sinceAll([]string{listed}, at))
	if err != nil || len(stops) != 1 || !stops[listed] {
		t.Fatalf("stops: %v %v", stops, err)
	}
	if last, err := s.LastIncoming(ctx, account, listed); err != nil || !last.Equal(at.Add(time.Second)) {
		t.Fatalf("last reply is the listed person's, not another hidden chat's: %v %v", last, err)
	}
}

func TestOptOutRepliesAreCaughtInEveryUsualWording(t *testing.T) {
	stop := []string{"stop", "STOP", "Stop.", "STOP!", " stop\n", "please stop", "Stop please 🙏", "stop messaging me", "STOP ALL", "stopall",
		"unsubscribe", "Unsubscribe me", "opt out", "opt-out", "OPTOUT", "please remove me from this list", "take me off your list",
		"Cancel", "end.", "QUIT", "pls stop!!!", "I said STOP", "Don\u2019t message me, stop"}
	for _, text := range stop {
		if !IsOptOut(text) {
			t.Errorf("%q is an opt-out", text)
		}
	}
	keep := []string{"", "hi", "nonstop", "unstoppable", "I stopped by yesterday", "stops", "cancel my 3pm, see you at 4", "the end of the week works",
		"option outdoors?", "removed", "sounds good", "remember me?"}
	for _, text := range keep {
		if IsOptOut(text) {
			t.Errorf("%q is an ordinary reply", text)
		}
	}
}

func TestOptOutWordingsAreFoundInTheArchive(t *testing.T) {
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
	start := time.Now().UTC()
	texts := []string{"STOP!", "please stop messaging me", "Unsubscribe", "opt-out please", "quit", "take me off your list, thanks", "nonstop", "a long ordinary reply that talks about many things but never asks to leave"}
	var chats []string
	var msgs []Message
	for i, text := range texts {
		chat := fmt.Sprintf("1555123%04d@s.whatsapp.net", i)
		chats = append(chats, chat)
		msgs = append(msgs, Message{GroupID: chat, MessageID: fmt.Sprintf("m%d", i), SenderID: chat, Timestamp: time.Now().UTC(), Type: "text", Text: text})
	}
	if err = s.SaveGroups(ctx, account, func() (g []Group) {
		for _, c := range chats {
			g = append(g, Group{ID: c, Name: c})
		}
		return
	}()); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, account, chats, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, account, msgs); err != nil {
		t.Fatal(err)
	}
	stops, err := s.StopSenders(ctx, account, sinceAll(chats, start))
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range chats {
		if want := i < 6; stops[c] != want {
			t.Errorf("%q: stopped=%v, want %v", texts[i], stops[c], want)
		}
	}
}

// Only a reply after wasync's first message to a number can drop it: older
// chat with that person, such as "see you at the bus stop", never counts.
func TestOnlyRepliesAfterTheFirstMessageCountAsOptOuts(t *testing.T) {
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
	chat := "15551230000@s.whatsapp.net"
	if err = s.SaveGroups(ctx, account, []Group{{ID: chat, Name: "A"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, account, []string{chat}, true); err != nil {
		t.Fatal(err)
	}
	first := time.Now().Add(-time.Hour).UTC()
	old := Message{GroupID: chat, MessageID: "old", SenderID: chat, Timestamp: first.Add(-24 * time.Hour), Type: "text", Text: "see you at the bus stop"}
	if _, err = s.Apply(ctx, account, []Message{old}); err != nil {
		t.Fatal(err)
	}
	for _, since := range []map[string]time.Time{{chat: first}, {chat: {}}} {
		if stops, err := s.StopSenders(ctx, account, since); err != nil || len(stops) != 0 {
			t.Fatalf("an old message or a number never messaged dropped it: %v %v", stops, err)
		}
	}
	reply := Message{GroupID: chat, MessageID: "new", SenderID: chat, Timestamp: first.Add(time.Minute), Type: "text", Text: "stop"}
	if _, err = s.Apply(ctx, account, []Message{reply}); err != nil {
		t.Fatal(err)
	}
	if stops, err := s.StopSenders(ctx, account, map[string]time.Time{chat: first}); err != nil || !stops[chat] {
		t.Fatalf("a STOP after the first message: %v %v", stops, err)
	}
	if stops, err := s.StopSenders(ctx, account, map[string]time.Time{chat: first.Add(time.Hour)}); err != nil || len(stops) != 0 {
		t.Fatalf("a STOP before the first message: %v %v", stops, err)
	}
}
