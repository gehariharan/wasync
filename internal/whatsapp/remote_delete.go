package whatsapp

import (
	"context"
	"fmt"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

// These methods are called only after human CLI or TUI confirmation.
func (c *Client) RevokeOwnMessage(ctx context.Context, chat string, message storage.Message) error {
	jid, err := ParseChat(chat)
	if err != nil {
		return err
	}
	if !message.FromMe || message.MessageID == "" {
		return fmt.Errorf("only your own archived messages can be revoked")
	}
	if !c.wa().IsConnected() {
		return fmt.Errorf("connect before revoking")
	}
	id := c.wa().GenerateMessageID()
	_, err = c.wa().SendMessage(ctx, jid, c.wa().BuildRevoke(jid, types.EmptyJID, message.MessageID), whatsmeow.SendRequestExtra{ID: id, Timeout: 45 * time.Second, CheckPermissions: true})
	if err != nil {
		return fmt.Errorf("WhatsApp revocation outcome unknown; inspect WhatsApp before trying again")
	}
	return nil
}
func (c *Client) DeleteWhatsAppChat(ctx context.Context, chat string, last storage.Message) error {
	jid, err := ParseChat(chat)
	if err != nil {
		return err
	}
	if !c.wa().IsConnected() {
		return fmt.Errorf("connect before deleting chat state")
	}
	if last.MessageID == "" || last.Timestamp.IsZero() {
		return fmt.Errorf("a known archived message is required to bound WhatsApp chat deletion")
	}
	sender, err := types.ParseJID(last.SenderID)
	if err != nil {
		return fmt.Errorf("unknown last message sender")
	}
	if last.FromMe {
		sender = types.EmptyJID
	}
	key := c.wa().BuildMessageKey(jid, sender, last.MessageID)
	patch := appstate.BuildDeleteChat(jid, last.Timestamp, key, true)
	requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err = c.wa().SendAppState(requestCtx, patch); err != nil {
		return fmt.Errorf("WhatsApp chat deletion outcome unknown; inspect WhatsApp before trying again")
	}
	return nil
}
