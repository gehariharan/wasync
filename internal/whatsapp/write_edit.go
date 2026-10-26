package whatsapp

import (
	"context"
	"fmt"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// EditRequest is the wire form of an edit of the account's own text message.
type EditRequest struct {
	Chat      string `json:"chat"`
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// EditInput is an edit with its archived target resolved.
type EditInput struct {
	To     string
	Target storage.Message
	Text   string
}
type Editor func(context.Context, EditInput) (SendResult, error)

func (r EditRequest) Validate() error {
	if _, err := ParseChat(r.Chat); err != nil {
		return err
	}
	if r.MessageID == "" || len(r.MessageID) > 128 {
		return fmt.Errorf("provide --message-id")
	}
	return validText(r.Text)
}

// checkEditable applies WhatsApp's rules before anything is sent: only your
// own text messages, and only within the edit window after the original was
// sent (later edits do not extend it).
func checkEditable(target storage.Message, now time.Time) error {
	if !target.FromMe {
		return ErrNotOwnMessage
	}
	if target.Type != "text" {
		return ErrNotEditable
	}
	if target.Timestamp.IsZero() || now.Sub(target.Timestamp) >= whatsmeow.EditWindow {
		return ErrEditWindowExpired
	}
	return nil
}

// NewEdit pairs a validated request with its archived target.
func NewEdit(r EditRequest, target storage.Message, now time.Time) (EditInput, error) {
	if err := r.Validate(); err != nil {
		return EditInput{}, err
	}
	chat, _ := ParseChat(r.Chat)
	if err := checkTarget(chat, target); err != nil {
		return EditInput{}, err
	}
	if err := checkEditable(target, now); err != nil {
		return EditInput{}, err
	}
	return EditInput{To: r.Chat, Target: target, Text: r.Text}, nil
}

// editMessage builds the edit of the target's text.
func editMessage(in EditInput, now time.Time) (*waE2E.Message, error) {
	chat, err := ParseChat(in.To)
	if err != nil {
		return nil, err
	}
	if err = checkTarget(chat, in.Target); err != nil {
		return nil, err
	}
	if err = checkEditable(in.Target, now); err != nil {
		return nil, err
	}
	if err = validText(in.Text); err != nil {
		return nil, err
	}
	// BuildEdit reads no client state.
	return new(whatsmeow.Client).BuildEdit(chat, in.Target.MessageID, &waE2E.Message{Conversation: proto.String(in.Text)}), nil
}

// Edit replaces the text of one of your own messages; it never retries.
func (c *Client) Edit(ctx context.Context, in EditInput) (SendResult, error) {
	out := SendResult{ChatID: in.To, Status: "not_sent", TargetMessageID: in.Target.MessageID}
	if err := c.requireConnected("editing"); err != nil {
		return out, err
	}
	message, err := editMessage(in, time.Now())
	if err != nil {
		return out, err
	}
	jid, _ := ParseChat(in.To)
	return c.sendWrite(ctx, jid, message, out)
}
