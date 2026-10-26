// Package webhook delivers change-journal notifications to HTTPS endpoints
// that a human registered with wasync webhooks add.
//
// Webhooks are opt-in per endpoint and carry identifiers only: the event names
// the journal position, operation, chat and message ref, never message
// content. A receiver fetches content through the scoped /api/v1. Endpoints
// are managed only by a human through the wasync CLI; nothing in this package
// is reachable over HTTP and no agent scope grants it.
package webhook

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/storage"
)

const (
	// FileName is the private endpoint registry in the data directory.
	FileName = "webhooks.json"
	// LockFileName serializes registry writers across processes.
	LockFileName = "webhooks.lock"
	FileVersion  = 1
	// MaxEndpoints bounds the registry; each active endpoint costs one
	// delivery loop and one journal reader.
	MaxEndpoints = 16
	maxFileSize  = 1 << 20
	lockWait     = 5 * time.Second

	// SecretPrefix marks signing secrets so they are recognizable in logs and
	// secret scanners.
	SecretPrefix = "whsec_"
	secretBytes  = 32
	endpointIDLn = 8
)

// Endpoint is a registered webhook destination. Secret never marshals: it is
// loaded for delivery and stored only in the private registry file.
type Endpoint struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	Chats     []string   `json:"chats"`
	Ops       []string   `json:"ops"`
	CreatedAt time.Time  `json:"created_at"`
	RemovedAt *time.Time `json:"removed_at,omitempty"`
	Secret    string     `json:"-"`
}

// Active reports whether the endpoint still receives events.
func (e *Endpoint) Active() bool { return e.RemovedAt == nil }

// Matches reports whether a journal entry passes the endpoint's filters.
// Empty filters match everything.
func (e *Endpoint) Matches(c storage.Change) bool {
	if len(e.Chats) > 0 && !contains(e.Chats, c.ChatID) {
		return false
	}
	return len(e.Ops) == 0 || contains(e.Ops, c.Op)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// AllOps lists every journal operation an endpoint may subscribe to.
var AllOps = []string{storage.OpMessageCreated, storage.OpMessageUpdated, storage.OpMessageRevoked, storage.OpMessageExpired, storage.OpMessagePurged, storage.OpCaptureEnabled, storage.OpCaptureDisabled}

type record struct {
	Endpoint
	Secret string `json:"secret"`
}

type registry struct {
	Version   int      `json:"version"`
	Endpoints []record `json:"endpoints"`
}

// AddRequest describes a new endpoint.
type AddRequest struct {
	Name  string
	URL   string
	Chats []string
	Ops   []string
}

func (r *AddRequest) normalize() error {
	if err := agentauth.ValidName(r.Name); err != nil {
		return fmt.Errorf("endpoint name must be 1-32 characters of a-z, 0-9 and -")
	}
	if _, err := ValidateURL(r.URL); err != nil {
		return err
	}
	seen := map[string]bool{}
	chats := []string{}
	for _, c := range r.Chats {
		c = strings.TrimSpace(c)
		if err := agentauth.ValidChatJID(c); err != nil {
			return err
		}
		if !seen[c] {
			seen[c] = true
			chats = append(chats, c)
		}
	}
	r.Chats = chats
	ops := map[string]bool{}
	for _, value := range r.Ops {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			if !contains(AllOps, part) {
				return fmt.Errorf("unknown operation %q; valid operations: %s", part, strings.Join(AllOps, ","))
			}
			ops[part] = true
		}
	}
	r.Ops = []string{}
	for _, op := range AllOps {
		if ops[op] {
			r.Ops = append(r.Ops, op)
		}
	}
	return nil
}

// ValidateAdd checks an add request without touching disk, so the CLI can
// reject bad input before asking for confirmation.
func ValidateAdd(r AddRequest) error { return r.normalize() }

// List returns every endpoint, removed ones included, without secrets. A
// missing registry is an empty list.
func List(dir string) ([]Endpoint, error) {
	reg, err := load(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, len(reg.Endpoints))
	for _, r := range reg.Endpoints {
		e := r.Endpoint
		e.Secret = ""
		out = append(out, e)
	}
	return out, nil
}

// Active returns the endpoints that receive events, with their signing
// secrets. Only the delivery loop and webhooks test call it.
func Active(dir string) ([]Endpoint, error) {
	reg, err := load(dir)
	if err != nil {
		return nil, err
	}
	out := []Endpoint{}
	for _, r := range reg.Endpoints {
		if r.Active() {
			e := r.Endpoint
			e.Secret = r.Secret
			out = append(out, e)
		}
	}
	return out, nil
}

// ActiveByName returns the active endpoint with this name, with its secret.
func ActiveByName(dir, name string) (*Endpoint, error) {
	all, err := Active(dir)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, nil
}

// Add registers an endpoint and returns its signing secret, which can be read
// again only from the private registry file by the same OS user. Names are
// unique among active endpoints.
func Add(dir string, req AddRequest, now time.Time) (secret string, ep Endpoint, err error) {
	if err = req.normalize(); err != nil {
		return "", Endpoint{}, err
	}
	raw := make([]byte, secretBytes)
	if _, err = rand.Read(raw); err != nil {
		return "", Endpoint{}, err
	}
	secret = SecretPrefix + base64.RawURLEncoding.EncodeToString(raw)
	err = update(dir, func(reg *registry) error {
		ids := map[string]bool{}
		active := 0
		for _, r := range reg.Endpoints {
			ids[r.ID] = true
			if r.Active() {
				active++
				if r.Name == req.Name {
					return fmt.Errorf("endpoint %s already exists; remove it first with wasync webhooks remove %s", req.Name, req.Name)
				}
			}
		}
		if active >= MaxEndpoints {
			return fmt.Errorf("at most %d active webhook endpoints are supported", MaxEndpoints)
		}
		id := ""
		for attempt := 0; attempt < 16 && (id == "" || ids[id]); attempt++ {
			if id, err = newID(); err != nil {
				return err
			}
		}
		if ids[id] {
			return fmt.Errorf("could not allocate an endpoint ID")
		}
		ep = Endpoint{ID: id, Name: req.Name, URL: req.URL, Chats: req.Chats, Ops: req.Ops, CreatedAt: now.UTC().Truncate(time.Second)}
		reg.Endpoints = append(reg.Endpoints, record{Endpoint: ep, Secret: secret})
		return nil
	})
	if err != nil {
		return "", Endpoint{}, err
	}
	return secret, ep, nil
}

// Remove stops delivery to the active endpoint with this name. The record is
// kept, without its meaning changing, so audit entries stay attributable.
func Remove(dir, name string, now time.Time) (ep Endpoint, err error) {
	err = update(dir, func(reg *registry) error {
		for i := range reg.Endpoints {
			if reg.Endpoints[i].Name == name && reg.Endpoints[i].Active() {
				removed := now.UTC().Truncate(time.Second)
				reg.Endpoints[i].RemovedAt = &removed
				// The secret has no use after removal; drop it from disk.
				reg.Endpoints[i].Secret = ""
				ep = reg.Endpoints[i].Endpoint
				return nil
			}
		}
		return fmt.Errorf("no active endpoint named %s", name)
	})
	return ep, err
}

func newID() (string, error) {
	b := make([]byte, endpointIDLn/2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validID(id string) bool {
	if len(id) != endpointIDLn {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// load reads and validates the registry. It refuses files that are not
// private and fails closed on any malformed entry.
func load(dir string) (*registry, error) {
	path := filepath.Join(dir, FileName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return &registry{Version: FileVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	if err = localfs.CheckPrivateFile(path); err != nil {
		return nil, fmt.Errorf("%s must be a regular private file (0600, owner only): %w", FileName, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%s changed while opening; retry", FileName)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("%s is too large", FileName)
	}
	var reg registry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", FileName, err)
	}
	if reg.Version != FileVersion {
		return nil, fmt.Errorf("unsupported %s version %d; upgrade wasync", FileName, reg.Version)
	}
	ids := map[string]bool{}
	active := map[string]bool{}
	for _, r := range reg.Endpoints {
		if err = validRecord(r); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", FileName, err)
		}
		if ids[r.ID] || (r.Active() && active[r.Name]) {
			return nil, fmt.Errorf("invalid %s: duplicate endpoint %s", FileName, r.Name)
		}
		ids[r.ID] = true
		if r.Active() {
			active[r.Name] = true
		}
	}
	return &reg, nil
}

func validRecord(r record) error {
	if !validID(r.ID) || agentauth.ValidName(r.Name) != nil || r.CreatedAt.IsZero() {
		return fmt.Errorf("malformed endpoint entry")
	}
	if r.Active() && (!strings.HasPrefix(r.Secret, SecretPrefix) || len(r.Secret) != len(SecretPrefix)+43) {
		return fmt.Errorf("endpoint %s has a malformed signing secret", r.Name)
	}
	if _, err := ValidateURL(r.URL); err != nil {
		return fmt.Errorf("endpoint %s: %w", r.Name, err)
	}
	for _, c := range r.Chats {
		if err := agentauth.ValidChatJID(c); err != nil {
			return err
		}
	}
	for _, op := range r.Ops {
		if !contains(AllOps, op) {
			return fmt.Errorf("endpoint %s has unknown operation %q", r.Name, op)
		}
	}
	return nil
}

// update applies fn under the cross-process writer lock and replaces the
// registry atomically. Readers never lock: they see the old or the new file.
func update(dir string, fn func(*registry) error) (result error) {
	if err := localfs.EnsureDir(dir); err != nil {
		return err
	}
	lock, err := acquire(filepath.Join(dir, LockFileName))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	reg, err := load(dir)
	if err != nil {
		return err
	}
	if err = fn(reg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(dir, FileName, append(data, '\n'))
}

func acquire(path string) (*processlock.Lock, error) {
	deadline := time.Now().Add(lockWait)
	for {
		lock, err := processlock.Acquire(path)
		if !errors.Is(err, processlock.ErrBusy) {
			return lock, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another wasync process is updating webhook state; retry")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// writeAtomic replaces dir/name with data: private temp file, fsync, rename,
// directory fsync.
func writeAtomic(dir, name string, data []byte) (result error) {
	suffix, err := newID()
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+name+".tmp-"+suffix)
	f, err := localfs.OpenFile(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			os.Remove(tmp)
		}
	}()
	if err = f.Truncate(0); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil // Directory handles cannot be fsynced on Windows; MoveFileEx is durable enough.
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
