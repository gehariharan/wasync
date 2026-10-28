package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/testutil"
)

const reactionEndpoint = "/messages/" + testRef + "/reaction"

// mutations returns the PUT and DELETE requests the fake API received.
func mutations(srv *testutil.API) (methods, bodies []string) {
	for i, m := range srv.Methods {
		if m == "PUT" || m == "DELETE" {
			methods = append(methods, m)
			bodies = append(bodies, srv.Bodies[i])
		}
	}
	return methods, bodies
}

func TestReactSetsAndClearsWithOneRequestEach(t *testing.T) {
	h, srv := writerHarness(t)
	srv.Routes[reactionEndpoint] = ackEnvelope("server_acknowledged")
	out, code := h.run(t, "react", testRef, "👍")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "transport: api", "reaction:", "target: "+testRef, "status: server_acknowledged", "not proof of delivery")
	methods, bodies := mutations(srv)
	if len(methods) != 1 || methods[0] != "PUT" {
		t.Fatalf("requests %v", methods)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(bodies[0]), &sent); err != nil || sent["emoji"] != "👍" || len(sent) != 1 {
		t.Fatalf("body %s", bodies[0])
	}
	if got := srv.Auth[len(srv.Auth)-1]; got != "Bearer wsa_writer" {
		t.Fatalf("credential %q", got)
	}

	out, code = h.run(t, "react", testRef, "--remove", "--json")
	var js struct {
		Transport string          `json:"transport"`
		Data      json.RawMessage `json:"data"`
	}
	if code != ExitOK || json.Unmarshal([]byte(out), &js) != nil || js.Transport != "api" || !strings.Contains(string(js.Data), `"message_id":"NEW1"`) {
		t.Fatalf("remove %d %s", code, out)
	}
	methods, bodies = mutations(srv)
	if len(methods) != 2 || methods[1] != "DELETE" || bodies[1] != "" {
		t.Fatalf("requests %v bodies %q", methods, bodies)
	}
}

func TestReactReportsWasyncRefusalsWithoutRetrying(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		help   string
	}{
		{"missing scope", 403, "forbidden", "Missing scope react. A human must re-grant the client in an interactive terminal: wasync agents grant writer --scope react"},
		{"chat not allowed", 403, "forbidden", "Chat one@g.us is not in the allowlist for scope react."},
		{"rate limited", 429, "rate_limited", "Retry after 7s."},
		{"unknown message", 404, "not_found", "it may have expired"},
		{"deleted target", 409, "target_unavailable", "choose a message that is still present in the conversation"},
		{"unknown outcome", 502, "send_failed", "do not retry automatically"},
		{"owner offline", 503, "owner_offline", "start wasync tui or wasync serve"},
		{"bad emoji", 400, "invalid_argument", ""},
	} {
		for _, args := range [][]string{{"react", testRef, "👍"}, {"react", testRef, "--remove"}} {
			h, srv := writerHarness(t)
			srv.Routes[reactionEndpoint] = map[string]any{"request_id": "r-x", "error_code": tc.code, "error": "refused: " + tc.name, "help": tc.help}
			srv.Codes[reactionEndpoint] = tc.status
			out, code := h.run(t, args...)
			if code != ExitError {
				t.Errorf("%s %v: exit %d\n%s", tc.name, args, code, out)
			}
			mustContain(t, out, tc.code, "refused: "+tc.name)
			if tc.help != "" {
				mustContain(t, out, tc.help)
			}
			if methods, _ := mutations(srv); len(methods) != 1 {
				t.Errorf("%s %v: %d requests; a refused or unknown write must not be retried", tc.name, args, len(methods))
			}
		}
	}
}

func TestReactNeedsReactScopeCredentialHelp(t *testing.T) {
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	out, code := h.run(t, "react", testRef, "👍")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "credential_missing", "--scope read.chats,read.messages,react --chat <chat-jid>")
	if methods, _ := mutations(srv); len(methods) != 0 {
		t.Fatalf("sent without a credential: %v", methods)
	}
}

func TestReactNeverWritesOfflineOrToIncompatibleWasync(t *testing.T) {
	offline := newHarness(t)
	bin, log := testutil.FakeWasync(t, testutil.ModeOK)
	offline.env["WASYNC_BIN"] = bin
	offline.env["WHATSAPP_AXI_TOKEN"] = "wsa_writer"
	out, code := offline.run(t, "react", testRef, "👍")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "owner_offline", "needs the running wasync API")
	for _, call := range testutil.ReadFakeCalls(t, log) {
		if strings.Contains(strings.Join(call.Args, " "), "react") {
			t.Fatalf("offline reaction reached wasync query: %v", call.Args)
		}
	}

	h, srv := writerHarness(t)
	v := testutil.FakeVersion()
	v.Features = []string{"message.ref", "write.reply"}
	srv.Version = v
	srv.Routes[reactionEndpoint] = ackEnvelope("server_acknowledged")
	out, code = h.run(t, "react", testRef, "👍")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "write.react")
	if methods, _ := mutations(srv); len(methods) != 0 {
		t.Fatalf("sent to a wasync without write.react: %v", methods)
	}
}

func TestReactUsageErrors(t *testing.T) {
	h, srv := writerHarness(t)
	for _, args := range [][]string{
		{"react"},
		{"react", testRef},
		{"react", testRef, "👍", "--remove"},
		{"react", testRef, "👍", "👍"},
		{"react", "12345", "👍"},
		{"react", testRef, "--remove=yes"},
		{"react", testRef, "👍", "--bogus"},
	} {
		out, code := h.run(t, args...)
		if code != ExitUsage {
			t.Errorf("%v: exit %d\n%s", args, code, out)
		}
	}
	if methods, _ := mutations(srv); len(methods) != 0 {
		t.Fatalf("usage errors reached the API: %v", methods)
	}
}
