// Command wasync-axi is the agent-facing CLI for wasync. It reads wasync
// through its loopback API or `wasync query`, and never talks to WhatsApp or
// opens the archive itself.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gehariharan/wasync/axi/internal/cli"
)

func main() {
	if legacyName(os.Args[0]) {
		fmt.Fprintln(os.Stderr, "note: whatsapp-axi is now wasync-axi; the old name will be removed in a future release")
	}
	args := os.Args[1:]
	if cli.FastPath(args, os.Stdout) {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	app := cli.New(os.Stdout)
	app.MCP = serveMCP
	code := app.Run(ctx, args)
	stop()
	os.Exit(code)
}

// legacyName reports whether the program was run by its old name,
// whatsapp-axi, which installs keep as an alias for wasync-axi.
func legacyName(argv0 string) bool {
	base := argv0[strings.LastIndexAny(argv0, `/\`)+1:]
	return strings.TrimSuffix(strings.ToLower(base), ".exe") == "whatsapp-axi"
}
