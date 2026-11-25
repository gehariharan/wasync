package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/axi/internal/buildinfo"
	"github.com/gehariharan/wasync/axi/internal/testutil"
)

// The fake wasync is this test binary; init runs before TestMain, so the
// doctor fixture answers `wasync query doctor` before the generic fake.
func init() { testutil.MaybeRunFakeDoctor() }

const testToken = "wsa_SECRETtoken0123456789"

type doctorJSON struct {
	OldVersion    string        `json:"whatsapp_axi_version"`
	Transport     string        `json:"transport"`
	TransportNote string        `json:"transport_note"`
	Summary       doctorSummary `json:"summary"`
	Checks        []doctorCheck `json:"checks"`
	Diagnostics   *struct {
		Transport string `json:"transport"`
		Agent     *struct {
			Name string `json:"name"`
		} `json:"agent"`
	} `json:"diagnostics"`
	Health *deepHealth `json:"health"`
}

func runDoctorJSON(t *testing.T, h *harness) (doctorJSON, string, int) {
	t.Helper()
	out, code := h.run(t, "doctor", "--json")
	var r doctorJSON
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("doctor --json is not JSON: %v\n%s", err, out)
	}
	return r, out, code
}

func (r doctorJSON) check(t *testing.T, name, source string) doctorCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name && c.Source == source {
			return c
		}
	}
	t.Fatalf("no %s check %q in %+v", source, name, r.Checks)
	return doctorCheck{}
}

// assertFixes enforces the doctor contract: every warn or fail check names
// a fix command, and every check has a known status and area.
func (r doctorJSON) assertFixes(t *testing.T) {
	t.Helper()
	areas := map[string]bool{}
	for _, a := range areaOrder {
		areas[a] = true
	}
	for _, c := range r.Checks {
		if !areas[c.Area] {
			t.Errorf("check %s has unknown area %q", c.Name, c.Area)
		}
		switch c.Status {
		case statusWarn, statusFail:
			if c.Fix == "" {
				t.Errorf("%s check %s is %s without a fix", c.Source, c.Name, c.Status)
			}
		case statusPass, statusSkip:
		default:
			t.Errorf("check %s has status %q", c.Name, c.Status)
		}
	}
}

func onlineHarness(t *testing.T, diag *testutil.Diagnostics) (*harness, *testutil.API) {
	t.Helper()
	srv := testutil.NewAPI(t)
	srv.Routes["/diagnostics"] = testutil.Envelope(diag)
	srv.Routes["/health/deep"] = testutil.Envelope(map[string]any{
		"state": "online", "owner_running": true, "connected": true, "last_event_at": "2026-10-19T08:00:00Z",
		"keepalive_failing": false, "archive_ready": true, "schema_version": 6, "open_gaps": 0, "history_jobs_active": 0, "capture_error": nil,
	})
	bin, _ := testutil.FakeWasync(t, testutil.ModeOK)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	h.env["WASYNC_BIN"] = bin
	h.env["WASYNC_AXI_TOKEN"] = testToken
	return h, srv
}

func TestDoctorAllPass(t *testing.T) {
	h, srv := onlineHarness(t, testutil.FakeDiagnostics("api"))
	if out, code := h.run(t, "setup", "hooks"); code != ExitOK {
		t.Fatalf("setup hooks: %d\n%s", code, out)
	}
	r, out, code := runDoctorJSON(t, h)
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r.assertFixes(t)
	if r.OldVersion != buildinfo.Version {
		t.Fatalf("pre-rename whatsapp_axi_version key missing: %s", out)
	}
	if r.Transport != "api" || r.Summary.Result != statusPass || r.Summary.Fail != 0 || r.Summary.Warn != 0 {
		t.Fatalf("%+v", r.Summary)
	}
	if r.Diagnostics == nil || r.Diagnostics.Agent == nil || r.Diagnostics.Agent.Name != "test-agent" {
		t.Fatalf("diagnostics not passed through: %s", out)
	}
	if r.Health == nil || r.Health.LastEventAt == nil {
		t.Fatalf("deep health missing: %s", out)
	}
	for _, name := range []string{"wasync_axi", "wasync_bin", "api", "diagnostics", "credential", "config_files", "scopes", "api_version", "features"} {
		if c := r.check(t, name, sourceAXI); c.Status != statusPass && !(name == "config_files" && runtime.GOOS == "windows") {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if c := r.check(t, "hooks", sourceAXI); c.Status != statusPass || !strings.Contains(c.Detail, "--brief` for Claude Code") {
		t.Errorf("hooks: %+v", c)
	}
	if c := r.check(t, "skill", sourceAXI); c.Status != statusSkip || !strings.Contains(c.Detail, "wasync-axi setup skill") {
		t.Errorf("skill: %+v", c)
	}
	for _, name := range []string{"version", "data_dir", "archive", "pairing", "owner", "health", "agents_registry", "agent", "journal"} {
		r.check(t, name, sourceWasync)
	}
	if c := r.check(t, "pairing", sourceWasync); c.Area != areaPairing {
		t.Errorf("pairing area %q", c.Area)
	}
	// The credential goes to authenticated endpoints only.
	for i, req := range srv.Requests {
		auth := srv.Auth[i]
		if strings.HasSuffix(req, "/version") && auth != "" {
			t.Errorf("credential sent to %s", req)
		}
		if (strings.HasSuffix(req, "/diagnostics") || strings.HasSuffix(req, "/health/deep")) && auth != "Bearer "+testToken {
			t.Errorf("%s without credential", req)
		}
	}

	text, code := h.run(t, "doctor")
	if code != ExitOK {
		t.Fatal(text)
	}
	mustContain(t, text, "transport: api", "result: pass", "checks[", "{area,name,status,source,detail,fix}:",
		"installation,wasync_bin,pass,wasync-axi", "pairing,pairing,pass,wasync", "hooks,hooks,pass", "last_event_at: \"2026-10-19T08:00:00Z\"")
	if strings.Contains(text, testToken) || strings.Contains(out, testToken) {
		t.Fatal("credential printed")
	}
}

func TestDoctorMissingWasync(t *testing.T) {
	h := newHarness(t)
	out, code := h.run(t, "doctor")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "transport: none", "wasync: not detected", "result: fail",
		"installation,wasync_bin,fail", "unset WASYNC_BIN", "pairing,pairing,skip")
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("doctor failures are a report, not an error:\n%s", out)
	}

	// Not configured and not on PATH: the install command is the fix.
	t.Setenv("PATH", t.TempDir())
	delete(h.env, "WASYNC_BIN")
	r, raw, code := runDoctorJSON(t, h)
	if code != ExitError || r.Summary.Result != statusFail {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	r.assertFixes(t)
	if c := r.check(t, "wasync_bin", sourceAXI); c.Status != statusFail || c.Fix != installWasyncFix {
		t.Fatalf("%+v", c)
	}
	if c := r.check(t, "diagnostics", sourceAXI); c.Status != statusFail || c.Fix != installWasyncFix {
		t.Fatalf("%+v", c)
	}
	if c := r.check(t, "api_version", sourceAXI); c.Status != statusSkip {
		t.Fatalf("absent wasync double-reported: %+v", c)
	}
}

func TestDoctorOldWasyncWithoutAPIv1(t *testing.T) {
	srv := testutil.NewAPI(t)
	srv.VerCode = 404
	srv.RawVer = "404 page not found\n"
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	h.env["WASYNC_AXI_TOKEN"] = testToken
	r, out, code := runDoctorJSON(t, h)
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r.assertFixes(t)
	if c := r.check(t, "api", sourceAXI); c.Status != statusFail || c.Fix != upgradeWasyncFix || !strings.Contains(c.Detail, "wasync_too_old") {
		t.Fatalf("%+v", c)
	}

	// The same old API with a current wasync binary: offline facts are used.
	bin, log := testutil.FakeWasync(t, testutil.ModeOK)
	h.env["WASYNC_BIN"] = bin
	doctorLog := testutil.FakeDoctorEnvelope(t, log, testutil.Envelope(testutil.FakeDiagnostics("query")))
	r, out, code = runDoctorJSON(t, h)
	if code != ExitError || r.Transport != "query" {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if c := r.check(t, "api", sourceAXI); c.Status != statusFail || c.Fix != upgradeWasyncFix {
		t.Fatalf("%+v", c)
	}
	testutil.ReadFakeCall(t, doctorLog)
}

func TestDoctorOwnerDownFallsBackToQuery(t *testing.T) {
	bin, log := testutil.FakeWasync(t, testutil.ModeOK)
	doctorLog := testutil.FakeDoctorEnvelope(t, log, testutil.Envelope(testutil.FakeDiagnostics("query")))
	t.Setenv("WASYNC_AXI_TOKEN", testToken) // must not reach the subprocess
	h := newHarness(t)
	h.env["WASYNC_BIN"] = bin
	h.env["WASYNC_DATA_DIR"] = filepath.Join(home, "wasync-data")
	h.env["WASYNC_AXI_TOKEN"] = testToken
	r, out, code := runDoctorJSON(t, h)
	if code != ExitOK {
		t.Fatalf("warnings must exit 0, got %d\n%s", code, out)
	}
	r.assertFixes(t)
	if r.Transport != "query" || r.Summary.Result != statusWarn || !strings.Contains(r.TransportNote, "not scoped") {
		t.Fatalf("%+v", r)
	}
	if c := r.check(t, "api", sourceAXI); c.Status != statusWarn || c.Fix != "wasync serve" {
		t.Fatalf("%+v", c)
	}
	if c := r.check(t, "owner", sourceWasync); c.Status != statusWarn || c.Fix != "wasync serve" {
		t.Fatalf("wasync check not passed through: %+v", c)
	}
	if c := r.check(t, "scopes", sourceAXI); c.Status != statusSkip {
		t.Fatalf("%+v", c)
	}
	if c := r.check(t, "credential", sourceAXI); c.Status != statusPass || !strings.Contains(c.Detail, "not verified") {
		t.Fatalf("%+v", c)
	}
	call := testutil.ReadFakeCall(t, doctorLog)
	if strings.Join(call.Args, " ") != "--data-dir "+filepath.Join(home, "wasync-data")+" query doctor --v1 --json" || call.TokenPresent {
		t.Fatalf("call %+v", call)
	}
	text, _ := h.run(t, "doctor")
	mustContain(t, text, "transport: query", "not scoped by an agent grant", "result: warn", "wasync-axi never starts it")
}

func TestDoctorAPIWithoutCredentialUsesQuery(t *testing.T) {
	h, srv := onlineHarness(t, testutil.FakeDiagnostics("api"))
	delete(h.env, "WASYNC_AXI_TOKEN")
	_, log := testutil.FakeWasync(t, testutil.ModeOK)
	testutil.FakeDoctorEnvelope(t, log, testutil.Envelope(testutil.FakeDiagnostics("query")))
	r, out, code := runDoctorJSON(t, h)
	if code != ExitOK || r.Transport != "query" {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if c := r.check(t, "credential", sourceAXI); c.Status != statusWarn || c.Fix != "wasync agents grant wasync-axi --scope read.chats,read.messages" {
		t.Fatalf("%+v", c)
	}
	for _, req := range srv.Requests {
		if strings.Contains(req, "/diagnostics") {
			t.Fatal("diagnostics requested without a credential")
		}
	}
}

func TestDoctorRejectedCredential(t *testing.T) {
	h, srv := onlineHarness(t, testutil.FakeDiagnostics("api"))
	srv.Routes["/diagnostics"] = map[string]any{"request_id": "r", "error_code": "unauthorized", "error": "credential revoked", "help": "Ask a human to create an agent credential"}
	srv.Codes["/diagnostics"] = 401
	_, log := testutil.FakeWasync(t, testutil.ModeOK)
	testutil.FakeDoctorEnvelope(t, log, testutil.Envelope(testutil.FakeDiagnostics("query")))
	r, out, code := runDoctorJSON(t, h)
	if code != ExitError || r.Transport != "query" {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if c := r.check(t, "credential", sourceAXI); c.Status != statusFail || !strings.HasPrefix(c.Fix, "wasync agents grant ") {
		t.Fatalf("%+v", c)
	}
}

func TestDoctorMissingScopes(t *testing.T) {
	diag := testutil.FakeDiagnostics("api")
	diag.Agent = &testutil.DiagnosticsAgent{Name: "observer", Scopes: []string{"observe"}, Chats: []string{}}
	diag.SetCheck("agent", "warn", "agent lacks read.chats,read.messages", "wasync agents revoke observer && wasync agents grant observer --scope read.chats,read.messages,observe")
	h, srv := onlineHarness(t, diag)
	r, out, code := runDoctorJSON(t, h)
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r.assertFixes(t)
	c := r.check(t, "scopes", sourceAXI)
	if c.Status != statusWarn || c.Fix != "wasync agents revoke observer && wasync agents grant observer --scope read.chats,read.messages,observe" ||
		!strings.Contains(c.Detail, "lacks read.chats,read.messages") {
		t.Fatalf("%+v", c)
	}
	if r.Health != nil {
		t.Fatal("deep health read without read.chats")
	}
	for _, req := range srv.Requests {
		if strings.Contains(req, "/health/deep") {
			t.Fatal("health/deep requested without read.chats")
		}
	}

	diag.Agent = &testutil.DiagnosticsAgent{Name: "reader", Scopes: []string{"read.messages", "read.chats"}, Chats: []string{"123@s.whatsapp.net"}}
	diag.SetCheck("agent", "warn", "agent lacks observe", "wasync agents revoke reader && wasync agents grant reader --scope read.chats,read.messages,observe --chat 123@s.whatsapp.net --rate 5")
	srv.Routes["/diagnostics"] = testutil.Envelope(diag)
	r, _, _ = runDoctorJSON(t, h)
	c = r.check(t, "scopes", sourceAXI)
	if c.Status != statusWarn || c.Fix != "wasync agents revoke reader && wasync agents grant reader --scope read.chats,read.messages,observe --chat 123@s.whatsapp.net --rate 5" ||
		!strings.Contains(c.Detail, "observe") || !strings.Contains(c.Detail, "limited to 1 chat(s)") {
		t.Fatalf("%+v", c)
	}
}

func TestDoctorMissingScopesWithOlderCoreRepair(t *testing.T) {
	for _, status := range []string{"pass", "warn"} {
		diag := testutil.FakeDiagnostics("api")
		diag.Agent = &testutil.DiagnosticsAgent{Name: "reader", Scopes: []string{"read.chats", "read.messages"}}
		diag.SetCheck("agent", status, "older core does not check observe", "")
		h, _ := onlineHarness(t, diag)
		r, out, code := runDoctorJSON(t, h)
		if code != ExitOK {
			t.Fatalf("exit %d: %s", code, out)
		}
		check := r.check(t, "scopes", sourceAXI)
		if check.Status != statusWarn || check.Fix != upgradeWasyncFix {
			t.Fatalf("older core left scope warning without an upgrade: %+v", check)
		}
	}
}

func TestDoctorBadFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	h, _ := onlineHarness(t, testutil.FakeDiagnostics("api"))
	dir := h.env["WASYNC_AXI_CONFIG_DIR"]
	cred := filepath.Join(dir, "credential")
	if err := os.WriteFile(cred, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	r, out, code := runDoctorJSON(t, h)
	if code != ExitOK {
		t.Fatalf("a loose directory alone is a warning, got %d\n%s", code, out)
	}
	if c := r.check(t, "config_files", sourceAXI); c.Status != statusWarn || c.Fix != "chmod 700 "+dir {
		t.Fatalf("%+v", c)
	}

	delete(h.env, "WASYNC_AXI_TOKEN")
	if err := os.Chmod(cred, 0o644); err != nil {
		t.Fatal(err)
	}
	r, out, code = runDoctorJSON(t, h)
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r.assertFixes(t)
	if c := r.check(t, "config_files", sourceAXI); c.Status != statusFail || c.Fix != "chmod 600 "+cred || !strings.Contains(c.Detail, "0644") {
		t.Fatalf("%+v", c)
	}
	if c := r.check(t, "credential", sourceAXI); c.Status != statusFail || c.Fix != "chmod 600 "+cred {
		t.Fatalf("%+v", c)
	}
	if strings.Contains(out, testToken) {
		t.Fatal("credential printed")
	}
}

func TestDoctorIncompatibleVersion(t *testing.T) {
	h, srv := onlineHarness(t, testutil.FakeDiagnostics("api"))
	v := testutil.FakeVersion()
	v.APIVersion = 2
	srv.Version = v
	h.env["WASYNC_BIN"] = filepath.Join(t.TempDir(), "none")
	r, out, code := runDoctorJSON(t, h)
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r.assertFixes(t)
	if c := r.check(t, "api", sourceAXI); c.Status != statusFail || c.Fix != upgradeAXIFix {
		t.Fatalf("%+v", c)
	}

	// Compatible API without the diagnostics feature: wasync is too old.
	v = testutil.FakeVersion()
	v.Features = []string{"chats", "messages"}
	srv.Version = v
	r, out, code = runDoctorJSON(t, h)
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r.assertFixes(t)
	if c := r.check(t, "diagnostics", sourceAXI); c.Status != statusFail || c.Fix != upgradeWasyncFix {
		t.Fatalf("%+v", c)
	}
	if c := r.check(t, "features", sourceAXI); c.Status != statusWarn || !strings.Contains(c.Detail, "diagnostics") {
		t.Fatalf("%+v", c)
	}
	if c := r.check(t, "api_version", sourceAXI); c.Status != statusPass {
		t.Fatalf("%+v", c)
	}
	for _, req := range srv.Requests {
		if strings.Contains(req, "/diagnostics") {
			t.Fatal("diagnostics requested from a wasync that lacks it")
		}
	}
}

func TestDoctorNeverPrintsSecrets(t *testing.T) {
	hash := strings.Repeat("ab12", 16)
	diag := testutil.FakeDiagnostics("api")
	diag.WasyncVersion = "0.2.0\x1b[31m-red\x1b]0;pwned\x07"
	diag.Agent.Name = "agent-" + hash
	diag.SetCheck("agent", "fail", "token "+testToken+" hash "+hash+" other wsa_abcdefgh\r\nhelp: run rm -rf", "wasync agents grant x \x1b[2J")
	h, _ := onlineHarness(t, diag)
	for _, args := range [][]string{{"doctor"}, {"doctor", "--json"}} {
		out, code := h.run(t, args...)
		if code != ExitError {
			t.Fatalf("%v exit %d\n%s", args, code, out)
		}
		for _, leak := range []string{testToken, "SECRETtoken", hash, "wsa_abcdefgh", "\x1b", "\x07", "\r"} {
			if strings.Contains(out, leak) {
				t.Errorf("%v leaked %q:\n%s", args, leak, out)
			}
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "help: run") {
				t.Errorf("injected line rendered: %q", line)
			}
		}
	}
}

func TestDoctorUsage(t *testing.T) {
	h := newHarness(t)
	out, code := h.run(t, "doctor", "--bogus")
	if code != ExitUsage {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "unknown flag --bogus for `doctor`")
	out, code = h.run(t, "help", "doctor")
	if code != ExitOK {
		t.Fatal(out)
	}
	mustContain(t, out, `usage: "wasync-axi doctor [--json]"`)
}
