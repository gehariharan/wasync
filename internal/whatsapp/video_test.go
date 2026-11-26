package whatsapp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// The fixtures are one- and two-second clips made with ffmpeg's test source.
func videoFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "video", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProbeMP4ReadsAPlayableVideo(t *testing.T) {
	data := videoFixture(t, "h264-aac.mp4")
	info, err := ProbeMP4(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if info != (VideoInfo{Seconds: 2, Width: 96, Height: 54, Audio: true}) {
		t.Fatalf("%+v", info)
	}
	// A phone recording turned a quarter turn reports its displayed shape.
	data = videoFixture(t, "rotated.mp4")
	if info, err = ProbeMP4(bytes.NewReader(data), int64(len(data))); err != nil || info.Width != 54 || info.Height != 96 || info.Audio {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestProbeMP4RefusesWhatWhatsAppWouldNotPlay(t *testing.T) {
	for name, want := range map[string]string{
		"hevc.mp4":      `"hvc1" codec`,
		"mp3-audio.mp4": `"mp4a" format`,
		"quicktime.mov": "MP4 files with H.264",
	} {
		data := videoFixture(t, name)
		if _, err := ProbeMP4(bytes.NewReader(data), int64(len(data))); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	good := videoFixture(t, "h264-aac.mp4")
	for name, data := range map[string][]byte{
		"empty":     nil,
		"text":      []byte("this is not a video at all"),
		"jpeg":      append([]byte{0xff, 0xd8, 0xff, 0xe0}, make([]byte, 64)...),
		"truncated": good[:len(good)/3],
		"bad size":  append(binary.BigEndian.AppendUint32(nil, 1<<30), good[4:]...),
	} {
		if _, err := ProbeMP4(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// The size is refused before anything is read.
	if _, err := ProbeMP4(bytes.NewReader(good), MaxVideoBytes+1); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Fatal(err)
	}
}

func TestProbeMP4StopsOnEndlessBoxes(t *testing.T) {
	data := videoFixture(t, "h264-aac.mp4")[:32] // keep ftyp
	ftyp := int(binary.BigEndian.Uint32(data[:4]))
	data = data[:ftyp]
	for range maxMP4Boxes + 10 {
		data = append(data, 0, 0, 0, 8, 'f', 'r', 'e', 'e')
	}
	if _, err := ProbeMP4(bytes.NewReader(data), int64(len(data))); err == nil {
		t.Fatal("a file of empty boxes was accepted")
	}
}

func TestVideoThumbnailIsASmallJPEG(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for i := range big.Pix {
		big.Pix[i] = 0xc0
	}
	var src bytes.Buffer
	if err := png.Encode(&src, big); err != nil {
		t.Fatal(err)
	}
	thumb, err := VideoThumbnail(src.Bytes(), VideoInfo{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(thumb))
	if err != nil || cfg.Width != 320 || cfg.Height != 180 {
		t.Fatalf("%+v %v", cfg, err)
	}
	// Without an image the message still gets a preview of the video's shape.
	thumb, err = VideoThumbnail(nil, VideoInfo{Width: 54, Height: 96})
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err = jpeg.DecodeConfig(bytes.NewReader(thumb)); err != nil || cfg.Width != 54 || cfg.Height != 96 {
		t.Fatalf("%+v %v", cfg, err)
	}
	for name, img := range map[string][]byte{
		"not an image": []byte("hello"),
		"too large":    make([]byte, MaxThumbnailBytes+1),
		"too wide":     pngOf(t, thumbnailMaxIn+1, 1),
	} {
		if _, err := VideoThumbnail(img, VideoInfo{}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.White)
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestVideoFrameUsesFFmpegWhenInstalled(t *testing.T) {
	path := filepath.Join("testdata", "video", "h264-aac.mp4")
	frame, err := VideoFrame(context.Background(), path)
	if errors.Is(err, ErrNoFFmpeg) {
		t.Skip("ffmpeg is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(frame)); err != nil || cfg.Width != 96 {
		t.Fatalf("%+v %v", cfg, err)
	}
	old := findFFmpeg
	t.Cleanup(func() { findFFmpeg = old })
	findFFmpeg = func() (string, error) { return "", ErrNoFFmpeg }
	if _, err = VideoFrame(context.Background(), path); !errors.Is(err, ErrNoFFmpeg) {
		t.Fatal(err)
	}
}

func TestSendInputValidatesVideo(t *testing.T) {
	ok := []SendInput{
		{To: "one@g.us", File: "/tmp/a.mp4", Type: "video"},
		{To: "one@g.us", File: "/tmp/a.mp4", Type: "video", Text: "caption", Thumbnail: []byte{1}},
		{To: "one@g.us", Video: []byte{1}, Type: "video"},
	}
	for _, in := range ok {
		if err := in.Validate(); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
	bad := map[string]SendInput{
		"unknown type":         {To: "one@g.us", File: "/tmp/a", Type: "gif"},
		"file and bytes":       {To: "one@g.us", File: "/tmp/a.mp4", Video: []byte{1}, Type: "video"},
		"bytes as a document":  {To: "one@g.us", Video: []byte{1}, Type: "document"},
		"bytes with no type":   {To: "one@g.us", Video: []byte{1}},
		"type without file":    {To: "one@g.us", Text: "hi", Type: "video"},
		"thumbnail on image":   {To: "one@g.us", File: "/tmp/a.png", Type: "image", Thumbnail: []byte{1}},
		"thumbnail too large":  {To: "one@g.us", File: "/tmp/a.mp4", Type: "video", Thumbnail: make([]byte, MaxThumbnailBytes+1)},
		"video bytes too long": {To: "one@g.us", Video: make([]byte, MaxVideoBytes+1), Type: "video"},
	}
	for name, in := range bad {
		if err := in.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// fakeWhatsApp stands in for the WhatsApp connection: it keeps what was
// uploaded and sent and acknowledges both.
type fakeWhatsApp struct {
	uploaded  []byte
	mediaType whatsmeow.MediaType
	sent      *waE2E.Message
	to        types.JID
	uploadErr error
}

func (f *fakeWhatsApp) UploadReader(_ context.Context, plaintext io.Reader, _ io.ReadWriteSeeker, appInfo whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	if f.uploadErr != nil {
		return whatsmeow.UploadResponse{}, f.uploadErr
	}
	data, err := io.ReadAll(plaintext)
	if err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	f.uploaded, f.mediaType = data, appInfo
	return whatsmeow.UploadResponse{URL: "https://mmg.example/v", DirectPath: "/v/t62/abc", MediaKey: []byte("key"), FileEncSHA256: []byte("enc"), FileSHA256: []byte("sha"), FileLength: uint64(len(data))}, nil
}

func (f *fakeWhatsApp) GenerateMessageID() types.MessageID { return "VIDEO1" }

func (f *fakeWhatsApp) SendMessage(_ context.Context, to types.JID, message *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.to, f.sent = to, message
	return whatsmeow.SendResponse{Timestamp: time.Now()}, nil
}

func TestSendVideoArrivesAsAPlayableVideoAndIsArchived(t *testing.T) {
	c, s, account := historyClient(t)
	ctx := context.Background()
	data := videoFixture(t, "h264-aac.mp4")
	path := filepath.Join(t.TempDir(), "explainer.mp4")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	thumb := pngOf(t, 640, 360)
	for name, in := range map[string]SendInput{
		"file":  {To: "123@g.us", File: path, Type: "video", Text: "Watch this", Thumbnail: thumb},
		"bytes": {To: "123@g.us", Video: data, Type: "video", Text: "Watch this"},
	} {
		wa := &fakeWhatsApp{}
		res, err := c.send(ctx, wa, ownJID, in)
		if err != nil || res.Status != "server_acknowledged" || res.ArchiveWarning != "" {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		if wa.mediaType != whatsmeow.MediaVideo || !bytes.Equal(wa.uploaded, data) {
			t.Fatalf("%s: uploaded %q, %d bytes", name, wa.mediaType, len(wa.uploaded))
		}
		v := wa.sent.GetVideoMessage()
		if v == nil || wa.sent.GetDocumentMessage() != nil {
			t.Fatalf("%s: not sent as a video message: %v", name, wa.sent)
		}
		if v.GetMimetype() != "video/mp4" || v.GetSeconds() != 2 || v.GetWidth() != 96 || v.GetHeight() != 54 || v.GetCaption() != "Watch this" || v.GetFileLength() != uint64(len(data)) || v.GetDirectPath() == "" || len(v.GetMediaKey()) == 0 {
			t.Fatalf("%s: %v", name, v)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(v.GetJPEGThumbnail()))
		if err != nil || cfg.Width == 0 {
			t.Fatalf("%s: thumbnail %+v %v", name, cfg, err)
		}
		m, err := s.MessageByWhatsAppID(ctx, account, "123@g.us", "VIDEO1")
		if err != nil || m.Type != "video" || m.Text != "Watch this" || !m.FromMe {
			t.Fatalf("%s: archived %+v %v", name, m, err)
		}
		var meta struct {
			MIME     string `json:"mime"`
			Duration uint32 `json:"duration_seconds"`
			Width    uint32 `json:"width"`
		}
		if err = json.Unmarshal([]byte(m.Media), &meta); err != nil || meta.MIME != "video/mp4" || meta.Duration != 2 || meta.Width != 96 {
			t.Fatalf("%s: archived media %s %v", name, m.Media, err)
		}
	}
}

func TestSendVideoRefusalsSendNothing(t *testing.T) {
	c, _, _ := historyClient(t)
	ctx := context.Background()
	hevc := videoFixture(t, "hevc.mp4")
	for name, in := range map[string]SendInput{
		"hevc":          {To: "123@g.us", Video: hevc, Type: "video"},
		"bad thumbnail": {To: "123@g.us", Video: videoFixture(t, "h264-aac.mp4"), Type: "video", Thumbnail: []byte("not an image")},
	} {
		wa := &fakeWhatsApp{}
		res, err := c.send(ctx, wa, ownJID, in)
		if err == nil || res.Status != "not_sent" || wa.uploaded != nil || wa.sent != nil {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
	}
	wa := &fakeWhatsApp{uploadErr: errors.New("network")}
	res, err := c.send(ctx, wa, ownJID, SendInput{To: "123@g.us", Video: videoFixture(t, "h264-aac.mp4"), Type: "video"})
	if err == nil || res.Status != "not_sent" || wa.sent != nil {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSendVideoAsDocumentStaysADocument(t *testing.T) {
	c, _, _ := historyClient(t)
	path := filepath.Join(t.TempDir(), "explainer.mp4")
	if err := os.WriteFile(path, videoFixture(t, "h264-aac.mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	wa := &fakeWhatsApp{}
	if _, err := c.send(context.Background(), wa, ownJID, SendInput{To: "123@g.us", File: path, Type: "document"}); err != nil {
		t.Fatal(err)
	}
	if wa.mediaType != whatsmeow.MediaDocument || wa.sent.GetDocumentMessage().GetFileName() != "explainer.mp4" {
		t.Fatalf("%q %v", wa.mediaType, wa.sent)
	}
}
