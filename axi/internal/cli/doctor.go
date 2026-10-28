package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/axi/internal/buildinfo"
	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/installhint"
	"github.com/gehariharan/wasync/axi/internal/output"
	"github.com/gehariharan/wasync/axi/internal/transport"
)

// errReported tells Run that the command already printed its result and
// only the exit code (1) is left to set. doctor uses it for failed checks.
var errReported = errors.New("result already reported")

// Check statuses. skip means the check could not run here (no facts, not
// applicable on this OS, or not implemented yet); it never fails doctor.
const (
	statusPass = "pass"
	statusWarn = "warn"
	statusFail = "fail"
	statusSkip = "skip"
)

// Check areas, in display order (ADR-001 D-5).
const (
	areaInstallation  = "installation"
	areaService       = "service"
	areaPairing       = "pairing"
	areaPermissions   = "permissions"
	areaCompatibility = "compatibility"
	areaHooks         = "hooks"
	areaOther         = "wasync"
)

var areaOrder = []string{areaInstallation, areaService, areaPairing, areaPermissions, areaCompatibility, areaHooks, areaOther}

// Check sources.
const (
	sourceAXI    = "whatsapp-axi"
	sourceWasync = "wasync"
)

// wasyncCheckArea places wasync's own checks (GET /api/v1/diagnostics,
// `wasync query doctor`) in doctor's areas. Unknown names go to areaOther.
var wasyncCheckArea = map[string]string{
	"version":         areaInstallation,
	"data_dir":        areaInstallation,
	"archive":         areaInstallation,
	"pairing":         areaPairing,
	"owner":           areaService,
	"health":          areaService,
	"agents_registry": areaPermissions,
	"agent":           areaPermissions,
	"journal":         areaCompatibility,
}

// Agent scopes whatsapp-axi commands use through the API, in wasync's
// canonical order. Granting is always a human action in wasync.
const (
	scopeReadChats    = "read.chats"
	scopeReadMessages = "read.messages"
	scopeObserve      = "observe"
)

var scopeOrder = []string{scopeReadChats, scopeReadMessages, "read.media", scopeObserve, "send", "react", "edit.own", "chat.state", "group.admin", "capture.manage", "export"}

// scopeUses names what each scope enables, for the scopes check.
var scopeUses = []struct{ scope, use string }{
	{scopeReadChats, "chat list and status"},
	{scopeReadMessages, "messages, search and threads"},
	{scopeObserve, "observe"},
}

// defaultAgentName is suggested when no agent is known yet.
const defaultAgentName = "whatsapp-axi"

var (
	upgradeWasyncFix = installhint.UpgradeCore()
	installWasyncFix = installhint.InstallCore()
)

const upgradeAXIFix = "go install github.com/gehariharan/wasync/axi/cmd/whatsapp-axi@latest"

type doctorCheck struct {
	Area   string `json:"area"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Source string `json:"source"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// diagnostics mirrors the data of GET /api/v1/diagnostics and `wasync query
// doctor --v1`. Decoding into this type drops any field wasync may add, so
// only known, secret-free facts are passed through.
type diagnostics struct {
	Transport      string `json:"transport"`
	WasyncVersion  string `json:"wasync_version"`
	Distribution   string `json:"distribution"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	DataDirPrivate bool   `json:"data_dir_private"`
	Archive        struct {
		Exists                 bool `json:"exists"`
		SchemaVersion          *int `json:"schema_version"`
		SupportedSchemaVersion int  `json:"supported_schema_version"`
		Readable               bool `json:"readable"`
	} `json:"archive"`
	Paired *bool `json:"paired"`
	Owner  struct {
		Running    bool `json:"running"`
		APIVersion *int `json:"api_version"`
	} `json:"owner"`
	Health                string            `json:"health"`
	Agent                 *diagnosticsAgent `json:"agent"`
	AgentsRegistryPrivate bool              `json:"agents_registry_private"`
	JournalAvailable      bool              `json:"journal_available"`
	Checks                []wasyncCheck     `json:"checks"`
}

type diagnosticsAgent struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	Chats     []string   `json:"chats"`
	OnDemand  bool       `json:"on_demand"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type wasyncCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// deepHealth is the part of GET /api/v1/health/deep doctor shows.
type deepHealth struct {
	State            string     `json:"state"`
	Connected        bool       `json:"connected"`
	LastEventAt      *time.Time `json:"last_event_at"`
	KeepAliveFailing bool       `json:"keepalive_failing"`
	ArchiveReady     bool       `json:"archive_ready"`
	OpenGaps         int64      `json:"open_gaps"`
	CaptureError     *string    `json:"capture_error"`
}

// facts is what doctor learned from wasync.
type facts struct {
	diag      *diagnostics
	health    *deepHealth
	transport string // transport that supplied diag: api, query or none
	apiErr    error  // why GET /api/v1/diagnostics was not used
	err       error  // why no facts were obtained (diag == nil)
}

type doctorSummary struct {
	Result string `json:"result"` // pass, warn or fail
	Pass   int    `json:"pass"`
	Warn   int    `json:"warn"`
	Fail   int    `json:"fail"`
	Skip   int    `json:"skip"`
}

type doctorReport struct {
	WhatsappAXIVersion string        `json:"whatsapp_axi_version"`
	Bin                string        `json:"bin"`
	Transport          string        `json:"transport"`
	TransportNote      string        `json:"transport_note,omitempty"`
	Summary            doctorSummary `json:"summary"`
	Checks             []doctorCheck `json:"checks"`
	Wasync             wasyncJSON    `json:"wasync"`
	Compatibility      compatJSON    `json:"compatibility"`
	Diagnostics        *diagnostics  `json:"diagnostics"`
	Health             *deepHealth   `json:"health"`
	Help               []string      `json:"help"`
}

func (a *App) runDoctor(ctx context.Context, inv invocation) error {
	d := a.detect(ctx, "doctor")
	f := a.gatherFacts(ctx, d)
	r := a.doctorReport(d, f)
	var err error
	if inv.json {
		err = output.JSON(a.Out, r)
	} else {
		err = a.writeDoctor(r)
	}
	if err != nil {
		return err
	}
	if r.Summary.Result == statusFail {
		return errReported
	}
	return nil
}

// gatherFacts asks wasync for its diagnostics: through the API when it was
// selected and a credential is configured, otherwise (or when the API call
// fails) through offline `wasync query doctor`.
func (a *App) gatherFacts(ctx context.Context, d detection) facts {
	f := facts{transport: "none"}
	if d.sel.Transport == nil {
		f.err = firstErr(d.sel.Incompatible, d.sel.QueryErr, d.binErr, d.sel.APIErr)
		return f
	}
	if d.sel.Version != nil && !d.sel.Version.HasFeature(contract.FeatureDiagnostics) {
		f.err = &contract.Error{Code: contract.CodeWasyncTooOld, Message: "wasync " + orUnknownVersion(d.sel.Version.WasyncVersion) + " does not report diagnostics", Help: "upgrade wasync: " + a.Compat.UpgradeHelp}
		return f
	}
	if d.sel.Name() == transport.NameAPI {
		if d.cred == nil {
			f.apiErr = &contract.Error{Code: contract.CodeCredentialAbsent, Message: "no whatsapp-axi agent credential is configured", Help: config.GrantHelp, Transport: transport.NameAPI}
		} else {
			var diag diagnostics
			err := d.sel.Transport.Get(ctx, "/diagnostics", nil, &diag)
			if err == nil {
				f.diag, f.transport = &diag, transport.NameAPI
				f.health = a.fetchHealth(ctx, d, &diag)
				return f
			}
			f.apiErr = err
		}
	}
	q := d.sel.Transport
	if q.Name() != transport.NameQuery {
		if d.bin == "" {
			f.err = firstErr(f.apiErr, d.binErr)
			return f
		}
		q = transport.NewQuery(transport.QueryConfig{Bin: d.bin, DataDir: d.settings.DataDir})
	}
	var diag diagnostics
	if err := q.Get(ctx, "/diagnostics", nil, &diag); err != nil {
		f.err = err
		return f
	}
	f.diag, f.transport = &diag, transport.NameQuery
	return f
}

// fetchHealth reads deep health when the agent may (read.chats); failures
// are ignored because the diagnostics already carry the health state.
func (a *App) fetchHealth(ctx context.Context, d detection, diag *diagnostics) *deepHealth {
	if !d.sel.Version.HasFeature(contract.FeatureHealthDeep) || diag.Agent == nil || !hasString(diag.Agent.Scopes, scopeReadChats) {
		return nil
	}
	var h deepHealth
	if d.sel.Transport.Get(ctx, "/health/deep", nil, &h) != nil {
		return nil
	}
	return &h
}

func (a *App) doctorReport(d detection, f facts) doctorReport {
	r := doctorReport{
		WhatsappAXIVersion: buildinfo.Version, Bin: a.binPath(), Transport: f.transport,
		Wasync: a.wasyncJSON(d), Compatibility: compatOf(d), Diagnostics: f.diag, Health: f.health,
	}
	switch f.transport {
	case transport.NameAPI:
		r.TransportNote = "wasync facts from GET /api/v1/diagnostics"
	case transport.NameQuery:
		r.TransportNote = "wasync facts from offline `wasync query doctor`; offline reads are not scoped by an agent grant"
	default:
		r.TransportNote = "no wasync facts: neither the API nor `wasync query doctor` answered"
	}
	r.Checks = append(r.Checks, a.installationChecks(d)...)
	r.Checks = append(r.Checks, a.serviceChecks(d, f)...)
	if f.diag == nil {
		r.Checks = append(r.Checks, doctorCheck{Area: areaPairing, Name: "pairing", Status: statusSkip, Source: sourceAXI, Detail: "pairing state unknown: no wasync facts"})
	}
	r.Checks = append(r.Checks, a.permissionChecks(d, f)...)
	r.Checks = append(r.Checks, a.compatibilityChecks(d)...)
	r.Checks = append(r.Checks, a.hookChecks()...)
	if f.diag != nil {
		for _, c := range f.diag.Checks {
			area, ok := wasyncCheckArea[c.Name]
			if !ok {
				area = areaOther
			}
			status := c.Status
			if status != statusPass && status != statusWarn && status != statusFail {
				status = statusWarn // unknown wasync status: never hide it as a pass
			}
			r.Checks = append(r.Checks, doctorCheck{Area: area, Name: c.Name, Status: status, Source: sourceWasync, Detail: c.Detail, Fix: c.Fix})
		}
	}
	rank := make(map[string]int, len(areaOrder))
	for i, ar := range areaOrder {
		rank[ar] = i
	}
	sort.SliceStable(r.Checks, func(i, j int) bool { return rank[r.Checks[i].Area] < rank[r.Checks[j].Area] })

	secret := ""
	if d.cred != nil {
		secret = d.cred.Secret
	}
	for i := range r.Checks {
		c := &r.Checks[i]
		c.Name, c.Detail, c.Fix = scrub(c.Name, secret), scrub(c.Detail, secret), scrub(c.Fix, secret)
	}
	if r.Diagnostics != nil {
		scrubDiagnostics(r.Diagnostics, secret)
	}
	if r.Health != nil {
		r.Health.State = scrub(r.Health.State, secret)
		if r.Health.CaptureError != nil {
			s := scrub(*r.Health.CaptureError, secret)
			r.Health.CaptureError = &s
		}
	}
	r.Summary = summarize(r.Checks)
	r.Help = []string{}
	if r.Summary.Result != statusPass {
		r.Help = append(r.Help, "Run each fix for fail checks first, then warn checks, and rerun `whatsapp-axi doctor`")
	}
	if f.transport == transport.NameQuery {
		r.Help = append(r.Help, "Ask the user to start wasync (the TUI or `wasync serve`) for live, scoped reads; whatsapp-axi never starts it")
	}
	return r
}

func summarize(checks []doctorCheck) doctorSummary {
	var s doctorSummary
	for _, c := range checks {
		switch c.Status {
		case statusPass:
			s.Pass++
		case statusWarn:
			s.Warn++
		case statusFail:
			s.Fail++
		default:
			s.Skip++
		}
	}
	switch {
	case s.Fail > 0:
		s.Result = statusFail
	case s.Warn > 0:
		s.Result = statusWarn
	default:
		s.Result = statusPass
	}
	return s
}

func (a *App) installationChecks(d detection) []doctorCheck {
	out := []doctorCheck{{Area: areaInstallation, Name: "whatsapp_axi", Status: statusPass, Source: sourceAXI,
		Detail: "whatsapp-axi " + buildinfo.Version + " at " + a.binPath() + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"}}
	c := doctorCheck{Area: areaInstallation, Name: "wasync_bin", Source: sourceAXI}
	switch {
	case d.bin != "":
		from := "PATH"
		if d.settings.Bin != "" {
			from = config.EnvBin
		}
		c.Status, c.Detail = statusPass, "wasync found at "+a.collapseHome(d.bin)+" (from "+from+")"
	default:
		c.Status = statusFail
		if d.sel.Name() == transport.NameAPI {
			// The API works; only offline reads are lost.
			c.Status = statusWarn
		}
		c.Detail = errMessage(d.binErr) + "; offline reads are unavailable"
		c.Fix = installWasyncFix
		if d.settings.Bin != "" {
			c.Fix = "unset " + config.EnvBin
		}
	}
	return append(out, c)
}

func (a *App) serviceChecks(d detection, f facts) []doctorCheck {
	api := doctorCheck{Area: areaService, Name: "api", Source: sourceAXI}
	switch {
	case d.sel.Name() == transport.NameAPI:
		api.Status, api.Detail = statusPass, "wasync API answered at "+d.settings.APIAddr
	case d.sel.APIErr == nil:
		api.Status, api.Detail = statusSkip, "wasync API not probed"
	default:
		api.Detail = "wasync API at " + d.settings.APIAddr + ": " + errMessage(d.sel.APIErr)
		switch errCode(d.sel.APIErr) {
		case contract.CodeOwnerOffline:
			api.Status, api.Fix = statusWarn, "wasync serve"
			api.Detail = "wasync API not reachable at " + d.settings.APIAddr + " (no owner running)"
			if d.sel.Name() == transport.NameQuery {
				api.Detail += "; using offline wasync query"
			}
		case contract.CodeWasyncTooOld:
			api.Status, api.Fix = statusFail, upgradeWasyncFix
		case contract.CodeAXITooOld:
			api.Status, api.Fix = statusFail, upgradeAXIFix
		case contract.CodeInvalidArgument:
			api.Status, api.Fix = statusFail, "unset "+config.EnvAPIAddr
		default:
			api.Status, api.Fix = statusFail, fixFromHelp(helpOf(d.sel.APIErr), "unset "+config.EnvAPIAddr)
		}
	}
	diag := doctorCheck{Area: areaService, Name: "diagnostics", Source: sourceAXI}
	switch f.transport {
	case transport.NameAPI:
		diag.Status, diag.Detail = statusPass, "wasync facts from GET /api/v1/diagnostics (transport api)"
	case transport.NameQuery:
		diag.Status, diag.Detail = statusPass, "wasync facts from offline `wasync query doctor` (transport query); offline reads are not scoped by an agent grant"
		if f.apiErr != nil {
			diag.Detail += "; API diagnostics not used: " + errMessage(f.apiErr)
		}
	default:
		diag.Status = statusFail
		diag.Detail = "no wasync facts: " + errMessage(f.err)
		switch {
		case d.sel.Transport == nil && d.binErr != nil && d.sel.Incompatible == nil && errCode(d.sel.APIErr) == contract.CodeOwnerOffline:
			diag.Fix = installWasyncFix
			if d.settings.Bin != "" {
				diag.Fix = "unset " + config.EnvBin
			}
		case errCode(f.err) == contract.CodeAXITooOld:
			diag.Fix = upgradeAXIFix
		default:
			diag.Fix = fixFromHelp(helpOf(f.err), upgradeWasyncFix)
		}
	}
	return []doctorCheck{api, diag}
}

func (a *App) permissionChecks(d detection, f facts) []doctorCheck {
	agent := (*diagnosticsAgent)(nil)
	if f.transport == transport.NameAPI && f.diag != nil {
		agent = f.diag.Agent
	}
	name := defaultAgentName
	if agent != nil && shellSafe(agent.Name) {
		name = agent.Name
	}
	grant := "wasync agents grant " + name + " --scope " + scopeReadChats + "," + scopeReadMessages

	cred := doctorCheck{Area: areaPermissions, Name: "credential", Source: sourceAXI}
	switch {
	case d.credErr != nil:
		cred.Status, cred.Detail = statusFail, errMessage(d.credErr)
		cred.Fix = fixFromHelp(helpOf(d.credErr), grant)
	case d.cred == nil:
		cred.Status = statusWarn
		cred.Detail = "no agent credential in " + config.EnvToken + " or " + a.collapseHome(filepath.Join(d.settings.ConfigDir, config.CredentialFile)) + "; live API reads are unavailable"
		cred.Fix = grant
	case agent != nil:
		cred.Status = statusPass
		cred.Detail = "accepted by wasync as agent " + agent.Name + " (from " + credSource(d.cred) + ")"
		if agent.ExpiresAt != nil {
			cred.Detail += "; expires " + agent.ExpiresAt.UTC().Format(time.RFC3339)
		}
	case errCode(f.apiErr) == contract.CodeUnauthorized:
		cred.Status, cred.Fix = statusFail, grant
		cred.Detail = "wasync rejected the credential from " + credSource(d.cred) + " (revoked, expired or not an agent credential)"
	default:
		cred.Status = statusPass
		cred.Detail = "present (from " + credSource(d.cred) + "); not verified because the API is not in use"
	}

	scopes := doctorCheck{Area: areaPermissions, Name: "scopes", Source: sourceAXI}
	if agent == nil {
		scopes.Status = statusSkip
		scopes.Detail = "granted scopes are known only through the API with a credential; offline reads are not scoped"
	} else {
		var missing, uses []string
		for _, s := range scopeUses {
			if !hasString(agent.Scopes, s.scope) {
				missing = append(missing, s.scope)
				uses = append(uses, s.use)
			}
		}
		if len(missing) == 0 {
			scopes.Status = statusPass
			scopes.Detail = "agent " + agent.Name + " has " + strings.Join(orderScopes(agent.Scopes), ",")
		} else {
			scopes.Status = statusWarn
			scopes.Detail = "agent " + agent.Name + " lacks " + strings.Join(missing, ",") + " (needed for " + strings.Join(uses, "; ") + ")"
			for _, check := range f.diag.Checks {
				if check.Name == "agent" {
					scopes.Fix = check.Fix
					break
				}
			}
			if strings.TrimSpace(scopes.Fix) == "" {
				scopes.Fix = upgradeWasyncFix
			}
		}
		if agent.OnDemand {
			scopes.Detail += "; chats are approved on demand (" + strconv.Itoa(len(agent.Chats)) + " so far; ask with `whatsapp-axi access request`)"
		} else if len(agent.Chats) > 0 {
			scopes.Detail += "; limited to " + strconv.Itoa(len(agent.Chats)) + " chat(s)"
		}
	}
	return []doctorCheck{cred, a.configFilesCheck(d), scopes}
}

// configFilesCheck verifies whatsapp-axi's own config directory and
// credential file are private. It never reads wasync's files.
func (a *App) configFilesCheck(d detection) doctorCheck {
	c := doctorCheck{Area: areaPermissions, Name: "config_files", Source: sourceAXI}
	dir := d.settings.ConfigDir
	switch {
	case runtime.GOOS == "windows":
		c.Status, c.Detail = statusSkip, "file permission bits are not checked on Windows"
		return c
	case dir == "":
		c.Status, c.Detail = statusSkip, "no whatsapp-axi config directory could be determined"
		return c
	}
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		c.Status, c.Detail = statusPass, "no whatsapp-axi config directory yet ("+a.collapseHome(dir)+")"
		return c
	}
	if err != nil || !info.IsDir() {
		c.Status, c.Detail, c.Fix = statusFail, a.collapseHome(dir)+" is not a readable directory", "mkdir -m 700 "+shellQuote(dir)
		return c
	}
	path := filepath.Join(dir, config.CredentialFile)
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o077 != 0 {
		c.Status, c.Fix = statusFail, "chmod 600 "+shellQuote(path)
		c.Detail = "credential file " + a.collapseHome(path) + " has mode " + modeString(fi.Mode()) + "; other users can read it"
		return c
	}
	if info.Mode().Perm()&0o022 != 0 {
		c.Status, c.Fix = statusWarn, "chmod 700 "+shellQuote(dir)
		c.Detail = "config directory " + a.collapseHome(dir) + " has mode " + modeString(info.Mode()) + "; other users can replace the credential"
		return c
	}
	c.Status, c.Detail = statusPass, "config directory "+a.collapseHome(dir)+" and credential file are private"
	return c
}

func (a *App) compatibilityChecks(d detection) []doctorCheck {
	api := doctorCheck{Area: areaCompatibility, Name: "api_version", Source: sourceAXI}
	feat := doctorCheck{Area: areaCompatibility, Name: "features", Source: sourceAXI}
	v := d.sel.Version
	if v == nil {
		noWasync := d.binErr != nil && errCode(d.sel.APIErr) == contract.CodeOwnerOffline
		if noWasync {
			api.Status, api.Detail = statusSkip, "no wasync answered"
		} else {
			cause := firstErr(d.sel.QueryErr, d.sel.APIErr, d.binErr)
			api.Status, api.Detail = statusFail, "wasync version not detected: "+errMessage(cause)
			api.Fix = fixFromHelp(helpOf(cause), upgradeWasyncFix)
		}
		feat.Status, feat.Detail = statusSkip, "wasync features unknown"
		return []doctorCheck{api, feat}
	}
	base := "wasync " + orUnknownVersion(v.WasyncVersion) + " speaks API " + v.API() + ", schema " + strconv.Itoa(v.SchemaVersion)
	if err := a.Compat.CheckAPI(*v); err != nil {
		api.Status, api.Detail = statusFail, base+"; "+errMessage(err)
		api.Fix = upgradeWasyncFix
		if errCode(err) == contract.CodeAXITooOld {
			api.Fix = upgradeAXIFix
		}
		feat.Status, feat.Detail = statusSkip, "not checked: API version incompatible"
		return []doctorCheck{api, feat}
	}
	api.Status, api.Detail = statusPass, base+"; whatsapp-axi needs API "+a.Compat.MinAPI+" to "+strconv.Itoa(contract.APIVersion)+".x"
	var problems []string
	for _, cmd := range a.Compat.CommandNames() {
		if err := a.Compat.Check(cmd, *v); err != nil {
			problems = append(problems, errMessage(err))
		}
	}
	if len(problems) == 0 {
		feat.Status = statusPass
		feat.Detail = "wasync has every feature whatsapp-axi uses (" + strconv.Itoa(len(a.Compat.Commands)) + " commands checked)"
	} else {
		feat.Status, feat.Fix = statusWarn, upgradeWasyncFix
		feat.Detail = strings.Join(problems, "; ")
	}
	return []doctorCheck{api, feat}
}

func (a *App) writeDoctor(r doctorReport) error {
	o := output.New(a.Out)
	o.Field(0, "whatsapp-axi", r.WhatsappAXIVersion)
	o.Field(0, "transport", r.Transport)
	o.Field(0, "transport_note", r.TransportNote)
	if r.Wasync.Version != nil {
		o.Field(0, "wasync", r.Wasync.Version.WasyncVersion)
	} else {
		o.Field(0, "wasync", "not detected")
	}
	s := r.Summary
	o.Field(0, "result", s.Result)
	o.Field(0, "summary", strconv.Itoa(s.Pass)+" pass, "+strconv.Itoa(s.Warn)+" warn, "+strconv.Itoa(s.Fail)+" fail, "+strconv.Itoa(s.Skip)+" skip")
	rows := make([][]string, 0, len(r.Checks))
	for _, c := range r.Checks {
		rows = append(rows, []string{c.Area, c.Name, c.Status, c.Source, c.Detail, c.Fix})
	}
	o.Table(0, "checks", []string{"area", "name", "status", "source", "detail", "fix"}, rows)
	if h := r.Health; h != nil {
		o.Object(0, "health")
		o.Field(1, "state", h.State)
		o.Field(1, "connected", strconv.FormatBool(h.Connected))
		if h.LastEventAt != nil {
			o.Field(1, "last_event_at", h.LastEventAt.UTC().Format(time.RFC3339))
		} else {
			o.Field(1, "last_event_at", "none")
		}
		o.Field(1, "open_gaps", strconv.FormatInt(h.OpenGaps, 10))
	}
	o.Help(r.Help...)
	return o.Err()
}

// Secrets never reach doctor's output: the configured credential, anything
// shaped like an agent credential, and long hex strings (token hashes).
var (
	credentialPattern = regexp.MustCompile(`wsa_[A-Za-z0-9_\-+/=]{4,}`)
	hashPattern       = regexp.MustCompile(`[0-9A-Fa-f]{32,}`)
)

func scrub(s, secret string) string {
	s = output.Sanitize(s)
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	s = credentialPattern.ReplaceAllString(s, "wsa_[redacted]")
	return hashPattern.ReplaceAllString(s, "[redacted]")
}

func scrubDiagnostics(d *diagnostics, secret string) {
	for _, p := range []*string{&d.Transport, &d.WasyncVersion, &d.Distribution, &d.OS, &d.Arch, &d.Health} {
		*p = scrub(*p, secret)
	}
	for i := range d.Checks {
		c := &d.Checks[i]
		c.Name, c.Status, c.Detail, c.Fix = scrub(c.Name, secret), scrub(c.Status, secret), scrub(c.Detail, secret), scrub(c.Fix, secret)
	}
	if d.Checks == nil {
		d.Checks = []wasyncCheck{}
	}
	if ag := d.Agent; ag != nil {
		ag.Name = scrub(ag.Name, secret)
		for i := range ag.Scopes {
			ag.Scopes[i] = scrub(ag.Scopes[i], secret)
		}
		for i := range ag.Chats {
			ag.Chats[i] = scrub(ag.Chats[i], secret)
		}
		if ag.Scopes == nil {
			ag.Scopes = []string{}
		}
		if ag.Chats == nil {
			ag.Chats = []string{}
		}
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func errMessage(err error) string {
	if err == nil {
		return "unknown reason"
	}
	var ce *contract.Error
	if errors.As(err, &ce) {
		if ce.Code != "" && ce.Code != contract.CodeInternal {
			return ce.Message + " (" + ce.Code + ")"
		}
		return ce.Message
	}
	return err.Error()
}

func helpOf(err error) string {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Help
	}
	return ""
}

// fixFromHelp turns a contract help line into one fix command: the first
// `backticked` command, the command after "upgrade wasync: ", or help itself
// when it already is a command. Otherwise fallback.
func fixFromHelp(help, fallback string) string {
	if _, rest, ok := strings.Cut(help, "`"); ok {
		if cmd, _, ok := strings.Cut(rest, "`"); ok && cmd != "" {
			return cmd
		}
	}
	if rest, ok := strings.CutPrefix(help, "upgrade wasync: "); ok {
		return rest
	}
	for _, p := range []string{"wasync ", "brew ", "chmod ", "unset ", "go install "} {
		if strings.HasPrefix(help, p) {
			return help
		}
	}
	return fallback
}

func credSource(c *config.Credential) string {
	if c.Source == "env" {
		return config.EnvToken
	}
	return "credential file"
}

func hasString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// orderScopes dedupes scopes into wasync's canonical order; unknown scopes
// follow, sorted.
func orderScopes(scopes []string) []string {
	var out, extra []string
	for _, s := range scopeOrder {
		if hasString(scopes, s) {
			out = append(out, s)
		}
	}
	for _, s := range scopes {
		if !hasString(scopeOrder, s) && !hasString(extra, s) {
			extra = append(extra, s)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

var shellSafePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func shellSafe(s string) bool { return shellSafePattern.MatchString(s) }

var shellPathPattern = regexp.MustCompile(`^[A-Za-z0-9._/~:@%+=-]+$`)

// shellQuote quotes a path for a POSIX shell fix command.
func shellQuote(p string) string {
	if shellPathPattern.MatchString(p) {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

func modeString(m fs.FileMode) string { return "0" + strconv.FormatUint(uint64(m.Perm()), 8) }

func orUnknownVersion(s string) string {
	if s == "" {
		return "(unknown version)"
	}
	return s
}
