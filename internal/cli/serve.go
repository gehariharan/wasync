package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/tui"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// withAPI closes all HTTP handlers before the caller closes the store/client.
func (a *App) withAPI(ctx context.Context, cfg config.Config, store *storage.Store, status func() whatsapp.CaptureStatus, run func(context.Context, string) error, sender ...whatsapp.Sender) (result error) {
	if cfg.NoAPI {
		return run(ctx, "")
	}
	server, err := api.StartOperations(cfg.APIAddr, cfg.DataDir, store, status, a.Approvals, a.Approve, a.Sync, a.MediaDownload, a.Operations, sender...)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, server.Close()) }()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failed := make(chan error, 1)
	go func() {
		select {
		case err := <-server.Errors:
			if err != nil {
				failed <- err
				cancel()
			}
		case <-runCtx.Done():
		}
	}()
	result = run(runCtx, server.URL)
	select {
	case err := <-failed:
		result = errors.Join(result, err)
	default:
	}
	return result
}
func (a *App) serveAction(offline bool) action {
	return func(ctx context.Context, cfg config.Config, store *storage.Store, log *slog.Logger) (result error) {
		if cfg.NoAPI {
			return fmt.Errorf("serve requires the local API; remove --no-api")
		}
		var client Client
		var live tui.Client
		if !offline {
			var err error
			client, err = a.OpenClient(ctx, filepath.Join(cfg.DataDir, "session.db"), log)
			if err != nil {
				return err
			}
			defer func() { result = errors.Join(result, client.Close()) }()
			if !client.Paired() {
				return fmt.Errorf("pair using wasync login first, or use serve --offline for archive queries")
			}
			var ok bool
			live, ok = client.(tui.Client)
			if !ok {
				return fmt.Errorf("this client does not implement capture")
			}
			if history, ok := client.(interface{ ConfigurePairingHistory(bool) }); ok {
				history.ConfigurePairingHistory(cfg.PairingFullHistory)
			}
			if media, ok := client.(interface{ ConfigureMedia(string, bool) }); ok {
				media.ConfigureMedia(filepath.Join(cfg.DataDir, "media"), cfg.DownloadMedia)
			}
			if err = live.EnableCapture(ctx, store); err != nil {
				return err
			}
		}
		var status func() whatsapp.CaptureStatus
		if live != nil {
			status = live.Status
		}
		var send whatsapp.Sender
		if active, ok := client.(interface {
			Send(context.Context, whatsapp.SendInput) (whatsapp.SendResult, error)
		}); ok {
			send = active.Send
		}
		a.MediaDownload = nil
		a.Operations = operationsOf(client)
		a.Approvals = &approval.Queue{}
		session := tui.NewSession(ctx, store, live)
		session.Approvals = a.Approvals
		session.MediaRoot = filepath.Join(cfg.DataDir, "media")
		a.Approve = session.ApproveAction
		a.Sync = session.Sync
		if media, ok := client.(interface {
			RequestMedia(context.Context, string, string) (whatsapp.MediaResult, error)
		}); ok {
			a.MediaDownload = media.RequestMedia
		}
		return a.withAPI(ctx, cfg, store, status, func(ctx context.Context, url string) error {
			fmt.Fprintf(a.Out, "Local API: %s\nBearer token file: %s\n", url, filepath.Join(cfg.DataDir, "api.token"))
			if client != nil {
				err := client.Connect(ctx)
				if err == nil {
					discoveryCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
					_, _, err = discover(discoveryCtx, store, client)
					cancel()
				}
				if err != nil {
					return err
				}
			}
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					if live != nil {
						if err := live.Maintain(ctx); err != nil {
							live.StopCapture(err)
							return err
						}
					} else if err := store.Expire(ctx); err != nil {
						return err
					}
				}
			}
		}, send)
	}
}
