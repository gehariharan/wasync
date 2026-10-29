package axicred

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestPathHonoursConfigDirOverride(t *testing.T) {
	got, err := Path(func(k string) string {
		if k == EnvConfigDir {
			return "/tmp/x"
		}
		return ""
	})
	if err != nil || got != filepath.Join("/tmp/x", "credential") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestPathDefaultsToUserConfigDir(t *testing.T) {
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skip("no user config dir")
	}
	got, err := Path(func(string) string { return "" })
	if err != nil || got != filepath.Join(base, "whatsapp-axi", "credential") {
		t.Fatalf("%q %v", got, err)
	}
}

var (
	axiOnce sync.Once
	axiBin  string
	axiErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if axiBin != "" {
		os.RemoveAll(filepath.Dir(axiBin))
	}
	os.Exit(code)
}

// whatsappAxi builds the real whatsapp-axi (a separate Go module, so it
// cannot be imported) once per test run.
func whatsappAxi(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds whatsapp-axi")
	}
	axiOnce.Do(func() {
		dir, err := os.MkdirTemp("", "axicred-axi-")
		if err != nil {
			axiErr = err
			return
		}
		axiBin = filepath.Join(dir, "whatsapp-axi")
		if runtime.GOOS == "windows" {
			axiBin += ".exe"
		}
		cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", axiBin, "./cmd/whatsapp-axi")
		cmd.Dir = filepath.Join("..", "..", "axi")
		if out, err := cmd.CombinedOutput(); err != nil {
			axiErr = fmt.Errorf("build whatsapp-axi: %v\n%s", err, out)
		}
	})
	if axiErr != nil {
		t.Fatal(axiErr)
	}
	return axiBin
}

// axiCredentialState runs whatsapp-axi's home view with no wasync reachable
// and returns its "credential:" line.
func axiCredentialState(t *testing.T, bin string, env map[string]string) string {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Dir = t.TempDir()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != EnvToken && k != EnvConfigDir {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "WASYNC_API_ADDR=127.0.0.1:1", "WASYNC_BIN="+filepath.Join(t.TempDir(), "none"))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("whatsapp-axi: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "wsa_saved") {
		t.Fatalf("whatsapp-axi printed the credential:\n%s", out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "credential: "); ok {
			return v
		}
	}
	t.Fatalf("no credential line:\n%s", out)
	return ""
}

func TestWhatsappAxiReadsSavedCredentialFromConfigDir(t *testing.T) {
	bin := whatsappAxi(t)
	dir := filepath.Join(t.TempDir(), "axi")
	env := map[string]string{EnvConfigDir: dir}
	if got := axiCredentialState(t, bin, env); got != "missing" {
		t.Fatalf("before save: %q", got)
	}
	path, err := Path(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "wsa_saved"); err != nil {
		t.Fatal(err)
	}
	if got := axiCredentialState(t, bin, env); got != "file" {
		t.Fatalf("after save: %q", got)
	}
}

func TestWhatsappAxiReadsSavedCredentialFromUserConfigDir(t *testing.T) {
	bin := whatsappAxi(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("AppData", filepath.Join(home, "appdata"))
	path, err := Path(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, home) {
		t.Fatalf("path %q is not under the test home", path)
	}
	if err := Save(path, "wsa_saved"); err != nil {
		t.Fatal(err)
	}
	if got := axiCredentialState(t, bin, nil); got != "file" {
		t.Fatalf("after save: %q", got)
	}
}

func TestSaveIsPrivateAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "whatsapp-axi", "credential")
	if err := Save(path, "wsa_one"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Existing(path); got != "wsa_one" {
		t.Fatalf("got %q", got)
	}
	if err := Save(path, "wsa_two"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Existing(path); got != "wsa_two" {
		t.Fatalf("got %q", got)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		di, _ := os.Stat(filepath.Dir(path))
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Fatalf("file %v dir %v", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestExistingMissing(t *testing.T) {
	if got, err := Existing(filepath.Join(t.TempDir(), "none")); got != "" || err != nil {
		t.Fatalf("%q %v", got, err)
	}
}
