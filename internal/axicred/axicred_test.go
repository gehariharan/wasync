package axicred

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
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

// whatsapp-axi lives in a separate Go module, so compare the literals in its
// source rather than importing it.
func TestPathRulesMatchWhatsappAxi(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "axi", "internal", "config", "config.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{
		`EnvToken     = "` + EnvToken + `"`,
		`EnvConfigDir = "` + EnvConfigDir + `"`,
		`CredentialFile = "` + FileName + `"`,
		`filepath.Join(base, "` + DirName + `")`,
		`os.UserConfigDir()`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("axi config no longer contains %s", want)
		}
	}
	if !regexp.MustCompile(`filepath\.Join\(configDir, CredentialFile\)`).MatchString(text) {
		t.Error("axi no longer reads <config dir>/credential")
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
