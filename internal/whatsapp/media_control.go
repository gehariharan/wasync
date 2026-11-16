package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/mediafiles"
)

var ErrMediaQueueFull = errors.New("media queue is full; retry this message later")

type MediaResult struct {
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}
type MediaDownloader func(context.Context, string, string) (MediaResult, error)

func (c *Client) autoMedia(ctx context.Context, account int64, chat string) (bool, error) {
	if c.capture == nil {
		return c.mediaEnabled, nil
	}
	mode, err := c.capture.store.MediaPolicy(ctx, account, chat)
	if err != nil {
		return false, err
	}
	switch mode {
	case "auto":
		return true, nil
	case "manual":
		return false, nil
	default:
		return c.mediaEnabled, nil
	}
}

// RequestMedia queues one explicit request without changing the chat policy.
func (c *Client) RequestMedia(ctx context.Context, chat, message string) (MediaResult, error) {
	result := MediaResult{ChatID: chat, MessageID: message}
	if c.capture == nil || c.mediaRoot == "" || c.mediaQueue == nil {
		return result, fmt.Errorf("start the live TUI or serve command first")
	}
	account := c.Status().AccountID
	if account == 0 {
		return result, fmt.Errorf("connect the paired account first")
	}
	eligible, err := c.capture.store.MediaEligible(ctx, account, chat, message)
	if err != nil {
		return result, err
	}
	if !eligible {
		return result, fmt.Errorf("enable capture for a chat containing an available, unedited attachment")
	}
	original, err := c.capture.store.MessageByWhatsAppID(ctx, account, chat, message)
	if err != nil {
		return result, err
	}
	switch original.Type {
	case "image", "document", "video", "audio", "sticker":
	default:
		return result, fmt.Errorf("selected message has no downloadable attachment")
	}
	var metadata struct {
		State    string `json:"download_state"`
		Relative string `json:"relative_path"`
	}
	_ = json.Unmarshal([]byte(original.Media), &metadata)
	path := filepath.Join(c.mediaRoot, filepath.FromSlash(metadata.Relative))
	if metadata.State == "downloaded" && mediafiles.ManagedPath(c.mediaRoot, path) && localfs.CheckPrivateFile(path) == nil {
		result.Status = "already_downloaded"
		return result, nil
	}
	wa := c.wa()
	online := wa != nil && wa.IsConnected()
	if c.mediaConnected != nil {
		online = c.mediaConnected()
	}
	if !online {
		return result, fmt.Errorf("connect WhatsApp before downloading")
	}
	ref, err := readMediaReference(referencePath(ReferenceRoot(c.mediaRoot), account, chat, message))
	if err != nil {
		if os.IsNotExist(err) {
			return result, fmt.Errorf("download reference unavailable; request history and wait for WhatsApp to deliver the original attachment metadata again")
		}
		return result, fmt.Errorf("cannot read protected download reference")
	}
	if ref.Account != account || ref.Chat != chat || ref.Message != message {
		return result, fmt.Errorf("attachment reference identity mismatch")
	}
	job, err := ref.job()
	if err != nil {
		return result, err
	}
	job.manual = true
	if err = c.enqueueMedia(ctx, job); err != nil {
		return result, err
	}
	result.Status = "queued"
	return result, nil
}
func (c *Client) enqueueMedia(ctx context.Context, job mediaJob) error {
	c.mediaRequestMu.Lock()
	defer c.mediaRequestMu.Unlock()
	key := fmt.Sprintf("%d/%s/%s/%t", job.account, job.message.GroupID, job.message.MessageID, job.manual)
	if c.mediaPending == nil {
		c.mediaPending = make(map[string]bool)
	}
	if c.mediaPending[key] {
		return nil
	}

	if c.capture != nil {
		original, err := c.capture.store.MessageByWhatsAppID(ctx, job.account, job.message.GroupID, job.message.MessageID)
		if err != nil {
			return err
		}
		var meta struct {
			State    string `json:"download_state"`
			Relative string `json:"relative_path"`
		}
		_ = json.Unmarshal([]byte(original.Media), &meta)
		path := filepath.Join(c.mediaRoot, filepath.FromSlash(meta.Relative))
		if meta.State == "downloaded" && mediafiles.ManagedPath(c.mediaRoot, path) && localfs.CheckPrivateFile(path) == nil {
			return nil
		}
	}
	if len(c.mediaQueue) >= cap(c.mediaQueue) {
		if c.capture != nil {
			_, err := c.capture.store.SetMediaDownload(ctx, job.account, job.message.GroupID, job.message.MessageID, "queue_full", "")
			if err != nil {
				return err
			}
		}
		return ErrMediaQueueFull
	}
	if c.capture != nil {
		eligible, err := c.capture.store.SetMediaDownload(ctx, job.account, job.message.GroupID, job.message.MessageID, "queued", "")
		if err != nil {
			return err
		}
		if !eligible {
			return fmt.Errorf("attachment is no longer eligible")
		}
	}
	c.mediaPending[key] = true
	c.mediaQueue <- job
	return nil
}
func (c *Client) finishMedia(job mediaJob) {
	c.mediaRequestMu.Lock()
	defer c.mediaRequestMu.Unlock()
	delete(c.mediaPending, fmt.Sprintf("%d/%s/%s/%t", job.account, job.message.GroupID, job.message.MessageID, job.manual))
}
