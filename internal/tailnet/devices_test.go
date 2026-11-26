package tailnet

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/passkey"
)

func device(n byte, host, login string) Device {
	return Device{ID: base64.RawURLEncoding.EncodeToString([]byte{n, 1, 2, 3}), PublicKey: []byte("public key"), Alg: passkey.AlgES256, Host: host, Login: login, Name: "Laptop", AddedAt: time.Now().UTC()}
}

func TestDevicesBelongToOneHostAndLogin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if list, err := LoadDevices(dir); err != nil || len(list) != 0 {
		t.Fatalf("default: %v %v", list, err)
	}
	s := Setting{Host: "machine.example.ts.net", Login: "owner@example.com"}
	for _, d := range []Device{device(1, s.Host, s.Login), device(2, s.Host, "someone@example.com"), device(3, "other.example.ts.net", s.Login)} {
		if err := AddDevice(dir, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddDevice(dir, device(1, s.Host, s.Login)); err == nil {
		t.Fatal("the same passkey was set up twice")
	}
	all, err := LoadDevices(dir)
	if err != nil || len(all) != 3 || len(s.For(all)) != 1 || s.For(all)[0].ID != device(1, "", "").ID {
		t.Fatalf("for this setting: %v %v", all, err)
	}
	if err := UsedDevice(dir, device(1, "", "").ID, 7, time.Now()); err != nil {
		t.Fatal(err)
	}
	if all, _ = LoadDevices(dir); s.For(all)[0].SignCount != 7 || s.For(all)[0].UsedAt == nil {
		t.Fatalf("after use: %+v", s.For(all)[0])
	}
	if err := KeepDevices(dir, s); err != nil {
		t.Fatal(err)
	}
	if all, _ = LoadDevices(dir); len(all) != 1 {
		t.Fatalf("after keeping only this setting's: %v", all)
	}
	if removed, found, err := RemoveDevice(dir, device(1, "", "").ID); err != nil || !found || removed.Name != "Laptop" {
		t.Fatalf("remove: %v %v %v", removed, found, err)
	}
	if _, found, err := RemoveDevice(dir, device(1, "", "").ID); err != nil || found {
		t.Fatalf("removing twice: %v %v", found, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, DevicesFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an empty list leaves a file behind: %v", err)
	}
}

func TestAtMostMaxDevices(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	for i := range MaxDevices {
		if err := AddDevice(dir, device(byte(i), "machine.example.ts.net", "owner@example.com")); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddDevice(dir, device(200, "machine.example.ts.net", "owner@example.com")); !errors.Is(err, ErrTooManyDevices) {
		t.Fatalf("one too many: %v", err)
	}
}

func TestDamagedOrSharedDeviceFilesCountAsNone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, DevicesFileName)
	good := `{"id":"AQID","public_key":"cGs=","alg":-7,"sign_count":0,"host":"machine.example.ts.net","login":"owner@example.com","name":"x","added_at":"2026-11-26T00:00:00Z"}`
	for _, content := range []string{
		`not json`,
		`{"devices":[{"id":"","public_key":"cGs=","alg":-7,"host":"machine.example.ts.net","login":"a@b"}]}`,
		`{"devices":[{"id":"AQID","public_key":"cGs=","alg":-999,"host":"machine.example.ts.net","login":"a@b"}]}`,
		`{"devices":[{"id":"AQID","public_key":"cGs=","alg":-7,"host":"127.0.0.1","login":"a@b"}]}`,
		`{"devices":[{"id":"AQID","public_key":"","alg":-7,"host":"machine.example.ts.net","login":"a@b"}]}`,
		`{"devices":[` + good + `,` + good + `]}`,
		`{"devices":[],"extra":1}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if list, err := LoadDevices(dir); err == nil {
			t.Fatalf("%q loaded: %v", content, list)
		}
		// A damaged list is never rewritten by adding to it.
		if err := AddDevice(dir, device(9, "machine.example.ts.net", "owner@example.com")); err == nil {
			t.Fatalf("added to damaged %q", content)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(path, []byte(`{"devices":[`+good+`]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDevices(dir); err == nil {
			t.Fatal("a file others can read was trusted")
		}
	}
	if err := RemoveDevices(dir); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDevices(dir); err != nil {
		t.Fatal("removing twice:", err)
	}
}
