package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/localfs"
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
	if err := run(app, "status"); err != nil || !strings.Contains(out.String(), "on") || !strings.Contains(out.String(), "machine.example.ts.net") {
		t.Fatalf("status: %v %s", err, out.String())
	}
	// Turning it off is always allowed and needs no confirmation.
	app, _ = build("", false)
	if err := run(app, "off"); err != nil || on() {
		t.Fatalf("off: %v", err)
	}
	app, out = build("", false)
	if err := run(app, "status"); err != nil || !strings.Contains(out.String(), "tailnet access: off") {
		t.Fatalf("status after off: %v %s", err, out.String())
	}
}
