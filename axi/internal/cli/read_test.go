package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/installhint"
	"github.com/gehariharan/wasync/axi/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite testdata/golden files")

const fixturesFile = "testdata/fixtures.json"

// Refs used by testdata/fixtures.json.
const (
	refBig     = "m1_bWVAbGlkAGZhbWlseUBnLnVzAGY2" // 1500-char text with media
	refAnchor  = "m1_bWVAbGlkAGZhbWlseUBnLnVzAGYy" // edited, from me
	refDeleted = "m1_bWVAbGlkAGZhbWlseUBnLnVzAGYz"
	refGone    = "m1_bWVAbGlkAGZhbWlseUBnLnVzAGdvbmU"
)

// apiHarness serves the fixtures over a fake /api/v1 with a credential.
func apiHarness(t *testing.T) (*harness, *testutil.API) {
	t.Helper()
	fixtures, err := testutil.LoadFixtures(fixturesFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := testutil.NewAPI(t)
	srv.ServeFixtures(t, fixtures)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	h.env["WASYNC_AXI_TOKEN"] = "wsa_0123abcd_test"
	return h, srv
}

// queryHarness serves the same fixtures through the fake wasync binary,
// with no API listening.
func queryHarness(t *testing.T) (*harness, string) {
	t.Helper()
	path, err := filepath.Abs(fixturesFile)
	if err != nil {
		t.Fatal(err)
	}
	bin, log := testutil.FakeFixtures(t, path)
	h := newHarness(t)
	h.env["WASYNC_BIN"] = bin
	return h, log
}

// sameAsAPI rewrites the transport-specific lines of query output so it can
// be compared with API output.
func sameAsAPI(out string) string {
	out = strings.ReplaceAll(out, "transport: query\nnote: "+offlineNote+"\n", "transport: api\n")
	return strings.ReplaceAll(out, `"transport":"query"`, `"transport":"api"`)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/cli -run TestReadCommandsGolden -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s: output differs from %s\n--- got\n%s\n--- want\n%s", name, path, got, want)
	}
}

func TestReadCommandsGolden(t *testing.T) {
	cases := []struct {
		name string
		args []string
		exit int
	}{
		{"chats", []string{"chats"}, ExitOK},
		{"chats_limit", []string{"chats", "--limit", "2"}, ExitOK},
		{"chats_match_empty", []string{"chats", "--match", "nobody"}, ExitOK},
		{"chats_ambiguous", []string{"chats", "--match", "team"}, ExitError},
		{"chats_json", []string{"chats", "--json"}, ExitOK},
		{"chats_ambiguous_json", []string{"chats", "--match=team", "--json"}, ExitError},
		{"messages", []string{"messages", "family"}, ExitOK},
		{"messages_json", []string{"messages", "family", "--json"}, ExitOK},
		{"messages_last_page", []string{"messages", "family@g.us", "--cursor", "c2"}, ExitOK},
		{"messages_ambiguous", []string{"messages", "team"}, ExitError},
		{"messages_no_chat", []string{"messages", "nobody"}, ExitError},
		{"messages_bad_cursor", []string{"messages", "family", "--cursor", "bad"}, ExitError},
		{"messages_forbidden", []string{"messages", "team-a@g.us"}, ExitError},
		{"search", []string{"search", "dinner"}, ExitOK},
		{"search_context", []string{"search", "dinner", "--chat", "family", "--context", "1", "--limit", "1"}, ExitOK},
		{"search_empty", []string{"search", "--", "-nothing here"}, ExitOK},
		{"search_ambiguous_chat", []string{"search", "dinner", "--chat", "team"}, ExitError},
		{"message", []string{"message", refBig}, ExitOK},
		{"message_full", []string{"message", refBig, "--full"}, ExitOK},
		{"message_deleted", []string{"message", refDeleted}, ExitOK},
		{"message_not_found", []string{"message", refGone}, ExitError},
		{"message_not_found_json", []string{"message", refGone, "--json"}, ExitError},
		{"thread", []string{"thread", refAnchor}, ExitOK},
		{"thread_window", []string{"thread", refAnchor, "--before", "0", "--after", "1"}, ExitOK},
	}
	api, _ := apiHarness(t)
	query, _ := queryHarness(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, code := api.run(t, c.args...)
			if code != c.exit {
				t.Errorf("api exit %d, want %d\n%s", code, c.exit, got)
			}
			offline, qcode := query.run(t, c.args...)
			if qcode != c.exit {
				t.Errorf("query exit %d, want %d\n%s", qcode, c.exit, offline)
			}
			if sameAsAPI(offline) != got {
				t.Errorf("transports differ\n--- api\n%s\n--- query\n%s", got, offline)
			}
			if c.exit == ExitOK && !strings.Contains(offline, "query") {
				t.Errorf("query output does not name its transport:\n%s", offline)
			}
			golden(t, c.name, got)
		})
	}
}

func TestReadOutputIsSanitized(t *testing.T) {
	h, _ := apiHarness(t)
	for _, args := range [][]string{{"chats"}, {"messages", "family"}, {"thread", refAnchor}, {"message", refBig, "--full"}, {"search", "dinner", "--chat", "family", "--context", "1", "--limit", "1"}} {
		out, _ := h.run(t, args...)
		if strings.ContainsAny(out, "\x1b\x07\r") {
			t.Errorf("%v: control characters leaked: %q", args, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "help: run") || strings.HasPrefix(line, "ignore previous") {
				t.Errorf("%v: message text escaped its field: %q", args, line)
			}
		}
	}
}

func TestReadQueryArgs(t *testing.T) {
	h, log := queryHarness(t)
	h.env["WASYNC_DATA_DIR"] = filepath.Join(home, "data")
	if out, code := h.run(t, "search", "dinner", "--chat", "family", "--context", "1", "--limit", "1"); code != ExitOK {
		t.Fatal(out)
	}
	var got []string
	for _, c := range testutil.ReadFakeCalls(t, log) {
		if c.TokenPresent {
			t.Fatal("credential passed to wasync query")
		}
		got = append(got, strings.Join(c.Args, " "))
	}
	data := "--data-dir " + filepath.Join(home, "data") + " query "
	want := []string{
		data + "version --v1 --json",
		data + "chats --match=family --v1 --json",
		data + "search --chat=family@g.us --context=1 --limit=1 --v1 --json -- dinner",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestReadAPIRequests(t *testing.T) {
	h, srv := apiHarness(t)
	if out, code := h.run(t, "messages", "family", "--cursor", "c2"); code != ExitOK {
		t.Fatal(out)
	}
	want := []string{"/api/v1/version", "/api/v1/chats?match=family", "/api/v1/chats/family@g.us/messages?cursor=c2&limit=20"}
	if strings.Join(srv.Requests, " ") != strings.Join(want, " ") {
		t.Fatalf("requests %v", srv.Requests)
	}
	if srv.Auth[1] != "Bearer wsa_0123abcd_test" || srv.Auth[2] != srv.Auth[1] {
		t.Fatalf("auth %v", srv.Auth)
	}
}

func TestReadWithoutCredentialDoesNotFallBack(t *testing.T) {
	h, srv := apiHarness(t)
	delete(h.env, "WASYNC_AXI_TOKEN")
	bin, log := testutil.FakeWasync(t, testutil.ModeOK)
	h.env["WASYNC_BIN"] = bin
	out, code := h.run(t, "chats")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "code: credential_missing", "wasync agents grant")
	if len(srv.Requests) != 1 {
		t.Fatalf("requests %v", srv.Requests)
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("fell back to the unscoped offline transport")
	}

	h.env["WASYNC_AXI_TOKEN"] = "0123456789abcdef"
	out, code = h.run(t, "chats")
	if code != ExitError || strings.Contains(out, "0123456789abcdef") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "code: credential_invalid")
}

func TestReadWithoutWasync(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"chats"}, {"messages", "x"}, {"search", "x"}, {"thread", refAnchor}, {"message", refAnchor, "--json"}} {
		out, code := h.run(t, args...)
		if code != ExitError || !strings.Contains(out, "wasync_not_found") || !strings.Contains(out, installhint.InstallCore()) {
			t.Errorf("%v: exit %d\n%s", args, code, out)
		}
	}
}

func TestReadNeedsFeatures(t *testing.T) {
	h, srv := apiHarness(t)
	v := testutil.FakeVersion()
	v.Features = []string{"chats", "messages"}
	srv.Version = v
	out, code := h.run(t, "chats")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "code: wasync_too_old", "chats.match", installhint.UpgradeCore())
	if len(srv.Requests) != 1 {
		t.Fatalf("read attempted: %v", srv.Requests)
	}
}

func TestHomeShowsRecentChats(t *testing.T) {
	api, _ := apiHarness(t)
	query, _ := queryHarness(t)
	for name, h := range map[string]*harness{"api": api, "query": query} {
		out, code := h.run(t)
		if code != ExitOK {
			t.Fatalf("%s: exit %d\n%s", name, code, out)
		}
		mustContain(t, out, "chats:\n  total: 4\n  groups: 3\n  direct: 1\n  capture_enabled: 3\n  archived_messages: 7\n  recent[4]{id,kind,messages,untrusted_name}:\n    team-a@g.us,",
			"Run `wasync-axi messages <id>` to read a chat")
		// Newest first; a chat without messages sorts last.
		if i, j := strings.Index(out, "    family@g.us,"), strings.Index(out, "    team-b@g.us,"); i < 0 || j < i {
			t.Errorf("%s: order\n%s", name, out)
		}
		if strings.ContainsRune(out, '\x1b') {
			t.Errorf("%s: escape leaked", name)
		}
	}
}

func TestHomeReportsChatsError(t *testing.T) {
	srv := testutil.NewAPI(t)
	srv.Routes["/chats"] = map[string]any{"request_id": "r", "error_code": "forbidden", "error": "client x lacks scope read.chats", "help": "Missing scope read.chats."}
	srv.Codes["/chats"] = 403
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	h.env["WASYNC_AXI_TOKEN"] = "wsa_test"
	out, code := h.run(t)
	if code != ExitOK {
		t.Fatal(out)
	}
	mustContain(t, out, "chats_error: \"forbidden: client x lacks scope read.chats\"", "Missing scope read.chats.")
}

func TestHintCommandLines(t *testing.T) {
	cases := map[string]string{
		commandLine("messages", "family@g.us", "--cursor", "c2"):  "`wasync-axi messages family@g.us --cursor c2`",
		commandLine(searchArgs("-5% off", "--chat", "a@g.us")...): "`wasync-axi search --chat a@g.us -- '-5% off'`",
		commandLine(searchArgs("it's", "--limit", "5")...):        "`wasync-axi search 'it'\\''s' --limit 5`",
		commandLine("search", "--json"):                           "`wasync-axi search --json`",
		commandLine("thread", "m1_a-b_c", "--before", "10"):       "`wasync-axi thread m1_a-b_c --before 10`",
		commandLine(searchArgs("$(rm -rf ~)")...):                 "`wasync-axi search '$(rm -rf ~)'`",
		commandLine(searchArgs("a\nb")...):                        "`wasync-axi search 'a\nb'`",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %s want %s", got, want)
		}
	}
}
