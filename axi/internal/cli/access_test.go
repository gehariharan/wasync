package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	accessEndpoint = "/access-requests"
	accessID       = "0123456789abcdef0123456789abcdef"
)

func pendingEnvelope() map[string]any {
	return map[string]any{"request_id": "r-a", "data": map[string]any{
		"request_id": accessID, "status": "pending", "access": []string{"read", "send"},
		"expires_at": "2026-10-28T04:00:00Z", "approve_with": "wasync actions approve " + accessID,
	}}
}

func TestAccessRequestPostsOnceAndExplainsTheNextStep(t *testing.T) {
	h, srv := writerHarness(t)
	srv.Routes[accessEndpoint] = pendingEnvelope()
	srv.Codes[accessEndpoint] = 202
	out, code := h.run(t, "access", "request", "--match", "+15551234567", "--send")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "transport: api", "access_request:", "request_id: "+accessID, "status: pending", "access: read+send", "Nothing is granted yet", "wasync actions approve "+accessID, "whatsapp-axi access status "+accessID, "expires after five minutes")
	n, body := posts(srv)
	var sent map[string]any
	if n != 1 || json.Unmarshal([]byte(body), &sent) != nil || sent["match"] != "+15551234567" || sent["send"] != true || sent["chat"] != nil {
		t.Fatalf("posts %d body %s", n, body)
	}
	out, code = h.run(t, "access", "request", "--chat", "one@g.us", "--json")
	var js struct {
		Transport string          `json:"transport"`
		Data      json.RawMessage `json:"data"`
	}
	if code != ExitOK || json.Unmarshal([]byte(out), &js) != nil || js.Transport != "api" || !strings.Contains(string(js.Data), accessID) {
		t.Fatalf("json %d %s", code, out)
	}
	_, body = posts(srv)
	if !strings.Contains(body, `"chat":"one@g.us"`) || !strings.Contains(body, `"send":false`) {
		t.Fatalf("body %s", body)
	}
}

func TestAccessStatusReportsEachOutcome(t *testing.T) {
	for _, tc := range []struct {
		status string
		extra  map[string]any
		want   []string
	}{
		{"approved", map[string]any{"chat": "one@g.us", "history": "full"}, []string{"status: approved", "chat: one@g.us", "full history sync requested", "whatsapp-axi messages one@g.us"}},
		{"rejected", nil, []string{"status: rejected", "Do not ask again"}},
		{"expired", nil, []string{"status: expired", "No access was granted"}},
		{"pending", map[string]any{"approve_with": "wasync actions approve " + accessID}, []string{"status: pending", "Nothing is granted yet"}},
	} {
		h, srv := writerHarness(t)
		data := map[string]any{"request_id": accessID, "status": tc.status, "access": []string{"read"}}
		for k, v := range tc.extra {
			data[k] = v
		}
		srv.Routes[accessEndpoint+"/"+accessID] = map[string]any{"request_id": "r", "data": data}
		out, code := h.run(t, "access", "status", accessID)
		if code != ExitOK {
			t.Fatalf("%s: exit %d\n%s", tc.status, code, out)
		}
		mustContain(t, out, tc.want...)
		if n, _ := posts(srv); n != 0 {
			t.Fatal("status wrote")
		}
	}
}

func TestAccessUsageErrorsAndWasyncRefusals(t *testing.T) {
	h, srv := writerHarness(t)
	for _, args := range [][]string{{"access"}, {"access", "grant"}, {"access", "request"}, {"access", "request", "--chat", "a@g.us", "--match", "x"}, {"access", "status"}, {"access", "status", "short"}, {"access", "request", "extra", "--chat", "a@g.us"}} {
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
		{404, "not_found", "ask with the phone number"},
		{429, "rate_limited", "wait for the person"},
		{503, "owner_offline", "start wasync"},
		{403, "forbidden", "re-grant"},
	} {
		srv.Routes[accessEndpoint] = map[string]any{"request_id": "r", "error_code": tc.code, "error": "refused", "help": tc.help}
		srv.Codes[accessEndpoint] = tc.status
		out, code := h.run(t, "access", "request", "--match", "x")
		if code != ExitError {
			t.Fatalf("%s: exit %d\n%s", tc.code, code, out)
		}
		mustContain(t, out, tc.code, tc.help)
	}
}
