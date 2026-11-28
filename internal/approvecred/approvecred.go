// Package approvecred keeps the approval credential (approve.token) where only
// wasync can read it silently. On macOS a signed release keeps it in the login
// Keychain instead of a file: the Keychain trusts the program that stored it, so
// any other program that asks for it (an agent running `security
// find-generic-password`, say) makes macOS ask the person for their password.
// wasync itself never makes macOS ask: each item is named after the signature
// of the program that keeps it, so a release only ever reads its own item, and
// every call fails rather than show a window. Elsewhere, and for a build that is
// not signed by a developer team, it stays a private file in the data directory.
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
	"testing"

	"github.com/gehariharan/wasync/internal/localfs"
)

// FileName is the legacy file, still used where there is no Keychain.
const FileName = "approve.token"

// ErrNotFound is returned by a Backend when it holds nothing for the account.
var ErrNotFound = errors.New("approval credential not found")

// ErrAccessDenied is returned by a Backend when the person cancelled or denied
// the system's request to read the credential, or it could not ask them.
var ErrAccessDenied = errors.New("access to the approval credential was not allowed")

// ErrForeignItem is returned when the Keychain already holds an item for this
// data directory that a different program made, so this one could use it only
// by asking the person, which wasync never does.
var ErrForeignItem = errors.New("the Keychain item for this data directory belongs to a different program")

// ErrLocked is returned when the Keychain is locked, so macOS would have to
// ask for its password before wasync could use it.
var ErrLocked = errors.New("the Keychain is locked")

// ErrRealKeychainInTest is returned when a test binary reaches the person's real
// Keychain without having been asked to. See RealKeychainEnv.
var ErrRealKeychainInTest = errors.New("a test reached the real Keychain; set " + RealKeychainEnv + "=1 only to test the Keychain itself")

// RealKeychainEnv, set to 1, lets a test binary, or a build that is not signed
// by a developer team, use the person's real login Keychain. Without it a test
// can never read or write there: every data directory, including each temporary
// test directory, would otherwise leave its own item behind. Set to the absolute
// path of a keychain file instead, it makes any build use that keychain, so the
// Keychain code can be tried without touching the person's own.
const RealKeychainEnv = "WASYNC_TEST_KEYCHAIN"

// Backend keeps one secret per data directory outside the data directory.
// folder is the data directory the secret belongs to; it is stored beside the
// secret so a later cleanup can tell which items have lost their folder.
type Backend interface {
	Get(account string) (string, error)
	Set(account, secret, folder string) error
	Delete(account string) error
}

// Item describes one stored credential without its secret.
type Item struct {
	Account string
	// Folder is the data directory the item was made for. It is empty for an
	// item made before wasync recorded it.
	Folder string
}

// Lister is implemented by a Backend that can list its items without reading
// any secret, so listing never raises a system prompt.
type Lister interface {
	List() ([]Item, error)
}

// backend keeps the credential; identity is the signature it is kept under
// ("TEAM.identifier", or "" for the names wasync used before).
var backend, identity = defaultBackend()

// unlocker is a Backend that can ask the person to unlock it.
type unlocker interface{ unlock() error }

// defaultBackend is the Keychain for a signed macOS release and nothing
// elsewhere. A test binary gets nothing either, unless it asked for a Keychain.
func defaultBackend() (Backend, string) {
	if v := os.Getenv(RealKeychainEnv); testing.Testing() && v != "1" && !filepath.IsAbs(v) {
		return nil, ""
	}
	return platform()
}

// refuseInTest is called by a backend before it touches the real Keychain.
func refuseInTest() error {
	if testing.Testing() && os.Getenv(RealKeychainEnv) != "1" {
		return ErrRealKeychainInTest
	}
	return nil
}

// Use replaces the backend; nil means the legacy file. For tests and for
// platforms that add a secure store later.
func Use(b Backend) { backend = b }

// Items lists what is stored for wasync without reading any secret. It reports
// errors.ErrUnsupported on a computer that keeps the credential in a file.
func Items() ([]Item, error) {
	l, ok := backend.(Lister)
	if !ok {
		return nil, errors.ErrUnsupported
	}
	return l.List()
}

// Forget removes one stored item. Removing nothing is not an error.
func Forget(account string) error {
	if backend == nil {
		return errors.ErrUnsupported
	}
	return backend.Delete(account)
}

// Account is the name of the item for a data directory: one per directory and
// signature.
func Account(dir string) (string, error) { return account(dir) }

// InKeychain reports whether this wasync keeps the credential outside the data directory.
func InKeychain() bool { return backend != nil }

// account names the item this program keeps for a data directory. A signed
// release adds its signature, "approve:<folder>@TEAM.identifier", so the item
// it reads is always one a program with that signature made, which the
// Keychain gives it without asking. Every build with the same signature (each
// later release, and the app beside its command) shares it.
func account(dir string) (string, error) {
	acct, err := legacyAccount(dir)
	if err != nil || identity == "" {
		return acct, err
	}
	return acct + "@" + identity, nil
}

// legacyAccount is the name wasync gave every item before it named them after
// the signature: "approve:<folder>", whichever program made it.
func legacyAccount(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return "approve:" + hex.EncodeToString(sum[:8]), nil
}

// folder is the absolute data directory, or the name as given if that fails.
func folder(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func valid(token string) bool {
	b, err := hex.DecodeString(token)
	return err == nil && len(b) == 32
}

// Owner returns the credential for the running owner, creating it on first use
// and moving an existing file into the Keychain. fromFile is the legacy loader
// that reads or creates the private file; it is used only without a Keychain.
//
// It never makes macOS ask the person anything, with one exception: when the
// login Keychain is locked it asks once to unlock it, as any program would.
func Owner(dir string, fromFile func() (string, error)) (string, error) {
	if backend == nil {
		return fromFile()
	}
	secret, err := owner(dir)
	if errors.Is(err, ErrLocked) {
		if u, ok := backend.(unlocker); ok {
			if err = u.unlock(); err != nil {
				return "", fmt.Errorf("unlock the Keychain: %w", err)
			}
			secret, err = owner(dir)
		}
	}
	return secret, err
}

func owner(dir string) (string, error) {
	acct, err := account(dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName)
	secret, err := backend.Get(acct)
	switch {
	case err == nil && valid(secret):
		// A copy left in the data folder from before is no longer the credential; remove it.
		removeFile(path)
		return secret, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return "", readFailure(acct, err, true)
	}
	// Nothing under this program's name yet. Take over the item an earlier
	// release kept under the older name, but only when the Keychain gives it
	// without asking (a release with this same signature made it). An item
	// another program made is left alone: reading it would make macOS ask.
	// Otherwise move an existing file in, or make a new credential.
	moved := ""
	if old, _ := legacyAccount(dir); old != acct {
		if s, err := backend.Get(old); err == nil && valid(s) {
			secret, moved = s, old
		}
	}
	if moved == "" {
		if b, readErr := readFile(path); readErr == nil && valid(b) {
			secret = b
		} else {
			raw := make([]byte, 32)
			if _, err = rand.Read(raw); err != nil {
				return "", err
			}
			secret = hex.EncodeToString(raw)
		}
	}
	if err = backend.Set(acct, secret, folder(dir)); err != nil {
		return "", fmt.Errorf("store the approval credential in the Keychain: %w", err)
	}
	if moved != "" {
		_ = backend.Delete(moved) // best effort: it is this program's own, and only a leftover now
	}
	removeFile(path)
	return secret, nil
}

func removeFile(path string) {
	if _, err := os.Lstat(path); err == nil {
		_ = os.Remove(path)
	}
}

// readFailure explains a Keychain read that did not give the credential.
func readFailure(acct string, err error, owner bool) error {
	switch {
	case errors.Is(err, ErrForeignItem) && owner:
		return fmt.Errorf("%w; delete the %q item in Keychain Access and start wasync again", err, acct)
	case errors.Is(err, ErrForeignItem):
		return fmt.Errorf("%w: the wasync that owns this data directory is a different build from this one, so this one cannot read its approval credential without macOS asking for your password; use the wasync that came with the running app or service, or start this one instead", err)
	case errors.Is(err, ErrLocked):
		return fmt.Errorf("%w: unlock the login Keychain (Keychain Access) and try again", err)
	}
	return fmt.Errorf("read the approval credential from the Keychain: %w", err)
}

// Read returns the credential for a command that signs a request. It never
// creates one: if the owner has not started, there is nothing to sign for. It
// never makes macOS ask the person: a credential this program could read only
// by asking is an error.
func Read(dir string) (string, error) {
	if backend != nil {
		acct, err := account(dir)
		if err != nil {
			return "", err
		}
		names := []string{acct}
		if old, _ := legacyAccount(dir); old != acct {
			// An owner from a release before items were named after the signature.
			names = append(names, old)
		}
		for _, name := range names {
			secret, err := backend.Get(name)
			if err == nil && valid(secret) {
				return secret, nil
			}
			if err != nil && !errors.Is(err, ErrNotFound) {
				return "", readFailure(name, err, false)
			}
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
