package cli

import (
	"encoding/json"
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
	h.env["WHATSAPP_AXI_TOKEN"] = "wsa_writer"
	return h, srv
}

func ack(status string) map[string]any {
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
	srv.Routes[replyEndpoint] = ack("server_acknowledged")
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

// whatsapp-axi enforces no scope or allowlist itself: it sends the request
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
	mustContain(t, out, "credential_missing", "--scope read.chats,read.messages,send --chat <chat-jid>")
	if n, _ := posts(srv); n != 0 {
		t.Fatalf("posted without a credential: %d", n)
	}
}

func TestReplyNeverWritesOffline(t *testing.T) {
	h := newHarness(t)
	bin, log := testutil.FakeWasync(t, testutil.ModeOK)
	h.env["WASYNC_BIN"] = bin
	h.env["WHATSAPP_AXI_TOKEN"] = "wsa_writer"
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
	srv.Routes[replyEndpoint] = ack("server_acknowledged")
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
