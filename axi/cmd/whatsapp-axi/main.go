// Command whatsapp-axi is the agent-facing CLI for wasync. It reads wasync
// through its loopback API or `wasync query`, and never talks to WhatsApp or
// opens the archive itself.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/gehariharan/wasync/axi/internal/cli"
)

func main() {
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
