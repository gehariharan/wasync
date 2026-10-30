package approvecred

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gehariharan/wasync/internal/localfs"
)

type memory struct {
	items   map[string]string
	getErr  error
	setErrs int
}

func newMemory() *memory { return &memory{items: map[string]string{}} }

func (m *memory) Get(a string) (string, error) {
	if m.getErr != nil {
		return "", m.getErr
	}
	v, ok := m.items[a]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}
func (m *memory) Set(a, s string) error {
	if m.setErrs > 0 {
		m.setErrs--
		return errors.New("keychain locked")
	}
	m.items[a] = s
	return nil
}
func (m *memory) Delete(a string) error { delete(m.items, a); return nil }

const legacy = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func dataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeLegacy(t *testing.T, dir, token string) string {
	t.Helper()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileLoader(t *testing.T) func() (string, error) {
	return func() (string, error) {
		t.Helper()
		t.Fatal("the file loader ran with a Keychain available")
		return "", nil
	}
}

func TestNewCredentialLivesOnlyInTheKeychain(t *testing.T) {
	mem := newMemory()
	Use(mem)
	t.Cleanup(func() { Use(nil) })
	dir := dataDir(t)
	first, err := Owner(dir, fileLoader(t))
	if err != nil || !valid(first) {
		t.Fatalf("first: %q %v", first, err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatal("a readable approve.token file was created")
	}
	if again, _ := Owner(dir, fileLoader(t)); again != first {
		t.Fatal("the credential changed between starts")
	}
	if read, err := Read(dir); err != nil || read != first {
		t.Fatalf("the command reads a different credential: %q %v", read, err)
	}
	other := dataDir(t)
	if o, _ := Owner(other, fileLoader(t)); o == first {
		t.Fatal("two data directories share one credential")
	}
}

func TestAnExistingFileMovesIntoTheKeychainAndIsRemoved(t *testing.T) {
	mem := newMemory()
	Use(mem)
	t.Cleanup(func() { Use(nil) })
	dir := dataDir(t)
	path := writeLegacy(t, dir, legacy)
	got, err := Owner(dir, fileLoader(t))
	if err != nil || got != legacy {
		t.Fatalf("migration kept a different credential: %q %v", got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the readable file was left behind")
	}
	if read, _ := Read(dir); read != legacy {
		t.Fatalf("read %q", read)
	}
}

func TestAStaleFileNeverBeatsTheKeychain(t *testing.T) {
	mem := newMemory()
	Use(mem)
	t.Cleanup(func() { Use(nil) })
	dir := dataDir(t)
	acct, _ := account(dir)
	const current = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
	mem.items[acct] = current
	path := writeLegacy(t, dir, legacy)
	if got, _ := Owner(dir, fileLoader(t)); got != current {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the stale file was left readable")
	}
}

func TestAKeychainFailureNeverMakesANewCredential(t *testing.T) {
	mem := newMemory()
	Use(mem)
	t.Cleanup(func() { Use(nil) })
	dir := dataDir(t)
	acct, _ := account(dir)
	mem.items[acct] = legacy
	mem.getErr = errors.New("user denied access")
	if got, err := Owner(dir, fileLoader(t)); err == nil || got != "" {
		t.Fatalf("a denied read must stop the owner, got %q %v", got, err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("a denied read must not fall back to something else")
	}
	mem.getErr = nil
	if got, _ := Owner(dir, fileLoader(t)); got != legacy {
		t.Fatal("the stored credential was lost")
	}
}

func TestAFailedWriteKeepsTheFile(t *testing.T) {
	mem := newMemory()
	mem.setErrs = 1
	Use(mem)
	t.Cleanup(func() { Use(nil) })
	dir := dataDir(t)
	path := writeLegacy(t, dir, legacy)
	if _, err := Owner(dir, fileLoader(t)); err == nil {
		t.Fatal("expected the write failure to be reported")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the only copy of the credential was deleted after a failed move")
	}
}

func TestReadFallsBackToTheFileForAnOwnerThatPredatesTheKeychain(t *testing.T) {
	Use(newMemory())
	t.Cleanup(func() { Use(nil) })
	dir := dataDir(t)
	writeLegacy(t, dir, legacy)
	if got, err := Read(dir); err != nil || got != legacy {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Read(dataDir(t)); err == nil {
		t.Fatal("reading must never create a credential")
	}
}

func TestWithoutAKeychainTheFileLoaderRuns(t *testing.T) {
	Use(nil)
	dir := dataDir(t)
	ran := false
	got, err := Owner(dir, func() (string, error) { ran = true; return legacy, nil })
	if err != nil || !ran || got != legacy {
		t.Fatalf("%q %v ran=%v", got, err, ran)
	}
	if InKeychain() {
		t.Fatal("reports a Keychain it does not have")
	}
}
