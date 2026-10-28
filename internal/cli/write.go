package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/msgref"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// operationsOf returns the writes a live client supports, or none.
func operationsOf(c Client) whatsapp.Operations {
	if o, ok := c.(interface{ Operations() whatsapp.Operations }); ok {
		return o.Operations()
	}
	return whatsapp.Operations{}
}

// writeCommand is one explicit WhatsApp write (reply, react, edit) on an
// archived message. path is its signed owner route and body its wire request.
// prepare applies every rule that depends on the archived target (for example
// a deleted message) and returns the send, so a refusal never opens the device.
type writeCommand struct {
	name, path, chat, messageID string
	body                        any
	prepare                     func(storage.Message) (func(context.Context, whatsapp.Operations) (whatsapp.SendResult, error), error)
}

// runWrite reuses a running owner through its command channel or, when none
// is running, connects with the saved session, like send. It never retries.
func (a *App) runWrite(ctx context.Context, cfg config.Config, w writeCommand) (result error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err := localfs.EnsureDir(cfg.DataDir); err != nil {
		return err
	}
	lock, err := processlock.Acquire(filepath.Join(cfg.DataDir, "device.lock"))
	if errors.Is(err, processlock.ErrBusy) {
		return a.writeThroughOwner(ctx, cfg, w)
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
	account, err := store.Account(ctx)
	if err != nil {
		return err
	}
	if account == nil {
		return fmt.Errorf("pair using wasync first")
	}
	target, err := store.MessageByRef(ctx, account.ID, account.JID, msgref.Ref{Account: account.JID, Chat: w.chat, Message: w.messageID})
	if errors.Is(err, storage.ErrMessageNotFound) {
		return fmt.Errorf("message %s is not in the archive of chat %s", w.messageID, w.chat)
	}
	if err != nil {
		return err
	}
	send, err := w.prepare(target)
	if err != nil {
		return err
	}
	client, err := a.OpenClient(ctx, filepath.Join(cfg.DataDir, "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, client.Close()) }()
	if !client.Paired() {
		return fmt.Errorf("pair using wasync first")
	}
	if live, ok := client.(interface {
		EnableCaptureForSend(context.Context, *storage.Store) error
	}); ok {
		if err = live.EnableCaptureForSend(ctx, store); err != nil {
			return err
		}
	}
	if err = client.Connect(ctx); err != nil {
		return fmt.Errorf("could not connect; nothing was sent")
	}
	sent, err := send(ctx, operationsOf(client))
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	if encodeErr := json.NewEncoder(a.Out).Encode(map[string]any{"result": sent, "error": errorMessage}); encodeErr != nil {
		return encodeErr
	}
	return err
}

func (a *App) writeThroughOwner(ctx context.Context, cfg config.Config, w writeCommand) error {
	if cfg.NoAPI {
		return fmt.Errorf("capture is running without a command channel; quit it before %s", w.name)
	}
	path := filepath.Join(cfg.DataDir, "send.token")
	if err := localfs.CheckPrivateFile(path); err != nil {
		return err
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body, err := json.Marshal(w.body)
	if err != nil {
		return err
	}
	response, err := commandRequest(ctx, cfg, "POST", w.path, body, token)
	if err != nil {
		return fmt.Errorf("command channel failed; %s outcome may be unknown; check WhatsApp before retrying", w.name)
	}
	defer response.Body.Close()
	if err = printCommandJSON(a.Out, response.Body); err != nil {
		return fmt.Errorf("could not read acknowledgement; check WhatsApp before retrying")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s did not return an acknowledgement; check the JSON before retrying", w.name)
	}
	return nil
}
