package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/golden"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

type liveStatus struct {
	mu sync.Mutex
	s  whatsapp.CaptureStatus
}

func (l *liveStatus) get() whatsapp.CaptureStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s
}
func (l *liveStatus) set(s whatsapp.CaptureStatus) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.s = s
}

// withStatus serves the fixture's archive with a live owner status.
func (f fixture) withStatus(t *testing.T, status func() whatsapp.CaptureStatus) fixture {
	t.Helper()
	srv, err := Start("127.0.0.1:0", f.dir, f.store, status)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
	})
	f.server = srv
	return f
}

func grantAgent(t *testing.T, dir, name string, scopes ...string) string {
	t.Helper()
	credential, _, err := agentauth.Grant(dir, agentauth.GrantRequest{Name: name, Scopes: scopes}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func decodeV1[T any](t *testing.T, body []byte) T {
	t.Helper()
	var env struct {
		RequestID string `json:"request_id"`
		Data      T      `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil || !requestIDPattern.MatchString(env.RequestID) {
		t.Fatalf("bad envelope %v: %s", err, body)
	}
	return env.Data
}

func TestV1HealthDeepStateMapping(t *testing.T) {
	f := setup(t)
	live := &liveStatus{}
	g := f.withStatus(t, live.get)
	now := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	stale := now.Add(-3 * time.Minute)
	for _, tc := range []struct {
		connection string
		failing    bool
		state      string
		connected  bool
		errText    string
		captureErr string
	}{
		{"online", false, "online", true, "", ""},
		{"online", true, "degraded", false, "", ""},
		{"connecting", false, "connecting", false, "", ""},
		{"reconnecting", true, "reconnecting", false, "", ""},
		{"pairing", false, "pairing", false, "", ""},
		{"offline", false, "offline", false, "", ""},
		{"attention required", false, "attention_required", false, whatsapp.ErrUnlinked.Error(), "WhatsApp rejected the session; check the phone. If it unlinked this device, run wasync login to link again"},
		{"capture stopped", false, "capture_stopped", false, "Archive operation failed. Capture stopped; check disk space and restart.", "archive operation failed; capture stopped"},
		{"unlinked", false, "unlinked", false, "", ""},
		{"online", false, "online", true, "History batch had 3 unparseable messages; coverage remains uncertain.", "capture reported a problem; see the wasync terminal"},
	} {
		live.set(whatsapp.CaptureStatus{Connection: tc.connection, Error: tc.errText, KeepAliveFailing: tc.failing, LastEventAt: now, LastConnectedAt: stale, LastKeepAliveOK: stale, Paired: true})
		res, body := g.v1(t, "GET", "/api/v1/health/deep", f.agent)
		if res.StatusCode != 200 {
			t.Fatalf("%+v: %d %s", tc, res.StatusCode, body)
		}
		d := decodeV1[query.HealthDeep](t, body)
		if d.State != tc.state || d.Connected != tc.connected || !d.OwnerRunning || d.KeepAliveFailing != tc.failing || !d.ArchiveReady {
			t.Fatalf("%+v: got %s", tc, body)
		}
		if d.LastEventAt == nil || !d.LastEventAt.Equal(now) || d.LastKeepAliveOKAt == nil || !d.LastKeepAliveOKAt.Equal(stale) || d.LastConnectedAt == nil {
			t.Fatalf("%+v: timestamps %s", tc, body)
		}
		if got := ""; d.CaptureError != nil {
			got = *d.CaptureError
			if got != tc.captureErr {
				t.Fatalf("%+v: capture error %q", tc, got)
			}
		} else if tc.captureErr != "" {
			t.Fatalf("%+v: capture error missing", tc)
		}
		if strings.Contains(string(body), "unparseable") || strings.Contains(string(body), "disk space") || strings.Contains(string(body), "hello archive") {
			t.Fatalf("health leaked details: %s", body)
		}
	}
}

func TestV1HealthDeepOfflineServe(t *testing.T) {
	f := setup(t) // status func nil, as with serve --offline
	ctx := context.Background()
	if err := f.store.Gap(ctx, f.account, "network disconnected; recovery unverified"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.QueueHistory(ctx, f.account, "one@g.us"); err != nil {
		t.Fatal(err)
	}
	res, body := f.v1(t, "GET", "/api/v1/health/deep", f.agent)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	d := decodeV1[query.HealthDeep](t, body)
	if d.State != "offline" || d.Connected || !d.OwnerRunning || !d.ArchiveReady || d.SchemaVersion != storage.SchemaVersion || d.OpenGaps != 1 || d.HistoryJobsActive < 1 || d.LastEventAt != nil || d.CaptureError != nil {
		t.Fatalf("offline health %s", body)
	}
	if err := f.store.Resume(ctx, f.account); err != nil {
		t.Fatal(err)
	}
	_, body = f.v1(t, "GET", "/api/v1/health/deep", f.agent)
	if d = decodeV1[query.HealthDeep](t, body); d.OpenGaps != 0 {
		t.Fatalf("closed gap still open: %s", body)
	}
}

func TestV1HealthAndDiagnosticsAuthorization(t *testing.T) {
	f := setup(t)
	observer := grantAgent(t, f.dir, "observer", agentauth.ScopeObserve)
	for _, path := range []string{"/api/v1/health/deep", "/api/v1/diagnostics"} {
		for _, token := range []string{"", "wrong", f.token} {
			res, body := f.v1(t, "GET", path, token)
			var e query.V1Error
			if res.StatusCode != 401 || json.Unmarshal(body, &e) != nil || e.ErrorCode != query.CodeUnauthorized {
				t.Fatalf("%s %q: %d %s", path, token, res.StatusCode, body)
			}
		}
	}
	res, body := f.v1(t, "GET", "/api/v1/health/deep", observer)
	var e query.V1Error
	if res.StatusCode != 403 || json.Unmarshal(body, &e) != nil || e.ErrorCode != query.CodeForbidden || !strings.Contains(e.Help, "read.chats") {
		t.Fatalf("observer read health: %d %s", res.StatusCode, body)
	}
	res, body = f.v1(t, "GET", "/api/v1/diagnostics", observer)
	if res.StatusCode != 200 {
		t.Fatalf("diagnostics needs only authentication: %d %s", res.StatusCode, body)
	}
	d := decodeV1[query.Diagnostics](t, body)
	if d.Agent == nil || d.Agent.Name != "observer" || len(d.Agent.Scopes) != 1 || d.Agent.Scopes[0] != "observe" {
		t.Fatalf("caller agent %s", body)
	}
	var agentCheck query.DiagnosticCheck
	for _, c := range d.Checks {
		if c.Name == "agent" {
			agentCheck = c
		}
	}
	if agentCheck.Status != "warn" || agentCheck.Fix != "wasync agents revoke observer && wasync agents grant observer --scope read.chats,read.messages,observe" {
		t.Fatalf("agent check %+v", agentCheck)
	}
	res, _ = f.v1(t, "POST", "/api/v1/health/deep", f.agent)
	if res.StatusCode == 200 {
		t.Fatal("POST accepted")
	}
}

func TestV1DiagnosticsOwnerFacts(t *testing.T) {
	f := setup(t)
	// serve --offline: no client; pairing comes from the session store read-only.
	before := dirEntryNames(t, f.dir)
	res, body := f.v1(t, "GET", "/api/v1/diagnostics", f.agent)
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	d := decodeV1[query.Diagnostics](t, body)
	if d.Transport != "api" || !d.Owner.Running || d.Owner.APIVersion == nil || *d.Owner.APIVersion != 1 || d.Health != "offline" || d.Paired == nil || *d.Paired || !d.DataDirPrivate || !d.AgentsRegistryPrivate {
		t.Fatalf("offline owner diagnostics %s", body)
	}
	if !d.Archive.Exists || !d.Archive.Readable || d.Archive.SchemaVersion == nil || *d.Archive.SchemaVersion != d.Archive.SupportedSchemaVersion {
		t.Fatalf("archive facts %s", body)
	}
	if after := dirEntryNames(t, f.dir); strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatalf("diagnostics created files: %v -> %v", before, after)
	}
	live := &liveStatus{}
	live.set(whatsapp.CaptureStatus{Connection: "online", KeepAliveFailing: true, Paired: true})
	g := f.withStatus(t, live.get)
	_, body = g.v1(t, "GET", "/api/v1/diagnostics", f.agent)
	d = decodeV1[query.Diagnostics](t, body)
	if d.Health != "degraded" || d.Paired == nil || !*d.Paired {
		t.Fatalf("live diagnostics %s", body)
	}
	for _, c := range d.Checks {
		if c.Status != "pass" && c.Status != "warn" && c.Status != "fail" || c.Name == "" || c.Detail == "" || c.Status != "pass" && c.Fix == "" && c.Name != "health" {
			t.Fatalf("bad check %+v", c)
		}
	}
}

func TestV1DiagnosticsNeverLeaksSecrets(t *testing.T) {
	f := setup(t)
	registry, err := os.ReadFile(filepath.Join(f.dir, agentauth.FileName))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Clients []struct {
			ID     string `json:"id"`
			Secret string `json:"secret_sha256"`
		} `json:"clients"`
	}
	if err = json.Unmarshal(registry, &reg); err != nil || len(reg.Clients) == 0 {
		t.Fatal(err)
	}
	_, secret, err := agentauth.ParseCredential(f.agent)
	if err != nil {
		t.Fatal(err)
	}
	live := &liveStatus{}
	live.set(whatsapp.CaptureStatus{Connection: "attention required", Error: "WhatsApp session replaced; stop any other client using this device store", Paired: true})
	g := f.withStatus(t, live.get)
	for _, path := range []string{"/api/v1/diagnostics", "/api/v1/health/deep"} {
		_, body := g.v1(t, "GET", path, f.agent)
		text := string(body)
		for _, forbidden := range []string{f.agent, secret, f.token, reg.Clients[0].Secret, reg.Clients[0].ID, "secret", "sha256", "hello archive", "test@lid", "@g.us", f.dir, "session replaced"} {
			if forbidden != "" && strings.Contains(text, forbidden) {
				t.Fatalf("%s leaked %q: %s", path, forbidden, text)
			}
		}
	}
}

func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestV1HealthSnapshots(t *testing.T) {
	f := setup(t)
	observer := grantAgent(t, f.dir, "observer", agentauth.ScopeObserve)
	live := &liveStatus{}
	stamp := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	live.set(whatsapp.CaptureStatus{Connection: "online", KeepAliveFailing: true, LastEventAt: stamp, LastConnectedAt: stamp.Add(-time.Hour), LastKeepAliveOK: stamp.Add(-2 * time.Minute), Paired: true})
	g := f.withStatus(t, live.get)
	type call struct {
		f           fixture
		label, path string
		token, auth string
	}
	calls := []call{
		{f, "serve --offline", "/api/v1/health/deep", f.agent, "agent"},
		{g, "keepalive failing", "/api/v1/health/deep", f.agent, "agent"},
		{g, "keepalive failing", "/api/v1/health/deep", observer, "agent without read.chats"},
		{g, "keepalive failing", "/api/v1/health/deep", "", "none"},
		{f, "serve --offline", "/api/v1/diagnostics", f.agent, "agent"},
		{g, "keepalive failing", "/api/v1/diagnostics", observer, "agent without read.chats"},
		{g, "keepalive failing", "/api/v1/diagnostics", f.token, "api.token"},
	}
	snapshots := make([]any, 0, len(calls))
	for _, c := range calls {
		res, body := c.f.v1(t, "GET", c.path, c.token)
		snapshots = append(snapshots, map[string]any{
			"request": "GET " + c.path,
			"owner":   c.label,
			"auth":    c.auth,
			"status":  res.StatusCode,
			"body":    redactDiagnostics(golden.Decode(t, body)),
		})
	}
	golden.Compare(t, "v1/health.json", snapshots)
}

// redactDiagnostics hides build- and platform-dependent values, schema
// numbers that advance with migrations, and the journal facts that depend on
// which features this build registers.
func redactDiagnostics(v any) any {
	v = golden.Redact(v, "request_id", "wasync_version", "os", "arch", "journal_available", "schema_version", "supported_schema_version")
	data, _ := v.(map[string]any)["data"].(map[string]any)
	checks, _ := data["checks"].([]any)
	for _, c := range checks {
		check := c.(map[string]any)
		switch check["name"] {
		case "version":
			check["detail"] = "<detail>"
		case "journal":
			check["status"], check["detail"], check["fix"] = "<status>", "<detail>", "<fix>"
		}
	}
	return v
}
