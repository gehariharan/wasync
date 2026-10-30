package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMCPCommandNeedsTheServerFromMain(t *testing.T) {
	var out bytes.Buffer
	app := New(&out)
	if code := app.Run(context.Background(), []string{"mcp"}); code == ExitOK || !strings.Contains(out.String(), "not available") {
		t.Fatalf("without a server: code %d %q", code, out.String())
	}
	out.Reset()
	called := false
	app.MCP = func(context.Context) error { called = true; return nil }
	if code := app.Run(context.Background(), []string{"mcp"}); code != ExitOK || !called || out.Len() != 0 {
		t.Fatalf("with a server: code %d called=%v out=%q", code, called, out.String())
	}
	out.Reset()
	if code := app.Run(context.Background(), []string{"mcp", "--bogus"}); code != ExitUsage {
		t.Fatalf("a stray flag must be a usage error, got %d", code)
	}
}
