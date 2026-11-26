package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (a *App) runSend(ctx context.Context, cfg config.Config, args []string) (result error) {
	fs := a.flags("send")
	in := whatsapp.SendInput{}
	fs.StringVar(&in.To, "to", "", "exact group or direct chat JID")
	fs.StringVar(&in.Text, "text", "", "text, URL or attachment caption")
	fs.StringVar(&in.File, "file", "", "local attachment (WhatsApp enforces supported transfer limits)")
	fs.StringVar(&in.Type, "type", "", "image, video or document (default document)")
	if err := a.parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync send --to CHAT_ID --text TEXT [--file PATH --type image|video|document]")
	}
	if err := in.Validate(); err != nil {
		return err
	}
	if in.File != "" {
		absolute, err := filepath.Abs(in.File)
		if err != nil {
			return err
		}
		in.File = absolute
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if in.Type == "video" {
		preview, err := a.videoPreview(ctx, in.File)
		if err != nil {
			return err
		}
		in.Thumbnail = preview
	}
	if err := localfs.EnsureDir(cfg.DataDir); err != nil {
		return err
	}
	lock, err := processlock.Acquire(filepath.Join(cfg.DataDir, "device.lock"))
	if errors.Is(err, processlock.ErrBusy) {
		return a.sendThroughOwner(ctx, cfg, in)
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
	if !client.Paired() {
		return fmt.Errorf("pair using wasync first")
	}
	sender, ok := client.(interface {
		Send(context.Context, whatsapp.SendInput) (whatsapp.SendResult, error)
	})
	if !ok {
		return fmt.Errorf("sending is unavailable in this client")
	}
	if live, ok := client.(interface {
		EnableCaptureForSend(context.Context, *storage.Store) error
	}); ok {
		if err = live.EnableCaptureForSend(ctx, store); err != nil {
			return err
		}
	}
	if err = client.Connect(ctx); err != nil {
		return fmt.Errorf("could not connect; conversation message was not sent")
	}
	sent, err := sender.Send(ctx, in)
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	if encodeErr := json.NewEncoder(a.Out).Encode(map[string]any{"result": sent, "error": errorMessage}); encodeErr != nil {
		return encodeErr
	}
	return err
}

// videoPreview checks the video before anything connects and takes its
// preview frame here, in the command the person ran, so the running owner
// never starts ffmpeg. Without ffmpeg, or when it fails, it returns nothing
// and the message gets a plain preview.
func (a *App) videoPreview(ctx context.Context, path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open attachment: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("attachment must be a regular file")
	}
	info, err := whatsapp.ProbeMP4(f, stat.Size())
	if err != nil {
		return nil, err
	}
	frame, err := whatsapp.VideoFrame(ctx, path)
	if err == nil {
		frame, err = whatsapp.VideoThumbnail(frame, info)
	}
	switch {
	case errors.Is(err, whatsapp.ErrNoFFmpeg):
		fmt.Fprintln(a.Err, "ffmpeg is not installed, so the video gets a plain preview; install ffmpeg for a real one")
		return nil, nil
	case err != nil:
		fmt.Fprintln(a.Err, "could not take a preview frame, so the video gets a plain preview:", err)
		return nil, nil
	}
	return frame, nil
}

func (a *App) sendThroughOwner(ctx context.Context, cfg config.Config, in whatsapp.SendInput) error {
	if cfg.NoAPI {
		return fmt.Errorf("capture is running without a command channel; quit it before sending")
	}
	if err := api.ValidateAddr(cfg.APIAddr); err != nil {
		return err
	}
	path := filepath.Join(cfg.DataDir, "send.token")
	if err := localfs.CheckPrivateFile(path); err != nil {
		return err
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	response, err := commandRequest(ctx, cfg, "POST", "/api/send", body, token)
	if err != nil {
		return fmt.Errorf("command channel failed; send outcome may be unknown; check WhatsApp before retrying")
	}
	defer response.Body.Close()
	if err = printCommandJSON(a.Out, response.Body); err != nil {
		return fmt.Errorf("could not read acknowledgement; check WhatsApp before retrying")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("send did not return an acknowledgement; check the JSON status before retrying")
	}
	return nil
}

func commandRequest(ctx context.Context, cfg config.Config, method, path string, body, token []byte) (*http.Response, error) {
	if err := api.ValidateAddr(cfg.APIAddr); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+cfg.APIAddr+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	nonceBytes := make([]byte, 16)
	if _, err = rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(nonceBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("X-Wasync-Nonce", nonce)
	req.Header.Set("X-Wasync-Time", timestamp)
	req.Header.Set("X-Wasync-Signature", api.SignCommand(strings.TrimSpace(string(token)), timestamp, nonce, req.Method, req.URL.Path, body))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: cfg.Timeout, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	return response, nil
}
