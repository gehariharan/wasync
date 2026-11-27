package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/testutil"
)

const (
	sendRequestEndpoint = "/send-requests"
	sendRequestID       = "0123456789abcdef0123456789abcdef"
)

func pendingSendRequest() map[string]any {
	return map[string]any{"request_id": "r-s", "data": map[string]any{
		"request_id": sendRequestID, "status": "pending", "chat": "15551230000@s.whatsapp.net", "new_number": true,
		"expires_at": "2026-11-26T12:30:00Z", "approve_with": "wasync actions approve " + sendRequestID,
	}}
}

func TestSendRequestPostsOnceAndExplainsTheNextStep(t *testing.T) {
	h, srv := writerHarness(t)
	srv.Routes[sendRequestEndpoint] = pendingSendRequest()
	srv.Codes[sendRequestEndpoint] = 202
	out, code := h.run(t, "send-request", "--to", " +15551230000 ", "--text", "Hi, it is Sam from the flat")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "transport: api", "send_request:", "request_id: "+sendRequestID, "status: pending",
		"chat: 15551230000@s.whatsapp.net", "new_number: true", `expires_at: "2026-11-26T12:30:00Z"`,
		"Nothing is sent yet", "Approvals page", "wasync actions approve "+sendRequestID,
		"wasync-axi send-request status "+sendRequestID, "do not send or request this message again", "expires after 30 minutes")
	n, body := posts(srv)
	var sent map[string]any
	if n != 1 || json.Unmarshal([]byte(body), &sent) != nil || sent["to"] != "+15551230000" || sent["text"] != "Hi, it is Sam from the flat" || len(sent) != 2 {
		t.Fatalf("posts %d body %s", n, body)
	}

	out, code = h.run(t, "send-request", "--chat", "one@g.us", "--text", "--json is text", "--json")
	var js struct {
		Transport string          `json:"transport"`
		Data      json.RawMessage `json:"data"`
	}
	if code != ExitOK || json.Unmarshal([]byte(out), &js) != nil || js.Transport != "api" || !strings.Contains(string(js.Data), sendRequestID) {
		t.Fatalf("json %d %s", code, out)
	}
	_, body = posts(srv)
	sent = nil
	if json.Unmarshal([]byte(body), &sent) != nil || sent["chat"] != "one@g.us" || sent["text"] != "--json is text" || len(sent) != 2 {
		t.Fatalf("body %s", body)
	}
}

func TestSendRequestAlreadyAllowedPointsAtSend(t *testing.T) {
	h, srv := writerHarness(t)
	srv.Routes[sendRequestEndpoint] = map[string]any{"request_id": "r", "data": map[string]any{"status": "already_allowed", "chat": "one@g.us"}}
	out, code := h.run(t, "send-request", "--chat", "one@g.us", "--text", "hi")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "status: already_allowed", "chat: one@g.us", "no approval is needed", "wasync-axi send --chat one@g.us --text TEXT")
	if strings.Contains(out, "request_id") {
		t.Fatalf("already_allowed has no request:\n%s", out)
	}
}

func TestSendRequestStatusReportsEachOutcome(t *testing.T) {
	for _, tc := range []struct {
		status string
		extra  map[string]any
		want   []string
		absent string
	}{
		{"pending", map[string]any{"expires_at": "2026-11-26T12:30:00Z"}, []string{"status: pending", "Nothing is sent yet", "wasync actions approve " + sendRequestID, "wasync-axi send-request status " + sendRequestID}, "message_id"},
		{"sent", map[string]any{"message_id": "3EB0ABC"}, []string{"status: sent", "message_id: 3EB0ABC", "sent it once (message_id 3EB0ABC)", "Do not send it again"}, "expires_at"},
		{"declined", nil, []string{"status: declined", "nothing was sent", "Do not ask again unless the person asks"}, "message_id"},
		{"expired", nil, []string{"status: expired", "Nothing was sent", "only if the message is still needed"}, "message_id"},
		{"failed", nil, []string{"status: failed", "Check WhatsApp before you ask again", "never resend automatically"}, "message_id"},
	} {
		h, srv := writerHarness(t)
		data := map[string]any{"request_id": sendRequestID, "status": tc.status, "chat": "one@g.us", "new_number": false}
		for k, v := range tc.extra {
			data[k] = v
		}
		srv.Routes[sendRequestEndpoint+"/"+sendRequestID] = map[string]any{"request_id": "r", "data": data}
		out, code := h.run(t, "send-request", "status", sendRequestID)
		if code != ExitOK {
			t.Fatalf("%s: exit %d\n%s", tc.status, code, out)
		}
		mustContain(t, out, append(tc.want, "request_id: "+sendRequestID, "chat: one@g.us", "new_number: false")...)
		if strings.Contains(out, tc.absent) {
			t.Errorf("%s: shows %s\n%s", tc.status, tc.absent, out)
		}
		if n, _ := posts(srv); n != 0 {
			t.Fatal("status wrote")
		}
		out, code = h.run(t, "send-request", "status", sendRequestID, "--json")
		if code != ExitOK || !strings.Contains(out, `"status":"`+tc.status+`"`) {
			t.Fatalf("%s json: %d %s", tc.status, code, out)
		}
	}
}

func TestSendRequestUsageErrorsAndWasyncRefusals(t *testing.T) {
	h, srv := writerHarness(t)
	for _, args := range [][]string{
		{"send-request"},
		{"send-request", "--text", "hi"},
		{"send-request", "--chat", "a@g.us"},
		{"send-request", "--chat", "a@g.us", "--text", ""},
		{"send-request", "--chat", " ", "--text", "hi"},
		{"send-request", "--chat", "a@g.us", "--to", "+15551230000", "--text", "hi"},
		{"send-request", "--chat", "a@g.us", "--text", "hi", "--video", "a.mp4"},
		{"send-request", "--match", "Mum", "--text", "hi"},
		{"send-request", "request", "--chat", "a@g.us", "--text", "hi"},
		{"send-request", "status"},
		{"send-request", "status", "short"},
		{"send-request", "status", sendRequestID, "--chat", "a@g.us"},
		{"send-request", "status", sendRequestID, "extra"},
	} {
		if out, code := h.run(t, args...); code != ExitUsage {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
	}
	if n, _ := posts(srv); n != 0 {
		t.Fatal("a usage error reached wasync")
	}
	for _, tc := range []struct {
		status int
		code   string
		help   string
	}{
		{429, "rate_limited", "wait for the person to answer the requests already waiting"},
		{503, "owner_offline", "start wasync"},
		{403, "forbidden", "Missing scope send."},
		{400, "invalid_argument", "text is longer than 4096 bytes"},
	} {
		srv.Routes[sendRequestEndpoint] = map[string]any{"request_id": "r", "error_code": tc.code, "error": "refused", "help": tc.help}
		srv.Codes[sendRequestEndpoint] = tc.status
		before, _ := posts(srv)
		out, code := h.run(t, "send-request", "--chat", "a@g.us", "--text", "hi")
		if code != ExitError {
			t.Fatalf("%s: exit %d\n%s", tc.code, code, out)
		}
		mustContain(t, out, tc.code, tc.help)
		if after, _ := posts(srv); after != before+1 {
			t.Fatalf("%s: %d posts; a refused request must not be retried", tc.code, after-before)
		}
	}
}

func TestSendRequestNeedsTheRunningAPIAndTheFeature(t *testing.T) {
	h := newHarness(t)
	bin, _ := testutil.FakeWasync(t, testutil.ModeOK)
	h.env["WASYNC_BIN"] = bin
	h.env["WASYNC_AXI_TOKEN"] = "wsa_writer"
	out, code := h.run(t, "send-request", "--chat", "a@g.us", "--text", "hi")
	if code != ExitError {
		t.Fatalf("offline: exit %d\n%s", code, out)
	}
	mustContain(t, out, "owner_offline", "needs the running wasync API")

	old, oldSrv := writerHarness(t)
	v := testutil.FakeVersion()
	v.Features = []string{"write.send"}
	oldSrv.Version = v
	oldSrv.Routes[sendRequestEndpoint] = pendingSendRequest()
	out, code = old.run(t, "send-request", "--chat", "a@g.us", "--text", "hi")
	if code == ExitOK {
		t.Fatalf("send-request succeeded against a wasync without write.send.request:\n%s", out)
	}
	mustContain(t, out, "write.send.request")
	if n, _ := posts(oldSrv); n != 0 {
		t.Fatalf("posted to a wasync without write.send.request: %d", n)
	}
}

func TestSendRequestNeedsCredentialWithSendScopeHelp(t *testing.T) {
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	out, code := h.run(t, "send-request", "--to", "+15551230000", "--text", "hi")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "credential_missing", "--scope read.chats,read.messages,send --synced-chats")
	if n, _ := posts(srv); n != 0 {
		t.Fatalf("posted without a credential: %d", n)
	}
}

// Per-chat access requests are gone; an old invocation says what replaced them.
func TestAccessIsRemovedWithAPointerToTheNewModel(t *testing.T) {
	h, srv := writerHarness(t)
	for _, args := range [][]string{{"access"}, {"access", "request", "--match", "Mum"}, {"access", "status", sendRequestID}} {
		out, code := h.run(t, args...)
		if code != ExitUsage {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
		mustContain(t, out, "`access` was removed", "every chat synced in wasync", "wasync-axi send-request (--chat JID | --to PHONE) --text TEXT", "ask the person to sync it in wasync")
	}
	out, code := h.run(t, "access", "request", "--json")
	var e map[string]any
	if code != ExitUsage || json.Unmarshal([]byte(out), &e) != nil || e["error_code"] != "usage" {
		t.Fatalf("json: %d %s", code, out)
	}
	if len(srv.Requests) != 0 {
		t.Fatalf("a removed command reached wasync: %v", srv.Requests)
	}
}
