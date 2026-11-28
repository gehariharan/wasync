package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
)

func gifOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := gif.Encode(&out, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestProbeImageReadsJPEGAndPNG(t *testing.T) {
	jpg := jpegOf(t, 120, 80)
	info, err := ProbeImage(bytes.NewReader(jpg), int64(len(jpg)))
	if err != nil {
		t.Fatal(err)
	}
	if info != (ImageInfo{MIME: "image/jpeg", Width: 120, Height: 80}) {
		t.Fatalf("%+v", info)
	}
	pngData := pngOf(t, 64, 96)
	info, err = ProbeImage(bytes.NewReader(pngData), int64(len(pngData)))
	if err != nil {
		t.Fatal(err)
	}
	if info != (ImageInfo{MIME: "image/png", Width: 64, Height: 96}) {
		t.Fatalf("%+v", info)
	}
}

func TestProbeImageRefusesUnsupportedAndMalformedInputs(t *testing.T) {
	good := jpegOf(t, 64, 48)
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
	for name, data := range map[string][]byte{
		"empty":          nil,
		"text":           []byte("this is not an image"),
		"gif":            gifOf(t, 32, 32),
		"webp":           webp,
		"mp4":            videoFixture(t, "h264-aac.mp4"),
		"truncated jpeg": []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10},
		"truncated png":  []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00},
	} {
		if _, err := ProbeImage(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// The size is refused before anything is read.
	if _, err := ProbeImage(bytes.NewReader(good), MaxImageBytes+1); err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("oversized image: %v", err)
	}
}

func TestSendInputValidatesImage(t *testing.T) {
	ok := []SendInput{
		{To: "one@g.us", File: "/tmp/a.png", Type: "image"},
		{To: "one@g.us", File: "/tmp/a.jpg", Type: "image", Text: "caption"},
		{To: "one@g.us", Image: []byte{1}, Type: "image"},
		{To: "one@g.us", Image: []byte{1}, Type: "image", Text: "caption"},
	}
	for _, in := range ok {
		if err := in.Validate(); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
	bad := map[string]SendInput{
		"file and image bytes":  {To: "one@g.us", File: "/tmp/a.png", Image: []byte{1}, Type: "image"},
		"image and video bytes": {To: "one@g.us", Image: []byte{1}, Video: []byte{1}, Type: "image"},
		"bytes as a document":   {To: "one@g.us", Image: []byte{1}, Type: "document"},
		"bytes as a video":      {To: "one@g.us", Image: []byte{1}, Type: "video"},
		"bytes with no type":    {To: "one@g.us", Image: []byte{1}},
		"type without file":     {To: "one@g.us", Text: "hi", Type: "image"},
		"frame on image bytes":  {To: "one@g.us", Image: []byte{1}, Type: "image", Thumbnail: []byte{1}},
		"image bytes too long":  {To: "one@g.us", Image: make([]byte, MaxImageBytes+1), Type: "image"},
	}
	for name, in := range bad {
		if err := in.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSendImageArrivesAsAnImageAndIsArchived(t *testing.T) {
	ctx := context.Background()
	jpg := jpegOf(t, 120, 80)
	pngData := pngOf(t, 64, 96)
	path := filepath.Join(t.TempDir(), "compare.jpg")
	if err := os.WriteFile(path, jpg, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		in      SendInput
		data    []byte
		mime    string
		width   uint32
		height  uint32
		caption string
	}{
		"file jpeg":       {SendInput{To: "123@g.us", File: path, Type: "image", Text: "Compare screen"}, jpg, "image/jpeg", 0, 0, "Compare screen"},
		"bytes jpeg":      {SendInput{To: "123@g.us", Image: jpg, Type: "image", Text: "Compare screen"}, jpg, "image/jpeg", 120, 80, "Compare screen"},
		"bytes png":       {SendInput{To: "123@g.us", Image: pngData, Type: "image", Text: "Compare screen"}, pngData, "image/png", 64, 96, "Compare screen"},
		"uncaptioned png": {SendInput{To: "123@g.us", Image: pngData, Type: "image"}, pngData, "image/png", 64, 96, ""},
	} {
		t.Run(name, func(t *testing.T) {
			c, s, account := historyClient(t)
			wa := &fakeWhatsApp{}
			res, err := c.send(ctx, wa, ownJID, tc.in)
			if err != nil || res.Status != "server_acknowledged" || res.ArchiveWarning != "" {
				t.Fatalf("%+v %v", res, err)
			}
			if wa.mediaType != whatsmeow.MediaImage || !bytes.Equal(wa.uploaded, tc.data) {
				t.Fatalf("uploaded %q, %d bytes", wa.mediaType, len(wa.uploaded))
			}
			img := wa.sent.GetImageMessage()
			if img == nil || wa.sent.GetDocumentMessage() != nil || wa.sent.GetVideoMessage() != nil {
				t.Fatalf("not sent as an image message: %v", wa.sent)
			}
			if img.GetMimetype() != tc.mime || img.GetWidth() != tc.width || img.GetHeight() != tc.height || img.GetCaption() != tc.caption || img.GetFileLength() != uint64(len(tc.data)) || img.GetDirectPath() == "" || len(img.GetMediaKey()) == 0 {
				t.Fatalf("%v", img)
			}
			if tc.caption == "" && img.Caption != nil {
				t.Fatalf("empty caption should be omitted")
			}
			m, err := s.MessageByWhatsAppID(ctx, account, "123@g.us", "VIDEO1")
			if err != nil || m.Type != "image" || m.Text != tc.caption || !m.FromMe {
				t.Fatalf("archived %+v %v", m, err)
			}
			var meta struct {
				MIME   string `json:"mime"`
				Width  uint32 `json:"width"`
				Height uint32 `json:"height"`
			}
			if err = json.Unmarshal([]byte(m.Media), &meta); err != nil || meta.MIME != tc.mime || meta.Width != tc.width || meta.Height != tc.height {
				t.Fatalf("archived media %s %v", m.Media, err)
			}
		})
	}
}

func TestSendImageRefusalsSendNothing(t *testing.T) {
	c, _, _ := historyClient(t)
	ctx := context.Background()
	for name, in := range map[string]SendInput{
		"gif":            {To: "123@g.us", Image: gifOf(t, 32, 32), Type: "image"},
		"plain text":     {To: "123@g.us", Image: []byte("not an image"), Type: "image"},
		"truncated jpeg": {To: "123@g.us", Image: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}, Type: "image"},
	} {
		wa := &fakeWhatsApp{}
		res, err := c.send(ctx, wa, ownJID, in)
		if err == nil || res.Status != "not_sent" || wa.uploaded != nil || wa.sent != nil {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
	}
	wa := &fakeWhatsApp{uploadErr: errors.New("network")}
	res, err := c.send(ctx, wa, ownJID, SendInput{To: "123@g.us", Image: pngOf(t, 32, 32), Type: "image"})
	if err == nil || res.Status != "not_sent" || wa.sent != nil {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSendImageAsDocumentStaysADocument(t *testing.T) {
	c, _, _ := historyClient(t)
	path := filepath.Join(t.TempDir(), "compare.png")
	if err := os.WriteFile(path, pngOf(t, 64, 48), 0o600); err != nil {
		t.Fatal(err)
	}
	wa := &fakeWhatsApp{}
	if _, err := c.send(context.Background(), wa, ownJID, SendInput{To: "123@g.us", File: path, Type: "document"}); err != nil {
		t.Fatal(err)
	}
	if wa.mediaType != whatsmeow.MediaDocument || wa.sent.GetDocumentMessage().GetFileName() != "compare.png" {
		t.Fatalf("%q %v", wa.mediaType, wa.sent)
	}
}

func TestSendImageFileKeepsTheOwnerPathUnchecked(t *testing.T) {
	c, _, _ := historyClient(t)
	truncated := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
	path := filepath.Join(t.TempDir(), "partial.jpg")
	if err := os.WriteFile(path, truncated, 0o600); err != nil {
		t.Fatal(err)
	}
	wa := &fakeWhatsApp{}
	if _, err := c.send(context.Background(), wa, ownJID, SendInput{To: "123@g.us", File: path, Type: "image"}); err != nil {
		t.Fatal(err)
	}
	img := wa.sent.GetImageMessage()
	if wa.mediaType != whatsmeow.MediaImage || img == nil || img.Caption == nil || img.Width != nil || img.Height != nil || !bytes.Equal(wa.uploaded, truncated) {
		t.Fatalf("%q %v", wa.mediaType, wa.sent)
	}
	gifPath := filepath.Join(t.TempDir(), "a.gif")
	if err := os.WriteFile(gifPath, gifOf(t, 8, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	wa = &fakeWhatsApp{}
	if _, err := c.send(context.Background(), wa, ownJID, SendInput{To: "123@g.us", File: gifPath, Type: "image"}); err == nil || wa.sent != nil {
		t.Fatalf("gif file sent as an image: %v", err)
	}
}
