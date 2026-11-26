package agentconnect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

// RegistryFile records, in wasync's data folder, which apps wasync
// connected and what it changed, so Disconnect can undo exactly that.
const RegistryFile = "agent-connections.json"

const registryVersion = 1

// Connection is one app wasync connected.
type Connection struct {
	Agent Kind `json:"agent"`
	// Client and ClientID name the agent credential made for it.
	Client   string `json:"client"`
	ClientID string `json:"client_id"`
	// CredentialDir holds the credential; the entry points wasync-axi at it.
	CredentialDir string `json:"credential_dir"`
	// ConfigPath is the settings file wasync changed, Backup the copy it
	// kept of it first ("" when the file was new).
	ConfigPath string `json:"config_path"`
	Backup     string `json:"backup,omitempty"`
	// Entry is what wasync wrote, so it is recognised later.
	Entry Entry `json:"entry"`
	// Previous is the "whatsapp" entry the file had before, put back on Disconnect.
	Previous string `json:"previous,omitempty"`
	// CodexPlugin and CodexMarketplace say wasync installed the WhatsApp
	// plugin and its local marketplace in Codex, and removes them again.
	CodexPlugin      bool      `json:"codex_plugin,omitempty"`
	CodexMarketplace bool      `json:"codex_marketplace,omitempty"`
	ConnectedAt      time.Time `json:"connected_at"`
}

type registry struct {
	Version     int          `json:"version"`
	Connections []Connection `json:"connections"`
}

// registryMu serialises changes within this process; only the owner writes
// the registry.
var registryMu sync.Mutex

// Connections lists the apps wasync connected.
func Connections(dataDir string) ([]Connection, error) {
	reg, err := loadRegistry(dataDir)
	if err != nil {
		return nil, err
	}
	return reg.Connections, nil
}

// Find returns the connection of kind, or nil.
func Find(dataDir string, kind Kind) (*Connection, error) {
	all, err := Connections(dataDir)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Agent == kind {
			return &all[i], nil
		}
	}
	return nil, nil
}

// Save records c, replacing an earlier connection of the same app.
func Save(dataDir string, c Connection) error {
	return updateRegistry(dataDir, func(reg *registry) {
		for i := range reg.Connections {
			if reg.Connections[i].Agent == c.Agent {
				reg.Connections[i] = c
				return
			}
		}
		reg.Connections = append(reg.Connections, c)
	})
}

// Forget removes the connection of kind.
func Forget(dataDir string, kind Kind) error {
	return updateRegistry(dataDir, func(reg *registry) {
		kept := reg.Connections[:0]
		for _, c := range reg.Connections {
			if c.Agent != kind {
				kept = append(kept, c)
			}
		}
		reg.Connections = kept
	})
}

func loadRegistry(dataDir string) (*registry, error) {
	path := filepath.Join(dataDir, RegistryFile)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return &registry{Version: registryVersion}, nil
	} else if err != nil {
		return nil, err
	}
	if err := localfs.CheckPrivateFile(path); err != nil {
		return nil, fmt.Errorf("%s must be a regular private file (0600, owner only): %w", RegistryFile, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("%s is too large", RegistryFile)
	}
	var reg registry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", RegistryFile, err)
	}
	if reg.Version != registryVersion {
		return nil, fmt.Errorf("unsupported %s version %d; upgrade wasync", RegistryFile, reg.Version)
	}
	for _, c := range reg.Connections {
		if _, ok := Lookup(string(c.Agent)); !ok {
			return nil, fmt.Errorf("invalid %s: unknown agent %q", RegistryFile, c.Agent)
		}
	}
	return &reg, nil
}

func updateRegistry(dataDir string, fn func(*registry)) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	if err := localfs.EnsureDir(dataDir); err != nil {
		return err
	}
	reg, err := loadRegistry(dataDir)
	if err != nil {
		return err
	}
	fn(reg)
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(dataDir, RegistryFile, append(data, '\n'))
}

// writePrivate replaces dir/name atomically with a file only this user can
// read, the way wasync writes its other private files (on Windows the
// private access list comes from localfs).
func writePrivate(dir, name string, data []byte) (result error) {
	tmp := filepath.Join(dir, "."+name+".tmp-"+time.Now().Format("150405.000000000"))
	f, err := localfs.OpenFile(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			_ = os.Remove(tmp)
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
	return localfs.SyncDir(dir)
}
