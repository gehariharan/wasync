// Package testutil provides fixtures for tests: a fake wasync binary (the
// test binary re-executed through TestMain, so it works on every OS) and a
// fake loopback /api/v1 server. It is imported only from _test.go files.
package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

// Environment variables that drive the fake wasync.
const (
	EnvFakeMode     = "WHATSAPP_AXI_FAKE_WASYNC_MODE"
	EnvFakeLog      = "WHATSAPP_AXI_FAKE_WASYNC_LOG"
	EnvFakeFixtures = "WHATSAPP_AXI_FAKE_WASYNC_FIXTURES"
)

// Fake wasync modes.
const (
	ModeOK      = "ok"      // success envelope with FakeVersion
	ModeOld     = "old"     // a wasync without --v1: usage error, no stdout
	ModeError   = "error"   // archive_unavailable error envelope, exit 1
	ModeGarbage = "garbage" // non-JSON stdout, exit 0
	ModeAPI2    = "api2"    // speaks API 2.0
	ModeANSI    = "ansi"    // version string carrying terminal escapes
	ModeSleep   = "sleep"   // hangs for 30s
	ModeFixture = "fixture" // version as ModeOK; other reads from the EnvFakeFixtures file
)

// FakeVersion is the version the fake wasync and fake API report.
func FakeVersion() contract.VersionInfo {
	archive := 6
	return contract.VersionInfo{
		APIVersion: 1, APIMinor: 0, WasyncVersion: "0.2.0-test", SchemaVersion: 6, ArchiveSchemaVersion: &archive,
		Features: []string{"chats", "chats.match", "messages", "search", "search.context", "thread", "message.ref", "health.deep", "auth.scopes", "changes", "checkpoints", "diagnostics", "write.reply"},
	}
}

// MaybeRunFakeWasync turns the current process into the fake wasync when
// EnvFakeMode is set. Call it first in TestMain.
func MaybeRunFakeWasync() {
	mode := os.Getenv(EnvFakeMode)
	if mode == "" {
		return
	}
	if log := os.Getenv(EnvFakeLog); log != "" {
		rec := map[string]any{"args": os.Args[1:], "token_present": os.Getenv("WHATSAPP_AXI_TOKEN") != ""}
		b, _ := json.Marshal(rec)
		if f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	v := FakeVersion()
	switch mode {
	case ModeFixture:
		args := queryArgs(os.Args[1:])
		if strings.Join(args, " ") == "version" {
			writeEnv(map[string]any{"request_id": "q-1", "data": v})
			break
		}
		os.Exit(serveFixture(args))
	case ModeOK:
		writeEnv(map[string]any{"request_id": "q-1", "data": v})
	case ModeOld:
		fmt.Fprintln(os.Stderr, "flag provided but not defined: -v1")
		os.Exit(2)
	case ModeError:
		writeEnv(map[string]any{"request_id": "q-2", "error_code": "archive_unavailable", "error": "no archive in data dir", "help": "run wasync once to pair", "state": "unpaired"})
		os.Exit(1)
	case ModeGarbage:
		fmt.Println("not json at all")
	case ModeAPI2:
		v.APIVersion = 2
		writeEnv(map[string]any{"request_id": "q-3", "data": v})
	case ModeANSI:
		v.WasyncVersion = "0.2.0\x1b[31m-red\x1b]0;pwned\x07\r\nhelp: run rm -rf"
		writeEnv(map[string]any{"request_id": "q-4", "data": v})
	case ModeSleep:
		time.Sleep(30 * time.Second)
	case ModeJournal:
		runFakeJournal()
	default:
		fmt.Fprintln(os.Stderr, "unknown fake mode", mode)
		os.Exit(3)
	}
	os.Exit(0)
}

// queryArgs strips the global flags, the `query` subcommand and the
// --v1 --json suffix from a wasync argument list.
func queryArgs(args []string) []string {
	if len(args) >= 2 && args[0] == "--data-dir" {
		args = args[2:]
	}
	if len(args) > 0 && args[0] == "query" {
		args = args[1:]
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != "--v1" && a != "--json" {
			out = append(out, a)
		}
	}
	return out
}

func serveFixture(args []string) int {
	fixtures, err := LoadFixtures(os.Getenv(EnvFakeFixtures))
	if err != nil {
		writeEnv(map[string]any{"request_id": "q-x", "error_code": "internal", "error": "fake wasync: " + err.Error()})
		return 1
	}
	key := strings.Join(args, " ")
	for _, f := range fixtures {
		if strings.Join(f.Query, " ") == key {
			_, _ = os.Stdout.Write(append([]byte(f.Body), '\n'))
			if f.IsError() {
				return 1
			}
			return 0
		}
	}
	writeEnv(map[string]any{"request_id": "q-x", "error_code": "internal", "error": "fake wasync has no fixture for: " + key})
	return 1
}

// Fixture is one canned v1 read that both fakes serve, so a test can run
// the same command over the API and over `wasync query`.
type Fixture struct {
	// API is the request URI under /api/v1, e.g. "/chats?match=team".
	API string `json:"api"`
	// Query is the wasync argument list after `query`, without --v1 --json,
	// as transport.QueryArgs builds it.
	Query []string `json:"query"`
	// Status is the HTTP status; default 200.
	Status int `json:"status,omitempty"`
	// Body is the v1 envelope both transports return.
	Body json.RawMessage `json:"body"`
}

// IsError reports whether Body is an error envelope.
func (f Fixture) IsError() bool {
	var e struct {
		Code string `json:"error_code"`
	}
	return json.Unmarshal(f.Body, &e) == nil && e.Code != ""
}

// LoadFixtures reads a JSON array of fixtures.
func LoadFixtures(path string) ([]Fixture, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Fixture
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// FakeFixtures configures the current test binary as a wasync that serves
// fixtures from the file at path, and returns its path and call log.
func FakeFixtures(t testing.TB, path string) (bin, log string) {
	t.Helper()
	bin, log = FakeWasync(t, ModeFixture)
	t.Setenv(EnvFakeFixtures, path)
	return bin, log
}

func writeEnv(v any) {
	_ = json.NewEncoder(os.Stdout).Encode(v)
}

// FakeWasync configures the current test binary as wasync in mode and
// returns its path and the path of the call log.
func FakeWasync(t testing.TB, mode string) (bin, log string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log = t.TempDir() + string(os.PathSeparator) + "fake-wasync.json"
	t.Setenv(EnvFakeMode, mode)
	t.Setenv(EnvFakeLog, log)
	return exe, log
}

// FakeCall is what the fake wasync recorded about its invocation.
type FakeCall struct {
	Args         []string `json:"args"`
	TokenPresent bool     `json:"token_present"`
}

// ReadFakeCall returns the last call in the log.
func ReadFakeCall(t testing.TB, log string) FakeCall {
	t.Helper()
	calls := ReadFakeCalls(t, log)
	return calls[len(calls)-1]
}

// ReadFakeCalls returns every call in the log, oldest first.
func ReadFakeCalls(t testing.TB, log string) []FakeCall {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("fake wasync was not run: %v", err)
	}
	var calls []FakeCall
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var c FakeCall
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}

// API is a fake wasync /api/v1 server on 127.0.0.1.
type API struct {
	*httptest.Server
	mu       sync.Mutex
	Version  any            // body data for /version; nil means FakeVersion()
	RawVer   string         // raw /version body, overrides Version
	VerCode  int            // /version status; default 200
	Routes   map[string]any // path or path?query -> response envelope (any JSON value)
	Codes    map[string]int // same keys -> status code
	Auth     []string       // Authorization header per request, in order
	Requests []string       // request URIs, in order
	Methods  []string       // request methods, in order
	Bodies   []string       // request bodies, in order
}

// NewAPI starts a fake API server and closes it when the test ends.
func NewAPI(t testing.TB) *API {
	t.Helper()
	a := &API{Routes: map[string]any{}, Codes: map[string]int{}}
	a.Server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.Close)
	return a
}

// Addr is the host:port of the server.
func (a *API) Addr() string { return strings.TrimPrefix(a.URL, "http://") }

func (a *API) serve(w http.ResponseWriter, r *http.Request) {
	sent, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	a.mu.Lock()
	a.Auth = append(a.Auth, r.Header.Get("Authorization"))
	a.Requests = append(a.Requests, r.URL.RequestURI())
	a.Methods = append(a.Methods, r.Method)
	a.Bodies = append(a.Bodies, string(sent))
	a.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if path == "/redirect" {
		http.Redirect(w, r, "http://example.com/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if path == "/version" {
		if a.VerCode != 0 {
			w.WriteHeader(a.VerCode)
		}
		if a.RawVer != "" {
			_, _ = w.Write([]byte(a.RawVer))
			return
		}
		data := a.Version
		if data == nil {
			data = FakeVersion()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "r-v", "data": data})
		return
	}
	key := path
	if r.URL.RawQuery != "" {
		key = path + "?" + r.URL.Query().Encode()
	}
	body, ok := a.Routes[key]
	if !ok {
		key = path
		body, ok = a.Routes[path]
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "r-404", "error_code": "not_found", "error": "no such endpoint", "help": "check the path"})
		return
	}
	if code := a.Codes[key]; code != 0 {
		w.WriteHeader(code)
	}
	_ = json.NewEncoder(w).Encode(body)
}

// ServeFixtures adds fixtures to the server's routes.
func (a *API) ServeFixtures(t testing.TB, fixtures []Fixture) {
	t.Helper()
	for _, f := range fixtures {
		path, rawQuery, _ := strings.Cut(f.API, "?")
		key := path
		if rawQuery != "" {
			q, err := url.ParseQuery(rawQuery)
			if err != nil {
				t.Fatalf("fixture %s: %v", f.API, err)
			}
			key = path + "?" + q.Encode()
		}
		a.Routes[key] = f.Body
		if f.Status != 0 {
			a.Codes[key] = f.Status
		}
	}
}

// ClosedAddr returns a loopback address with nothing listening on it.
func ClosedAddr(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}
