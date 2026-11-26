package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func videoBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "whatsapp", "testdata", "video", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func videoBody(t *testing.T, target map[string]any, fields map[string]any) string {
	t.Helper()
	body := map[string]any{}
	for k, v := range target {
		body[k] = v
	}
	for k, v := range fields {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestV1SendCarriesAVideoToAnAllowedChat(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	video := videoBytes(t, "h264-aac.mp4")
	var thumb bytes.Buffer
	if err := jpeg.Encode(&thumb, image.NewGray(image.Rect(0, 0, 64, 36)), nil); err != nil {
		t.Fatal(err)
	}
	body := videoBody(t, map[string]any{"chat": "one@g.us"}, map[string]any{"text": "Our explainer", "video": video, "thumbnail": thumb.Bytes()})
	if status, data := f.send(t, "POST", "/api/v1/messages", agent, body); status != 200 {
		t.Fatalf("video send: %d %s", status, data)
	}
	if rec.count() != 1 {
		t.Fatalf("sends: %d", rec.count())
	}
	in := rec.calls[0]
	if in.Type != "video" || !bytes.Equal(in.Video, video) || !bytes.Equal(in.Thumbnail, thumb.Bytes()) || in.Text != "Our explainer" || in.File != "" {
		t.Fatalf("what reached WhatsApp: type %q, %d video bytes, file %q", in.Type, len(in.Video), in.File)
	}
	// A caption is optional.
	body = videoBody(t, map[string]any{"chat": "one@g.us"}, map[string]any{"video": video})
	if status, data := f.send(t, "POST", "/api/v1/messages", agent, body); status != 200 {
		t.Fatalf("uncaptioned video: %d %s", status, data)
	}
	if entries := f.sentEntries(t); len(entries) != 2 || entries[0].Actor != "poster" {
		t.Fatalf("sent log: %+v", entries)
	}
}

func TestV1VideoSendsAreAuthorizedExactlyLikeText(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	narrow := f.grantWriter(t, "narrow", []string{agentauth.ScopeSend}, 0, "one@g.us")
	reactOnly := f.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, 0, "one@g.us")
	video := map[string]any{"video": videoBytes(t, "h264-aac.mp4")}
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
		status, _ := f.send(t, "POST", "/api/v1/messages", tc.token, videoBody(t, tc.target, video))
		if status != 401 && status != 403 {
			t.Fatalf("%s: %d", tc.name, status)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("a refused video reached WhatsApp: %d", rec.count())
	}
}

func TestV1VideoToAListedNumberIsPacedLikeText(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000")
	video := videoBytes(t, "h264-aac.mp4")
	// An unplayable video is refused before it uses the number's one message.
	status, data := f.send(t, "POST", "/api/v1/messages", agent, videoBody(t, map[string]any{"to": "15551230000"}, map[string]any{"video": videoBytes(t, "hevc.mp4")}))
	wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	if !strings.Contains(string(data), "H.264") {
		t.Fatalf("the refusal should say what plays: %s", data)
	}
	if status, data = f.send(t, "POST", "/api/v1/messages", agent, videoBody(t, map[string]any{"to": "15551230000"}, map[string]any{"text": "hi", "video": video})); status != 200 {
		t.Fatalf("first video: %d %s", status, data)
	}
	status, data = f.send(t, "POST", "/api/v1/messages", agent, videoBody(t, map[string]any{"to": "15551230000"}, map[string]any{"video": video}))
	wantV1Error(t, status, data, 429, query.CodeRateLimited)
	entries := f.sentEntries(t)
	if rec.count() != 1 || len(entries) != 1 || !entries[0].List || !entries[0].NewNumber {
		t.Fatalf("list pacing: %d sends, %+v", rec.count(), entries)
	}
}

func TestV1SendRefusesBadVideoRequests(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	video := videoBytes(t, "h264-aac.mp4")
	for name, fields := range map[string]map[string]any{
		"not a video":         {"video": []byte("plain text, not an mp4")},
		"quicktime":           {"video": videoBytes(t, "quicktime.mov")},
		"mp3 sound":           {"video": videoBytes(t, "mp3-audio.mp4")},
		"thumbnail not image": {"video": video, "thumbnail": []byte("nope")},
		"thumbnail too wide":  {"video": video, "thumbnail": jpegFrame(t, 321, 180)},
		"thumbnail alone":     {"text": "hi", "thumbnail": []byte("nope")},
		"a file path":         {"video": video, "file": "/etc/passwd"},
		"a type":              {"video": video, "type": "document"},
	} {
		t.Run(name, func(t *testing.T) {
			status, data := f.send(t, "POST", "/api/v1/messages", agent, videoBody(t, map[string]any{"chat": "one@g.us"}, fields))
			wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
		})
	}
	// Bytes that are not base64 fail strict decoding.
	status, data := f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"one@g.us","video":"!!not base64!!"}`)
	wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	if rec.count() != 0 {
		t.Fatalf("a bad video reached WhatsApp: %d", rec.count())
	}
}

func TestV1SendRefusesABodyOverTheVideoLimit(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	// Base64 of more than the largest video: the target cannot be read within
	// the limit, so it fails closed.
	huge := `{"chat":"one@g.us","video":"` + strings.Repeat("A", int(maxSendBody)) + `"}`
	status, data := f.send(t, "POST", "/api/v1/messages", agent, huge)
	wantV1Error(t, status, data, 403, query.CodeForbidden)
	// Just over the video limit inside the body limit is refused as too large.
	over := base64.StdEncoding.EncodeToString(make([]byte, whatsapp.MaxVideoBytes+1))
	status, data = f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"one@g.us","video":"`+over+`"}`)
	wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	if rec.count() != 0 {
		t.Fatalf("an oversized video reached WhatsApp: %d", rec.count())
	}
}

func TestV1SendAdvertisesVideo(t *testing.T) {
	f := setupSend(t, &sendRecorder{})
	status, body := f.send(t, "GET", "/api/v1/version", f.agent, "")
	if status != 200 || !strings.Contains(string(body), `"write.send.video"`) {
		t.Fatalf("version: %d %s", status, body)
	}
}

func jpegFrame(t *testing.T, w, h int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := jpeg.Encode(&out, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// countingBody is a request body of n bytes that records how many were read.
type countingBody struct{ left, read int64 }

func (b *countingBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.left)
	for i := range p[:n] {
		p[i] = 'A'
	}
	b.left -= n
	b.read += n
	return int(n), nil
}

func TestV1SendReadsAVideoSizedBodyOnlyForASendCredential(t *testing.T) {
	f := setupSend(t, &sendRecorder{})
	poster := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	reactOnly := f.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, 0, "one@g.us")
	h := &handler{dataDir: f.dir, agents: agentauth.NewAuthorizer(f.dir)}
	const smallLimit = 128 << 10
	for _, tc := range []struct {
		name, token string
		status      int
		readsAll    bool
	}{
		{"no credential", "", 401, false},
		{"a credential without the send scope", reactOnly, 403, false},
		{"a send credential", poster, 403, true},
	} {
		body := &countingBody{left: maxSendBody}
		req := httptest.NewRequest("POST", "/api/v1/messages", io.NopCloser(body))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		rec := httptest.NewRecorder()
		if h.authorizeSend(rec, req, "r1") {
			t.Fatalf("%s: a body naming no chat was allowed", tc.name)
		}
		if rec.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
		if tc.readsAll && body.read != maxSendBody || !tc.readsAll && body.read > smallLimit+1 {
			t.Fatalf("%s: read %d bytes of the body", tc.name, body.read)
		}
	}
}
