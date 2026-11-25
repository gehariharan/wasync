// Package entry is the main program of wasync-axi, shared by its command
// package and the legacy whatsapp-axi command package.
package entry

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gehariharan/wasync/axi/internal/cli"
)

// Main runs wasync-axi with the process arguments and exits.
func Main() {
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
