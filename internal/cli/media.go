package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (a *App) runMedia(parent context.Context, cfg config.Config, args []string) (result error) {
	if len(args) == 0 || (args[0] != "policy" && args[0] != "download") {
		return fmt.Errorf("usage: wasync media policy|download --chat CHAT_ID [--mode auto|manual|inherit | --message-id ID]")
	}
	f := a.flags("media " + args[0])
	chat := f.String("chat", "", "exact group/direct chat ID")
	var mode, message string
	if args[0] == "policy" {
		f.StringVar(&mode, "mode", "", "persistent auto, manual or inherit policy")
	} else {
		f.StringVar(&message, "message-id", "", "one-time attachment download")
	}
	if err := a.parseFlags(f, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if _, err := whatsapp.ParseChat(*chat); err != nil {
		return err
	}
	if args[0] == "policy" {
		if mode != "" && !storage.ValidMediaPolicy(mode) {
			return fmt.Errorf("policy accepts --mode auto, manual or inherit")
		}
	} else if message == "" || len(message) > 1024 {
		return fmt.Errorf("download requires --message-id and no --mode")
	}
	ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"chat_id": *chat, "mode": mode, "message_id": message})
	if err := localfs.EnsureDir(cfg.DataDir); err != nil {
		return err
	}
	lock, err := acquireData(cfg.DataDir)
	if errors.Is(err, processlock.ErrBusy) {
		if cfg.NoAPI {
			return fmt.Errorf("owner command channel is disabled; use the running TUI")
		}
		return a.ownerCommand(ctx, cfg, "POST", "/api/media/"+args[0], body, "send.token")
	}
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	if args[0] == "download" {
		return fmt.Errorf("start wasync or wasync serve, then request this download")
	}
	store, err := storage.Open(ctx, filepath.Join(cfg.DataDir, "archive.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	account, err := store.Account(ctx)
	if err != nil {
		return err
	}
	if account == nil {
		return fmt.Errorf("pair and discover chats first")
	}
	if mode != "" {
		if err = store.SetMediaPolicy(ctx, account.ID, *chat, mode); err != nil {
			return err
		}
	}
	current, err := store.MediaPolicy(ctx, account.ID, *chat)
	if err != nil {
		return err
	}
	return json.NewEncoder(a.Out).Encode(map[string]any{"chat_id": *chat, "mode": current, "persistent": true})
}
