package whatsapp

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func fixture(message *waE2E.Message) *events.Message {
	return &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("123", types.GroupServer), Sender: types.NewJID("456", types.HiddenUserServer), IsGroup: true}, ID: "original", Timestamp: time.Now()}, Message: message, RawMessage: message}
}
func TestNormalizeTextRepliesMediaAndPrivacy(t *testing.T) {
	e := fixture(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("safe text"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("reply-id"), QuotedMessage: &waE2E.Message{Conversation: proto.String("private quoted body")}}}})
	m, ok := Normalize(e, "live", time.Now())
	if !ok || m.Text != "safe text" || m.ReplyTo != "reply-id" {
		t.Fatalf("text normalization failed %+v", m)
	}
	e = fixture(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("caption"), Mimetype: proto.String("image/jpeg"), MediaKey: []byte("secret-key"), URL: proto.String("secret-url"), JPEGThumbnail: []byte("thumbnail")}})
	m, ok = Normalize(e, "history", time.Now())
	if !ok || m.Type != "image" || m.Text != "caption" || strings.Contains(m.Media, "secret") || strings.Contains(m.Media, "thumbnail") {
		t.Fatalf("media metadata leaked secrets %+v", m)
	}
	e.Info.Chat = types.NewJID("123", types.DefaultUserServer)
	if _, ok = Normalize(e, "live", time.Now()); !ok {
		t.Fatal("direct chat was not normalized")
	}
}
func TestNormalizeMutationsAndEphemeralBoundaries(t *testing.T) {
	now := time.Now()
	target := &waCommon.MessageKey{ID: proto.String("target"), RemoteJID: proto.String("123@g.us")}
	protocol := &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: target, EditedMessage: &waE2E.Message{Conversation: proto.String("edited")}, TimestampMS: proto.Int64(now.UnixMilli())}
	e := fixture(&waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ProtocolMessage: protocol}}})
	e.UnwrapRaw()
	m, ok := Normalize(e, "live", now)
	if !ok || m.Kind != "edit" || m.MessageID != "target" || m.Text != "edited" {
		t.Fatalf("bad edit %+v", m)
	}
	protocol.Type = waE2E.ProtocolMessage_REVOKE.Enum()
	e = fixture(&waE2E.Message{ProtocolMessage: protocol})
	m, ok = Normalize(e, "live", now)
	if !ok || m.Kind != "delete" || m.Text != "" {
		t.Fatalf("bad revoke %+v", m)
	}
	protocol.Key.RemoteJID = proto.String("other@g.us")
	if _, ok = Normalize(e, "live", now); ok {
		t.Fatal("cross-group mutation accepted")
	}
	e = fixture(&waE2E.Message{Conversation: proto.String("ephemeral")})
	e.IsEphemeral = true
	if _, ok = Normalize(e, "live", now); ok {
		t.Fatal("ephemeral without expiry retained")
	}
	e = fixture(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("expires"), ContextInfo: &waE2E.ContextInfo{Expiration: proto.Uint32(60)}}})
	e.IsEphemeral = true
	m, ok = Normalize(e, "live", now)
	if !ok || m.ExpiresAt == nil {
		t.Fatal("known ephemeral expiry not preserved")
	}
	e.IsViewOnce = true
	if _, ok = Normalize(e, "live", now); ok {
		t.Fatal("view-once retained")
	}
}
func TestHistoryAndLiveUseSameConsentAndDedup(t *testing.T) {
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
	if err = s.SaveGroups(ctx, account, []storage.Group{{ID: "123@g.us", Name: "selected"}, {ID: "789@g.us", Name: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(ctx, account, []string{"123@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	c, err := Open(ctx, filepath.Join(privateTestDir(t), "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.capture = &capture{ctx: ctx, store: s}
	c.capture.account.Store(account)
	mk := func(group, id, text string) *waHistorySync.Conversation {
		return &waHistorySync.Conversation{ID: proto.String(group), Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String(id), RemoteJID: proto.String(group), Participant: proto.String("456@lid")}, MessageTimestamp: proto.Uint64(1700000000), Message: &waE2E.Message{Conversation: proto.String(text)}}}}}
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{SyncType: waHistorySync.HistorySync_RECENT.Enum(), Conversations: []*waHistorySync.Conversation{mk("123@g.us", "original", "history text"), mk("789@g.us", "disabled", "private")}, Progress: proto.Uint32(100)}}
	if err = c.captureEvent(history); err != nil {
		t.Fatal(err)
	}
	if err = c.captureEvent(history); err != nil {
		t.Fatal(err)
	}
	live := fixture(&waE2E.Message{Conversation: proto.String("duplicate live text")})
	if err = c.captureEvent(live); err != nil {
		t.Fatal(err)
	}
	page, err := s.Messages(ctx, account, "123@g.us", storage.MessageQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Text != "history text" || page.Items[0].Timestamp.Unix() != 1700000000 {
		t.Fatalf("history not deduplicated %+v %v", page, err)
	}
	disabled, err := s.Messages(ctx, account, "789@g.us", storage.MessageQuery{})
	if err != nil || len(disabled.Items) != 0 {
		t.Fatal("disabled history was archived")
	}
	if !c.wa().SynchronousAck {
		t.Fatal("acks can precede handler completion")
	}
}

func TestDiagnosticLoginKeepsAnchorWithoutArchivingEnabledBodies(t *testing.T) {
	ctx := context.Background()
	dir := privateTestDir(t)
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	account, err := store.UseAccount(ctx, "synthetic@lid")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveGroups(ctx, account, []storage.Group{{ID: "123@g.us", Name: "One"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetEnabled(ctx, account, []string{"123@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	client, err := Open(ctx, filepath.Join(dir, "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.EnableDiscovery(ctx, store); err != nil {
		t.Fatal(err)
	}
	// Synthetic authenticated account, without any WhatsApp network connection.
	client.capture.account.Store(account)
	message := fixture(&waE2E.Message{Conversation: proto.String("never archive diagnostic body")})
	if !client.handleEvent(message) {
		t.Fatal("discovery event failed")
	}
	anchor, err := store.KnownHistoryAnchor(ctx, account, "123@g.us")
	if err != nil || anchor.ID != message.Info.ID {
		t.Fatal("group anchor not saved", err)
	}
	page, err := store.Messages(ctx, account, "123@g.us", storage.MessageQuery{Limit: 5})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("diagnostic mode archived a body", err)
	}
	message.Info.Chat = types.NewJID("555", types.HiddenUserServer)
	if !client.handleEvent(message) {
		t.Fatal("direct discovery failed")
	}
	direct, err := store.KnownHistoryAnchor(ctx, account, message.Info.Chat.String())
	if err != nil || direct.ID != "" {
		t.Fatal("disabled direct anchor persisted", err)
	}
}
