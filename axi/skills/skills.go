// Package skills embeds the agent skills shipped in this repository, so
// `wasync-axi setup skill` installs exactly the file under skills/ and
// `wasync-axi doctor` can tell when an installed copy is stale.
package skills

import _ "embed"

//go:generate go run ../cmd/generate-skill --output wasync-axi/SKILL.md

// WasyncAXI is skills/wasync-axi/SKILL.md.
//
//go:embed wasync-axi/SKILL.md
var WasyncAXI []byte
