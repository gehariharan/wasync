package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (a *App) runReply(ctx context.Context, cfg config.Config, args []string) error {
	fs := a.flags("reply")
	req := whatsapp.ReplyRequest{}
	var mentions stringList
	fs.StringVar(&req.Chat, "chat", "", "exact group or direct chat JID")
	fs.StringVar(&req.MessageID, "message-id", "", "archived WhatsApp message ID to quote")
	fs.StringVar(&req.Text, "text", "", "reply text")
	fs.Var(&mentions, "mention", "user JID to mention (repeatable); the text must contain @NUMBER for it")
	if err := a.parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync reply --chat CHAT_ID --message-id MESSAGE_ID --text TEXT [--mention USER_JID ...]")
	}
	req.Mentions = mentions
	if err := req.Validate(); err != nil {
		return err
	}
	return a.runWrite(ctx, cfg, writeCommand{name: "reply", path: "/api/reply", chat: req.Chat, messageID: req.MessageID, body: req,
		prepare: func(target storage.Message) (func(context.Context, whatsapp.Operations) (whatsapp.SendResult, error), error) {
			in, err := whatsapp.NewReply(req, target)
			if err != nil {
				return nil, err
			}
			return func(ctx context.Context, ops whatsapp.Operations) (whatsapp.SendResult, error) {
				if ops.Reply == nil {
					return whatsapp.SendResult{ChatID: req.Chat, Status: "not_sent"}, fmt.Errorf("replying is unavailable in this client")
				}
				return ops.Reply(ctx, in)
			}, nil
		}})
}
