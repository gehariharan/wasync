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

func (a *App) runReact(ctx context.Context, cfg config.Config, args []string) error {
	fs := a.flags("react")
	req := whatsapp.ReactRequest{}
	fs.StringVar(&req.Chat, "chat", "", "exact group or direct chat JID")
	fs.StringVar(&req.MessageID, "message-id", "", "archived WhatsApp message ID to react to")
	fs.StringVar(&req.Emoji, "emoji", "", "the emoji to react with")
	fs.BoolVar(&req.Remove, "remove", false, "clear your reaction instead")
	if err := a.parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync react --chat CHAT_ID --message-id MESSAGE_ID (--emoji EMOJI|--remove)")
	}
	if err := req.Validate(); err != nil {
		return err
	}
	return a.runWrite(ctx, cfg, writeCommand{name: "react", path: "/api/react", chat: req.Chat, messageID: req.MessageID, body: req,
		prepare: func(target storage.Message) (func(context.Context, whatsapp.Operations) (whatsapp.SendResult, error), error) {
			in, err := whatsapp.NewReact(req, target)
			if err != nil {
				return nil, err
			}
			return func(ctx context.Context, ops whatsapp.Operations) (whatsapp.SendResult, error) {
				if ops.React == nil {
					return whatsapp.SendResult{ChatID: req.Chat, Status: "not_sent"}, fmt.Errorf("reacting is unavailable in this client")
				}
				return ops.React(ctx, in)
			}, nil
		}})
}
