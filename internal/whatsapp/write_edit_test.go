package whatsapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

func TestEditRequestValidation(t *testing.T) {
	ok := EditRequest{Chat: "one@g.us", MessageID: "m1", Text: "fixed"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*EditRequest){
		"bad chat":   func(r *EditRequest) { r.Chat = "status@broadcast" },
		"no message": func(r *EditRequest) { r.MessageID = "" },
		"blank text": func(r *EditRequest) { r.Text = "  " },
		"huge text":  func(r *EditRequest) { r.Text = strings.Repeat("x", MaxMessageText+1) },
	} {
		r := ok
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestNewEditEnforcesOwnershipTypeAndWindow(t *testing.T) {
	req := EditRequest{Chat: "one@g.us", MessageID: "m1", Text: "fixed"}
	now := time.Now()
	sent := func(age time.Duration) storage.Message {
		m := archived("m1", true)
		m.Timestamp = now.Add(-age)
		return m
	}
	for _, age := range []time.Duration{0, time.Minute, whatsmeow.EditWindow - time.Second} {
		if _, err := NewEdit(req, sent(age), now); err != nil {
			t.Errorf("age %s refused: %v", age, err)
		}
	}
	for _, age := range []time.Duration{whatsmeow.EditWindow, whatsmeow.EditWindow + time.Second, 24 * time.Hour} {
		if _, err := NewEdit(req, sent(age), now); !errors.Is(err, ErrEditWindowExpired) {
			t.Errorf("age %s: %v", age, err)
		}
	}
	zero := sent(0)
	zero.Timestamp = time.Time{}
	if _, err := NewEdit(req, zero, now); !errors.Is(err, ErrEditWindowExpired) {
		t.Errorf("unknown send time: %v", err)
	}
	theirs := archived("m1", false)
	theirs.Timestamp = now
	if _, err := NewEdit(req, theirs, now); !errors.Is(err, ErrNotOwnMessage) {
		t.Errorf("someone else's message: %v", err)
	}
	for _, kind := range []string{"image", "document", "audio", "sticker", "video"} {
		m := sent(0)
		m.Type = kind
		if _, err := NewEdit(req, m, now); !errors.Is(err, ErrNotEditable) {
			t.Errorf("%s: %v", kind, err)
		}
	}
	deleted := sent(0)
	deleted.Type = "deleted"
	if _, err := NewEdit(req, deleted, now); !errors.Is(err, ErrTargetUnavailable) {
		t.Errorf("deleted: %v", err)
	}
	other := sent(0)
	other.GroupID = "two@g.us"
	if _, err := NewEdit(req, other, now); err == nil {
		t.Error("target from another chat accepted")
	}
	if !strings.Contains(ErrEditWindowExpired.Error(), whatsmeow.EditWindow.String()) {
		t.Errorf("expiry error does not state the window: %v", ErrEditWindowExpired)
	}
}

func TestEditMessageTargetsOriginalAndIsArchivedAsAnEdit(t *testing.T) {
	now := time.Now()
	target := archived("ORIGINAL", true)
	target.Timestamp = now.Add(-time.Minute)
	m, err := editMessage(EditInput{To: "one@g.us", Target: target, Text: "corrected"}, now)
	if err != nil {
		t.Fatal(err)
	}
	protocol := m.GetEditedMessage().GetMessage().GetProtocolMessage()
	key := protocol.GetKey()
	if protocol.GetType() != waE2E.ProtocolMessage_MESSAGE_EDIT || key.GetID() != "ORIGINAL" || key.GetRemoteJID() != "one@g.us" || !key.GetFromMe() || protocol.GetEditedMessage().GetConversation() != "corrected" {
		t.Fatalf("edit message %+v", protocol)
	}
	row, ok := Normalize(ownEvent("one@g.us", "ENVELOPE", m), "live", now)
	if !ok || row.Kind != "edit" || row.MessageID != "ORIGINAL" || row.Text != "corrected" || row.Type != "text" || !row.FromMe {
		t.Fatalf("edit row %+v %v", row, ok)
	}

	// The builder repeats every check, so a stale input cannot slip through.
	late := EditInput{To: "one@g.us", Target: target, Text: "x"}
	if _, err = editMessage(late, now.Add(time.Hour)); !errors.Is(err, ErrEditWindowExpired) {
		t.Fatalf("late edit built: %v", err)
	}
	late.Target.FromMe = false
	if _, err = editMessage(late, now); !errors.Is(err, ErrNotOwnMessage) {
		t.Fatalf("foreign edit built: %v", err)
	}
}
