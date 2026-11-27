package whatsapp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/mediafiles"
	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// These are credentials, not archive metadata. Only store fields required by
// DownloadToFile; never quoted messages, captions, URLs, or whole event payloads.
type mediaReference struct {
	Account       int64  `json:"account"`
	Chat          string `json:"chat"`
	Message       string `json:"message"`
	Kind          string `json:"kind"`
	MIME          string `json:"mime"`
	Size          int64  `json:"size"`
	Hash          []byte `json:"hash"`
	EncryptedHash []byte `json:"encrypted_hash"`
	Key           []byte `json:"key"`
	Path          string `json:"path"`
}

func ReferenceRoot(mediaRoot string) string {
	return filepath.Join(filepath.Dir(mediaRoot), "media-refs")
}
func referencePath(root string, account int64, chat, message string) string {
	return filepath.Join(mediafiles.Directory(root, account, chat), mediafiles.Prefix(message)+".ref")
}

// RemoveMediaReference removes only the exact hashed credential file.
func RemoveMediaReference(mediaRoot string, account int64, chat, message string) error {
	if mediaRoot == "" {
		return nil
	}
	err := os.Remove(referencePath(ReferenceRoot(mediaRoot), account, chat, message))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func (c *Client) saveMediaReference(ctx context.Context, job mediaJob) error {
	root := ReferenceRoot(c.mediaRoot)
	dir, err := mediafiles.Ensure(root, job.account, job.message.GroupID)
	if err != nil {
		return err
	}
	ref := mediaReference{job.account, job.message.GroupID, job.message.MessageID, job.message.Type, "", job.size, job.attachment.GetFileSHA256(), job.attachment.GetFileEncSHA256(), job.attachment.GetMediaKey(), job.attachment.GetDirectPath()}
	if v, ok := job.attachment.(interface{ GetMimetype() string }); ok {
		ref.MIME = v.GetMimetype()
	}
	data, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("attachment reference is too large")
	}
	c.noteMediaWrite()
	temp, err := os.CreateTemp(dir, ".reference-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Close(); err != nil {
		return err
	}
	temp, err = localfs.OpenFile(name)
	if err != nil {
		return err
	}
	_, err = temp.Write(data)
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	path := referencePath(root, ref.Account, ref.Chat, ref.Message)
	if err = os.Rename(name, path); err != nil {
		return err
	}
	retained, err := c.capture.store.ReferenceRetained(ctx, ref.Account, ref.Chat, ref.Message)
	if err != nil || !retained {
		_ = os.Remove(path)
	}
	return err
}
func readMediaReference(path string) (mediaReference, error) {
	var ref mediaReference
	if err := localfs.CheckPrivateFile(path); err != nil {
		return ref, err
	}
	file, err := os.Open(path)
	if err != nil {
		return ref, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return ref, err
	}
	if len(data) > 1<<20 {
		return ref, fmt.Errorf("invalid reference")
	}
	err = json.Unmarshal(data, &ref)
	return ref, err
}
func (ref mediaReference) job() (mediaJob, error) {
	if ref.Size < 0 || ref.Size > int64(^uint64(0)>>1)-64 || len(ref.Hash) != 32 {
		return mediaJob{}, fmt.Errorf("invalid attachment reference")
	}
	var attachment whatsmeow.DownloadableMessage
	switch ref.Kind {
	case "image":
		attachment = &waE2E.ImageMessage{Mimetype: proto.String(ref.MIME), FileLength: proto.Uint64(uint64(ref.Size)), FileSHA256: ref.Hash, FileEncSHA256: ref.EncryptedHash, MediaKey: ref.Key, DirectPath: proto.String(ref.Path)}
	case "document":
		attachment = &waE2E.DocumentMessage{Mimetype: proto.String(ref.MIME), FileLength: proto.Uint64(uint64(ref.Size)), FileSHA256: ref.Hash, FileEncSHA256: ref.EncryptedHash, MediaKey: ref.Key, DirectPath: proto.String(ref.Path)}
	case "video":
		attachment = &waE2E.VideoMessage{Mimetype: proto.String(ref.MIME), FileLength: proto.Uint64(uint64(ref.Size)), FileSHA256: ref.Hash, FileEncSHA256: ref.EncryptedHash, MediaKey: ref.Key, DirectPath: proto.String(ref.Path)}
	case "audio":
		attachment = &waE2E.AudioMessage{Mimetype: proto.String(ref.MIME), FileLength: proto.Uint64(uint64(ref.Size)), FileSHA256: ref.Hash, FileEncSHA256: ref.EncryptedHash, MediaKey: ref.Key, DirectPath: proto.String(ref.Path)}
	case "sticker":
		attachment = &waE2E.StickerMessage{Mimetype: proto.String(ref.MIME), FileLength: proto.Uint64(uint64(ref.Size)), FileSHA256: ref.Hash, FileEncSHA256: ref.EncryptedHash, MediaKey: ref.Key, DirectPath: proto.String(ref.Path)}
	default:
		return mediaJob{}, fmt.Errorf("unsupported attachment")
	}
	return mediaJob{account: ref.Account, message: storage.Message{GroupID: ref.Chat, MessageID: ref.Message, Type: ref.Kind}, attachment: attachment, extension: mediaExtension(ref.MIME), size: ref.Size}, nil
}

// sweepMediaReferences removes download keys whose message is gone. It returns
// how many reference files it found, counting an incomplete walk as one so the
// caller never takes it for an empty folder.
func (c *Client) sweepMediaReferences(ctx context.Context) int {
	root := ReferenceRoot(c.mediaRoot)
	incomplete := false
	seen := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			incomplete = true
			return nil
		}
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(entry.Name(), ".reference-") {
			seen++
			info, e := entry.Info()
			if e != nil {
				incomplete = true
				return nil
			}
			if time.Since(info.ModTime()) > 2*time.Minute {
				if e := os.Remove(path); e != nil {
					incomplete = true
				}
				return nil
			}
			return nil
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".ref") {
			return nil
		}
		if !managedReferencePath(root, path) {
			return nil
		}
		seen++
		ref, err := readMediaReference(path)
		if err != nil || path != referencePath(root, ref.Account, ref.Chat, ref.Message) {
			if e := os.Remove(path); e != nil {
				incomplete = true
			}
			return nil
		}
		retained, err := c.capture.store.ReferenceRetained(ctx, ref.Account, ref.Chat, ref.Message)
		if err != nil {
			incomplete = true
			return nil
		}
		if !retained {
			if e := os.Remove(path); e != nil {
				incomplete = true
			}
		}
		return nil
	})
	if (err != nil || incomplete) && ctx.Err() == nil {
		c.log.Warn("media reference cleanup incomplete; retrying later")
	}
	if err != nil || incomplete {
		seen++
	}
	return seen
}

func managedReferencePath(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 {
		return false
	}
	account, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || account <= 0 {
		return false
	}
	name := strings.TrimSuffix(parts[2], ".ref")
	if len(parts[1]) != 64 || len(name) != 64 || name == parts[2] {
		return false
	}
	_, err = hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	_, err = hex.DecodeString(name)
	return err == nil
}
