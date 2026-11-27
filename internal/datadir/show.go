package datadir

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Show opens dir in the computer's file manager: Finder, File Explorer, or
// the desktop's default on Linux.
func Show(dir string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("/usr/bin/open", dir).Run()
	case "windows":
		// Explorer reports failure even when it opens the folder, so its
		// exit status says nothing; it is only reaped.
		cmd := exec.Command("explorer.exe", dir)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	if err := exec.Command("xdg-open", dir).Run(); err != nil {
		return fmt.Errorf("open the folder with xdg-open: %w", err)
	}
	return nil
}
