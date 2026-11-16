//go:build darwin && cgo

package approvecred

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
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
