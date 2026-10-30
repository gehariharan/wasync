package main

import (
	"bytes"
	"context"
	"os"

	"github.com/gehariharan/wasync/axi/internal/buildinfo"
	"github.com/gehariharan/wasync/axi/internal/cli"
	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/mcp"
	"github.com/gehariharan/wasync/axi/internal/transport"
)

// serveMCP runs the tool server on this process's standard input and output.
// Each tool runs a whatsapp-axi command with its own output buffer and no
// access to the tool channel, so a command can never write into the protocol
// stream or read a request.
func serveMCP(ctx context.Context) error {
	launcher := &mcp.Launcher{Bin: func() (string, error) {
		return transport.ResolveBin(config.Load(os.Getenv).Bin)
	}}
	server := &mcp.Server{
		In:         os.Stdin,
		Out:        os.Stdout,
		Version:    buildinfo.Version,
		StartSetup: launcher.Start,
		Run: func(ctx context.Context, args []string) (string, int) {
			var out bytes.Buffer
			app := cli.New(&out)
			app.Stdin = nil
			code := app.Run(ctx, args)
			return out.String(), code
		},
	}
	return server.Serve(ctx)
}
