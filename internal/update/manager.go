package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

// States an update moves through. A check ends in StateCurrent, StateAvailable
// or StateFailed; an update goes StateDownloading, StateVerifying, then
// StateInstalling, when this wasync hands over to the helper and stops.
const (
	StateIdle        = "idle"
	StateChecking    = "checking"
	StateCurrent     = "up-to-date"
	StateAvailable   = "available"
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateInstalling  = "installing"
	StateFailed      = "failed"
)

// ErrNotReady means there is no update this wasync can install right now.
var ErrNotReady = errors.New("there is no update to install; check for updates first")

// ErrBusy means an update is already under way.
var ErrBusy = errors.New("an update is already under way")

// Status is everything a page needs to show about updates.
type Status struct {
	// Current is the running version.
	Current string `json:"current"`
	// Channel is how this copy was installed.
	Channel Channel `json:"channel"`
	State   string  `json:"state"`
	// CheckedAt is when the last check finished.
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// Latest is the newest published release the last check found.
	Latest *Latest `json:"latest,omitempty"`
	// CanInstall is set when one click would install Latest.
	CanInstall bool `json:"can_install"`
	// Note says how to update when CanInstall is not set but Latest is newer.
	Note     string    `json:"note,omitempty"`
	Progress *Progress `json:"progress,omitempty"`
	// Error says why the last check or update failed.
	Error string `json:"error,omitempty"`
	// LastResult is how the last installed update ended, kept across restarts,
	// while it still describes the running version.
	LastResult *Result `json:"last_result,omitempty"`
}

// Latest describes a published release.
type Latest struct {
	Version     string    `json:"version"`
	Page        string    `json:"page,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	// Asset is the file one click would download, when there is one.
	Asset string `json:"asset,omitempty"`
	Size  int64  `json:"size,omitempty"`
}

// Progress counts downloaded bytes.
type Progress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// Percent is how much has arrived, from 0 to 100.
func (p Progress) Percent() int {
	if p.Total <= 0 || p.Done <= 0 {
		return 0
	}
	if p.Done >= p.Total {
		return 100
	}
	return int(p.Done * 100 / p.Total)
}

// Config is what a Manager needs.
type Config struct {
	DataDir string
	// Current is the running version (internal/buildinfo.Version).
	Current string
	// Channel is how this copy was installed (see Detect).
	Channel  Channel
	Source   Source
	Platform Platform
	// Relaunch is how the helper starts wasync again.
	Relaunch Relaunch
	// Launch starts the helper (wasync update-apply) detached from this
	// process. Stop stops this wasync, as quitting it would.
	Launch func(dataDir string) error
	Stop   func()
	Now    func() time.Time
}

// Manager checks for and installs updates for one running wasync. It never
// checks by itself: each check is a request to GitHub the owner asked for.
type Manager struct {
	cfg Config
	ctx context.Context

	mu       sync.Mutex
	probed   bool
	state    string
	checked  time.Time
	release  *Release
	asset    *Asset
	note     string
	err      string
	progress *Progress
}

// NewManager returns a Manager whose downloads stop when ctx ends.
func NewManager(ctx context.Context, cfg Config) *Manager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Platform == nil {
		cfg.Platform = unsupportedPlatform{}
	}
	return &Manager{cfg: cfg, ctx: ctx, state: StateIdle}
}

// Status reports where updates stand.
func (m *Manager) Status() Status {
	m.mu.Lock()
	s := m.statusLocked()
	m.mu.Unlock()
	s.LastResult = m.lastResult()
	return s
}

// lastResult is how the last update ended, while it still describes the
// running version: a success that installed it or a failure that kept it.
func (m *Manager) lastResult() *Result {
	r, _ := LastResult(m.cfg.DataDir)
	if r == nil || (r.OK && r.To != m.cfg.Current) || (!r.OK && r.From != m.cfg.Current) {
		return nil
	}
	return r
}

func (m *Manager) statusLocked() Status {
	s := Status{Current: m.cfg.Current, Channel: m.cfg.Channel, State: m.state, Note: m.note, Error: m.err}
	if !m.checked.IsZero() {
		t := m.checked
		s.CheckedAt = &t
	}
	if m.release != nil {
		s.Latest = &Latest{Version: m.release.Version(), Page: m.release.PageURL, PublishedAt: m.release.PublishedAt}
		if m.asset != nil {
			s.Latest.Asset, s.Latest.Size = m.asset.Name, m.asset.Size
		}
	}
	s.CanInstall = m.state == StateAvailable && m.installable()
	if m.progress != nil {
		p := *m.progress
		s.Progress = &p
	}
	return s
}

func (m *Manager) installable() bool {
	return m.cfg.Channel.OneClick && m.asset != nil && m.cfg.Launch != nil && m.cfg.Stop != nil && m.cfg.Source != nil
}

func (m *Manager) busyLocked() bool {
	switch m.state {
	case StateChecking, StateDownloading, StateVerifying, StateInstalling:
		return true
	}
	return false
}

// Check asks GitHub for the newest published release.
func (m *Manager) Check(ctx context.Context) Status {
	m.mu.Lock()
	if m.busyLocked() || m.cfg.Source == nil {
		s := m.statusLocked()
		m.mu.Unlock()
		return s
	}
	m.state, m.err, m.note, m.progress = StateChecking, "", "", nil
	probe := !m.probed
	m.probed = true
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	ch := m.cfg.Channel
	if probe {
		m.cfg.Platform.Probe(ctx, &ch)
	}
	rel, err := m.cfg.Source.Latest(ctx)

	m.mu.Lock()
	if probe {
		m.cfg.Channel = ch
	}
	m.checked = m.cfg.Now().UTC()
	m.release, m.asset = rel, nil
	switch {
	case err != nil:
		m.state, m.err = StateFailed, err.Error()
	case rel == nil || !Newer(rel.Version(), m.cfg.Current):
		m.state = StateCurrent
	default:
		m.state = StateAvailable
		for _, name := range m.cfg.Channel.AssetNames(rel.Version()) {
			if a, ok := rel.Find(name); ok {
				m.asset = &a
				break
			}
		}
		switch {
		case !m.cfg.Channel.OneClick:
			m.note = m.cfg.Channel.Note
		case m.asset == nil:
			m.note = "wasync " + rel.Version() + " has no download for this computer yet. See " + rel.PageURL + "."
		case !m.installable():
			m.note = "This wasync cannot restart itself, so it cannot install the update. Download it from " + rel.PageURL + "."
		}
	}
	s := m.statusLocked()
	m.mu.Unlock()
	s.LastResult = m.lastResult()
	return s
}

// Start downloads, checks and installs the update the last check found, then
// stops this wasync so the helper can put the new version in place. version
// is the one the person agreed to; if a check found another since, nothing
// starts. It returns at once; Status reports progress.
func (m *Manager) Start(version string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busyLocked() {
		return m.statusLocked(), ErrBusy
	}
	if m.state != StateAvailable || !m.installable() || m.release.Version() != version {
		return m.statusLocked(), ErrNotReady
	}
	rel, asset, ch := *m.release, *m.asset, m.cfg.Channel
	m.state, m.err, m.progress = StateDownloading, "", &Progress{Total: asset.Size}
	go m.run(rel, asset, ch)
	return m.statusLocked(), nil
}

func (m *Manager) set(state string) {
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
}

func (m *Manager) fail(err error) {
	m.mu.Lock()
	m.state, m.err = StateFailed, err.Error()
	m.mu.Unlock()
}

const (
	// checkTimeout bounds asking GitHub for the newest release.
	checkTimeout = 30 * time.Second
	// downloadTimeout bounds a whole download and check.
	downloadTimeout = 30 * time.Minute
)

func (m *Manager) run(rel Release, asset Asset, ch Channel) {
	ctx, cancel := context.WithTimeout(m.ctx, downloadTimeout)
	defer cancel()
	dir := Folder(m.cfg.DataDir)
	cleanStaging(m.cfg.DataDir)
	if err := localfs.EnsureDir(dir); err != nil {
		m.fail(fmt.Errorf("could not prepare the update folder: %w", err))
		return
	}
	staging, err := os.MkdirTemp(dir, stagingPrefix+"*")
	if err != nil {
		m.fail(fmt.Errorf("could not prepare the update folder: %w", err))
		return
	}
	plan, err := m.stage(ctx, rel, asset, ch, staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		m.fail(err)
		return
	}
	if err := writeJSONFile(dir, planFile, plan); err != nil {
		_ = os.RemoveAll(staging)
		m.fail(fmt.Errorf("could not save the update plan: %w", err))
		return
	}
	m.set(StateInstalling)
	if err := m.cfg.Launch(m.cfg.DataDir); err != nil {
		_ = os.Remove(filepath.Join(dir, planFile))
		_ = os.RemoveAll(staging)
		m.fail(fmt.Errorf("could not start the installer: %w", err))
		return
	}
	m.cfg.Stop()
}

// stage downloads the asset, checks it and gets it ready to install.
func (m *Manager) stage(ctx context.Context, rel Release, asset Asset, ch Channel, staging string) (Plan, error) {
	if asset.Size <= 0 || asset.Size > maxAssetBytes {
		return Plan{}, fmt.Errorf("the release lists %s with an unexpected size", asset.Name)
	}
	file := filepath.Join(staging, filepath.Base(asset.Name))
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Plan{}, err
	}
	h := sha256.New()
	err = m.cfg.Source.Fetch(ctx, asset.URL, io.MultiWriter(f, h), asset.Size, func(done int64) {
		m.mu.Lock()
		if m.progress != nil {
			m.progress.Done = done
		}
		m.mu.Unlock()
	})
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Plan{}, fmt.Errorf("could not download %s: %w", asset.Name, err)
	}
	m.set(StateVerifying)
	if info, err := os.Stat(file); err != nil || info.Size() != asset.Size {
		return Plan{}, fmt.Errorf("the download of %s is incomplete", asset.Name)
	}
	sums, err := fetchChecksums(ctx, m.cfg.Source, rel)
	if err != nil {
		return Plan{}, err
	}
	if err := checkDigest(hex.EncodeToString(h.Sum(nil)), asset, sums); err != nil {
		return Plan{}, err
	}
	staged, err := m.cfg.Platform.Prepare(ctx, ch, file, staging)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Kind: ch.Kind, From: m.cfg.Current, To: rel.Version(), Target: ch.Target, Staged: staged.Path, SHA256: staged.SHA256, Requirement: staged.Requirement, Relaunch: m.cfg.Relaunch, Created: m.cfg.Now().UTC()}, nil
}
