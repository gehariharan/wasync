package whatsapp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type SendInput struct {
	To   string `json:"to"`
	Text string `json:"text,omitempty"`
	File string `json:"file,omitempty"`
	Type string `json:"type,omitempty"`
	// Thumbnail carries a video's preview frame, a JPEG of at most 320 pixels
	// on each side that the sending command took with ffmpeg; it is not a
	// user option. Without one the video carries a plain preview and still plays.
	Thumbnail []byte `json:"thumbnail,omitempty"`
	// Video holds a video's bytes when an agent sends one through the API,
	// in place of a file wasync opens. It is never read from JSON, so only
	// code that has checked the agent's right to send can set it.
	Video []byte `json:"-"`
	// SentBy names the agent sending, so the archived copy shows it; agents
	// cannot set it themselves. It is empty for the owner's own sends.
	SentBy string `json:"-"`
}
type SendResult struct {
	ChatID         string    `json:"chat_id"`
	MessageID      string    `json:"message_id,omitempty"`
	Status         string    `json:"status"`
	Timestamp      time.Time `json:"timestamp,omitempty"`
	ArchiveWarning string    `json:"archive_warning,omitempty"`
	// TargetMessageID is the message a reply, reaction or edit refers to.
	TargetMessageID string `json:"target_message_id,omitempty"`
}
type Sender func(context.Context, SendInput) (SendResult, error)

func (in SendInput) Validate() error {
	if _, err := ParseChat(in.To); err != nil {
		return err
	}
	hasVideo := len(in.Video) > 0
	if strings.TrimSpace(in.Text) == "" && in.File == "" && !hasVideo {
		return fmt.Errorf("provide --text or --file")
	}
	if len(in.Text) > 16000 {
		return fmt.Errorf("text exceeds 16000 bytes")
	}
	switch in.Type {
	case "", "image", "document", "video":
	default:
		return fmt.Errorf("attachment type must be image, video or document")
	}
	switch {
	case in.File != "" && hasVideo:
		return fmt.Errorf("give a video file or video bytes, not both")
	case hasVideo && in.Type != "video":
		return fmt.Errorf("video bytes must be sent as type video")
	case in.File == "" && !hasVideo && in.Type != "":
		return fmt.Errorf("attachment type requires --file")
	case len(in.Video) > MaxVideoBytes:
		return fmt.Errorf("video exceeds %d MiB, the most WhatsApp sends as playable video; shorten or compress it", MaxVideoBytes>>20)
	case len(in.Thumbnail) > 0 && in.Type != "video":
		return fmt.Errorf("a preview frame is only sent with a video")
	case len(in.Thumbnail) > MaxThumbnailBytes:
		return fmt.Errorf("preview frame exceeds %d KiB", MaxThumbnailBytes>>10)
	}
	return nil
}

// messageSender is the part of the WhatsApp connection a send uses.
type messageSender interface {
	UploadReader(ctx context.Context, plaintext io.Reader, tempFile io.ReadWriteSeeker, appInfo whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	GenerateMessageID() types.MessageID
	SendMessage(ctx context.Context, to types.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
}

// Send sends only an explicitly requested message; it does not run an application retry loop.
func (c *Client) Send(ctx context.Context, in SendInput) (SendResult, error) {
	out := SendResult{ChatID: in.To, Status: "not_sent"}
	if err := in.Validate(); err != nil {
		return out, err
	}
	if err := c.requireConnected("sending"); err != nil {
		return out, err
	}
	return c.send(ctx, c.out(), c.wa().Store.ID.ToNonAD(), in)
}

// send uploads any attachment, sends the message once and archives it as the
// account's own message.
func (c *Client) send(ctx context.Context, wa messageSender, own types.JID, in SendInput) (SendResult, error) {
	out := SendResult{ChatID: in.To, Status: "not_sent"}
	jid, _ := ParseChat(in.To)
	message := &waE2E.Message{Conversation: proto.String(in.Text)}
	if in.File != "" || len(in.Video) > 0 {
		var err error
		if message, err = attachmentMessage(ctx, wa, in); err != nil {
			return out, err
		}
	}
	out.MessageID = wa.GenerateMessageID()
	out.Status = "unknown"
	response, err := wa.SendMessage(ctx, jid, message, whatsmeow.SendRequestExtra{ID: out.MessageID, Timeout: 45 * time.Second, CheckPermissions: true})
	if err != nil {
		return out, fmt.Errorf("send outcome unknown; check WhatsApp before retrying; do not retry automatically")
	}
	out.Status = "server_acknowledged"
	out.Timestamp = response.Timestamp
	if c.capture != nil {
		event := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, Sender: own, IsFromMe: true, IsGroup: jid.Server == types.GroupServer}, ID: out.MessageID, Timestamp: response.Timestamp}, Message: message, RawMessage: message}
		if err = c.captureSent(event, in.SentBy); err != nil {
			out.ArchiveWarning = "Message acknowledged, but local archive failed; do not resend"
			c.StopCapture(err)
		}
	}
	return out, nil
}

// attachmentMessage checks and uploads the attachment and builds its message.
// Nothing is sent when any of it fails.
func attachmentMessage(ctx context.Context, wa messageSender, in SendInput) (*waE2E.Message, error) {
	var (
		content io.ReaderAt
		size    int64
		mime    = videoMIME
	)
	if in.File != "" {
		f, sniffed, err := openAttachment(in.File)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("stat attachment: %w", err)
		}
		// Read only this file's initial length so appending cannot grow a transfer.
		content, size, mime = f, info.Size(), sniffed
	} else {
		content, size = bytes.NewReader(in.Video), int64(len(in.Video))
	}
	mediaType := whatsmeow.MediaDocument
	var video VideoInfo
	var thumbnail []byte
	switch in.Type {
	case "image":
		if mime != "image/jpeg" && mime != "image/png" {
			return nil, fmt.Errorf("image sending supports JPEG and PNG; use document for other files")
		}
		mediaType = whatsmeow.MediaImage
	case "video":
		var err error
		if video, err = ProbeMP4(content, size); err != nil {
			return nil, err
		}
		if thumbnail, err = VideoThumbnail(in.Thumbnail, video); err != nil {
			return nil, err
		}
		mediaType, mime = whatsmeow.MediaVideo, videoMIME
	}
	// UploadReader encrypts to a temporary file and streams the upload.
	plaintext := &contextReader{ctx: ctx, reader: io.NewSectionReader(content, 0, size)}
	uploaded, err := wa.UploadReader(ctx, plaintext, nil, mediaType)
	if err != nil {
		return nil, fmt.Errorf("attachment upload failed; conversation message was not sent")
	}
	message := &waE2E.Message{}
	switch mediaType {
	case whatsmeow.MediaImage:
		message.ImageMessage = &waE2E.ImageMessage{Caption: proto.String(in.Text), Mimetype: proto.String(mime), URL: &uploaded.URL, DirectPath: &uploaded.DirectPath, MediaKey: uploaded.MediaKey, FileEncSHA256: uploaded.FileEncSHA256, FileSHA256: uploaded.FileSHA256, FileLength: &uploaded.FileLength}
	case whatsmeow.MediaVideo:
		message.VideoMessage = &waE2E.VideoMessage{Mimetype: proto.String(mime), URL: &uploaded.URL, DirectPath: &uploaded.DirectPath, MediaKey: uploaded.MediaKey, FileEncSHA256: uploaded.FileEncSHA256, FileSHA256: uploaded.FileSHA256, FileLength: &uploaded.FileLength,
			Seconds: proto.Uint32(video.Seconds), Width: proto.Uint32(video.Width), Height: proto.Uint32(video.Height), JPEGThumbnail: thumbnail}
		if in.Text != "" {
			message.VideoMessage.Caption = proto.String(in.Text)
		}
	default:
		message.DocumentMessage = &waE2E.DocumentMessage{Caption: proto.String(in.Text), FileName: proto.String(filepath.Base(in.File)), Mimetype: proto.String(mime), URL: &uploaded.URL, DirectPath: &uploaded.DirectPath, MediaKey: uploaded.MediaKey, FileEncSHA256: uploaded.FileEncSHA256, FileSHA256: uploaded.FileSHA256, FileLength: &uploaded.FileLength}
	}
	return message, nil
}

// openAttachment reads only the sniffing prefix; supported transfer sizes are
// determined by WhatsApp rather than an application-specific 25 MiB limit.
func openAttachment(path string) (*os.File, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open attachment: %w", err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, "", fmt.Errorf("attachment must be a regular file")
	}
	var prefix [512]byte
	n, err := f.ReadAt(prefix[:], 0)
	if err != nil && err != io.EOF {
		f.Close()
		return nil, "", fmt.Errorf("read attachment: %w", err)
	}
	return f, http.DetectContentType(prefix[:n]), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
