// Command wasync-axi is the agent-facing CLI for wasync. It reads wasync
// through its loopback API or `wasync query`, and never talks to WhatsApp or
// opens the archive itself.
package main

import "github.com/gehariharan/wasync/axi/internal/entry"

func main() { entry.Main() }
