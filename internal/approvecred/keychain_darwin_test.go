//go:build darwin && cgo

package approvecred

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
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
	if err := k.Set(acct, legacy); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get(acct); err != nil || got != legacy {
		t.Fatalf("get: %q %v", got, err)
	}
	if err := k.Set(acct, "second"); err != nil {
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
