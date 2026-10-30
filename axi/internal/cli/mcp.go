package cli

import (
	"context"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

var mcpCommand = &command{
	name:        "mcp",
	usage:       "whatsapp-axi mcp",
	description: "Serve the same reads and the allowed writes as MCP tools over standard input and output, for apps such as the Codex desktop app; it never approves, grants or reveals a credential",
	examples:    []string{"whatsapp-axi mcp"},
	run:         (*App).runMCP,
}

func (a *App) runMCP(ctx context.Context, _ invocation) error {
	if a.MCP == nil {
		return contract.Errorf(contract.CodeUsage, "run `whatsapp-axi mcp` from the installed program", "the tool server is not available in this build")
	}
	return a.MCP(ctx)
}
