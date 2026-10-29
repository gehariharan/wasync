// Package config resolves whatsapp-axi's own settings: where wasync is,
// which loopback address its API listens on, and the agent credential. It
// never reads wasync's data directory or its tokens.
package config

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

// Environment variables read by whatsapp-axi.
const (
	EnvToken     = "WHATSAPP_AXI_TOKEN"
	EnvConfigDir = "WHATSAPP_AXI_CONFIG_DIR"
	EnvAPIAddr   = "WASYNC_API_ADDR"
	EnvBin       = "WASYNC_BIN"
	EnvDataDir   = "WASYNC_DATA_DIR"
	// EnvAgentHome replaces the home directory as the place where `setup`
	// and `doctor` find agent settings (.claude, .codex).
	EnvAgentHome = "WHATSAPP_AXI_AGENT_HOME"
)

// DefaultAPIAddr matches wasync's default API address.
const DefaultAPIAddr = "127.0.0.1:8787"

// CredentialPrefix starts every wasync agent credential.
const CredentialPrefix = "wsa_"

// CredentialFile is the file name inside the AXI config dir.
const CredentialFile = "credential"

// SaveHelp says where a new credential goes; grant offers to save it.
const SaveHelp = "; wasync offers to save the credential where whatsapp-axi reads it (or set " + EnvToken + ")"

// GrantHelp is the human action that produces a credential.
const GrantHelp = "ask a human to run `wasync agents grant <name> --scope read.chats,read.messages`" + SaveHelp

// Getenv abstracts os.Getenv for tests.
type Getenv func(string) string

// Settings is the resolved configuration.
type Settings struct {
	APIAddr   string
	Bin       string // WASYNC_BIN as given; empty means look up PATH
	DataDir   string
	ConfigDir string
}

// Load resolves settings from the environment.
func Load(getenv Getenv) Settings {
	s := Settings{
		APIAddr: strings.TrimSpace(getenv(EnvAPIAddr)),
		Bin:     strings.TrimSpace(getenv(EnvBin)),
		DataDir: strings.TrimSpace(getenv(EnvDataDir)),
	}
	if s.APIAddr == "" {
		s.APIAddr = DefaultAPIAddr
	}
	s.ConfigDir = ConfigDir(getenv)
	return s
}

// ConfigDir returns WHATSAPP_AXI_CONFIG_DIR or <user config dir>/whatsapp-axi.
func ConfigDir(getenv Getenv) string {
	if dir := strings.TrimSpace(getenv(EnvConfigDir)); dir != "" {
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "whatsapp-axi")
}

// Credential is a loaded agent credential. Secret is never printed.
type Credential struct {
	Secret string
	Source string // "env" or the file path
}

// LoadCredential reads the agent credential from WHATSAPP_AXI_TOKEN or the
// private credential file. It returns (nil, nil) when none is configured,
// and a *contract.Error when one is configured but unusable.
func LoadCredential(getenv Getenv, configDir string) (*Credential, error) {
	if v := strings.TrimSpace(getenv(EnvToken)); v != "" {
		if err := validateSecret(v, EnvToken); err != nil {
			return nil, err
		}
		return &Credential{Secret: v, Source: "env"}, nil
	}
	if configDir == "" {
		return nil, nil
	}
	path := filepath.Join(configDir, CredentialFile)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, contract.Errorf(contract.CodeCredentialBad, "check the credential file", "cannot read credential file %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, contract.Errorf(contract.CodeCredentialBad, "replace it with a regular file", "credential file %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, contract.Errorf(contract.CodeCredentialBad, "run `chmod 600 "+path+"`", "credential file %s is readable by other users", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, contract.Errorf(contract.CodeCredentialBad, "check the credential file", "cannot read credential file %s", path)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return nil, contract.Errorf(contract.CodeCredentialBad, "check the credential file", "cannot read credential file %s", path)
	}
	v := strings.TrimSpace(string(b))
	if err := validateSecret(v, path); err != nil {
		return nil, err
	}
	return &Credential{Secret: v, Source: path}, nil
}

// validateSecret rejects anything that is not an agent credential, which
// keeps wasync's api.token, send.token and approve.token out of AXI.
func validateSecret(v, where string) error {
	if !strings.HasPrefix(v, CredentialPrefix) || len(v) <= len(CredentialPrefix) || strings.ContainsAny(v, " \t\r\n") {
		return contract.Errorf(contract.CodeCredentialBad, GrantHelp,
			"%s does not hold a wasync agent credential (expected %s...); wasync api/send tokens are never accepted", where, CredentialPrefix)
	}
	return nil
}
