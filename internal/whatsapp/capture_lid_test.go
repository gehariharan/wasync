package whatsapp

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/sendlist"
	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestOnlyAListedNumbersHiddenAddressRepliesAreFollowed(t *testing.T) {
	ctx := context.Background()
	dir := privateTestDir(t)
	s, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	account, err := s.UseAccount(ctx, "test@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	listed, other, refused := "15551230000@s.whatsapp.net", "15559990000@s.whatsapp.net", "15557770000@s.whatsapp.net"
	for _, pn := range []string{listed, other} {
		if err = s.SaveDiscoveredChat(ctx, account, storage.Group{ID: pn, Name: pn}); err != nil {
			t.Fatal(err)
		}
		if err = s.EnableCapture(ctx, account, pn, false); err != nil {
			t.Fatal(err)
		}
	}
	if err = sendlist.Put(dir, sendlist.List{AgentID: "a1", Agent: "poster", GrantedAt: time.Now(), Numbers: []sendlist.Number{{Chat: listed}, {Chat: refused}}}); err != nil {
		t.Fatal(err)
	}
	// The owner captured, then turned off, the hidden-address chat of a listed number.
	if err = s.SaveDiscoveredChat(ctx, account, storage.Group{ID: "777@lid", Name: "777@lid"}); err != nil {
		t.Fatal(err)
	}
	if err = s.EnableCapture(ctx, account, "777@lid", false); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, account, []string{"777@lid"}, false); err != nil {
		t.Fatal(err)
	}
	c, err := Open(ctx, filepath.Join(privateTestDir(t), "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.ConfigureLists(dir)
	c.capture = &capture{ctx: ctx, store: s}
	c.capture.account.Store(account)
	reply := func(lid, alt, id string) *events.Message {
		chat := types.NewJID(lid, types.HiddenUserServer)
		msg := &waE2E.Message{Conversation: proto.String("STOP")}
		return &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat, SenderAlt: types.NewADJID(alt, 0, 3)}, ID: types.MessageID(id), Timestamp: time.Now()}, Message: msg, RawMessage: msg}
	}
	for _, e := range []*events.Message{reply("999", "15551230000", "R1"), reply("888", "15559990000", "R2"), reply("777", "15557770000", "R3")} {
		if err = c.captureEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	if last, err := s.LastIncoming(ctx, account, listed); err != nil || last.IsZero() {
		t.Fatalf("the listed number's reply under its hidden address was not archived: %v %v", last, err)
	}
	stops, err := s.StopSenders(ctx, account, []string{listed, other, refused})
	if err != nil || len(stops) != 1 || !stops[listed] {
		t.Fatalf("stops: %v %v", stops, err)
	}
	if on, err := s.IsEnabled(ctx, account, "888@lid"); err != nil || on {
		t.Fatalf("a captured but unlisted number's hidden address was turned on: %v %v", on, err)
	}
	if on, err := s.IsEnabled(ctx, account, "777@lid"); err != nil || on {
		t.Fatalf("a hidden address the owner turned off was turned back on: %v %v", on, err)
	}
}
