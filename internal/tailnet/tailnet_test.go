package tailnet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gehariharan/wasync/internal/localfs"
)

func TestHostNamesMustBeFullTailnetNames(t *testing.T) {
	for in, want := range map[string]string{
		"machine.example.ts.net":  "machine.example.ts.net",
		"Machine.Example.TS.net.": "machine.example.ts.net",
	} {
		if got, err := CanonicalHost(in); err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "machine", "localhost", "a.localhost", "127.0.0.1", "100.64.0.1", "[::1]:443", "machine.example.ts.net:443", "machine.example.ts.net:8443", "machine.example.ts.net:http", "bad_name.ts.net", "-a.ts.net", "a..ts.net", "https://machine.example.ts.net"} {
		if got, err := CanonicalHost(bad); err == nil {
			t.Fatalf("%q accepted as %q", bad, got)
		}
	}
}

func TestHostAndLoginMatching(t *testing.T) {
	s, err := Setting{Host: "Machine.example.ts.net", Login: "owner@example.com"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{
		"machine.example.ts.net":      true,
		"MACHINE.example.ts.net:443":  true,
		"machine.example.ts.net:8443": false,
		"other.example.ts.net":        false,
		"machine.example.ts.net.evil": false,
		"127.0.0.1:8787":              false,
	} {
		if got := s.Serves(host); got != want {
			t.Fatalf("Serves(%q) = %v, want %v", host, got, want)
		}
	}
	if !s.Allows("owner@example.com") || s.Allows("") || s.Allows("Owner@example.com") || s.Allows("someone@example.com") {
		t.Fatal("login matching")
	}
	if (Setting{}).Serves("") {
		t.Fatal("an empty setting serves an empty host")
	}
}

func TestSaveLoadRemoveAndDamagedFilesFailClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, on, err := Load(dir); on || err != nil {
		t.Fatalf("default: %v %v", on, err)
	}
	if err := Save(dir, Setting{Host: "machine.example.ts.net"}); err == nil {
		t.Fatal("saved without a login")
	}
	if err := Save(dir, Setting{Host: "machine.example.ts.net", Login: "owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	s, on, err := Load(dir)
	if err != nil || !on || !s.Serves("machine.example.ts.net") || !s.Allows("owner@example.com") || s.SetAt.IsZero() {
		t.Fatalf("round trip: %+v %v %v", s, on, err)
	}
	path := filepath.Join(dir, FileName)
	for _, content := range []string{`not json`, `{"host":"machine.example.ts.net","login":""}`, `{"host":"127.0.0.1","login":"a@b"}`, `{"host":"machine.example.ts.net:8443","login":"a@b"}`, `{"host":"machine.example.ts.net","login":"a@b","extra":1}`, `{"hosts":["machine.example.ts.net"],"logins":["a@b"]}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, on, err := Load(dir); on || err == nil {
			t.Fatalf("%q loaded: %v %v", content, on, err)
		}
	}
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir); err != nil {
		t.Fatal("removing twice:", err)
	}
	if _, on, _ := Load(dir); on {
		t.Fatal("still on after Remove")
	}
}
