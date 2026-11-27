package whatsapp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/mediafiles"
	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type mediaJob struct {
	account    int64
	message    storage.Message
	attachment whatsmeow.DownloadableMessage
	extension  string
	size       int64
	manual     bool
}

// ConfigureMedia sets where attachments are kept. enabled is the
// --download-media flag, which makes every chat that follows the wasync
// default save its attachments whatever the owner's setting says.
func (c *Client) ConfigureMedia(root string, enabled bool) {
	c.mediaRoot = root
	c.mediaEnabled = enabled
}

// ConfigureSaveMedia gives the owner's "Also save photos, videos and files"
// setting, read each time an attachment arrives so a change applies at once.
// Without it only the --download-media flag turns the default on.
func (c *Client) ConfigureSaveMedia(on func() bool) { c.saveMedia = on }
func (c *Client) startMediaWorker(ctx context.Context) error {
	if c.mediaRoot == "" {
		return nil
	}
	if err := c.capture.store.InterruptMediaDownloads(ctx); err != nil {
		return err
	}
	ctx, c.mediaCancel = context.WithCancel(ctx)
	c.mediaQueue = make(chan mediaJob, 64)
	c.mediaDone = make(chan struct{})
	go func() {
		defer close(c.mediaDone)
		c.sweepMedia(ctx)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if c.mediaFilesMayExist() {
					c.sweepMedia(ctx)
				}
			case job := <-c.mediaQueue:
				if ctx.Err() != nil {
					return
				}
				if err := c.downloadMedia(ctx, job); err != nil {
					if _, e := c.capture.store.SetMediaDownload(ctx, job.account, job.message.GroupID, job.message.MessageID, "failed", ""); e != nil && ctx.Err() == nil {
						c.StopCapture(e)
						c.finishMedia(job)
						return
					}
				} else if err := c.capture.store.ReleaseMediaDownload(ctx, job.account, job.message.GroupID, job.message.MessageID); err != nil && ctx.Err() == nil {
					c.StopCapture(err)
					c.finishMedia(job)
					return
				}
				c.finishMedia(job)
			}
		}
	}()
	return nil
}
func (c *Client) queueMedia(account int64, e *events.Message, m storage.Message) error {
	if c.mediaQueue == nil || m.Kind != "" || m.ExpiresAt != nil || viewOnceEvent(e) {
		return nil
	}
	message := unwrap(e.Message)
	if message == nil {
		return nil
	}
	job := mediaJob{account: account, message: m, extension: ".bin"}
	var length uint64
	var contentType string
	switch {
	case message.GetImageMessage() != nil:
		attachment := message.GetImageMessage()
		job.attachment = proto.Clone(attachment).(whatsmeow.DownloadableMessage)
		length, contentType = attachment.GetFileLength(), attachment.GetMimetype()
	case message.GetDocumentMessage() != nil:
		attachment := message.GetDocumentMessage()
		job.attachment = proto.Clone(attachment).(whatsmeow.DownloadableMessage)
		length, contentType = attachment.GetFileLength(), attachment.GetMimetype()
	case message.GetVideoMessage() != nil:
		attachment := message.GetVideoMessage()
		job.attachment = proto.Clone(attachment).(whatsmeow.DownloadableMessage)
		length, contentType = attachment.GetFileLength(), attachment.GetMimetype()
	case message.GetAudioMessage() != nil:
		attachment := message.GetAudioMessage()
		job.attachment = proto.Clone(attachment).(whatsmeow.DownloadableMessage)
		length, contentType = attachment.GetFileLength(), attachment.GetMimetype()
	case message.GetStickerMessage() != nil:
		attachment := message.GetStickerMessage()
		// Lottie sticker containers are not WebP images; retain their safe metadata.
		if attachment.GetIsLottie() {
			return nil
		}
		job.attachment = proto.Clone(attachment).(whatsmeow.DownloadableMessage)
		length, contentType = attachment.GetFileLength(), attachment.GetMimetype()
	default:
		return nil
	}
	job.extension = mediaExtension(contentType)
	if length > math.MaxInt64-64 || len(job.attachment.GetFileSHA256()) != 32 {
		_, err := c.capture.store.SetMediaDownload(c.capture.ctx, account, m.GroupID, m.MessageID, "invalid_metadata", "")
		return err
	}
	job.size = int64(length)
	if c.mediaRoot != "" && c.capture != nil {
		eligible, err := c.capture.store.MediaEligible(c.capture.ctx, account, m.GroupID, m.MessageID)
		if err != nil || !eligible {
			return err
		}
		if err = c.saveMediaReference(c.capture.ctx, job); err != nil {
			return err
		}
		if err = c.capture.store.MediaReferenceAvailable(c.capture.ctx, account, m.GroupID, m.MessageID); err != nil {
			return err
		}
	}
	queueCtx := context.Background()
	if c.capture != nil {
		queueCtx = c.capture.ctx
	}
	automatic, err := c.autoMedia(queueCtx, account, m.GroupID)
	if err != nil {
		return err
	}
	if !automatic {
		return nil
	}
	// Saturated automatic capture records queue_full, but must not stop capture.
	err = c.enqueueMedia(queueCtx, job)
	if err != nil && errors.Is(err, ErrMediaQueueFull) {
		return nil
	}
	return err
}
func (c *Client) downloadMedia(ctx context.Context, job mediaJob) error {
	enabled, err := c.capture.store.MediaEligible(ctx, job.account, job.message.GroupID, job.message.MessageID)
	if err != nil || !enabled {
		return err
	}
	if !job.manual {
		allowed, e := c.autoMedia(ctx, job.account, job.message.GroupID)
		if e != nil {
			return e
		}
		if !allowed {
			_, e = c.capture.store.SetMediaDownload(ctx, job.account, job.message.GroupID, job.message.MessageID, "available", "")
			return e
		}
	}
	dir, err := mediafiles.Ensure(c.mediaRoot, job.account, job.message.GroupID)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(job.attachment.GetFileSHA256())
	name := fmt.Sprintf("%s-%x%s", mediafiles.Prefix(job.message.MessageID), hash, job.extension)
	path := filepath.Join(dir, name)
	c.mediaRequestMu.Lock()
	err = localfs.CheckPrivateFile(path)
	if err == nil {
		_, err = c.capture.store.SetMediaDownload(ctx, job.account, job.message.GroupID, job.message.MessageID, "downloaded", path)
		c.mediaRequestMu.Unlock()
		return err
	}
	c.mediaRequestMu.Unlock()
	if !os.IsNotExist(err) {
		return err
	}
	temp := path + ".part"
	c.noteMediaWrite()
	f, err := localfs.OpenFile(temp)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if err = f.Truncate(0); err != nil {
		f.Close()
		return err
	}
	if _, err = c.capture.store.SetMediaDownload(ctx, job.account, job.message.GroupID, job.message.MessageID, "downloading", ""); err != nil {
		f.Close()
		return err
	}
	transfer := c.mediaTransfer
	if transfer == nil {
		transfer = c.wa().DownloadToFile
	}
	err = transfer(ctx, job.attachment, &boundedMediaFile{File: f, limit: job.size + 64})
	if err == nil {
		var info os.FileInfo
		info, err = f.Stat()
		if err == nil && info.Size() != job.size {
			err = fmt.Errorf("downloaded media length does not match message metadata")
		}
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// Recheck consent and deletion before making the file visible.
	valid, err := c.capture.store.MediaEligible(ctx, job.account, job.message.GroupID, job.message.MessageID)
	if err != nil || !valid {
		return err
	}
	c.mediaRequestMu.Lock()
	defer c.mediaRequestMu.Unlock()
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	valid, err = c.capture.store.CompleteMediaDownload(ctx, job.account, job.message.GroupID, job.message.MessageID, path, !job.manual, c.mediaDefault())
	if err != nil || !valid {
		os.Remove(path)
	}
	return err
}

// Bound writes to the attachment's own declared length plus encryption overhead,
// not an application-wide file-size cap. Hash validation remains upstream.
type boundedMediaFile struct {
	*os.File
	limit int64
}

func (f *boundedMediaFile) Write(p []byte) (int, error) {
	offset, err := f.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if offset > f.limit-int64(len(p)) {
		return 0, fmt.Errorf("media exceeds download limit")
	}
	return f.File.Write(p)
}
func (f *boundedMediaFile) WriteAt(p []byte, offset int64) (int, error) {
	if offset < 0 || offset > f.limit-int64(len(p)) {
		return 0, fmt.Errorf("media exceeds download limit")
	}
	return f.File.WriteAt(p, offset)
}
func (f *boundedMediaFile) Truncate(size int64) error {
	if size < 0 || size > f.limit {
		return fmt.Errorf("media exceeds download limit")
	}
	return f.File.Truncate(size)
}

// io.Copy prefers ReaderFrom; do not inherit os.File.ReadFrom, which would
// bypass our Write limit when downloading an oversized response.
func (f *boundedMediaFile) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{f}, r)
}

func (c *Client) removeMedia(account int64, chat, message string) {
	if err := RemoveMediaReference(c.mediaRoot, account, chat, message); err != nil {
		c.log.Warn("media reference cleanup deferred")
	}
	if err := mediafiles.RemoveMessage(c.mediaRoot, account, chat, message); err != nil {
		c.log.Warn("local media cleanup deferred; files may be open")
	}
}

// mediaFilesMayExist reports whether the media folders may hold a file the
// sweep has to look at. The sweep finds out when it last saw none, and every
// file written since then counts, so a wasync that keeps text only, or whose
// files are all gone, does not walk empty folders every 30 seconds.
func (c *Client) mediaFilesMayExist() bool {
	empty := c.mediaEmptyAt.Load()
	return empty == 0 || empty-1 != c.mediaWrites.Load()
}

// noteMediaWrite records that a file is about to appear in the media folders.
func (c *Client) noteMediaWrite() { c.mediaWrites.Add(1) }

func (c *Client) sweepMedia(ctx context.Context) {
	writes := c.mediaWrites.Load()
	refs := c.sweepMediaReferences(ctx)
	files := 0
	err := filepath.WalkDir(c.mediaRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !mediafiles.ManagedPath(c.mediaRoot, path) {
			return nil
		}
		files++
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".part") && time.Since(info.ModTime()) < 2*time.Minute {
			return nil
		}
		referenced, err := c.capture.store.MediaReferenced(ctx, path)
		if err != nil {
			return err
		}
		if !referenced {
			return os.Remove(path)
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		c.log.Warn("local media cleanup incomplete; retrying later")
	}
	if err == nil && refs == 0 && files == 0 {
		// Nothing to look at until a file is written after this sweep began.
		c.mediaEmptyAt.Store(writes + 1)
	} else {
		c.mediaEmptyAt.Store(0)
	}
}

// Use a fixed MIME mapping rather than platform-dependent extensions or sender
// filenames. Unknown types are retained as inert .bin files and never opened.
func mediaExtension(contentType string) string {
	kind, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ".bin"
	}
	switch kind {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "video/mp4":
		return ".mp4"
	case "video/3gpp":
		return ".3gp"
	case "video/webm":
		return ".webm"
	case "audio/ogg", "application/ogg":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4", "audio/x-m4a":
		return ".m4a"
	case "audio/aac":
		return ".aac"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "application/pdf":
		return ".pdf"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.ms-excel":
		return ".xls"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.ms-powerpoint":
		return ".ppt"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	case "text/plain":
		return ".txt"
	case "text/csv":
		return ".csv"
	case "application/zip":
		return ".zip"
	default:
		return ".bin"
	}
}
