package api

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/gif"
	"image/png"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func pngFrame(t *testing.T, w, h int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func gifFrame(t *testing.T, w, h int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := gif.Encode(&out, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestV1SendCarriesAnImageToAnAllowedChat(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	jpg := jpegFrame(t, 120, 80)
	body := videoBody(t, map[string]any{"chat": "one@g.us"}, map[string]any{"text": "Our compare screen", "image": jpg})
	if status, data := f.send(t, "POST", "/api/v1/messages", agent, body); status != 200 {
		t.Fatalf("image send: %d %s", status, data)
	}
	if rec.count() != 1 {
		t.Fatalf("sends: %d", rec.count())
	}
	in := rec.calls[0]
	if in.Type != "image" || !bytes.Equal(in.Image, jpg) || len(in.Video) != 0 || in.Text != "Our compare screen" || in.File != "" {
		t.Fatalf("what reached WhatsApp: type %q, %d image bytes, file %q", in.Type, len(in.Image), in.File)
	}
	// PNG and optional caption.
	pngImg := pngFrame(t, 64, 48)
	body = videoBody(t, map[string]any{"chat": "one@g.us"}, map[string]any{"image": pngImg})
	if status, data := f.send(t, "POST", "/api/v1/messages", agent, body); status != 200 {
		t.Fatalf("uncaptioned png: %d %s", status, data)
	}
	in = rec.calls[1]
	if in.Type != "image" || !bytes.Equal(in.Image, pngImg) || in.Text != "" {
		t.Fatalf("what reached WhatsApp for png: type %q, %d image bytes, text %q", in.Type, len(in.Image), in.Text)
	}
	if entries := f.sentEntries(t); len(entries) != 2 || entries[0].Actor != "poster" {
		t.Fatalf("sent log: %+v", entries)
	}
}

func TestV1ImageSendsAreAuthorizedExactlyLikeText(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	narrow := f.grantWriter(t, "narrow", []string{agentauth.ScopeSend}, 0, "one@g.us")
	reactOnly := f.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, 0, "one@g.us")
	img := map[string]any{"image": jpegFrame(t, 64, 48)}
	for _, tc := range []struct {
		name, token string
		target      map[string]any
	}{
		{"no credential", "", map[string]any{"chat": "one@g.us"}},
		{"legacy api token", f.token, map[string]any{"chat": "one@g.us"}},
		{"read-only agent", f.agent, map[string]any{"chat": "one@g.us"}},
		{"other write scope", reactOnly, map[string]any{"chat": "one@g.us"}},
		{"chat outside the allowlist", narrow, map[string]any{"chat": "two@g.us"}},
		{"a number not on a list", narrow, map[string]any{"to": "+15551230000"}},
	} {
		status, _ := f.send(t, "POST", "/api/v1/messages", tc.token, videoBody(t, tc.target, img))
		if status != 401 && status != 403 {
			t.Fatalf("%s: %d", tc.name, status)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("a refused image reached WhatsApp: %d", rec.count())
	}
}

func TestV1ImageToAListedNumberIsPacedLikeText(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000")
	img := pngFrame(t, 80, 60)
	// An unsupported image is refused before it uses the number's one message.
	status, data := f.send(t, "POST", "/api/v1/messages", agent, videoBody(t, map[string]any{"to": "15551230000"}, map[string]any{"image": gifFrame(t, 32, 32)}))
	wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	if !strings.Contains(string(data), "JPEG and PNG") {
		t.Fatalf("the refusal should say what formats work: %s", data)
	}
	if status, data = f.send(t, "POST", "/api/v1/messages", agent, videoBody(t, map[string]any{"to": "15551230000"}, map[string]any{"text": "hi", "image": img})); status != 200 {
		t.Fatalf("first image: %d %s", status, data)
	}
	status, data = f.send(t, "POST", "/api/v1/messages", agent, videoBody(t, map[string]any{"to": "15551230000"}, map[string]any{"image": img}))
	wantV1Error(t, status, data, 429, query.CodeRateLimited)
	entries := f.sentEntries(t)
	if rec.count() != 1 || len(entries) != 1 || !entries[0].List || !entries[0].NewNumber {
		t.Fatalf("list pacing: %d sends, %+v", rec.count(), entries)
	}
}

func TestV1SendRefusesBadImageRequests(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	img := jpegFrame(t, 64, 48)
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
	for name, fields := range map[string]map[string]any{
		"not an image":     {"image": []byte("plain text, not an image")},
		"gif":              {"image": gifFrame(t, 32, 32)},
		"webp":             {"image": webp},
		"truncated jpeg":   {"image": []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}},
		"truncated png":    {"image": []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00}},
		"image and video":  {"image": img, "video": videoBytes(t, "h264-aac.mp4")},
		"image with thumb": {"image": img, "thumbnail": jpegFrame(t, 32, 18)},
		"a file path":      {"image": img, "file": "/etc/passwd"},
		"a type":           {"image": img, "type": "document"},
	} {
		t.Run(name, func(t *testing.T) {
			status, data := f.send(t, "POST", "/api/v1/messages", agent, videoBody(t, map[string]any{"chat": "one@g.us"}, fields))
			wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
		})
	}
	// Bytes that are not base64 fail strict decoding.
	status, data := f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"one@g.us","image":"!!not base64!!"}`)
	wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	if rec.count() != 0 {
		t.Fatalf("a bad image reached WhatsApp: %d", rec.count())
	}
}

func TestV1SendRefusesAnImageOverTheLimit(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	// Just over the image limit inside the body limit is refused as too large.
	over := base64.StdEncoding.EncodeToString(make([]byte, whatsapp.MaxImageBytes+1))
	status, data := f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"one@g.us","image":"`+over+`"}`)
	wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	if !strings.Contains(string(data), "16 MiB") {
		t.Fatalf("oversized refusal should mention 16 MiB: %s", data)
	}
	if rec.count() != 0 {
		t.Fatalf("an oversized image reached WhatsApp: %d", rec.count())
	}
}

func TestV1SendAdvertisesImage(t *testing.T) {
	f := setupSend(t, &sendRecorder{})
	status, body := f.send(t, "GET", "/api/v1/version", f.agent, "")
	if status != 200 || !strings.Contains(string(body), `"write.send.image"`) {
		t.Fatalf("version: %d %s", status, body)
	}
}
