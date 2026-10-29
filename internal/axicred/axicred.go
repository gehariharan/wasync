// Package axicred saves an agent credential where whatsapp-axi looks for it.
// It mirrors the path rules in axi/internal/config (the two modules cannot
// import each other); TestPathRulesMatchWhatsappAxi keeps them in agreement.
package axicred

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvConfigDir overrides the whatsapp-axi config directory.
	EnvConfigDir = "WHATSAPP_AXI_CONFIG_DIR"
	// EnvToken is read by whatsapp-axi before the credential file.
	EnvToken = "WHATSAPP_AXI_TOKEN"
	// DirName is the folder inside the user config directory.
	DirName = "whatsapp-axi"
	// FileName is the credential file inside that folder.
	FileName = "credential"
)

// Path returns where whatsapp-axi reads its credential file: under
// WHATSAPP_AXI_CONFIG_DIR, else the OS user config directory (macOS
// Application Support, Linux XDG config, Windows AppData).
func Path(getenv func(string) string) (string, error) {
	dir := strings.TrimSpace(getenv(EnvConfigDir))
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("find the user config directory: %w", err)
		}
		dir = filepath.Join(base, DirName)
	}
	return filepath.Join(dir, FileName), nil
}

// Existing returns the credential currently saved at path, or "" when there
// is none.
func Existing(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Save writes the credential to path atomically: the directory is created
// private (0700), the file is written to a private (0600) temporary file in
// that directory and renamed into place, so a reader never sees a partial
// secret. It never logs the secret.
func Save(path, credential string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; remove it first", path)
	}
	tmp, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	name := tmp.Name()
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil && !isWindows() {
		return err
	}
	if _, err = tmp.WriteString(credential + "\n"); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	done = true
	return nil
}
