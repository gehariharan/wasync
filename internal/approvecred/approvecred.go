// Package approvecred keeps the approval credential (approve.token) where only
// wasync can read it silently. On macOS it lives in the login Keychain instead
// of a file: the Keychain trusts the program that stored it, so any other
// program that asks for it (an agent running `security find-generic-password`,
// say) makes macOS ask the person for their password. Elsewhere it stays a
// private file in the data directory, as before.
package approvecred

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gehariharan/wasync/internal/localfs"
)

// FileName is the legacy file, still used where there is no Keychain.
const FileName = "approve.token"

// ErrNotFound is returned by a Backend when it holds nothing for the account.
var ErrNotFound = errors.New("approval credential not found")

// ErrAccessDenied is returned by a Backend when the person cancelled or denied
// the system's request to read the credential, or it could not ask them.
var ErrAccessDenied = errors.New("access to the approval credential was not allowed")

// Backend keeps one secret per data directory outside the data directory.
type Backend interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

var backend = platform()

// Use replaces the backend; nil means the legacy file. For tests and for
// platforms that add a secure store later.
func Use(b Backend) { backend = b }

// InKeychain reports whether this computer keeps the credential outside the data directory.
func InKeychain() bool { return backend != nil }

func account(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return "approve:" + hex.EncodeToString(sum[:8]), nil
}

func valid(token string) bool {
	b, err := hex.DecodeString(token)
	return err == nil && len(b) == 32
}

// Owner returns the credential for the running owner, creating it on first use
// and moving an existing file into the Keychain. fromFile is the legacy loader
// that reads or creates the private file; it is used only without a Keychain.
func Owner(dir string, fromFile func() (string, error)) (string, error) {
	if backend == nil {
		return fromFile()
	}
	acct, err := account(dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName)
	secret, err := backend.Get(acct)
	switch {
	case err == nil && valid(secret):
		// A copy left in the data folder from before is no longer the credential; remove it.
		if _, statErr := os.Lstat(path); statErr == nil {
			_ = os.Remove(path)
		}
		return secret, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return "", fmt.Errorf("read the approval credential from the Keychain: %w", err)
	}
	// Move an existing file in, or make a new credential.
	if b, readErr := readFile(path); readErr == nil && valid(b) {
		secret = b
	} else {
		raw := make([]byte, 32)
		if _, err = rand.Read(raw); err != nil {
			return "", err
		}
		secret = hex.EncodeToString(raw)
	}
	if err = backend.Set(acct, secret); err != nil {
		return "", fmt.Errorf("store the approval credential in the Keychain: %w", err)
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		_ = os.Remove(path)
	}
	return secret, nil
}

// Read returns the credential for a command that signs a request. It never
// creates one: if the owner has not started, there is nothing to sign for.
func Read(dir string) (string, error) {
	if backend != nil {
		acct, err := account(dir)
		if err != nil {
			return "", err
		}
		secret, err := backend.Get(acct)
		if err == nil && valid(secret) {
			return secret, nil
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", fmt.Errorf("read the approval credential from the Keychain: %w", err)
		}
	}
	return readFile(filepath.Join(dir, FileName))
}

func readFile(path string) (string, error) {
	if err := localfs.CheckPrivateFile(path); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
