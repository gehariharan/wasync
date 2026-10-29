package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/axi/internal/buildinfo"
	"github.com/gehariharan/wasync/axi/internal/compat"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/installhint"
	"github.com/gehariharan/wasync/axi/internal/output"
	"github.com/gehariharan/wasync/axi/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.MaybeRunFakeWasync()
	os.Exit(m.Run())
}

var home = filepath.Join(string(filepath.Separator)+"home", "user")

type harness struct {
	env     map[string]string
	homeDir string
	stdin   io.Reader // session-end hook payload; nil is a terminal
	cwd     string    // working directory of the session
}

func newHarness(t *testing.T) *harness {
	return &harness{homeDir: home, env: map[string]string{
		"WASYNC_API_ADDR":         testutil.ClosedAddr(t),
		"WASYNC_BIN":              filepath.Join(t.TempDir(), "no-wasync"),
		"WHATSAPP_AXI_CONFIG_DIR": t.TempDir(),
		"WHATSAPP_AXI_AGENT_HOME": t.TempDir(),
	}}
}

func (h *harness) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	a := New(&out)
	a.Getenv = func(k string) string { return h.env[k] }
	a.Executable = func() (string, error) { return filepath.Join(home, "bin", "whatsapp-axi"), nil }
	a.HomeDir = func() (string, error) { return h.homeDir, nil }
	a.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	a.Stdin = h.stdin
	a.Getwd = func() (string, error) { return h.cwd, nil }
	a.ProbeTimeout = 2 * time.Second
	code := a.Run(context.Background(), args)
	return out.String(), code
}

func mustContain(t *testing.T, out string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(out, p) {
			t.Errorf("output lacks %q:\n%s", p, out)
		}
	}
}

func TestVersionFastPathDoesNotProbe(t *testing.T) {
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	for _, flag := range []string{"--version", "-v", "-V"} {
		out, code := h.run(t, flag)
		if code != ExitOK || out != buildinfo.Version+"\n" {
			t.Fatalf("%s: %q %d", flag, out, code)
		}
	}
	if len(srv.Requests) != 0 {
		t.Fatal("--version probed the API")
	}
	var b bytes.Buffer
	if !FastPath([]string{"-V"}, &b) || FastPath([]string{"version"}, &b) || FastPath([]string{"--version", "x"}, &b) {
		t.Fatal("FastPath matched the wrong arguments")
	}
}

func TestUsageErrorsExit2(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"version", "--bogus"}, []string{"error: unknown flag --bogus for `version`", "help: valid flags for `version`: --json (--help always allowed)"}},
		{[]string{"--limit=5"}, []string{"error: unknown flag --limit for `whatsapp-axi`", "valid flags for `whatsapp-axi`: --brief, --fields, --json"}},
		{[]string{"help", "-x"}, []string{"unknown flag -x for `help`"}},
		{[]string{"chat"}, []string{`error: unknown command "chat"`, "valid commands: chats, messages, search, thread, message, reply, react, edit, access, version, doctor, help"}},
		{[]string{"messages"}, []string{"`messages` needs CHAT", "usage: whatsapp-axi messages CHAT"}},
		{[]string{"search", "--chat"}, []string{"--chat needs a value (--chat CHAT)"}},
		{[]string{"search", "a", "b"}, []string{`unexpected argument "b" for ` + "`search`"}},
		{[]string{"messages", "x", "--limit", "ten"}, []string{`--limit must be a whole number >= 0, got "ten"`}},
		{[]string{"search", "x", "--context=-1"}, []string{`--context must be a whole number`}},
		{[]string{"thread", "12345"}, []string{`"12345" is not a message ref`, "m1_"}},
		{[]string{"message", "m1_abc", "--full=yes"}, []string{"--full takes no value"}},
		{[]string{"chats", "--cursor", "x"}, []string{"unknown flag --cursor for `chats`", "--fields, --json, --limit, --match"}},
		{[]string{"version", "extra"}, []string{`unexpected argument "extra" for ` + "`version`"}},
		{[]string{"help", "a", "b"}, []string{`unexpected argument "b"`}},
		{[]string{"help", "nope"}, []string{`unknown command "nope"`}},
		{[]string{"version", "--version"}, []string{"--version takes no other arguments"}},
		{[]string{"version", "--json=false"}, []string{"--json takes no value"}},
	}
	for _, c := range cases {
		out, code := h.run(t, c.args...)
		if code != ExitUsage {
			t.Errorf("%v: exit %d\n%s", c.args, code, out)
		}
		mustContain(t, out, c.want...)
		if strings.Contains(out, "code: usage") {
			t.Errorf("%v: usage errors print no code line", c.args)
		}
	}
	out, code := h.run(t, "version", "--bogus", "--json")
	var e map[string]string
	if code != ExitUsage || json.Unmarshal([]byte(out), &e) != nil || e["error_code"] != contract.CodeUsage {
		t.Fatalf("json usage error: %d %q", code, out)
	}
}

func TestHelpCatalogListsOnlyImplementedCommands(t *testing.T) {
	h := newHarness(t)
	out, code := h.run(t, "help", "--json")
	if code != ExitOK {
		t.Fatal(out)
	}
	var cat struct {
		Commands []commandJSON `json:"commands"`
	}
	if err := json.Unmarshal([]byte(out), &cat); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cat.Commands {
		names = append(names, c.Name)
		if strings.Contains(strings.ToLower(c.Description), "coming soon") {
			t.Errorf("%s advertises unimplemented work", c.Name)
		}
	}
	if !reflect.DeepEqual(names, []string{"whatsapp-axi", "chats", "messages", "search", "thread", "message", "reply", "react", "edit", "access", "version", "doctor", "help", "observe", "setup", "session-end"}) {
		t.Fatalf("catalog %v", names)
	}
	text, _ := h.run(t, "help")
	mustContain(t, text, "commands[16]{name,usage,description}:", "help[1]:")
	// Every command that talks to wasync has a compatibility entry.
	if got := compat.Default().CommandNames(); !reflect.DeepEqual(got, []string{"access", "chats", "doctor", "edit", "home", "message", "messages", "observe", "react", "reply", "search", "thread", "version"}) {
		t.Fatalf("compat entries %v", got)
	}
	for _, args := range [][]string{{"version", "--help"}, {"help", "version"}, {"--help"}, {"help", "help"}, {"messages", "--help"}, {"help", "search"}} {
		out, code := h.run(t, args...)
		if code != ExitOK {
			t.Errorf("%v exit %d", args, code)
		}
		mustContain(t, out, "usage: ", "flags[", "examples[")
	}
}

func TestHomeWithoutWasync(t *testing.T) {
	h := newHarness(t)
	out, code := h.run(t)
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out,
		"bin: "+output.Quote(filepath.Join("~", "bin", "whatsapp-axi")),
		"description: ",
		"state: not_installed",
		"transport: none",
		"(owner_offline)",
		installhint.InstallCore(),
		"WASYNC_BIN=<path to wasync>",
	)
	if strings.Contains(out, "error:") {
		t.Fatalf("absent wasync rendered as an error:\n%s", out)
	}
}

func TestHomeOnline(t *testing.T) {
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	h.env["WHATSAPP_AXI_TOKEN"] = "wsa_test"
	out, code := h.run(t)
	if code != ExitOK {
		t.Fatal(out)
	}
	mustContain(t, out, "state: online", "transport: api", "version: 0.2.0-test", `api_version: "1.0"`, "credential: env WHATSAPP_AXI_TOKEN", "status: ok")
	if strings.Contains(out, "agents grant") || strings.Contains(out, "features[") {
		t.Fatalf("unexpected content:\n%s", out)
	}
	for i, a := range srv.Auth {
		if (srv.Requests[i] == "/api/v1/version") != (a == "") {
			t.Fatalf("request %s auth %q: the credential goes to data reads only", srv.Requests[i], a)
		}
	}
}

func TestHomeOnlineWithoutCredentialHintsGrant(t *testing.T) {
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	out, _ := h.run(t)
	mustContain(t, out, "credential: missing", "wasync agents grant <name> --scope read.chats,read.messages", "wasync offers to save the credential where whatsapp-axi reads it", "WHATSAPP_AXI_TOKEN")
	if strings.Contains(out, "mode 600") {
		t.Fatalf("home hint still asks for a manual credential save:\n%s", out)
	}
}

func TestHomeOfflineUsesQuery(t *testing.T) {
	h := newHarness(t)
	bin, log := testutil.FakeWasync(t, testutil.ModeOK)
	h.env["WASYNC_BIN"] = bin
	h.env["WASYNC_DATA_DIR"] = filepath.Join(home, "wasync-data")
	out, code := h.run(t)
	if code != ExitOK {
		t.Fatal(out)
	}
	mustContain(t, out, "state: offline", "transport: query", "version: 0.2.0-test", "status: ok", "not scoped", "whatsapp-axi never starts it")
	call := testutil.ReadFakeCalls(t, log)[0]
	if strings.Join(call.Args, " ") != "--data-dir "+filepath.Join(home, "wasync-data")+" query version --v1 --json" {
		t.Fatalf("args %v", call.Args)
	}
}

func TestVersionJSON(t *testing.T) {
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	out, code := h.run(t, "version", "--json")
	var got struct {
		Version string `json:"whatsapp_axi_version"`
		Commit  string `json:"whatsapp_axi_commit"`
		Date    string `json:"whatsapp_axi_date"`
		Wasync  struct {
			State     string                `json:"state"`
			Transport string                `json:"transport"`
			Version   *contract.VersionInfo `json:"version"`
		} `json:"wasync"`
		Compat compatJSON `json:"compatibility"`
	}
	if code != ExitOK || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("%d %s", code, out)
	}
	if got.Version != buildinfo.Version || got.Commit != buildinfo.Commit || got.Date != buildinfo.Date || got.Wasync.Transport != "api" || got.Wasync.Version == nil || len(got.Wasync.Version.Features) != 16 || !got.Compat.OK {
		t.Fatalf("%+v", got)
	}
	text, _ := h.run(t, "version")
	mustContain(t, text, "whatsapp-axi: "+buildinfo.Version, "commit: "+buildinfo.Commit, "date: "+buildinfo.Date, `min_api: "1.0"`, "features[16]: chats,chats.match,messages", "archive_schema: \"6\"")
}

func TestIncompatibleWasync(t *testing.T) {
	srv := testutil.NewAPI(t)
	v := testutil.FakeVersion()
	v.APIVersion = 2
	srv.Version = v
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	out, code := h.run(t)
	if code != ExitOK {
		t.Fatal(out)
	}
	mustContain(t, out, "state: incompatible", "status: whatsapp_axi_too_old")

	bin, _ := testutil.FakeWasync(t, testutil.ModeOld)
	h = newHarness(t)
	h.env["WASYNC_BIN"] = bin
	out, _ = h.run(t)
	mustContain(t, out, "state: unavailable", "status: wasync_too_old")
	mustContain(t, out, installhint.UpgradeCore())
	if strings.Count(out, installhint.UpgradeCore()) != 1 {
		t.Fatalf("upgrade hint missing or repeated:\n%s", out)
	}
}

func TestOldMinorFailsCompat(t *testing.T) {
	srv := testutil.NewAPI(t)
	v := testutil.FakeVersion()
	v.APIVersion = 0
	srv.Version = v
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	out, _ := h.run(t, "version")
	mustContain(t, out, "status: wasync_too_old", installhint.UpgradeCore())
}

func TestWasyncStringsAreSanitized(t *testing.T) {
	h := newHarness(t)
	bin, _ := testutil.FakeWasync(t, testutil.ModeANSI)
	h.env["WASYNC_BIN"] = bin
	out, _ := h.run(t, "version")
	if strings.ContainsAny(out, "\x1b\x07\r") {
		t.Fatalf("control characters leaked: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "help: run") {
			t.Fatalf("injected line rendered as output: %q", out)
		}
	}
}

func TestNonLoopbackAPIAddrIsRefused(t *testing.T) {
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = "0.0.0.0:8787"
	out, code := h.run(t)
	if code != ExitOK {
		t.Fatal(out)
	}
	mustContain(t, out, "(invalid_argument)")
}

func TestBadCredentialIsReported(t *testing.T) {
	srv := testutil.NewAPI(t)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	h.env["WHATSAPP_AXI_TOKEN"] = "0123456789abcdef" // looks like wasync's api.token
	out, _ := h.run(t)
	mustContain(t, out, "credential: invalid", "Credential problem:")
	if strings.Contains(out, "0123456789abcdef") {
		t.Fatal("credential value printed")
	}
}

func TestVersionAliasesAsData(t *testing.T) {
	for _, alias := range []string{"--version", "-v", "-V"} {
		h, srv := apiHarness(t)
		for _, tc := range []struct {
			args []string
			path string
			key  string
		}{
			{[]string{"search", "--", alias}, "/messages/search", "q"},
			{[]string{"chats", "--match", alias}, "/chats", "match"},
			{[]string{"chats", "--match=" + alias}, "/chats", "match"},
		} {
			srv.Routes[tc.path] = testutil.Envelope(map[string]any{"items": []any{}})
			out, code := h.run(t, tc.args...)
			if code != ExitOK {
				t.Fatalf("%v: exit %d %s", tc.args, code, out)
			}
			u, err := url.Parse(srv.Requests[len(srv.Requests)-1])
			if err != nil || u.Path != "/api/v1"+tc.path || u.Query().Get(tc.key) != alias {
				t.Fatalf("data changed: %v %v", u, err)
			}
		}
	}
}
