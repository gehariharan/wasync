package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/tui"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (a *App) tuiAction(offline bool) action {
	return func(ctx context.Context, cfg config.Config, store *storage.Store, _ *slog.Logger) (result error) {
		if !a.Interactive {
			return fmt.Errorf("the TUI requires an interactive terminal; use wasync help for scriptable commands")
		}
		a.MediaDownload = nil
		a.Approvals = &approval.Queue{}
		if offline {
			session := tui.NewSession(ctx, store, nil)
			session.Approvals = a.Approvals
			session.MediaRoot = filepath.Join(cfg.DataDir, "media")
			a.Approve = session.ApproveAction
			a.Sync = session.Sync
			return a.withAPI(ctx, cfg, store, nil, func(ctx context.Context, url string) error {
				session.APIURL = url
				session.Approvals = a.Approvals
				session.MediaRoot = filepath.Join(cfg.DataDir, "media")
				return tui.Run(ctx, session, a.In, a.Out)
			})
		}
		file, err := localfs.OpenFile(filepath.Join(cfg.DataDir, "wasync.log"))
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, file.Close()) }()
		if _, err = file.Seek(0, io.SeekEnd); err != nil {
			return err
		}
		log := slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelInfo}))
		clientCtx, cancel := context.WithCancel(ctx)
		client, err := a.OpenClient(clientCtx, filepath.Join(cfg.DataDir, "session.db"), log)
		if err != nil {
			cancel()
			return err
		}
		defer func() { cancel(); result = errors.Join(result, client.Close()) }()
		live, ok := client.(tui.Client)
		if !ok {
			return fmt.Errorf("this client does not implement capture")
		}
		if history, ok := client.(interface{ ConfigurePairingHistory(bool) }); ok {
			history.ConfigurePairingHistory(cfg.PairingFullHistory)
		}
		if media, ok := client.(interface{ ConfigureMedia(string, bool) }); ok {
			media.ConfigureMedia(filepath.Join(cfg.DataDir, "media"), cfg.DownloadMedia)
		}
		if err = live.EnableCapture(clientCtx, store); err != nil {
			return err
		}
		var send whatsapp.Sender
		if active, ok := client.(interface {
			Send(context.Context, whatsapp.SendInput) (whatsapp.SendResult, error)
		}); ok {
			send = active.Send
		}
		a.Operations = operationsOf(client)
		session := tui.NewSession(clientCtx, store, live)
		session.Approvals = a.Approvals
		session.MediaRoot = filepath.Join(cfg.DataDir, "media")
		a.Approve = session.ApproveAction
		a.Sync = session.Sync
		if media, ok := client.(interface {
			RequestMedia(context.Context, string, string) (whatsapp.MediaResult, error)
		}); ok {
			a.MediaDownload = media.RequestMedia
		}
		return a.withAPI(clientCtx, cfg, store, live.Status, func(ctx context.Context, url string) error {
			session.APIURL = url
			session.Approvals = a.Approvals
			session.MediaRoot = filepath.Join(cfg.DataDir, "media")
			return tui.Run(ctx, session, a.In, a.Out)
		}, send)
	}
}
