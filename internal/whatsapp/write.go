package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Operations are the WhatsApp writes beyond plain send. The owner process
// wires them to its connected client; they are nil when it is offline.
type Operations struct {
	Reply Replier
	React Reactor
	Edit  Editor
}

// MaxMessageText bounds text accepted by every write operation, as send does.
const MaxMessageText = 16000

// Targets are always archived messages resolved by the caller (the API layer
// or the CLI) from the local archive. The errors below describe a target that
// exists but cannot take the requested operation.
var (
	ErrTargetUnavailable = errors.New("that message was deleted or is not a conversation message")
	ErrNotOwnMessage     = errors.New("only your own messages can be edited")
	ErrNotEditable       = errors.New("only text messages can be edited")
	ErrEditWindowExpired = errors.New("WhatsApp only allows editing a message for " + whatsmeow.EditWindow.String() + " after it was sent")
)

// checkTarget rejects archived rows that are not real conversation messages.
func checkTarget(chat types.JID, target storage.Message) error {
	if target.GroupID != chat.String() || target.MessageID == "" {
		return fmt.Errorf("message is not in that chat")
	}
	if target.DeletedAt != nil || target.Type == "deleted" || target.Type == "reaction" {
		return ErrTargetUnavailable
	}
	return nil
}

// targetSender returns the archived sender of someone else's message as a
// user JID, refusing anything WhatsApp could not address.
func targetSender(target storage.Message) (types.JID, error) {
	jid, err := types.ParseJID(target.SenderID)
	if err != nil || !strings.Contains(target.SenderID, "@") || strings.ContainsAny(target.SenderID, " \t\r\n") || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) {
		return types.EmptyJID, fmt.Errorf("the target message has no usable sender")
	}
	return jid.ToNonAD(), nil
}

func validText(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("provide --text")
	}
	if len(text) > MaxMessageText {
		return fmt.Errorf("text exceeds %d bytes", MaxMessageText)
	}
	return nil
}

// requireConnected is the shared precondition of every write.
func (c *Client) requireConnected(action string) error {
	if !c.Paired() || !c.client.IsConnected() {
		return fmt.Errorf("connect your paired device before %s", action)
	}
	return nil
}

// sendWrite sends one built message and archives the sender's own copy, as
// Send does. A transport error leaves the outcome unknown and is never retried.
func (c *Client) sendWrite(ctx context.Context, jid types.JID, message *waE2E.Message, out SendResult) (SendResult, error) {
	out.MessageID = c.client.GenerateMessageID()
	out.Status = "unknown"
	response, err := c.client.SendMessage(ctx, jid, message, whatsmeow.SendRequestExtra{ID: out.MessageID, Timeout: 45 * time.Second, CheckPermissions: true})
	if err != nil {
		return out, fmt.Errorf("send outcome unknown; check WhatsApp before retrying; do not retry automatically")
	}
	out.Status = "server_acknowledged"
	out.Timestamp = response.Timestamp
	if c.capture != nil {
		event := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, Sender: c.client.Store.ID.ToNonAD(), IsFromMe: true, IsGroup: jid.Server == types.GroupServer}, ID: out.MessageID, Timestamp: response.Timestamp}, Message: message, RawMessage: message}
		if err = c.captureEvent(event); err != nil {
			out.ArchiveWarning = "Message acknowledged, but local archive failed; do not resend"
			c.StopCapture(err)
		}
	}
	return out, nil
}
