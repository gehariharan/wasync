package whatsapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// MaxMentions bounds the people one reply can mention.
const MaxMentions = 20

// ReplyRequest is the wire form of a reply: the target is named by chat and
// WhatsApp message ID and must already be in the archive.
type ReplyRequest struct {
	Chat      string   `json:"chat"`
	MessageID string   `json:"message_id"`
	Text      string   `json:"text"`
	Mentions  []string `json:"mentions,omitempty"`
}

// ReplyInput is a reply with its archived target resolved.
type ReplyInput struct {
	To       string
	Target   storage.Message
	Text     string
	Mentions []string
}
type Replier func(context.Context, ReplyInput) (SendResult, error)

func (r ReplyRequest) Validate() error {
	if _, err := ParseChat(r.Chat); err != nil {
		return err
	}
	if r.MessageID == "" || len(r.MessageID) > 128 {
		return fmt.Errorf("provide --message-id")
	}
	if err := validText(r.Text); err != nil {
		return err
	}
	_, err := parseMentions(r.Text, r.Mentions)
	return err
}

// NewReply pairs a validated request with its archived target.
func NewReply(r ReplyRequest, target storage.Message) (ReplyInput, error) {
	if err := r.Validate(); err != nil {
		return ReplyInput{}, err
	}
	chat, _ := ParseChat(r.Chat)
	if err := checkTarget(chat, target); err != nil {
		return ReplyInput{}, err
	}
	return ReplyInput{To: r.Chat, Target: target, Text: r.Text, Mentions: r.Mentions}, nil
}

// parseMentions requires every mention to be a user JID that the text
// visibly tags as @user, so a mention can never notify someone invisibly.
func parseMentions(text string, mentions []string) ([]string, error) {
	if len(mentions) > MaxMentions {
		return nil, fmt.Errorf("at most %d mentions are allowed", MaxMentions)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(mentions))
	for _, m := range mentions {
		jid, err := types.ParseJID(m)
		if err != nil || jid.Device != 0 || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) || jid.String() != m {
			return nil, fmt.Errorf("mention %q must be a user JID such as 123456789@s.whatsapp.net", m)
		}
		if !strings.Contains(text, "@"+jid.User) {
			return nil, fmt.Errorf("text must contain @%s for the mention of %s", jid.User, m)
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out, nil
}

// replyMessage builds a quoted text message. The quoted body is included only
// for archived text, so media captions and thumbnails are never copied.
func replyMessage(in ReplyInput, own types.JID) (*waE2E.Message, error) {
	chat, err := ParseChat(in.To)
	if err != nil {
		return nil, err
	}
	if err = checkTarget(chat, in.Target); err != nil {
		return nil, err
	}
	mentions, err := parseMentions(in.Text, in.Mentions)
	if err != nil {
		return nil, err
	}
	participant := own.ToNonAD()
	if !in.Target.FromMe {
		if participant, err = targetSender(in.Target); err != nil {
			return nil, err
		}
	}
	info := &waE2E.ContextInfo{StanzaID: proto.String(in.Target.MessageID), Participant: proto.String(participant.String()), MentionedJID: mentions}
	if in.Target.Type == "text" {
		info.QuotedMessage = &waE2E.Message{Conversation: proto.String(in.Target.Text)}
	}
	return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(in.Text), ContextInfo: info}}, nil
}

// Reply sends one quoted reply in the target's chat; it never retries.
func (c *Client) Reply(ctx context.Context, in ReplyInput) (SendResult, error) {
	out := SendResult{ChatID: in.To, Status: "not_sent", TargetMessageID: in.Target.MessageID}
	if err := c.requireConnected("replying"); err != nil {
		return out, err
	}
	message, err := replyMessage(in, *c.client.Store.ID)
	if err != nil {
		return out, err
	}
	jid, _ := ParseChat(in.To)
	return c.sendWrite(ctx, jid, message, out)
}

// Operations returns the writes this client can perform.
func (c *Client) Operations() Operations {
	return Operations{Reply: c.Reply, React: c.React}
}
