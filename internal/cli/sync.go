package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/tui"
	"github.com/gehariharan/wasync/internal/whatsapp"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

func (a *App) ownerCommand(ctx context.Context, cfg config.Config, method, path string, body []byte, credential string) error {
	var token []byte
	if credential == approvecred.FileName {
		secret, err := approvecred.Read(cfg.DataDir)
		if err != nil {
			return err
		}
		token = []byte(secret)
	} else {
		tokenPath := filepath.Join(cfg.DataDir, credential)
		if err := localfs.CheckPrivateFile(tokenPath); err != nil {
			return err
		}
		var err error
		if token, err = os.ReadFile(tokenPath); err != nil {
			return err
		}
	}
	response, err := commandRequest(ctx, cfg, method, path, body, token)
	if err != nil {
		return fmt.Errorf("command channel unavailable; start wasync serve or the TUI: %w", err)
	}
	defer response.Body.Close()
	if err = printCommandJSON(a.Out, response.Body); err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		return fmt.Errorf("command rejected (HTTP %d)", response.StatusCode)
	}
	return nil
}
func (a *App) runSync(ctx context.Context, cfg config.Config, args []string) (result error) {
	fs := a.flags("sync")
	wait := fs.Duration("wait", 30*time.Second, "observe incoming events for 0 to 60 seconds")
	in := whatsapp.SyncInput{}
	fs.BoolVar(&in.History, "history", false, "queue another best-effort history request")
	fs.StringVar(&in.Chat, "chat", "", "optional enabled chat JID")
	if err := a.parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *wait < 0 || *wait > 60*time.Second {
		return fmt.Errorf("usage: wasync sync [--wait 30s] [--history] [--chat ID]; wait 0 to 60s")
	}
	if in.Chat != "" {
		if _, err := whatsapp.ParseChat(in.Chat); err != nil {
			return err
		}
	}
	in.WaitSeconds = int((*wait + time.Second - 1) / time.Second)
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err := localfs.EnsureDir(cfg.DataDir); err != nil {
		return err
	}
	lock, err := processlock.Acquire(filepath.Join(cfg.DataDir, "device.lock"))
	if errors.Is(err, processlock.ErrBusy) {
		if cfg.NoAPI {
			return fmt.Errorf("running owner has no command channel")
		}
		body, _ := json.Marshal(in)
		return a.ownerCommand(ctx, cfg, "POST", "/api/sync", body, "send.token")
	}
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	store, err := storage.Open(ctx, filepath.Join(cfg.DataDir, "archive.db"))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	client, err := a.OpenClient(ctx, filepath.Join(cfg.DataDir, "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, client.Close()) }()
	live, ok := client.(tui.Client)
	if !ok {
		return fmt.Errorf("client does not support capture")
	}
	if !client.Paired() {
		return fmt.Errorf("pair with wasync login first")
	}
	if media, ok := client.(interface{ ConfigureMedia(string, bool) }); ok {
		media.ConfigureMedia(filepath.Join(cfg.DataDir, "media"), cfg.DownloadMedia)
	}
	if err = live.EnableCapture(ctx, store); err != nil {
		return err
	}
	if err = client.Connect(ctx); err != nil {
		return err
	}
	session := tui.NewSession(ctx, store, live)
	synced, err := session.Sync(ctx, in)
	// The error key appears only when there is an error, so a successful sync
	// does not begin with an empty "error" that reads like a failure.
	response := map[string]any{"result": synced}
	if err != nil {
		response["error"] = err.Error()
	}
	if encodeErr := json.NewEncoder(a.Out).Encode(response); encodeErr != nil {
		return encodeErr
	}
	return err
}
