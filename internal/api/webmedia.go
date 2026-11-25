package api

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/mediafiles"
	"github.com/gehariharan/wasync/internal/storage"
)

// Attachments on the chats page. The archive always keeps an attachment's
// details (type, size, name, pixel size, length); the file exists only once it
// was downloaded into the private media folder. There are no thumbnails, so an
// attachment that is not downloaded shows its details and a Download button,
// and a downloaded photo, video or voice note shows in the chat. Files are
// served only to the signed-in page, from the media folder, by message.

// chatAttachment is what a chat bubble shows for one attachment.
type chatAttachment struct {
	Kind, Icon, Label, Name, Meta string
	// State is ready, pending (queued or downloading), failed or none.
	State string
	// Show says how a ready file appears: img, video, audio or file.
	Show  string
	URL   string
	Ratio template.CSS
	MsgID string
}

// mediaShown maps a downloaded file's extension to how it may be shown inline
// and the content type it is served with. Anything else is offered as a
// download only.
var mediaShown = map[string]struct{ show, ctype string }{
	".jpg":  {"img", "image/jpeg"},
	".png":  {"img", "image/png"},
	".webp": {"img", "image/webp"},
	".gif":  {"img", "image/gif"},
	".mp4":  {"video", "video/mp4"},
	".webm": {"video", "video/webm"},
	".3gp":  {"video", "video/3gpp"},
	".ogg":  {"audio", "audio/ogg"},
	".mp3":  {"audio", "audio/mpeg"},
	".m4a":  {"audio", "audio/mp4"},
	".aac":  {"audio", "audio/aac"},
	".wav":  {"audio", "audio/wav"},
	".pdf":  {"file", "application/pdf"},
}

type mediaMeta struct {
	MIME      string `json:"mime"`
	Filename  string `json:"filename"`
	Title     string `json:"title"`
	Size      uint64 `json:"size"`
	Width     uint32 `json:"width"`
	Height    uint32 `json:"height"`
	Duration  uint32 `json:"duration_seconds"`
	VoiceNote bool   `json:"voice_note"`
	Lottie    bool   `json:"lottie"`
	Pages     uint32 `json:"pages"`
	State     string `json:"download_state"`
	Relative  string `json:"relative_path"`
}

func attachmentKind(t string) bool {
	switch t {
	case "image", "video", "document", "audio", "sticker":
		return true
	}
	return false
}

// attachmentFor describes a message's attachment, or nil when it has none.
func attachmentFor(chat string, m storage.Message) *chatAttachment {
	if !attachmentKind(m.Type) || m.DeletedAt != nil {
		return nil
	}
	var meta mediaMeta
	_ = json.Unmarshal([]byte(m.Media), &meta)
	a := &chatAttachment{Kind: m.Type, MsgID: m.MessageID, State: "none"}
	var parts []string
	switch m.Type {
	case "image":
		a.Icon, a.Label = "🖼", "Photo"
	case "video":
		a.Icon, a.Label = "🎬", "Video"
	case "sticker":
		a.Icon, a.Label = "🙂", "Sticker"
	case "audio":
		a.Icon, a.Label = "🎵", "Audio"
		if meta.VoiceNote {
			a.Icon, a.Label = "🎤", "Voice note"
		}
	case "document":
		a.Icon, a.Label = "📄", "Document"
		a.Name = humancheck.Clean(meta.Filename, 80)
		if a.Name == "" {
			a.Name = humancheck.Clean(meta.Title, 80)
		}
		if ext := docType(meta.Filename, meta.MIME); ext != "" {
			parts = append(parts, ext)
		}
		if meta.Pages > 0 {
			parts = append(parts, plural(int(meta.Pages), "page"))
		}
	}
	if a.Name == "" {
		a.Name = a.Label
	}
	if meta.Duration > 0 {
		parts = append(parts, fmt.Sprintf("%d:%02d", meta.Duration/60, meta.Duration%60))
	}
	if meta.Width > 0 && meta.Height > 0 && (m.Type == "image" || m.Type == "video") {
		parts = append(parts, fmt.Sprintf("%d×%d", meta.Width, meta.Height))
		a.Ratio = template.CSS(fmt.Sprintf("%d/%d", meta.Width, meta.Height))
	}
	if meta.Size > 0 {
		parts = append(parts, byteSize(meta.Size))
	}
	a.Meta = strings.Join(parts, " · ")
	switch meta.State {
	case "downloaded":
		a.State = "ready"
		a.Show = "file"
		if s, ok := mediaShown[strings.ToLower(path.Ext(meta.Relative))]; ok {
			a.Show = s.show
		}
		if m.Type == "document" {
			a.Show = "file"
		}
		a.URL = "/ui/media?" + url.Values{"c": {chat}, "m": {m.MessageID}}.Encode()
	case "queued", "downloading":
		a.State = "pending"
	case "failed", "queue_full", "invalid_metadata", "interrupted":
		a.State = "failed"
	}
	if meta.Lottie {
		a.State = "none"
	}
	return a
}

func docType(name, mime string) string {
	if ext := strings.TrimPrefix(path.Ext(name), "."); ext != "" && len(ext) <= 5 {
		return strings.ToUpper(humancheck.Clean(ext, 5))
	}
	if _, sub, ok := strings.Cut(mime, "/"); ok && len(sub) <= 5 {
		return strings.ToUpper(humancheck.Clean(sub, 5))
	}
	return ""
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func byteSize(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// mediaFile serves one downloaded attachment to the signed-in page. Only files
// inside wasync's private media folder, named the way wasync names them, are
// served; nothing a sender chose is used as a path.
func (h *handler) mediaFile(w http.ResponseWriter, r *http.Request, sess uiSession) {
	chat, msgID := r.URL.Query().Get("c"), r.URL.Query().Get("m")
	notFound := func() { http.Error(w, "not downloaded", http.StatusNotFound) }
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil || chat == "" || msgID == "" {
		notFound()
		return
	}
	m, err := h.store.MessageByWhatsAppID(r.Context(), account.ID, chat, msgID)
	if err != nil || m.DeletedAt != nil || !attachmentKind(m.Type) {
		notFound()
		return
	}
	var meta mediaMeta
	if json.Unmarshal([]byte(m.Media), &meta) != nil || meta.State != "downloaded" || meta.Relative == "" {
		notFound()
		return
	}
	root := filepath.Join(h.dataDir, "media")
	file := filepath.Join(root, filepath.FromSlash(meta.Relative))
	if !mediafiles.ManagedPath(root, file) || localfs.CheckPrivateFile(file) != nil {
		notFound()
		return
	}
	f, err := os.Open(file)
	if err != nil {
		notFound()
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		notFound()
		return
	}
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; frame-ancestors 'self'; sandbox")
	ext := strings.ToLower(filepath.Ext(file))
	if s, ok := mediaShown[ext]; ok {
		hd.Set("Content-Type", s.ctype)
		hd.Set("Content-Disposition", "inline")
		if ext == ".pdf" {
			// The browser's PDF viewer does not run in a sandboxed document.
			hd.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'self'")
		}
	} else {
		hd.Set("Content-Type", "application/octet-stream")
		hd.Set("Content-Disposition", "attachment; filename=\"attachment"+ext+"\"")
	}
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// chatsMedia downloads one attachment, or sets whether a chat's attachments
// download by themselves, the same as `wasync media download` and
// `wasync media policy`.
func (h *handler) chatsMedia(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiFormLimit(w, r, sess, 4096) {
		return
	}
	chat := strings.TrimSpace(r.PostForm.Get("chat"))
	back := func(notice string) {
		http.Redirect(w, r, "/ui/chats?"+url.Values{"c": {chat}, "n": {notice}}.Encode(), http.StatusSeeOther)
	}
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil || chat == "" {
		http.Error(w, "archive unavailable", http.StatusInternalServerError)
		return
	}
	switch r.PostForm.Get("action") {
	case "download":
		if h.mediaDownload == nil {
			back("dlfail")
			return
		}
		if _, err := h.mediaDownload(r.Context(), chat, r.PostForm.Get("msg")); err != nil {
			back("dlfail")
			return
		}
		back("dlqueued")
	case "policy":
		if err := h.store.SetMediaPolicy(r.Context(), account.ID, chat, r.PostForm.Get("mode")); err != nil {
			back("failed")
			return
		}
		back("dlpolicy")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}
