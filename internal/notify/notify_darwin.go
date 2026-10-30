//go:build darwin

package notify

import (
	"context"
	"os/exec"
	"time"
)

// The text travels as script arguments, never inside the script.
func send(title, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "/usr/bin/osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, text).Run()
}
