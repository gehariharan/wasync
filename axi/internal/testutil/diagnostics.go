package testutil

import (
	"encoding/json"
	"os"
	"testing"
)

// EnvFakeDiagnostics names a file whose contents the fake wasync prints for
// `wasync query doctor`. Other commands keep the EnvFakeMode behaviour.
const EnvFakeDiagnostics = "WASYNC_AXI_FAKE_WASYNC_DIAGNOSTICS"

// MaybeRunFakeDoctor answers `wasync query doctor` when the current process
// is the fake wasync and EnvFakeDiagnostics is set. Call it before
// MaybeRunFakeWasync (an init function in a _test.go file runs first).
func MaybeRunFakeDoctor() {
	path := os.Getenv(EnvFakeDiagnostics)
	if os.Getenv(EnvFakeMode) == "" || path == "" {
		return
	}
	isDoctor := false
	for _, a := range os.Args[1:] {
		if a == "doctor" {
			isDoctor = true
		}
	}
	if !isDoctor {
		return
	}
	if log := os.Getenv(EnvFakeLog); log != "" {
		rec := map[string]any{"args": os.Args[1:], "token_present": os.Getenv("WASYNC_AXI_TOKEN") != ""}
		b, _ := json.Marshal(rec)
		_ = os.WriteFile(log+".doctor", b, 0o600)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		os.Exit(3)
	}
	_, _ = os.Stdout.Write(b)
	os.Exit(0)
}

// FakeDoctorEnvelope makes the fake wasync answer `query doctor` with
// envelope and returns the path of its call log.
func FakeDoctorEnvelope(t testing.TB, log string, envelope any) string {
	t.Helper()
	b, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + string(os.PathSeparator) + "diagnostics.json"
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvFakeDiagnostics, path)
	return log + ".doctor"
}

// DiagnosticsCheck is one wasync diagnostics check.
type DiagnosticsCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// DiagnosticsAgent is the agent block of wasync diagnostics.
type DiagnosticsAgent struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	Chats       []string `json:"chats"`
	SyncedChats bool     `json:"synced_chats,omitempty"`
	ExpiresAt   *string  `json:"expires_at"`
}

// Diagnostics is the data of GET /api/v1/diagnostics and `wasync query
// doctor --v1`, shaped like wasync's snapshots.
type Diagnostics struct {
	Transport             string             `json:"transport"`
	WasyncVersion         string             `json:"wasync_version"`
	Distribution          string             `json:"distribution"`
	OS                    string             `json:"os"`
	Arch                  string             `json:"arch"`
	DataDirPrivate        bool               `json:"data_dir_private"`
	Archive               map[string]any     `json:"archive"`
	Paired                *bool              `json:"paired"`
	Owner                 map[string]any     `json:"owner"`
	Health                string             `json:"health"`
	Agent                 *DiagnosticsAgent  `json:"agent"`
	AgentsRegistryPrivate bool               `json:"agents_registry_private"`
	JournalAvailable      bool               `json:"journal_available"`
	Checks                []DiagnosticsCheck `json:"checks"`
}

// SetCheck replaces the named check.
func (d *Diagnostics) SetCheck(name, status, detail, fix string) {
	for i := range d.Checks {
		if d.Checks[i].Name == name {
			d.Checks[i] = DiagnosticsCheck{name, status, detail, fix}
			return
		}
	}
	d.Checks = append(d.Checks, DiagnosticsCheck{name, status, detail, fix})
}

// FakeDiagnostics returns all-pass diagnostics. transport "api" includes an
// agent with read.chats, read.messages and observe; "query" has no agent,
// no owner and the offline warnings wasync reports.
func FakeDiagnostics(transport string) *Diagnostics {
	paired := true
	api := 1
	d := &Diagnostics{
		Transport: transport, WasyncVersion: "0.2.0-test", Distribution: "standalone", OS: "linux", Arch: "amd64",
		DataDirPrivate: true, Archive: map[string]any{"exists": true, "schema_version": 6, "supported_schema_version": 6, "readable": true},
		Paired: &paired, Owner: map[string]any{"running": true, "api_version": api}, Health: "online",
		Agent:                 &DiagnosticsAgent{Name: "test-agent", Scopes: []string{"read.chats", "read.messages", "observe"}, Chats: []string{}},
		AgentsRegistryPrivate: true, JournalAvailable: true,
		Checks: []DiagnosticsCheck{
			{"version", "pass", "wasync 0.2.0-test (standalone, linux/amd64)", ""},
			{"data_dir", "pass", "data directory is private", ""},
			{"archive", "pass", "archive schema supported", ""},
			{"pairing", "pass", "a WhatsApp session is paired", ""},
			{"owner", "pass", "a wasync process owns the data directory", ""},
			{"health", "pass", "connected to WhatsApp", ""},
			{"agents_registry", "pass", "agent registry is private", ""},
			{"agent", "pass", "agent credential valid", ""},
			{"journal", "pass", "change journal available", ""},
		},
	}
	if transport == "query" {
		d.Agent = nil
		d.Owner = map[string]any{"running": false, "api_version": nil}
		d.Health = "offline"
		d.SetCheck("owner", "warn", "no wasync process is running; reads fall back to wasync query", "wasync serve")
		d.SetCheck("health", "warn", "not connected to WhatsApp", "wasync serve")
		d.SetCheck("agent", "pass", "at least one active agent credential exists", "")
	}
	return d
}

// Envelope wraps data in a v1 success envelope.
func Envelope(data any) map[string]any {
	return map[string]any{"request_id": "r-test", "data": data}
}
