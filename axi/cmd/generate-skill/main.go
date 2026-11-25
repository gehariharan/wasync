// Command generate-skill writes or checks the CLI-derived agent skill.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/gehariharan/wasync/axi/internal/cli"
)

func main() {
	check := flag.Bool("check", false, "fail when the committed skill differs")
	output := flag.String("output", "skills/wasync-axi/SKILL.md", "skill path")
	flag.Parse()
	content := cli.SkillDocument()
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil || !bytes.Equal(current, content) {
			fmt.Fprintln(os.Stderr, "generated WhatsApp skill is stale; run go generate ./skills")
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(*output, content, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
