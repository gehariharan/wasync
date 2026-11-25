package whatsapp

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestAReplyUnderAHiddenAddressIsArchivedWhenItsNumberIsCaptured(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, filepath.Join(privateTestDir(t), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	account, err := s.UseAccount(ctx, "test@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	pn := "15551230000@s.whatsapp.net"
	if err = s.SaveDiscoveredChat(ctx, account, storage.Group{ID: pn, Name: pn}); err != nil {
		t.Fatal(err)
	}
	if err = s.EnableCapture(ctx, account, pn, false); err != nil {
		t.Fatal(err)
	}
	c, err := Open(ctx, filepath.Join(privateTestDir(t), "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.capture = &capture{ctx: ctx, store: s}
	c.capture.account.Store(account)
	reply := func(lid, alt, id, text string) *events.Message {
		chat := types.NewJID(lid, types.HiddenUserServer)
		msg := &waE2E.Message{Conversation: proto.String(text)}
		return &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat, SenderAlt: types.NewADJID(alt, 0, 3)}, ID: types.MessageID(id), Timestamp: time.Now()}, Message: msg, RawMessage: msg}
	}
	if err = c.captureEvent(reply("999", "15551230000", "R1", "STOP")); err != nil {
		t.Fatal(err)
	}
	if err = c.captureEvent(reply("888", "15559990000", "R2", "STOP")); err != nil {
		t.Fatal(err)
	}
	if last, err := s.LastIncoming(ctx, account, pn); err != nil || last.IsZero() {
		t.Fatalf("the reply under the hidden address was not archived: %v %v", last, err)
	}
	stops, err := s.StopSenders(ctx, account, []string{pn, "15559990000@s.whatsapp.net"})
	if err != nil || len(stops) != 1 || !stops[pn] {
		t.Fatalf("stops: %v %v", stops, err)
	}
	if on, err := s.IsEnabled(ctx, account, "888@lid"); err != nil || on {
		t.Fatalf("a hidden address whose number is not captured was turned on: %v %v", on, err)
	}
}
