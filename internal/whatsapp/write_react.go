package whatsapp

import (
	"context"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const maxEmojiBytes = 32

// ReactRequest is the wire form of a reaction. Exactly one of Emoji and
// Remove is given, so a missing field can never clear a reaction by accident.
type ReactRequest struct {
	Chat      string `json:"chat"`
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji,omitempty"`
	Remove    bool   `json:"remove,omitempty"`
}

// ReactInput is a reaction with its archived target resolved. An empty Emoji
// removes the account's reaction.
type ReactInput struct {
	To     string
	Target storage.Message
	Emoji  string
}
type Reactor func(context.Context, ReactInput) (SendResult, error)

// validEmoji accepts a short run of symbols that includes a non-ASCII rune,
// which keeps words, plain digits and ASCII punctuation out of reactions.
func validEmoji(s string) error {
	if s == "" || len(s) > maxEmojiBytes || !utf8.ValidString(s) {
		return fmt.Errorf("provide one emoji of at most %d bytes", maxEmojiBytes)
	}
	wide := false
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.IsLetter(r) {
			return fmt.Errorf("a reaction must be an emoji, not text")
		}
		wide = wide || r > unicode.MaxASCII
	}
	if !wide {
		return fmt.Errorf("a reaction must be an emoji, not text")
	}
	return nil
}

func (r ReactRequest) Validate() error {
	if _, err := ParseChat(r.Chat); err != nil {
		return err
	}
	if r.MessageID == "" || len(r.MessageID) > 128 {
		return fmt.Errorf("provide --message-id")
	}
	if r.Remove {
		if r.Emoji != "" {
			return fmt.Errorf("use either --emoji or --remove, not both")
		}
		return nil
	}
	if r.Emoji == "" {
		return fmt.Errorf("provide --emoji, or --remove to clear your reaction")
	}
	return validEmoji(r.Emoji)
}

// NewReact pairs a validated request with its archived target.
func NewReact(r ReactRequest, target storage.Message) (ReactInput, error) {
	if err := r.Validate(); err != nil {
		return ReactInput{}, err
	}
	chat, _ := ParseChat(r.Chat)
	if err := checkTarget(chat, target); err != nil {
		return ReactInput{}, err
	}
	return ReactInput{To: r.Chat, Target: target, Emoji: r.Emoji}, nil
}

// reactionMessage builds a reaction to the target. The key names the target's
// author the way WhatsApp expects: you for your own messages, and the sender
// for other people's messages in a group.
func reactionMessage(in ReactInput, now time.Time) (*waE2E.Message, error) {
	chat, err := ParseChat(in.To)
	if err != nil {
		return nil, err
	}
	if err = checkTarget(chat, in.Target); err != nil {
		return nil, err
	}
	if in.Emoji != "" {
		if err = validEmoji(in.Emoji); err != nil {
			return nil, err
		}
	}
	key := &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), ID: proto.String(in.Target.MessageID), FromMe: proto.Bool(in.Target.FromMe)}
	if !in.Target.FromMe && chat.Server == types.GroupServer {
		sender, err := targetSender(in.Target)
		if err != nil {
			return nil, err
		}
		key.Participant = proto.String(sender.String())
	}
	return &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: key, Text: proto.String(in.Emoji), SenderTimestampMS: proto.Int64(now.UnixMilli())}}, nil
}

// React sends one reaction, or clears it when the emoji is empty; it never retries.
func (c *Client) React(ctx context.Context, in ReactInput) (SendResult, error) {
	out := SendResult{ChatID: in.To, Status: "not_sent", TargetMessageID: in.Target.MessageID}
	if err := c.requireConnected("reacting"); err != nil {
		return out, err
	}
	message, err := reactionMessage(in, time.Now())
	if err != nil {
		return out, err
	}
	jid, _ := ParseChat(in.To)
	return c.sendWrite(ctx, jid, message, out)
}
