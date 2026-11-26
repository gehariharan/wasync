// Package plugins carries the WhatsApp Codex plugin inside the wasync
// program, so "Connect an agent" can install it for Codex from a local
// folder without Git or a network download, at the version of the wasync
// that installs it.
package plugins

import "embed"

// WhatsApp holds the files of the whatsapp plugin under "whatsapp/".
//
//go:embed whatsapp
var WhatsApp embed.FS

// Marketplace is the marketplace file that offers the plugin, as in the
// repository's .agents/plugins/marketplace.json (a test keeps them equal).
const Marketplace = `{
  "name": "wasync",
  "interface": {
    "displayName": "wasync"
  },
  "plugins": [
    {
      "name": "whatsapp",
      "source": {
        "source": "local",
        "path": "./plugins/whatsapp"
      },
      "policy": {
        "installation": "AVAILABLE",
        "authentication": "ON_INSTALL"
      },
      "category": "Productivity"
    }
  ]
}
`
