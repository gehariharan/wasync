package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/testutil"
)

const testRef = "m1_dGVzdEBsaWQAb25lQGcudXMAYQ"
const replyEndpoint = "/messages/" + testRef + "/reply"

func writerHarness(t *testing.T) (*harness, *testutil.API) {
	t.Helper()
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	h.env["WASYNC_AXI_TOKEN"] = "wsa_writer"
	return h, srv
}

func ackEnvelope(status string) map[string]any {
	return map[string]any{"request_id": "r-w", "data": map[string]any{
		"chat_id": "one@g.us", "message_id": "NEW1", "target_message_id": "a", "status": status, "timestamp": "2026-10-01T12:00:00Z",
	}}
}

// posts returns the writes the fake API received, as method and body.
func posts(srv *testutil.API) (n int, last string) {
	for i, m := range srv.Methods {
		if m == "POST" {
			n++
			last = srv.Bodies[i]
		}
	}
	return n, last
}

func TestReplyPostsOnceAndPrintsAcknowledgement(t *testing.T) {
	h, srv := writerHarness(t)
	srv.Routes[replyEndpoint] = ackEnvelope("server_acknowledged")
	out, code := h.run(t, "reply", testRef, "--text", "On my way @123", "--mentions", "123@s.whatsapp.net, ,456@s.whatsapp.net")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "transport: api", "reply:", "target: "+testRef, "chat: one@g.us", "message_id: NEW1", "status: server_acknowledged", "not proof of delivery", "never retry automatically")
	n, body := posts(srv)
	if n != 1 {
		t.Fatalf("posts %d, requests %v", n, srv.Requests)
	}
	var sent struct {
		Text     string   `json:"text"`
		Mentions []string `json:"mentions"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil || sent.Text != "On my way @123" || len(sent.Mentions) != 2 || sent.Mentions[1] != "456@s.whatsapp.net" {
		t.Fatalf("body %s", body)
	}
	if got := srv.Auth[len(srv.Auth)-1]; got != "Bearer wsa_writer" {
		t.Fatalf("credential %q", got)
	}

	out, code = h.run(t, "reply", testRef, "--text", "again", "--json")
	var js struct {
		Transport string          `json:"transport"`
		Data      json.RawMessage `json:"data"`
	}
	if code != ExitOK || json.Unmarshal([]byte(out), &js) != nil || js.Transport != "api" || !strings.Contains(string(js.Data), `"message_id":"NEW1"`) {
		t.Fatalf("json %d %s", code, out)
	}
	_, body = posts(srv)
	if !strings.Contains(body, `"mentions":[]`) {
		t.Fatalf("empty mentions must be sent as a list: %s", body)
	}
}

// wasync-axi enforces no scope or allowlist itself: it sends the request
// and reports exactly what wasync answers.
func TestReplyReportsWasyncRefusalsWithoutRetrying(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		help   string
	}{
		{"missing scope", 403, "forbidden", "Missing scope send. A human must re-grant the client in an interactive terminal: wasync agents grant writer --scope send"},
		{"chat not allowed", 403, "forbidden", "Chat one@g.us is not in the allowlist for scope send."},
		{"rate limited", 429, "rate_limited", "Retry after 12s."},
		{"unknown message", 404, "not_found", "it may have expired, been purged, or belong to another account"},
		{"deleted target", 409, "target_unavailable", "choose a message that is still present in the conversation"},
		{"unknown outcome", 502, "send_failed", "check WhatsApp before retrying; do not retry automatically"},
		{"owner offline", 503, "owner_offline", "start wasync tui or wasync serve with a paired session"},
		{"bad argument", 400, "invalid_argument", ""},
	} {
		h, srv := writerHarness(t)
		srv.Routes[replyEndpoint] = map[string]any{"request_id": "r-x", "error_code": tc.code, "error": "refused: " + tc.name, "help": tc.help}
		srv.Codes[replyEndpoint] = tc.status
		out, code := h.run(t, "reply", testRef, "--text", "hello")
		if code != ExitError {
			t.Errorf("%s: exit %d\n%s", tc.name, code, out)
		}
		mustContain(t, out, tc.code, "refused: "+tc.name)
		if tc.help != "" {
			mustContain(t, out, tc.help)
		}
		if n, _ := posts(srv); n != 1 {
			t.Errorf("%s: %d posts; a refused or unknown write must not be retried", tc.name, n)
		}
		jsonOut, code := h.run(t, "reply", testRef, "--text", "hello", "--json")
		var e map[string]any
		if code != ExitError || json.Unmarshal([]byte(jsonOut), &e) != nil || e["error_code"] != tc.code {
			t.Errorf("%s json: %d %s", tc.name, code, jsonOut)
		}
	}
}

func TestReplyNeedsCredentialWithWriteScopeHelp(t *testing.T) {
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	out, code := h.run(t, "reply", testRef, "--text", "hello")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "credential_missing", "--scope read.chats,read.messages,send --synced-chats", "offers to save the credential")
	if n, _ := posts(srv); n != 0 {
		t.Fatalf("posted without a credential: %d", n)
	}
}

func TestReplyNeverWritesOffline(t *testing.T) {
	h := newHarness(t)
	bin, log := testutil.FakeWasync(t, testutil.ModeOK)
	h.env["WASYNC_BIN"] = bin
	h.env["WASYNC_AXI_TOKEN"] = "wsa_writer"
	out, code := h.run(t, "reply", testRef, "--text", "hello")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "owner_offline", "needs the running wasync API", "never starts it")
	for _, call := range testutil.ReadFakeCalls(t, log) {
		if strings.Contains(strings.Join(call.Args, " "), "reply") {
			t.Fatalf("offline reply reached wasync query: %v", call.Args)
		}
	}
}

func TestReplyRefusesIncompatibleWasyncWithoutPosting(t *testing.T) {
	h, srv := writerHarness(t)
	v := testutil.FakeVersion()
	v.Features = []string{"message.ref"}
	srv.Version = v
	srv.Routes[replyEndpoint] = ackEnvelope("server_acknowledged")
	out, code := h.run(t, "reply", testRef, "--text", "hello")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "write.reply")
	if n, _ := posts(srv); n != 0 {
		t.Fatalf("posted to a wasync without write.reply: %d", n)
	}
}

func TestReplyUsageErrors(t *testing.T) {
	h, srv := writerHarness(t)
	for _, args := range [][]string{
		{"reply"},
		{"reply", testRef},
		{"reply", "12345", "--text", "x"},
		{"reply", testRef, "--text"},
		{"reply", testRef, "--text", "x", "--bogus"},
		{"reply", testRef, "extra", "--text", "x"},
	} {
		out, code := h.run(t, args...)
		if code != ExitUsage {
			t.Errorf("%v: exit %d\n%s", args, code, out)
		}
	}
	if len(srv.Methods) != 0 {
		t.Fatalf("usage errors reached the API: %v", srv.Requests)
	}
}

func TestSendPostsANewMessageOnceAndPrintsAcknowledgement(t *testing.T) {
	h, srv := writerHarness(t)
	srv.Routes["/messages"] = ackEnvelope("server_acknowledged")
	out, code := h.run(t, "send", "--chat", " one@g.us ", "--text", "Rent sent today")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "transport: api", "send:", "target: one@g.us", "message_id: NEW1", "status: server_acknowledged", "never retry automatically")
	n, body := posts(srv)
	if n != 1 {
		t.Fatalf("posts %d, requests %v", n, srv.Requests)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil || sent["chat"] != "one@g.us" || sent["text"] != "Rent sent today" || len(sent) != 2 {
		t.Fatalf("body %s", body)
	}
	if got := srv.Auth[len(srv.Auth)-1]; got != "Bearer wsa_writer" {
		t.Fatalf("credential %q", got)
	}
	out, code = h.run(t, "send", "--chat", "one@g.us", "--text", "again", "--json")
	var js struct {
		Transport string          `json:"transport"`
		Data      json.RawMessage `json:"data"`
	}
	if code != ExitOK || json.Unmarshal([]byte(out), &js) != nil || js.Transport != "api" || !strings.Contains(string(js.Data), `"message_id":"NEW1"`) {
		t.Fatalf("json %d %s", code, out)
	}
}

func TestSendNeedsAChatAndTextAndSendsNothingWithoutThem(t *testing.T) {
	h, srv := writerHarness(t)
	for _, args := range [][]string{{"send", "--text", "hi"}, {"send", "--chat", "one@g.us"}, {"send", "--chat", "  ", "--text", "hi"}, {"send", "one@g.us", "--text", "hi"}} {
		if out, code := h.run(t, args...); code == ExitOK {
			t.Fatalf("%v succeeded:\n%s", args, out)
		}
	}
	if n, _ := posts(srv); n != 0 {
		t.Fatalf("an incomplete send reached the API: %d", n)
	}
}

// A refusal, including "this chat was never given to you", is reported as
// wasync words it and is never turned into a retry or another route.
func TestSendReportsWasyncRefusalsWithoutRetrying(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		help   string
	}{
		{"chat not allowed", 403, "forbidden", "Chat 15551230000@s.whatsapp.net is not in the allowlist for scope send."},
		{"missing scope", 403, "forbidden", "Missing scope send."},
		{"unknown outcome", 502, "send_failed", "check WhatsApp before retrying; do not retry automatically"},
		{"owner offline", 503, "owner_offline", "start wasync tui or wasync serve with a paired session"},
	} {
		h, srv := writerHarness(t)
		srv.Routes["/messages"] = map[string]any{"request_id": "r-x", "error_code": tc.code, "error": "refused: " + tc.name, "help": tc.help}
		srv.Codes["/messages"] = tc.status
		out, code := h.run(t, "send", "--chat", "15551230000@s.whatsapp.net", "--text", "hello")
		if code != ExitError {
			t.Errorf("%s: exit %d\n%s", tc.name, code, out)
		}
		mustContain(t, out, tc.code, "refused: "+tc.name, tc.help)
		if hint := strings.Contains(out, "wasync-axi send-request (--chat JID | --to PHONE) --text TEXT"); hint != (tc.code == "forbidden") {
			t.Errorf("%s: send-request hint shown %v\n%s", tc.name, hint, out)
		}
		if n, _ := posts(srv); n != 1 {
			t.Errorf("%s: %d posts; a refused or unknown write must not be retried", tc.name, n)
		}
	}

	// wasync's own help already names the send request: it is not repeated.
	h, srv := writerHarness(t)
	own := "Chat a@g.us is not synced. Ask the person to approve this message with `wasync-axi send-request --chat a@g.us --text TEXT`."
	srv.Routes["/messages"] = map[string]any{"request_id": "r-x", "error_code": "forbidden", "error": "refused", "help": own}
	srv.Codes["/messages"] = 403
	out, _ := h.run(t, "send", "--chat", "a@g.us", "--text", "hello")
	if !strings.Contains(out, own) || strings.Count(out, "send-request") != 1 {
		t.Fatalf("help changed or repeated:\n%s", out)
	}
}

func TestReplyAndReactRefusalsSayToSyncTheChat(t *testing.T) {
	h, srv := writerHarness(t)
	reaction := "/messages/" + testRef + "/reaction"
	for _, ep := range []string{replyEndpoint, reaction} {
		srv.Routes[ep] = map[string]any{"request_id": "r-x", "error_code": "forbidden", "error": "refused", "help": "Chat one@g.us is not allowed for scope send."}
		srv.Codes[ep] = 403
	}
	for _, args := range [][]string{{"reply", testRef, "--text", "hi"}, {"react", testRef, "👍"}} {
		out, code := h.run(t, args...)
		if code != ExitError {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
		mustContain(t, out, "Chat one@g.us is not allowed for scope send. Reading, replying and reacting work in every chat synced in wasync", "ask the person to sync it in wasync")
		if strings.Contains(out, "send-request") {
			t.Fatalf("%v: a reply is never a send request:\n%s", args, out)
		}
	}
}

func TestSendToANumberPostsTheNumberAndNeedsTheNewFeature(t *testing.T) {
	h, srv := writerHarness(t)
	srv.Routes["/messages"] = ackEnvelope("server_acknowledged")
	out, code := h.run(t, "send", "--to", " +1 555 123 0000 ", "--text", "Hi, it is Sam")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "send:", "target: +1 555 123 0000", "status: server_acknowledged")
	n, body := posts(srv)
	var sent map[string]any
	if n != 1 || json.Unmarshal([]byte(body), &sent) != nil || sent["to"] != "+1 555 123 0000" || sent["text"] != "Hi, it is Sam" || len(sent) != 2 {
		t.Fatalf("posts %d body %s", n, body)
	}
	for _, args := range [][]string{
		{"send", "--chat", "one@g.us", "--to", "+15551230000", "--text", "hi"},
		{"send", "--to", " ", "--text", "hi"},
		{"send", "--to", "+15551230000"},
	} {
		if out, code := h.run(t, args...); code == ExitOK {
			t.Fatalf("%v succeeded:\n%s", args, out)
		}
	}
	if n, _ := posts(srv); n != 1 {
		t.Fatalf("an invalid send reached the API: %d posts", n)
	}

	// A wasync that cannot send to a number is told so before anything is posted.
	old, oldSrv := writerHarness(t)
	v := testutil.FakeVersion()
	v.Features = []string{"write.send"}
	oldSrv.Version = v
	oldSrv.Routes["/messages"] = ackEnvelope("server_acknowledged")
	out, code = old.run(t, "send", "--to", "+15551230000", "--text", "hi")
	if code == ExitOK {
		t.Fatalf("send --to succeeded against a wasync without write.send.to:\n%s", out)
	}
	mustContain(t, out, "write.send.to")
	if n, _ := posts(oldSrv); n != 0 {
		t.Fatalf("posted to a wasync without write.send.to: %d", n)
	}
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeFFmpegEnv makes the test binary print its value and exit, as ffmpeg
// writing a preview frame to stdout would.
const fakeFFmpegEnv = "WASYNC_AXI_TEST_FAKE_FFMPEG_OUTPUT"

func noFFmpeg(t *testing.T) {
	t.Helper()
	old := findFFmpeg
	t.Cleanup(func() { findFFmpeg = old })
	findFFmpeg = func() (string, error) { return "", errNoFFmpeg }
}

func TestSendVideoPostsItsBytesCaptionAndPreviewOnce(t *testing.T) {
	fake, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeFFmpegEnv, "synthetic jpeg")
	old := findFFmpeg
	t.Cleanup(func() { findFFmpeg = old })
	findFFmpeg = func() (string, error) { return fake, nil }
	h, srv := writerHarness(t)
	srv.Routes["/messages"] = ackEnvelope("server_acknowledged")
	video := writeTemp(t, "explainer.mp4", []byte("synthetic mp4 bytes"))
	out, code := h.run(t, "send", "--to", "+15551230000", "--video", video, "--text", "A quick look")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "send:", "status: server_acknowledged")
	n, body := posts(srv)
	var sent map[string]any
	if n != 1 || json.Unmarshal([]byte(body), &sent) != nil {
		t.Fatalf("posts %d body %s", n, body)
	}
	if sent["to"] != "+15551230000" || sent["text"] != "A quick look" || sent["video"] != base64.StdEncoding.EncodeToString([]byte("synthetic mp4 bytes")) || sent["thumbnail"] != base64.StdEncoding.EncodeToString([]byte("synthetic jpeg")) || len(sent) != 4 {
		t.Fatalf("body %s", body)
	}
	if strings.Contains(body, "explainer.mp4") {
		t.Fatalf("the path reached wasync: %s", body)
	}
}

func TestSendVideoWithoutFFmpegSendsNoPreviewAndSaysSo(t *testing.T) {
	noFFmpeg(t)
	h, srv := writerHarness(t)
	srv.Routes["/messages"] = ackEnvelope("server_acknowledged")
	out, code := h.run(t, "send", "--chat", "one@g.us", "--video", writeTemp(t, "v.mp4", []byte("mp4")))
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "plain preview")
	_, body := posts(srv)
	var sent map[string]any
	if json.Unmarshal([]byte(body), &sent) != nil || sent["chat"] != "one@g.us" || sent["thumbnail"] != nil || sent["text"] != nil || sent["video"] == nil {
		t.Fatalf("body %s", body)
	}
}

func TestSendVideoTakesAPreviewFrameWithFFmpeg(t *testing.T) {
	ffmpeg, err := findFFmpeg()
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=96x54:rate=5", "-t", "1", "-pix_fmt", "yuv420p", clip).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot make a test clip: %v %s", err, out)
	}
	h, srv := writerHarness(t)
	srv.Routes["/messages"] = ackEnvelope("server_acknowledged")
	if out, code := h.run(t, "send", "--chat", "one@g.us", "--video", clip); code != ExitOK || strings.Contains(out, "plain preview") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	_, body := posts(srv)
	var sent struct {
		Thumbnail []byte `json:"thumbnail"`
	}
	if json.Unmarshal([]byte(body), &sent) != nil {
		t.Fatal(body)
	}
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(sent.Thumbnail)); err != nil || cfg.Width != 96 {
		t.Fatalf("preview %+v %v", cfg, err)
	}
}

func TestSendVideoUsageAndLimitsPostNothing(t *testing.T) {
	noFFmpeg(t)
	h, srv := writerHarness(t)
	srv.Routes["/messages"] = ackEnvelope("server_acknowledged")
	video := writeTemp(t, "v.mp4", []byte("mp4"))
	big := filepath.Join(t.TempDir(), "big.mp4")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(maxVideoBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, args := range [][]string{
		{"send", "--chat", "one@g.us", "--thumbnail", video, "--text", "hi"},
		{"send", "--chat", "one@g.us", "--video", filepath.Join(t.TempDir(), "absent.mp4")},
		{"send", "--chat", "one@g.us", "--video", t.TempDir()},
		{"send", "--chat", "one@g.us", "--video", big},
		{"send", "--chat", "one@g.us", "--video", " "},
		{"send", "--chat", "one@g.us"},
	} {
		if out, code := h.run(t, args...); code == ExitOK {
			t.Fatalf("%v succeeded:\n%s", args, out)
		}
	}
	if n, _ := posts(srv); n != 0 {
		t.Fatalf("an invalid video send reached the API: %d posts", n)
	}
	// A wasync that cannot send video is told so before anything is posted.
	old, oldSrv := writerHarness(t)
	v := testutil.FakeVersion()
	v.Features = []string{"write.send", "write.send.to"}
	oldSrv.Version = v
	oldSrv.Routes["/messages"] = ackEnvelope("server_acknowledged")
	out, code := old.run(t, "send", "--to", "+15551230000", "--video", video)
	if code == ExitOK {
		t.Fatalf("send --video succeeded against a wasync without write.send.video:\n%s", out)
	}
	mustContain(t, out, "write.send.video")
	if n, _ := posts(oldSrv); n != 0 {
		t.Fatalf("posted to a wasync without write.send.video: %d", n)
	}
}
