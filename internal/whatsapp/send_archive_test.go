package whatsapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// ackingWhatsApp stands in for the WhatsApp connection: it acknowledges every
// message and records it, and never reaches WhatsApp. Uploads are refused;
// these tests send only text.
type ackingWhatsApp struct {
	sent   []*waE2E.Message
	to     []types.JID
	next   int
	at     time.Time
	refuse bool
}

func (f *ackingWhatsApp) IsConnected() bool { return true }
func (f *ackingWhatsApp) UploadReader(context.Context, io.Reader, io.ReadWriteSeeker, whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{}, errors.New("no uploads in this fake")
}
func (f *ackingWhatsApp) GenerateMessageID() types.MessageID {
	f.next++
	return types.MessageID("SENT" + string(rune('0'+f.next)))
}
func (f *ackingWhatsApp) SendMessage(_ context.Context, to types.JID, m *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	if f.refuse {
		return whatsmeow.SendResponse{}, errors.New("no acknowledgement")
	}
	f.sent, f.to = append(f.sent, m), append(f.to, to)
	return whatsmeow.SendResponse{Timestamp: f.at}, nil
}

// sendingClient is a paired, capturing client whose sends go to a fake.
func sendingClient(t *testing.T) (*Client, *ackingWhatsApp, *storage.Store, int64) {
	t.Helper()
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(privateTestDir(t), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	account, err := s.UseAccount(ctx, ownJID.String())
	if err != nil {
		t.Fatal(err)
	}
	c, err := Open(ctx, filepath.Join(privateTestDir(t), "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	own := ownJID
	c.wa().Store.ID = &own
	fake := &ackingWhatsApp{at: time.Now().Add(-time.Second).Truncate(time.Millisecond)}
	c.outbound = fake
	c.capture = &capture{ctx: ctx, store: s}
	c.capture.account.Store(account)
	return c, fake, s, account
}

func summary(t *testing.T, s *storage.Store, account int64, chat string) storage.Group {
	t.Helper()
	groups, err := s.GroupSummaries(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID == chat {
			return g
		}
	}
	t.Fatalf("chat %s is not in the chat list", chat)
	return storage.Group{}
}

func archivedText(t *testing.T, s *storage.Store, account int64, chat, id string) storage.Message {
	t.Helper()
	m, err := s.MessageByWhatsAppID(context.Background(), account, chat, id)
	if err != nil {
		t.Fatalf("message %s is not archived in %s: %v", id, chat, err)
	}
	return m
}

// A message sent to a chat the owner never chose to archive is not kept, and
// the send does not switch archiving on for that chat.
func TestSendToANeverChosenChatKeepsNothing(t *testing.T) {
	c, fake, s, account := sendingClient(t)
	ctx := context.Background()
	number := "15551230000@s.whatsapp.net"
	out, err := c.Send(ctx, SendInput{To: number, Text: "Hello there", SentBy: "outreach"})
	if err != nil || out.Status != "server_acknowledged" || out.ArchiveWarning != "" || len(fake.sent) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if on, err := s.IsEnabled(ctx, account, number); err != nil || on {
		t.Fatalf("archiving switched on by a send: %v %v", on, err)
	}
	if _, err := s.MessageByWhatsAppID(ctx, account, number, out.MessageID); err == nil {
		t.Fatal("a send to a never-chosen chat was archived")
	}
}

// A normal send to a chat that is already captured is archived there and
// moves the chat's last message time.
func TestSendToACapturedChatArchivesIt(t *testing.T) {
	c, fake, s, account := sendingClient(t)
	ctx := context.Background()
	chat := "one@g.us"
	if err := s.SaveDiscoveredChat(ctx, account, storage.Group{ID: chat, Name: "Family"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableCapture(ctx, account, chat, false); err != nil {
		t.Fatal(err)
	}
	out, err := c.Send(ctx, SendInput{To: chat, Text: "dinner at 8"})
	if err != nil || out.ArchiveWarning != "" {
		t.Fatalf("%+v %v", out, err)
	}
	if m := archivedText(t, s, account, chat, out.MessageID); m.Text != "dinner at 8" || !m.FromMe || m.SenderID != ownJID.String() || m.SentBy != "" {
		t.Fatalf("the owner's message: %+v", m)
	}
	if g := summary(t, s, account, chat); g.MessageCount != 1 || g.LastMessage == nil || !g.LastMessage.Equal(fake.at) || g.Name != "Family" {
		t.Fatalf("%+v", g)
	}
	out, err = c.Send(ctx, SendInput{To: chat, Text: "Hello from the shop list", SentBy: "outreach"})
	if err != nil || out.ArchiveWarning != "" {
		t.Fatalf("%+v %v", out, err)
	}
	if m := archivedText(t, s, account, chat, out.MessageID); m.Text != "Hello from the shop list" || !m.FromMe || m.SentBy != "outreach" {
		t.Fatalf("the agent's message: %+v", m)
	}
}

// A chat the owner turned off stays off: the message goes, but nothing of it
// is kept, and capture is not switched back on.
func TestSendToAChatTheOwnerTurnedOffKeepsNothing(t *testing.T) {
	c, _, s, account := sendingClient(t)
	ctx := context.Background()
	chat := "15559990000@s.whatsapp.net"
	if err := s.SaveDiscoveredChat(ctx, account, storage.Group{ID: chat, Name: "Ex"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableCapture(ctx, account, chat, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(ctx, account, []string{chat}, false); err != nil {
		t.Fatal(err)
	}
	out, err := c.Send(ctx, SendInput{To: chat, Text: "hi"})
	if err != nil || out.ArchiveWarning != "" {
		t.Fatalf("%+v %v", out, err)
	}
	if g := summary(t, s, account, chat); g.Enabled || g.MessageCount != 0 {
		t.Fatalf("%+v", g)
	}
}

// A send WhatsApp did not acknowledge is not archived and does not switch the
// chat on.
func TestUnacknowledgedSendKeepsNothing(t *testing.T) {
	c, fake, s, account := sendingClient(t)
	fake.refuse = true
	number := "15551230000@s.whatsapp.net"
	if _, err := c.Send(context.Background(), SendInput{To: number, Text: "hi"}); err == nil {
		t.Fatal("an unacknowledged send reported success")
	}
	if on, err := s.IsEnabled(context.Background(), account, number); err != nil || on {
		t.Fatalf("%v %v", on, err)
	}
}

// A reply goes through the same path, so it is archived the same way, with
// the agent that sent it.
func TestReplyIsArchivedInItsChat(t *testing.T) {
	c, _, s, account := sendingClient(t)
	ctx := context.Background()
	chat := "one@g.us"
	if err := s.SaveDiscoveredChat(ctx, account, storage.Group{ID: chat, Name: "Family"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableCapture(ctx, account, chat, false); err != nil {
		t.Fatal(err)
	}
	in, err := NewReply(ReplyRequest{Chat: chat, MessageID: "m1", Text: "yes"}, archived("m1", false))
	if err != nil {
		t.Fatal(err)
	}
	in.SentBy = "outreach"
	out, err := c.Reply(ctx, in)
	if err != nil || out.ArchiveWarning != "" {
		t.Fatalf("%+v %v", out, err)
	}
	if m := archivedText(t, s, account, chat, out.MessageID); m.Text != "yes" || m.ReplyTo != "m1" || m.SentBy != "outreach" {
		t.Fatalf("%+v", m)
	}
}
