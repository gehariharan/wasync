package api

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// archivingSender is a fake WhatsApp connection that keeps the sent copy the
// way the real client does: in the chat, when that chat is captured at the
// moment the message goes. It never reaches WhatsApp.
type archivingSender struct {
	mu      sync.Mutex
	store   *storage.Store
	account int64
	n       int
	at      time.Time
}

func (s *archivingSender) send(ctx context.Context, in whatsapp.SendInput) (whatsapp.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	id := "OUT" + string(rune('0'+s.n))
	m := storage.Message{GroupID: in.To, MessageID: id, SenderID: "synthetic@lid", Timestamp: s.at, Type: "text", Text: in.Text, FromMe: true, Source: "live"}
	if _, err := s.store.Apply(ctx, s.account, []storage.Message{m}); err != nil {
		return whatsapp.SendResult{}, err
	}
	return whatsapp.SendResult{ChatID: in.To, MessageID: id, Status: "server_acknowledged", Timestamp: s.at}, nil
}

func sentFixture(t *testing.T) (accessFixture, fixture, *pageClient, *archivingSender) {
	t.Helper()
	sender := &archivingSender{at: time.Now().Add(-time.Minute).Truncate(time.Millisecond)}
	a := setupAccessOwner(t, nil, func(o *Owner) { o.Sender = sender.send })
	sender.store, sender.account = a.store, a.account
	c := newPageClient(t, a)
	c.get(linkPath(t, a))
	return a, fixture{store: a.store, account: a.account, server: a.server, dir: a.dir}, c, sender
}

// The pilot: an agent messages a number on its approved list. The message is
// kept in that number's chat, which is at the top of Chats with its count and
// time, and the message is marked as the agent's, as on the Sent page.
func TestAListMessageShowsInItsChatAtTheTopOfChats(t *testing.T) {
	a, f, c, _ := sentFixture(t)
	agent := f.grantWriter(t, "outreach", []string{agentauth.ScopeSend}, 0, familyChat)
	f.approveList(t, "outreach", nil, "+1 555 123 0000")
	// The owner once captured, then turned off, this number's chat. An approved
	// list still needs its replies seen, so the list turns it back on.
	ctx := context.Background()
	if err := a.store.SetEnabled(ctx, a.account, []string{familyChat}, true); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SaveDiscoveredChat(ctx, a.account, storage.Group{ID: listed, Name: "Corner Shop"}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.EnableCapture(ctx, a.account, listed, false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetEnabled(ctx, a.account, []string{listed}, false); err != nil {
		t.Fatal(err)
	}
	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"+1 555 123 0000","text":"Hello Corner Shop"}`); status != 200 {
		t.Fatalf("list send: %d %s", status, body)
	}
	if _, err := a.store.MessageByWhatsAppID(ctx, a.account, listed, "OUT1"); err != nil {
		t.Fatalf("the list message was not kept in its chat: %v", err)
	}
	_, page, _ := c.get("/ui/chats")
	first := page[strings.Index(page, `<a class="row"`):]
	first = first[:strings.Index(first, "</a>")]
	if !strings.Contains(first, "Corner Shop") || !strings.Contains(first, "1 messages") || strings.Index(page, "Family Group") < strings.Index(page, "Corner Shop") {
		t.Fatalf("the messaged number is not the newest chat:\n%s", page)
	}
	_, page, _ = c.get("/ui/chats?c=" + url.QueryEscape(listed))
	if !strings.Contains(page, "Hello Corner Shop") || !strings.Contains(page, "🤖 outreach") {
		t.Fatalf("the chat does not show the agent's message:\n%s", page)
	}
}

// The owner's own message to a captured chat is shown as theirs.
func TestTheOwnersMessageShowsAsTheirs(t *testing.T) {
	a, _, c, _ := sentFixture(t)
	if err := a.store.SetEnabled(context.Background(), a.account, []string{familyChat}, true); err != nil {
		t.Fatal(err)
	}
	_, page, _ := c.get("/ui/chats/pick")
	code, _, hd := c.post("/ui/chats/send", url.Values{"csrf": {csrfOf(t, page)}, "chat": {familyChat}, "text": {"see you at 8"}})
	if code != 303 || notice(hd) != "sentok" {
		t.Fatalf("send: %d %q", code, notice(hd))
	}
	_, page, _ = c.get(hd.Get("Location"))
	i := strings.Index(page, "see you at 8")
	if i < 0 || !strings.HasSuffix(page[:i], `<span class="by">You</span>`) || strings.Contains(page, `<span class="by">🤖`) {
		t.Fatalf("the owner's message is not shown as theirs:\n%s", page)
	}
}

// Messages earlier releases sent to a list number but did not keep are added
// to the chat once, from the sent log, as the agent's, with no text.
func TestBackfillPutsEarlierSentMessagesInTheirChatsOnce(t *testing.T) {
	a, _, c, _ := sentFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 25, 9, 30, 0, 0, time.UTC)
	log := sentlog.New(a.dir)
	for _, e := range []sentlog.Entry{
		{Time: at, Actor: "outreach", Kind: "send", Chat: listed, MessageID: "OLD1", Outcome: sentlog.OutcomeSent, List: true, NewNumber: true},
		{Time: at, Actor: "outreach", Kind: "send", Chat: otherChat, MessageID: "OLD2", Outcome: sentlog.OutcomeSent},   // not captured: left alone
		{Time: at, Actor: "outreach", Kind: "send", Chat: listed, MessageID: "OLD3", Outcome: sentlog.OutcomeUnknown},   // may not have gone
		{Time: at, Actor: sentlog.Owner, Kind: "send", Chat: familyChat, MessageID: "m1", Outcome: sentlog.OutcomeSent}, // already archived
	} {
		if err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	// Earlier releases switched a list number's chat on after the send.
	if err := a.store.SaveDiscoveredChat(ctx, a.account, storage.Group{ID: listed, Name: "Corner Shop"}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.EnableCapture(ctx, a.account, listed, false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetEnabled(ctx, a.account, []string{familyChat}, true); err != nil {
		t.Fatal(err)
	}
	n, err := sentlog.Backfill(ctx, a.dir, a.store)
	if err != nil || n != 1 {
		t.Fatalf("backfill: %d %v", n, err)
	}
	m, err := a.store.MessageByWhatsAppID(ctx, a.account, listed, "OLD1")
	if err != nil || !m.FromMe || m.Text != "" || m.Source != sentlog.Source || !m.Timestamp.Equal(at) {
		t.Fatalf("backfilled message: %+v %v", m, err)
	}
	if fam, err := a.store.MessageByWhatsAppID(ctx, a.account, familyChat, "m1"); err != nil || fam.Text != "synthetic family text" {
		t.Fatalf("an archived message was changed: %+v %v", fam, err)
	}
	for chat, id := range map[string]string{otherChat: "OLD2", listed: "OLD3"} {
		if _, err := a.store.MessageByWhatsAppID(ctx, a.account, chat, id); err == nil {
			t.Fatalf("%s in %s should not have been added", id, chat)
		}
	}
	_, page, _ := c.get("/ui/chats?c=" + url.QueryEscape(listed))
	if !strings.Contains(page, "🤖 outreach") || !strings.Contains(page, "text not kept") {
		t.Fatalf("the backfilled message is not shown as the agent's:\n%s", page)
	}
	// Once only: a chat purged afterwards does not get it back.
	if err = a.store.PurgeGroup(ctx, a.account, listed); err != nil {
		t.Fatal(err)
	}
	if err = a.store.EnableCapture(ctx, a.account, listed, false); err != nil {
		t.Fatal(err)
	}
	if n, err = sentlog.Backfill(ctx, a.dir, a.store); err != nil || n != 0 {
		t.Fatalf("second backfill: %d %v", n, err)
	}
	if _, err = a.store.MessageByWhatsAppID(ctx, a.account, listed, "OLD1"); err == nil {
		t.Fatal("a purged message came back")
	}
}
