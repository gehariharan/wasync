//go:build darwin && cgo

package approvecred

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The real login Keychain belongs to the person running the tests, so this runs
// only when asked: WASYNC_TEST_KEYCHAIN=1 go test ./internal/approvecred
// It stores and removes one item with a random name.
func TestRealKeychainRoundTrip(t *testing.T) {
	if os.Getenv("WASYNC_TEST_KEYCHAIN") != "1" {
		t.Skip("set WASYNC_TEST_KEYCHAIN=1 to use the real Keychain")
	}
	raw := make([]byte, 8)
	rand.Read(raw)
	acct := "approve:test-" + hex.EncodeToString(raw)
	k := keychain{}
	t.Cleanup(func() { k.Delete(acct) })
	if _, err := k.Get(acct); err != ErrNotFound {
		t.Fatalf("before: %v", err)
	}
	if err := k.Set(acct, legacy, "/no/such/folder"); err != nil {
		t.Fatal(err)
	}
	items, err := k.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.Account == acct {
			found = it.Folder == "/no/such/folder"
		}
	}
	if !found {
		t.Fatalf("the new item is not listed with its folder: %v", items)
	}
	if got, err := k.Get(acct); err != nil || got != legacy {
		t.Fatalf("get: %q %v", got, err)
	}
	if err := k.Set(acct, "second", "/no/such/folder"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, _ := k.Get(acct); got != "second" {
		t.Fatalf("after update: %q", got)
	}
	if err := k.Delete(acct); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get(acct); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
}

// Without the opt-in, the real Keychain refuses every call before it reaches
// the system, so a test can neither read nor leave an item in it.
func TestTheRealKeychainRefusesATestThatDidNotAsk(t *testing.T) {
	t.Setenv(RealKeychainEnv, "")
	k := keychain{}
	if _, err := k.Get("approve:test"); !errors.Is(err, ErrRealKeychainInTest) {
		t.Fatalf("get: %v", err)
	}
	if err := k.Set("approve:test", legacy, "/x"); !errors.Is(err, ErrRealKeychainInTest) {
		t.Fatalf("set: %v", err)
	}
	if err := k.Delete("approve:test"); !errors.Is(err, ErrRealKeychainInTest) {
		t.Fatalf("delete: %v", err)
	}
	if _, err := k.List(); !errors.Is(err, ErrRealKeychainInTest) {
		t.Fatalf("list: %v", err)
	}
}

// fileKeychain makes a throwaway keychain file that is never on the person's
// keychain list, so these tests run against the real Keychain code without
// touching the person's own Keychain.
func fileKeychain(t *testing.T) keychain {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.keychain")
	if rc := newKeychainFile(path, "test"); rc != statusOK {
		t.Fatalf("create a keychain file: %s", describe(rc))
	}
	return keychain{path: path}
}

// byAnotherProgram stores an item the way a different program would: through
// /usr/bin/security, which the Keychain then trusts instead of this test.
func byAnotherProgram(t *testing.T, k keychain, account, secret string) {
	t.Helper()
	out, err := exec.Command("/usr/bin/security", "add-generic-password", "-s", service, "-a", account, "-w", secret, k.path).CombinedOutput()
	if err != nil {
		t.Fatalf("security add-generic-password: %v %s", err, out)
	}
}

// within fails the test, instead of hanging, if a Keychain call waits for a
// window nobody will answer.
func within(t *testing.T, what string, call func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { call(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("%s waited for a Keychain window", what)
	}
}

func TestAKeychainFileRoundTrip(t *testing.T) {
	k := fileKeychain(t)
	acct := "approve:roundtrip"
	if _, err := k.Get(acct); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before: %v", err)
	}
	if err := k.Set(acct, legacy, "/no/such/folder"); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get(acct); err != nil || got != legacy {
		t.Fatalf("get: %q %v", got, err)
	}
	if err := k.Set(acct, "second", "/no/such/folder"); err != nil {
		t.Fatalf("update: %v", err)
	}
	items, err := k.List()
	if err != nil || len(items) != 1 || items[0] != (Item{Account: acct, Folder: "/no/such/folder"}) {
		t.Fatalf("list: %v %v", items, err)
	}
	if err := k.Delete(acct); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get(acct); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

// What made macOS ask again and again: reading an item another program made.
// wasync's read now fails at once instead of showing the password window.
func TestReadingAnotherProgramsItemNeverShowsAWindow(t *testing.T) {
	k := fileKeychain(t)
	byAnotherProgram(t, k, "approve:foreign", legacy)
	var err error
	within(t, "reading another program's item", func() { _, err = k.Get("approve:foreign") })
	if !errors.Is(err, ErrForeignItem) {
		t.Fatalf("get: %v", err)
	}
	// Its name is still listed, without reading the secret.
	items, err := k.List()
	if err != nil || len(items) != 1 || items[0].Account != "approve:foreign" {
		t.Fatalf("list: %v %v", items, err)
	}
}

// End to end on a real keychain: a signed release that finds an item another
// program made for its data directory under the old name keeps one of its own
// and starts without asking; its commands read it without asking; and the
// next start reads the same item, again without asking.
func TestASignedReleaseStartsWithoutAskingBesideAnotherProgramsItem(t *testing.T) {
	k := fileKeychain(t)
	Use(k)
	was := identity
	identity = team
	t.Cleanup(func() { Use(nil); identity = was })
	dir := dataDir(t)
	old, _ := legacyAccount(dir)
	byAnotherProgram(t, k, old, legacy)
	var first, read, again string
	var errOwner, errRead, errAgain error
	within(t, "starting the owner", func() {
		first, errOwner = Owner(dir, fileLoader(t))
		read, errRead = Read(dir)
		again, errAgain = Owner(dir, fileLoader(t))
	})
	if errOwner != nil || !valid(first) || first == legacy {
		t.Fatalf("owner: %q %v", first, errOwner)
	}
	if errRead != nil || read != first {
		t.Fatalf("read: %q %v", read, errRead)
	}
	if errAgain != nil || again != first {
		t.Fatalf("again: %q %v", again, errAgain)
	}
	acct, _ := Account(dir)
	names := map[string]bool{}
	items, _ := k.List()
	for _, it := range items {
		names[it.Account] = true
	}
	if !names[acct] || !names[old] || len(names) != 2 {
		t.Fatalf("items: %v", items)
	}
}

// A locked keychain is reported, never unlocked behind the person's back by a read.
func TestALockedKeychainFileIsReportedWithoutAWindow(t *testing.T) {
	k := fileKeychain(t)
	if err := k.Set("approve:locked", legacy, "/x"); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/security", "lock-keychain", k.path).CombinedOutput(); err != nil {
		t.Fatalf("lock: %v %s", err, out)
	}
	var err error
	within(t, "reading a locked keychain", func() { _, err = k.Get("approve:locked") })
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("get: %v", err)
	}
}

// A test binary is not signed by a developer team, so it has no signature to
// name items after, and without WASYNC_TEST_KEYCHAIN it would keep the file.
func TestAnUnsignedBuildKeepsTheFile(t *testing.T) {
	if id := signer(); id != "" {
		t.Skipf("this test binary is signed by %s", id)
	}
	t.Setenv(RealKeychainEnv, "")
	if b, id := platform(); b != nil || id != "" {
		t.Fatalf("an unsigned build got %T %q", b, id)
	}
	path := filepath.Join(t.TempDir(), "x.keychain")
	t.Setenv(RealKeychainEnv, path)
	if b, _ := platform(); b != (keychain{path: path}) {
		t.Fatalf("a keychain file was not used: %#v", b)
	}
}
