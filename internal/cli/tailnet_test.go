package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/passkey"
	"github.com/gehariharan/wasync/internal/passkey/passkeytest"
	"github.com/gehariharan/wasync/internal/tailnet"
)

func TestTailnetAllowIsAHumanChoice(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	stub := &humanStub{}
	build := func(input string, interactive bool) (*App, *bytes.Buffer) {
		var out bytes.Buffer
		return &App{In: &lineReader{[]string{input}}, Out: &out, Err: &bytes.Buffer{}, Interactive: interactive, Human: humancheck.NewGate(stub)}, &out
	}
	run := func(app *App, args ...string) error {
		return app.Run(context.Background(), append([]string{"--data-dir", dir, "tailnet"}, args...))
	}
	allow := []string{"allow", "--host", "Machine.Example.ts.net", "--login", "owner@example.com"}
	on := func() bool { _, ok, _ := tailnet.Load(dir); return ok }

	app, _ := build("ALLOW", false)
	if err := run(app, allow...); err == nil || !strings.Contains(err.Error(), "interactive terminal") || on() {
		t.Fatalf("non-interactive allow: %v", err)
	}
	app, _ = build("yes", true)
	if err := run(app, allow...); err == nil || on() {
		t.Fatalf("weak confirmation: %v", err)
	}
	app, _ = build("ALLOW", true)
	if err := run(app, allow...); err == nil || !strings.Contains(err.Error(), "nothing changed") || on() {
		t.Fatalf("typed word but the person declined: %v", err)
	}
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], "owner@example.com") || !strings.Contains(stub.asked[0], "https://machine.example.ts.net/ui/") {
		t.Fatalf("prompt: %q", stub.asked)
	}
	for _, bad := range [][]string{
		{"allow", "--host", "machine.example.ts.net"},
		{"allow", "--login", "owner@example.com"},
		{"allow", "--host", "127.0.0.1", "--login", "owner@example.com"},
		{"allow", "--host", "localhost", "--login", "owner@example.com"},
		{"allow", "--host", "machine", "--login", "owner@example.com"},
		{"allow", "--host", "machine.example.ts.net", "--login", "two words"},
		{"allow", "--host", "machine.example.ts.net:8443", "--login", "owner@example.com"},
		{"allow", "--host", "machine.example.ts.net", "--host", "other.example.ts.net", "--login", "owner@example.com"},
		{"allow", "--host", "machine.example.ts.net", "--login", "owner@example.com", "--login", "someone@example.com"},
	} {
		app, _ = build("ALLOW", true)
		if err := run(app, bad...); err == nil || on() {
			t.Fatalf("%v accepted: %v", bad, err)
		}
	}
	stub.confirm = true
	app, out := build("ALLOW", true)
	if err := run(app, allow...); err != nil || !on() {
		t.Fatalf("confirmed allow: %v", err)
	}
	if !strings.Contains(out.String(), "tailscale serve --bg http://127.0.0.1:8787") || !strings.Contains(out.String(), "https://machine.example.ts.net/ui/") {
		t.Fatalf("allow output: %s", out.String())
	}
	app, out = build("", false)
	if err := run(app, "status"); err != nil || !strings.Contains(out.String(), "on") || !strings.Contains(out.String(), "machine.example.ts.net") || !strings.Contains(out.String(), "devices that approve with a passkey: 0") {
		t.Fatalf("status: %v %s", err, out.String())
	}
	// A device set up in the web app shows in status.
	s, _, _ := tailnet.Load(dir)
	addDevice(t, dir, s, "Laptop")
	app, out = build("", false)
	if err := run(app, "status"); err != nil || !strings.Contains(out.String(), "devices that approve with a passkey: 1") || !strings.Contains(out.String(), "Laptop (added ") {
		t.Fatalf("status with a device: %v %s", err, out.String())
	}
	// Turning it off is always allowed, needs no confirmation and forgets the devices.
	app, out = build("", false)
	if err := run(app, "off"); err != nil || on() || !strings.Contains(out.String(), "forgotten") {
		t.Fatalf("off: %v", err)
	}
	if devices, err := tailnet.LoadDevices(dir); err != nil || len(devices) != 0 {
		t.Fatalf("devices after off: %v %v", devices, err)
	}
	app, out = build("", false)
	if err := run(app, "status"); err != nil || !strings.Contains(out.String(), "tailnet access: off") {
		t.Fatalf("status after off: %v %s", err, out.String())
	}
}

func addDevice(t *testing.T, dir string, s tailnet.Setting, name string) {
	t.Helper()
	a := passkeytest.New(passkey.AlgES256)
	challenge := []byte("a challenge of the right sort, 32")
	key, err := passkey.VerifyRegistration(s.Host, challenge, a.Register(s.Host, challenge))
	if err != nil {
		t.Fatal(err)
	}
	d := tailnet.Device{ID: base64.RawURLEncoding.EncodeToString(key.ID), PublicKey: key.PublicKey, Alg: key.Alg, Host: s.Host, Login: s.Login, Name: name, AddedAt: time.Now().UTC().Truncate(time.Second)}
	if err := tailnet.AddDevice(dir, d); err != nil {
		t.Fatal(err)
	}
}

// Allowing another name or login forgets the devices set up for the old one,
// so they do not come back if it is allowed again later.
func TestTailnetAllowForgetsDevicesOfAnEarlierSetting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	first := tailnet.Setting{Host: "machine.example.ts.net", Login: "owner@example.com"}
	if err := tailnet.Save(dir, first); err != nil {
		t.Fatal(err)
	}
	addDevice(t, dir, first, "Laptop")
	stub := &humanStub{confirm: true}
	allow := func(host, login string) {
		t.Helper()
		app := &App{In: &lineReader{[]string{"ALLOW"}}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Interactive: true, Human: humancheck.NewGate(stub)}
		if err := app.Run(context.Background(), []string{"--data-dir", dir, "tailnet", "allow", "--host", host, "--login", login}); err != nil {
			t.Fatal(err)
		}
	}
	allow(first.Host, first.Login) // the same again keeps it
	if devices, _ := tailnet.LoadDevices(dir); len(devices) != 1 {
		t.Fatalf("devices after allowing the same: %v", devices)
	}
	allow(first.Host, "someone@example.com")
	allow(first.Host, first.Login)
	if devices, _ := tailnet.LoadDevices(dir); len(devices) != 0 {
		t.Fatalf("devices came back: %v", devices)
	}
}
