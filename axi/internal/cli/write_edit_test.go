package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/testutil"
)

const editEndpoint = "/messages/" + testRef + "/edit"

func TestEditPostsOnceAndPrintsAcknowledgement(t *testing.T) {
	h, srv := writerHarness(t)
	srv.Routes[editEndpoint] = ackEnvelope("server_acknowledged")
	out, code := h.run(t, "edit", testRef, "--text", "Meet at 7")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "transport: api", "edit:", "target: "+testRef, "status: server_acknowledged", "not proof of delivery")
	n, body := posts(srv)
	var sent map[string]string
	if n != 1 || json.Unmarshal([]byte(body), &sent) != nil || sent["text"] != "Meet at 7" || len(sent) != 1 {
		t.Fatalf("posts %d body %s", n, body)
	}
	if srv.Requests[len(srv.Requests)-1] != "/api/v1"+editEndpoint {
		t.Fatalf("requests %v", srv.Requests)
	}
	out, code = h.run(t, "edit", testRef, "--text", "again", "--json")
	var js struct {
		Transport string          `json:"transport"`
		Data      json.RawMessage `json:"data"`
	}
	if code != ExitOK || json.Unmarshal([]byte(out), &js) != nil || js.Transport != "api" || !strings.Contains(string(js.Data), `"target_message_id":"a"`) {
		t.Fatalf("json %d %s", code, out)
	}
}

// whatsapp-axi checks neither ownership nor the edit window: wasync decides
// and says why, and the refusal is printed unchanged.
func TestEditReportsWasyncRefusalsWithoutRetrying(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		help   string
	}{
		{"missing scope", 403, "forbidden", "Missing scope edit.own. A human must re-grant the client in an interactive terminal: wasync agents grant writer --scope edit.own"},
		{"chat not allowed", 403, "forbidden", "Chat one@g.us is not in the allowlist for scope edit.own."},
		{"rate limited", 429, "rate_limited", "Retry after 3s."},
		{"unknown message", 404, "not_found", "it may have expired"},
		{"someone else's message", 409, "not_editable", "only your own text messages can be edited"},
		{"window passed", 409, "edit_window_expired", "send a new message instead; an expired edit cannot be retried"},
		{"unknown outcome", 502, "send_failed", "do not retry automatically"},
		{"owner offline", 503, "owner_offline", "start wasync tui or wasync serve"},
	} {
		h, srv := writerHarness(t)
		srv.Routes[editEndpoint] = map[string]any{"request_id": "r-x", "error_code": tc.code, "error": "refused: " + tc.name, "help": tc.help}
		srv.Codes[editEndpoint] = tc.status
		out, code := h.run(t, "edit", testRef, "--text", "x")
		if code != ExitError {
			t.Errorf("%s: exit %d\n%s", tc.name, code, out)
		}
		mustContain(t, out, tc.code, "refused: "+tc.name, tc.help)
		if n, _ := posts(srv); n != 1 {
			t.Errorf("%s: %d posts; a refused or unknown write must not be retried", tc.name, n)
		}
	}
}

func TestEditNeedsEditScopeCredentialHelpAndNeverWritesOffline(t *testing.T) {
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	out, code := h.run(t, "edit", testRef, "--text", "x")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "credential_missing", "--scope read.chats,read.messages,edit.own --chat <chat-jid>")
	if n, _ := posts(srv); n != 0 {
		t.Fatalf("posted without a credential: %d", n)
	}

	offline := newHarness(t)
	bin, log := testutil.FakeWasync(t, testutil.ModeOK)
	offline.env["WASYNC_BIN"] = bin
	offline.env["WHATSAPP_AXI_TOKEN"] = "wsa_writer"
	out, code = offline.run(t, "edit", testRef, "--text", "x")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "owner_offline", "needs the running wasync API")
	for _, call := range testutil.ReadFakeCalls(t, log) {
		if strings.Contains(strings.Join(call.Args, " "), "edit") {
			t.Fatalf("offline edit reached wasync query: %v", call.Args)
		}
	}

	h, srv = writerHarness(t)
	v := testutil.FakeVersion()
	v.Features = []string{"message.ref", "write.reply", "write.react"}
	srv.Version = v
	srv.Routes[editEndpoint] = ackEnvelope("server_acknowledged")
	out, code = h.run(t, "edit", testRef, "--text", "x")
	mustContain(t, out, "write.edit")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if n, _ := posts(srv); n != 0 {
		t.Fatalf("sent to a wasync without write.edit: %d", n)
	}
}

func TestEditUsageErrors(t *testing.T) {
	h, srv := writerHarness(t)
	for _, args := range [][]string{
		{"edit"},
		{"edit", testRef},
		{"edit", "12345", "--text", "x"},
		{"edit", testRef, "--text"},
		{"edit", testRef, "--text", "x", "--bogus"},
		{"edit", testRef, "extra", "--text", "x"},
	} {
		out, code := h.run(t, args...)
		if code != ExitUsage {
			t.Errorf("%v: exit %d\n%s", args, code, out)
		}
	}
	if n, _ := posts(srv); n != 0 {
		t.Fatalf("usage errors reached the API: %d", n)
	}
}
