package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (a *App) runEdit(ctx context.Context, cfg config.Config, args []string) error {
	fs := a.flags("edit")
	req := whatsapp.EditRequest{}
	fs.StringVar(&req.Chat, "chat", "", "exact group or direct chat JID")
	fs.StringVar(&req.MessageID, "message-id", "", "archived ID of your own text message")
	fs.StringVar(&req.Text, "text", "", "the new text")
	if err := a.parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync edit --chat CHAT_ID --message-id MESSAGE_ID --text TEXT")
	}
	if err := req.Validate(); err != nil {
		return err
	}
	return a.runWrite(ctx, cfg, writeCommand{name: "edit", path: "/api/edit", chat: req.Chat, messageID: req.MessageID, body: req,
		prepare: func(target storage.Message) (func(context.Context, whatsapp.Operations) (whatsapp.SendResult, error), error) {
			in, err := whatsapp.NewEdit(req, target, time.Now())
			if err != nil {
				return nil, err
			}
			return func(ctx context.Context, ops whatsapp.Operations) (whatsapp.SendResult, error) {
				if ops.Edit == nil {
					return whatsapp.SendResult{ChatID: req.Chat, Status: "not_sent"}, fmt.Errorf("editing is unavailable in this client")
				}
				return ops.Edit(ctx, in)
			}, nil
		}})
}
