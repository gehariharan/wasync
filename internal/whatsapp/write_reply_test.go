package whatsapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/types"
)

var ownJID = types.JID{User: "15550001111", Server: types.DefaultUserServer}

func archived(id string, fromMe bool) storage.Message {
	m := storage.Message{GroupID: "one@g.us", MessageID: id, SenderID: "15552223333@s.whatsapp.net", Timestamp: time.Now().Add(-time.Minute), Type: "text", Text: "quoted body"}
	if fromMe {
		m.FromMe = true
		m.SenderID = ownJID.String()
	}
	return m
}

func TestReplyRequestValidation(t *testing.T) {
	ok := ReplyRequest{Chat: "one@g.us", MessageID: "ABC", Text: "hello"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ReplyRequest){
		"bad chat":        func(r *ReplyRequest) { r.Chat = "status@broadcast" },
		"no message":      func(r *ReplyRequest) { r.MessageID = "" },
		"huge message id": func(r *ReplyRequest) { r.MessageID = strings.Repeat("a", 129) },
		"blank text":      func(r *ReplyRequest) { r.Text = " \n" },
		"huge text":       func(r *ReplyRequest) { r.Text = strings.Repeat("x", MaxMessageText+1) },
		"invisible tag":   func(r *ReplyRequest) { r.Mentions = []string{"15552223333@s.whatsapp.net"} },
		"group mention":   func(r *ReplyRequest) { r.Text = "@123"; r.Mentions = []string{"123@g.us"} },
		"device mention":  func(r *ReplyRequest) { r.Text = "@123"; r.Mentions = []string{"123:4@s.whatsapp.net"} },
		"too many":        func(r *ReplyRequest) { r.Mentions = make([]string, MaxMentions+1) },
	} {
		r := ok
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	r := ok
	r.Text = "hi @15552223333 and again @15552223333"
	r.Mentions = []string{"15552223333@s.whatsapp.net", "15552223333@s.whatsapp.net"}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNewReplyRefusesUnusableTargets(t *testing.T) {
	req := ReplyRequest{Chat: "one@g.us", MessageID: "m1", Text: "hi"}
	now := time.Now()
	for name, mutate := range map[string]func(*storage.Message){
		"deleted at": func(m *storage.Message) { m.DeletedAt = &now },
		"tombstone":  func(m *storage.Message) { m.Type = "deleted" },
		"reaction":   func(m *storage.Message) { m.Type = "reaction" },
	} {
		target := archived("m1", false)
		mutate(&target)
		if _, err := NewReply(req, target); !errors.Is(err, ErrTargetUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	other := archived("m1", false)
	other.GroupID = "two@g.us"
	if _, err := NewReply(req, other); err == nil {
		t.Error("target from another chat accepted")
	}
	if in, err := NewReply(req, archived("m1", false)); err != nil || in.To != "one@g.us" {
		t.Fatalf("%+v %v", in, err)
	}
}

func TestReplyMessageQuotesTargetAndTagsMentions(t *testing.T) {
	in := ReplyInput{To: "one@g.us", Target: archived("THEIRS", false), Text: "ok @15554445555", Mentions: []string{"15554445555@s.whatsapp.net"}}
	message, err := replyMessage(in, ownJID)
	if err != nil {
		t.Fatal(err)
	}
	ext := message.GetExtendedTextMessage()
	info := ext.GetContextInfo()
	if ext.GetText() != in.Text || info.GetStanzaID() != "THEIRS" || info.GetParticipant() != "15552223333@s.whatsapp.net" || len(info.GetMentionedJID()) != 1 || info.GetMentionedJID()[0] != "15554445555@s.whatsapp.net" {
		t.Fatalf("message %+v", message)
	}
	if info.GetQuotedMessage().GetConversation() != "quoted body" {
		t.Fatalf("quoted text %+v", info.GetQuotedMessage())
	}

	// Quoting your own message names you as the participant.
	own, err := replyMessage(ReplyInput{To: "one@g.us", Target: archived("MINE", true), Text: "again"}, types.JID{User: ownJID.User, Server: ownJID.Server, Device: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got := own.GetExtendedTextMessage().GetContextInfo().GetParticipant(); got != ownJID.String() {
		t.Fatalf("own participant %q", got)
	}

	// Only text is copied into the quote; media captions and thumbnails are not.
	media := archived("PIC", false)
	media.Type, media.Text = "image", "caption"
	m, err := replyMessage(ReplyInput{To: "one@g.us", Target: media, Text: "nice"}, ownJID)
	if err != nil || m.GetExtendedTextMessage().GetContextInfo().GetQuotedMessage() != nil {
		t.Fatalf("media quote %+v %v", m, err)
	}

	bad := archived("BAD", false)
	bad.SenderID = "not a jid"
	if _, err = replyMessage(ReplyInput{To: "one@g.us", Target: bad, Text: "x"}, ownJID); err == nil {
		t.Fatal("unusable sender accepted")
	}
}
