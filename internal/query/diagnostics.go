package query

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/buildinfo"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

// Health states reported by GET /api/v1/health/deep and diagnostics.
const (
	StateOnline            = "online"
	StateConnecting        = "connecting"
	StateReconnecting      = "reconnecting"
	StatePairing           = "pairing"
	StateOffline           = "offline"
	StateAttentionRequired = "attention_required"
	StateCaptureStopped    = "capture_stopped"
	StateUnlinked          = "unlinked"
	StateDegraded          = "degraded"
	// StateUnknown appears only in offline diagnostics, when another process
	// owns the device but its live state cannot be read without the API.
	StateUnknown = "unknown"
)

// HealthState maps an owner connection label to a v1 state. A failing
// keepalive while nominally online is degraded: connected is true only for
// online with a working keepalive, never from a stale heartbeat.
func HealthState(connection string, keepAliveFailing bool) (state string, connected bool) {
	switch connection {
	case "online":
		if keepAliveFailing {
			return StateDegraded, false
		}
		return StateOnline, true
	case "connecting":
		return StateConnecting, false
	case "reconnecting":
		return StateReconnecting, false
	case "pairing":
		return StatePairing, false
	case "attention required":
		return StateAttentionRequired, false
	case "capture stopped":
		return StateCaptureStopped, false
	case "unlinked":
		return StateUnlinked, false
	}
	return StateOffline, false
}

// CaptureErrorSummary replaces owner error text with a fixed, generic
// sentence so health output never carries details, identifiers or bodies.
func CaptureErrorSummary(state, detail string) *string {
	var s string
	switch {
	case state == StateCaptureStopped:
		s = "archive operation failed; capture stopped"
	case state == StateAttentionRequired:
		s = "WhatsApp rejected the session; check the phone"
	case detail != "":
		s = "capture reported a problem; see the wasync terminal"
	default:
		return nil
	}
	return &s
}

// HealthDeep is the data of GET /api/v1/health/deep.
type HealthDeep struct {
	State             string     `json:"state"`
	OwnerRunning      bool       `json:"owner_running"`
	Connected         bool       `json:"connected"`
	LastEventAt       *time.Time `json:"last_event_at"`
	LastConnectedAt   *time.Time `json:"last_connected_at"`
	LastKeepAliveOKAt *time.Time `json:"last_keepalive_ok_at"`
	KeepAliveFailing  bool       `json:"keepalive_failing"`
	ArchiveReady      bool       `json:"archive_ready"`
	SchemaVersion     int        `json:"schema_version"`
	OpenGaps          int64      `json:"open_gaps"`
	HistoryJobsActive int64      `json:"history_jobs_active"`
	CaptureError      *string    `json:"capture_error"`
}

// TimePointer returns nil for a zero time, otherwise the UTC time.
func TimePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// Diagnostics is the data of GET /api/v1/diagnostics and wasync query doctor.
// It never carries credentials, hashes, paths or message content.
type Diagnostics struct {
	Transport             string            `json:"transport"`
	WasyncVersion         string            `json:"wasync_version"`
	Distribution          string            `json:"distribution"`
	OS                    string            `json:"os"`
	Arch                  string            `json:"arch"`
	DataDirPrivate        bool              `json:"data_dir_private"`
	Archive               DiagnosticArchive `json:"archive"`
	Paired                *bool             `json:"paired"`
	Owner                 DiagnosticOwner   `json:"owner"`
	Health                string            `json:"health"`
	Agent                 *DiagnosticAgent  `json:"agent"`
	AgentsRegistryPrivate bool              `json:"agents_registry_private"`
	JournalAvailable      bool              `json:"journal_available"`
	Checks                []DiagnosticCheck `json:"checks"`
}

type DiagnosticArchive struct {
	Exists                 bool `json:"exists"`
	SchemaVersion          *int `json:"schema_version"`
	SupportedSchemaVersion int  `json:"supported_schema_version"`
	Readable               bool `json:"readable"`
}

type DiagnosticOwner struct {
	Running    bool `json:"running"`
	APIVersion *int `json:"api_version"`
}

// DiagnosticAgent describes the calling agent client; never its secret.
type DiagnosticAgent struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	Chats     []string   `json:"chats"` // pre-approved and human-approved chats
	OnDemand  bool       `json:"on_demand"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type DiagnosticCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass, warn or fail
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// DiagnosticsInput holds the facts only the caller knows: whether it is the
// owner, the owner's live state and pairing, and the authenticated agent.
type DiagnosticsInput struct {
	Transport    string // "api" or "query"
	DataDir      string
	OwnerRunning bool
	APIVersion   *int
	Health       string
	Paired       *bool
	Agent        *agentauth.Client // nil offline
}

// Diagnose gathers local facts for doctor without creating or changing files
// and without any network access.
func Diagnose(ctx context.Context, in DiagnosticsInput) Diagnostics {
	d := Diagnostics{
		Transport: in.Transport, WasyncVersion: buildinfo.Version, Distribution: buildinfo.Distribution,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Paired: in.Paired, Health: in.Health,
		Owner:            DiagnosticOwner{Running: in.OwnerRunning, APIVersion: in.APIVersion},
		Archive:          DiagnosticArchive{SupportedSchemaVersion: storage.SchemaVersion},
		JournalAvailable: slices.Contains(Features, "changes"),
	}
	dirErr := localfs.CheckPrivateDir(in.DataDir)
	dirMissing := errors.Is(dirErr, os.ErrNotExist)
	d.DataDirPrivate = dirErr == nil
	probe, archiveErr := storage.ProbeArchive(ctx, filepath.Join(in.DataDir, "archive.db"))
	d.Archive.Exists = probe.Exists
	d.Archive.Readable = archiveErr == nil && probe.Readable
	if probe.Exists && archiveErr == nil {
		v := probe.SchemaVersion
		d.Archive.SchemaVersion = &v
	}
	registryErr := checkRegistry(in.DataDir)
	d.AgentsRegistryPrivate = registryErr == nil
	if in.Agent != nil {
		d.Agent = &DiagnosticAgent{Name: in.Agent.Name, Scopes: nonNil(in.Agent.Scopes), Chats: nonNil(in.Agent.AllowedChats()), OnDemand: in.Agent.OnDemand, ExpiresAt: in.Agent.ExpiresAt}
	}

	add := func(name, status, detail, fix string) {
		d.Checks = append(d.Checks, DiagnosticCheck{Name: name, Status: status, Detail: detail, Fix: fix})
	}
	add("version", "pass", "wasync "+buildinfo.Version+" ("+buildinfo.Distribution+", "+runtime.GOOS+"/"+runtime.GOARCH+")", "")
	switch {
	case dirMissing:
		add("data_dir", "fail", "data directory does not exist", "wasync login")
	case dirErr != nil:
		add("data_dir", "fail", "data directory is not private to the current user", privateDirFix())
	default:
		add("data_dir", "pass", "data directory is private", "")
	}
	switch {
	case !probe.Exists:
		add("archive", "fail", "no archive yet", "wasync login")
	case archiveErr != nil:
		add("archive", "fail", "archive cannot be read", "wasync serve")
	case probe.SchemaVersion > storage.SchemaVersion && !probe.Readable:
		add("archive", "fail", "archive schema is newer than this wasync supports", UpgradeFix())
	case probe.SchemaVersion < storage.SchemaVersion:
		add("archive", "warn", "archive schema is older and will be migrated when the owner starts", "wasync serve")
	default:
		add("archive", "pass", "archive schema supported", "")
	}
	switch {
	case in.Paired == nil:
		add("pairing", "warn", "pairing state unknown", "wasync login")
	case *in.Paired:
		add("pairing", "pass", "a WhatsApp session is paired", "")
	default:
		add("pairing", "fail", "no paired WhatsApp session", "wasync login")
	}
	if in.OwnerRunning {
		add("owner", "pass", "a wasync process owns the data directory", "")
	} else {
		add("owner", "warn", "no wasync process is running; reads fall back to wasync query", "wasync serve")
	}
	status, detail, fix := healthCheck(in.Health)
	add("health", status, detail, fix)
	switch {
	case registryErr != nil:
		add("agents_registry", "fail", "agents.json is not a private file", privateFileFix(agentauth.FileName))
	default:
		add("agents_registry", "pass", "agent registry is private", "")
	}
	status, detail, fix = agentCheck(in)
	add("agent", status, detail, fix)
	if d.JournalAvailable {
		add("journal", "pass", "change journal available", "")
	} else {
		add("journal", "warn", "this wasync has no change journal; observe is unavailable", UpgradeFix())
	}
	return d
}

func nonNil(s []string) []string {
	if len(s) == 0 {
		return []string{}
	}
	return append([]string(nil), s...)
}

// checkRegistry accepts an absent registry (no agents granted yet).
func checkRegistry(dir string) error {
	path := filepath.Join(dir, agentauth.FileName)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return localfs.CheckPrivateFile(path)
}

func healthCheck(state string) (status, detail, fix string) {
	switch state {
	case StateOnline:
		return "pass", "connected to WhatsApp", ""
	case StateConnecting, StateReconnecting:
		return "warn", "connecting to WhatsApp", ""
	case StateDegraded:
		return "warn", "keepalive failing; connection is not confirmed", ""
	case StatePairing:
		return "warn", "pairing in progress", "wasync login"
	case StateAttentionRequired, StateUnlinked:
		return "fail", "WhatsApp session needs attention", "wasync login"
	case StateCaptureStopped:
		return "fail", "capture stopped after an archive failure; restart wasync", "wasync serve"
	case StateUnknown:
		return "warn", "owner running; live state is only available from GET /api/v1/health/deep", ""
	}
	return "warn", "not connected to WhatsApp", "wasync serve"
}

var neededScopes = []string{agentauth.ScopeReadChats, agentauth.ScopeReadMessages, agentauth.ScopeObserve}

func agentCheck(in DiagnosticsInput) (status, detail, fix string) {
	grant := "wasync agents grant NAME --scope " + strings.Join(neededScopes, ",")
	if in.Agent == nil {
		clients, err := agentauth.List(in.DataDir)
		if err != nil {
			return "fail", "agent registry cannot be read", privateFileFix(agentauth.FileName)
		}
		for _, c := range clients {
			if c.Status(time.Now()) == "active" {
				return "pass", "at least one active agent credential exists", ""
			}
		}
		return "warn", "no active agent credential", grant
	}
	var missing []string
	for _, s := range neededScopes {
		if !in.Agent.HasScope(s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return "warn", "agent lacks " + strings.Join(missing, ","), agentauth.RegrantCommand(in.Agent, missing...)
	}
	return "pass", "agent credential valid", ""
}

// UpgradeFix is the upgrade command for this distribution.
func UpgradeFix() string {
	switch {
	case runtime.GOOS == "darwin":
		return "brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git && brew upgrade --cask gehariharan/tap/wasync"
	case buildinfo.Distribution == "msix":
		return "update wasync from the Microsoft Store"
	}
	return "install the latest wasync release"
}

func privateDirFix() string {
	if runtime.GOOS == "windows" {
		return "restrict the wasync data directory to your user account"
	}
	return "chmod 700 DATA_DIR"
}

func privateFileFix(name string) string {
	if runtime.GOOS == "windows" {
		return "restrict " + name + " in the wasync data directory to your user account"
	}
	return "chmod 600 DATA_DIR/" + name
}
