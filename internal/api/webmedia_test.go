package api

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/mediafiles"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

type mediaRecorder struct{ chat, msg string }

func (m *mediaRecorder) download(_ context.Context, chat, msg string) (whatsapp.MediaResult, error) {
	m.chat, m.msg = chat, msg
	return whatsapp.MediaResult{ChatID: chat, MessageID: msg, Status: "queued"}, nil
}

// mediaFixture has the family chat syncing with a photo, a document, a video
// and a voice note, behind a signed-in browser that can ask for downloads.
func mediaFixture(t *testing.T, rec *mediaRecorder) (accessFixture, *pageClient, string) {
	t.Helper()
	f := setupAccessOwner(t, nil, func(o *Owner) { o.Media = rec.download })
	ctx := context.Background()
	if err := f.store.SetEnabled(ctx, f.account, []string{familyChat}, true); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 5, 0, 0, time.UTC)
	if _, err := f.store.Apply(ctx, f.account, []storage.Message{
		{GroupID: familyChat, MessageID: "p1", SenderID: "alice@lid", Timestamp: at, Type: "image", Text: "Sunset", Media: `{"mime":"image/jpeg","size":1258291,"width":1600,"height":1200}`, Source: "live"},
		{GroupID: familyChat, MessageID: "d1", SenderID: "alice@lid", Timestamp: at.Add(time.Minute), Type: "document", Media: `{"mime":"application/pdf","filename":"Rental <b>agreement</b>.pdf","pages":12,"size":2516582}`, Source: "live"},
		{GroupID: familyChat, MessageID: "v1", SenderID: "alice@lid", Timestamp: at.Add(2 * time.Minute), Type: "video", Media: `{"mime":"video/mp4","size":8493465,"width":1280,"height":720,"duration_seconds":42}`, Source: "live"},
		{GroupID: familyChat, MessageID: "a1", SenderID: "alice@lid", Timestamp: at.Add(3 * time.Minute), Type: "audio", Media: `{"mime":"audio/ogg","size":34816,"duration_seconds":17,"voice_note":true}`, Source: "live"},
	}); err != nil {
		t.Fatal(err)
	}
	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	_, page, _ := c.get("/ui/chats/pick")
	return f, c, csrfOf(t, page)
}

// downloaded puts a file where wasync would have downloaded it and records it.
func downloaded(t *testing.T, f accessFixture, msg, ext, body string) {
	t.Helper()
	dir, err := mediafiles.Ensure(filepath.Join(f.dir, "media"), f.account, familyChat)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, mediafiles.Prefix(msg)+"-"+strings.Repeat("a", 64)+ext)
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.CompleteMediaDownload(context.Background(), f.account, familyChat, msg, file, false, false); err != nil || !ok {
		t.Fatalf("record download: %v %v", ok, err)
	}
}

func TestChatShowsAttachmentDetailsBeforeDownload(t *testing.T) {
	_, c, _ := mediaFixture(t, &mediaRecorder{})
	_, page, _ := c.get("/ui/chats?c=" + url.QueryEscape(familyChat))
	for _, want := range []string{"Photo · 1600×1200 · 1.2 MB", "Sunset", "Rental &lt;b&gt;agreement&lt;/b&gt;.pdf", "PDF · 12 pages · 2.4 MB", "Video · 0:42 · 1280×720 · 8.1 MB", "Voice note", "0:17 · 34 KB", `name="msg" value="p1"`, `name="msg" value="a1"`, `<option value="inherit" selected>`} {
		if !strings.Contains(page, want) {
			t.Fatalf("chat lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "/ui/media?") || strings.Contains(page, `<img class="media"`) {
		t.Fatalf("a file that was never downloaded was linked:\n%s", page)
	}
}

func TestDownloadButtonAndChatSettingAskTheRunningWasync(t *testing.T) {
	rec := &mediaRecorder{}
	f, c, csrf := mediaFixture(t, rec)
	code, _, hd := c.post("/ui/chats/media", url.Values{"csrf": {csrf}, "chat": {familyChat}, "msg": {"d1"}, "action": {"download"}})
	if code != 303 || notice(hd) != "dlqueued" || rec.chat != familyChat || rec.msg != "d1" {
		t.Fatalf("download: %d %q %+v", code, hd.Get("Location"), rec)
	}
	code, _, hd = c.post("/ui/chats/media", url.Values{"csrf": {csrf}, "chat": {familyChat}, "mode": {"auto"}, "action": {"policy"}})
	if mode, _ := f.store.MediaPolicy(context.Background(), f.account, familyChat); code != 303 || notice(hd) != "dlpolicy" || mode != "auto" {
		t.Fatalf("policy: %d %q %q", code, hd.Get("Location"), mode)
	}
	if code, _, _ = c.post("/ui/chats/media", url.Values{"chat": {familyChat}, "mode": {"manual"}, "action": {"policy"}}); code != 403 {
		t.Fatalf("a form without its token was accepted: %d", code)
	}
	if _, _, hd = c.post("/ui/chats/media", url.Values{"csrf": {csrf}, "chat": {familyChat}, "mode": {"everything"}, "action": {"policy"}}); notice(hd) != "failed" {
		t.Fatalf("a made-up setting: %q", hd.Get("Location"))
	}
}

func TestDownloadedAttachmentsShowInTheChatAndAreServedOnlyToThePage(t *testing.T) {
	f, c, _ := mediaFixture(t, &mediaRecorder{})
	downloaded(t, f, "p1", ".jpg", "synthetic jpeg bytes")
	downloaded(t, f, "d1", ".pdf", "%PDF synthetic")
	_, page, _ := c.get("/ui/chats?c=" + url.QueryEscape(familyChat))
	photo := "/ui/media?" + url.Values{"c": {familyChat}, "m": {"p1"}}.Encode()
	if !strings.Contains(page, `<img class="media" src="`+strings.ReplaceAll(photo, "&", "&amp;")+`"`) || !strings.Contains(page, `class="open"`) {
		t.Fatalf("downloaded files are not shown:\n%s", page)
	}
	code, body, hd := c.get(photo)
	if code != 200 || body != "synthetic jpeg bytes" || hd.Get("Content-Type") != "image/jpeg" || hd.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(hd.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("photo: %d %q %v", code, body, hd)
	}
	if code, _, _ = c.get("/ui/media?" + url.Values{"c": {familyChat}, "m": {"v1"}}.Encode()); code != 404 {
		t.Fatalf("a file that was never downloaded: %d", code)
	}
	if code, _, _ = newPageClient(t, f).get(photo); code != 401 {
		t.Fatalf("a file was served without a session: %d", code)
	}
}

func TestMediaFileRefusesPathsOutsideTheMediaFolder(t *testing.T) {
	f, c, _ := mediaFixture(t, &mediaRecorder{})
	outside := filepath.Join(f.dir, "archive-copy.jpg")
	if err := os.WriteFile(outside, []byte("not media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.CompleteMediaDownload(context.Background(), f.account, familyChat, "p1", outside, false, false); err != nil || !ok {
		t.Fatalf("record: %v %v", ok, err)
	}
	if code, body, _ := c.get("/ui/media?" + url.Values{"c": {familyChat}, "m": {"p1"}}.Encode()); code != 404 || strings.Contains(body, "not media") {
		t.Fatalf("a file outside the media folder was served: %d %q", code, body)
	}
}
