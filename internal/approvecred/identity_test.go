package approvecred

import (
	"errors"
	"strings"
	"testing"
)

const team = "57V379V642.com.gehariharan.wasynctuicli"

// signedRelease makes the package behave as a release signed by team, keeping
// its credential in mem.
func signedRelease(t *testing.T, mem *memory) {
	t.Helper()
	Use(mem)
	was := identity
	identity = team
	t.Cleanup(func() { Use(nil); identity = was })
}

func TestASignedReleaseNamesItsItemAfterItsSignature(t *testing.T) {
	mem := newMemory()
	signedRelease(t, mem)
	dir := dataDir(t)
	got, err := Owner(dir, fileLoader(t))
	if err != nil || !valid(got) {
		t.Fatalf("%q %v", got, err)
	}
	old, _ := legacyAccount(dir)
	acct, _ := Account(dir)
	if acct != old+"@"+team {
		t.Fatalf("account %q", acct)
	}
	if mem.items[acct] != got || len(mem.items) != 1 {
		t.Fatalf("items %v", mem.items)
	}
	if read, err := Read(dir); err != nil || read != got {
		t.Fatalf("read %q %v", read, err)
	}
}

// The case that kept asking for the Keychain password: an item for the data
// directory under the old name that another program made (an unsigned build,
// say). The Keychain would have asked before giving it to the release, so the
// release never reads it and keeps an item of its own.
func TestAnItemAnotherProgramMadeIsLeftAloneAndNeverAskedFor(t *testing.T) {
	mem := newMemory()
	signedRelease(t, mem)
	dir := dataDir(t)
	old, _ := legacyAccount(dir)
	mem.items[old] = legacy
	mem.errs = map[string]error{old: ErrForeignItem}
	got, err := Owner(dir, fileLoader(t))
	if err != nil || !valid(got) || got == legacy {
		t.Fatalf("%q %v", got, err)
	}
	if mem.items[old] != legacy {
		t.Fatal("the other program's item was changed")
	}
	if read, err := Read(dir); err != nil || read != got {
		t.Fatalf("read %q %v", read, err)
	}
	// The next start reads its own item; the old one is never touched again.
	if again, err := Owner(dir, fileLoader(t)); err != nil || again != got {
		t.Fatalf("again %q %v", again, err)
	}
}

// An earlier release kept the item under the old name. The Keychain gives it to
// this release without asking (same signature), so it moves to the new name,
// keeping the same credential.
func TestAnEarlierReleasesItemMovesToTheNewName(t *testing.T) {
	mem := newMemory()
	signedRelease(t, mem)
	dir := dataDir(t)
	old, _ := legacyAccount(dir)
	mem.items[old] = legacy
	got, err := Owner(dir, fileLoader(t))
	if err != nil || got != legacy {
		t.Fatalf("%q %v", got, err)
	}
	acct, _ := Account(dir)
	if mem.items[acct] != legacy {
		t.Fatal("not stored under the new name")
	}
	if _, ok := mem.items[old]; ok {
		t.Fatal("the old item was left behind")
	}
}

// A command from this release still reads the credential of an owner from a
// release before the items were renamed, so updating never breaks commands.
func TestACommandReadsAnOlderOwnersItem(t *testing.T) {
	mem := newMemory()
	signedRelease(t, mem)
	dir := dataDir(t)
	old, _ := legacyAccount(dir)
	mem.items[old] = legacy
	if got, err := Read(dir); err != nil || got != legacy {
		t.Fatalf("%q %v", got, err)
	}
	if len(mem.items) != 1 {
		t.Fatal("reading changed the Keychain")
	}
}

// A command never asks: an item this program could read only by asking is an
// error that says what to do.
func TestACommandNeverAsksForAnotherProgramsItem(t *testing.T) {
	mem := newMemory()
	signedRelease(t, mem)
	dir := dataDir(t)
	old, _ := legacyAccount(dir)
	mem.items[old] = legacy
	mem.errs = map[string]error{old: ErrForeignItem}
	_, err := Read(dir)
	if !errors.Is(err, ErrForeignItem) || !strings.Contains(err.Error(), "different build") {
		t.Fatalf("%v", err)
	}
}

// Someone else's item under this release's own name: the owner stops and says
// which item to delete, instead of asking.
func TestAnItemUnderItsOwnNameThatItCannotReadStopsTheOwner(t *testing.T) {
	mem := newMemory()
	signedRelease(t, mem)
	dir := dataDir(t)
	acct, _ := Account(dir)
	mem.items[acct] = legacy
	mem.errs = map[string]error{acct: ErrForeignItem}
	_, err := Owner(dir, fileLoader(t))
	if !errors.Is(err, ErrForeignItem) || !strings.Contains(err.Error(), acct) || !strings.Contains(err.Error(), "Keychain Access") {
		t.Fatalf("%v", err)
	}
	if mem.items[acct] != legacy {
		t.Fatal("the item was replaced")
	}
}

// A locked Keychain is reported to the owner and to a command alike; neither
// asks the person to unlock it, and no credential is made in its place.
func TestALockedKeychainIsReportedWithoutAsking(t *testing.T) {
	mem := newMemory()
	signedRelease(t, mem)
	dir := dataDir(t)
	mem.locked = true
	if _, err := Read(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("read: %v", err)
	}
	if _, err := Owner(dir, fileLoader(t)); !errors.Is(err, ErrLocked) {
		t.Fatalf("owner: %v", err)
	}
	if len(mem.items) != 0 {
		t.Fatalf("a locked Keychain was written to: %v", mem.items)
	}
}

// Without a signature the names are the ones wasync always used.
func TestWithoutASignatureTheNameIsUnchanged(t *testing.T) {
	Use(newMemory())
	t.Cleanup(func() { Use(nil) })
	dir := dataDir(t)
	acct, _ := Account(dir)
	old, _ := legacyAccount(dir)
	if acct != old || !strings.HasPrefix(acct, "approve:") || strings.Contains(acct, "@") {
		t.Fatalf("%q %q", acct, old)
	}
}
