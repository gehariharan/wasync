package whatsapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestReactRequestValidation(t *testing.T) {
	for _, ok := range []ReactRequest{
		{Chat: "one@g.us", MessageID: "m1", Emoji: "👍"},
		{Chat: "one@g.us", MessageID: "m1", Emoji: "❤️"},
		{Chat: "one@g.us", MessageID: "m1", Emoji: "1️⃣"},
		{Chat: "15550001111@s.whatsapp.net", MessageID: "m1", Emoji: "👍🏽"},
		{Chat: "one@g.us", MessageID: "m1", Remove: true},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	for name, bad := range map[string]ReactRequest{
		"neither":      {Chat: "one@g.us", MessageID: "m1"},
		"both":         {Chat: "one@g.us", MessageID: "m1", Emoji: "👍", Remove: true},
		"bad chat":     {Chat: "status@broadcast", MessageID: "m1", Emoji: "👍"},
		"no message":   {Chat: "one@g.us", Emoji: "👍"},
		"word":         {Chat: "one@g.us", MessageID: "m1", Emoji: "ok"},
		"ascii symbol": {Chat: "one@g.us", MessageID: "m1", Emoji: ":)"},
		"plain digit":  {Chat: "one@g.us", MessageID: "m1", Emoji: "1"},
		"space":        {Chat: "one@g.us", MessageID: "m1", Emoji: "👍 👍"},
		"control":      {Chat: "one@g.us", MessageID: "m1", Emoji: "👍\n"},
		"too long":     {Chat: "one@g.us", MessageID: "m1", Emoji: strings.Repeat("👍", 9)},
		"bad utf8":     {Chat: "one@g.us", MessageID: "m1", Emoji: "\xff\xfe"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestNewReactRefusesUnusableTargets(t *testing.T) {
	req := ReactRequest{Chat: "one@g.us", MessageID: "m1", Emoji: "👍"}
	now := time.Now()
	for name, mutate := range map[string]func(*storage.Message){
		"deleted at": func(m *storage.Message) { m.DeletedAt = &now },
		"tombstone":  func(m *storage.Message) { m.Type = "deleted" },
		"reaction":   func(m *storage.Message) { m.Type = "reaction" },
	} {
		target := archived("m1", false)
		mutate(&target)
		if _, err := NewReact(req, target); !errors.Is(err, ErrTargetUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	other := archived("m1", false)
	other.GroupID = "two@g.us"
	if _, err := NewReact(req, other); err == nil {
		t.Error("target from another chat accepted")
	}
	// Media can be reacted to, unlike a reply's quote it needs no text.
	media := archived("m1", false)
	media.Type = "image"
	if _, err := NewReact(req, media); err != nil {
		t.Fatal(err)
	}
}

// ownEvent is what sendWrite hands to capture for a message just sent.
func ownEvent(chat string, id string, message *waE2E.Message) *events.Message {
	jid, _ := ParseChat(chat)
	info := types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, Sender: ownJID, IsFromMe: true, IsGroup: jid.Server == types.GroupServer}, ID: id, Timestamp: time.Now()}
	return &events.Message{Info: info, Message: message, RawMessage: message}
}

func TestSentReactionAndReplyAreArchivedLikeIncomingOnes(t *testing.T) {
	m, err := reactionMessage(ReactInput{To: "one@g.us", Target: archived("THEIRS", false), Emoji: "👍"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	row, ok := Normalize(ownEvent("one@g.us", "NEW1", m), "live", time.Now())
	if !ok || row.Type != "reaction" || row.Text != "👍" || row.ReplyTo != "THEIRS" || !row.FromMe || row.MessageID != "NEW1" {
		t.Fatalf("reaction row %+v %v", row, ok)
	}
	m, _ = reactionMessage(ReactInput{To: "one@g.us", Target: archived("THEIRS", false)}, time.Now())
	if row, ok = Normalize(ownEvent("one@g.us", "NEW2", m), "live", time.Now()); !ok || row.Type != "reaction" || row.Text != "" || row.ReplyTo != "THEIRS" {
		t.Fatalf("removal row %+v %v", row, ok)
	}
	reply, err := replyMessage(ReplyInput{To: "one@g.us", Target: archived("THEIRS", false), Text: "answer"}, ownJID)
	if err != nil {
		t.Fatal(err)
	}
	if row, ok = Normalize(ownEvent("one@g.us", "NEW3", reply), "live", time.Now()); !ok || row.Type != "text" || row.Text != "answer" || row.ReplyTo != "THEIRS" || !row.FromMe {
		t.Fatalf("reply row %+v %v", row, ok)
	}
}

func TestReactionMessageKeyNamesTheTargetAuthor(t *testing.T) {
	now := time.UnixMilli(1760000000000)
	// Someone else's message in a group: the key carries the author.
	m, err := reactionMessage(ReactInput{To: "one@g.us", Target: archived("THEIRS", false), Emoji: "👍"}, now)
	if err != nil {
		t.Fatal(err)
	}
	r := m.GetReactionMessage()
	key := r.GetKey()
	if r.GetText() != "👍" || key.GetID() != "THEIRS" || key.GetRemoteJID() != "one@g.us" || key.GetFromMe() || key.GetParticipant() != "15552223333@s.whatsapp.net" || r.GetSenderTimestampMS() != 1760000000000 {
		t.Fatalf("group reaction %+v", r)
	}
	// Your own message: fromMe, no participant.
	m, err = reactionMessage(ReactInput{To: "one@g.us", Target: archived("MINE", true), Emoji: "🙏"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if key = m.GetReactionMessage().GetKey(); !key.GetFromMe() || key.GetParticipant() != "" {
		t.Fatalf("own reaction %+v", key)
	}
	// Direct chats never carry a participant.
	direct := archived("DM", false)
	direct.GroupID = "15552223333@s.whatsapp.net"
	m, err = reactionMessage(ReactInput{To: direct.GroupID, Target: direct, Emoji: "👍"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if key = m.GetReactionMessage().GetKey(); key.GetFromMe() || key.GetParticipant() != "" || key.GetRemoteJID() != direct.GroupID {
		t.Fatalf("direct reaction %+v", key)
	}
	// An empty emoji clears the reaction.
	m, err = reactionMessage(ReactInput{To: "one@g.us", Target: archived("THEIRS", false)}, now)
	if err != nil || m.GetReactionMessage().Text == nil || m.GetReactionMessage().GetText() != "" {
		t.Fatalf("removal %+v %v", m, err)
	}
	// An unusable author in a group is refused rather than guessed.
	bad := archived("BAD", false)
	bad.SenderID = "not a jid"
	if _, err = reactionMessage(ReactInput{To: "one@g.us", Target: bad, Emoji: "👍"}, now); err == nil {
		t.Fatal("unusable sender accepted")
	}
}
