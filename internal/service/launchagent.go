package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Label is the macOS login item's name.
const Label = "com.wasync.serve"

// launchAgent is a macOS per-user login item: a property list in
// ~/Library/LaunchAgents that starts `wasync serve` at login. Installing only
// writes the file; Start loads it now, which the caller does only when no
// other wasync holds the data directory.
type launchAgent struct {
	Home string
	// Run runs a command; tests replace it.
	Run func(name string, args ...string) error
	// Wait pauses between tries to load the item; nil does not pause.
	Wait func(time.Duration)
}

func (l launchAgent) Where() string { return "a macOS login item" }

func (l launchAgent) path() string {
	return filepath.Join(l.Home, "Library", "LaunchAgents", Label+".plist")
}

func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func plist(s Spec) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + escape(s.Exe) + `</string>
		<string>--data-dir</string>
		<string>` + escape(s.DataDir) + `</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
	<key>ThrottleInterval</key><integer>60</integer>
	<key>ProcessType</key><string>Background</string>
	<key>StandardOutPath</key><string>` + escape(filepath.Join(s.DataDir, "service.log")) + `</string>
	<key>StandardErrorPath</key><string>` + escape(filepath.Join(s.DataDir, "service.log")) + `</string>
</dict>
</plist>
`
}

func (l launchAgent) Install(s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	return writeAtomic(l.path(), plist(s), 0o644)
}

func (l launchAgent) Uninstall() error {
	if l.Run != nil {
		// Stop a copy that is running now. It may not be loaded, so ignore the answer.
		_ = l.Run("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+Label)
	}
	if err := os.Remove(l.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Start loads the login item now, replacing a loaded copy, so launchd starts
// wasync from the file as it is. Unloading a running copy stops it first, and
// loading may fail for a moment until that copy is gone, so it is retried.
func (l launchAgent) Start(Spec) error {
	if l.Run == nil {
		return ErrUnsupported
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = l.Run("launchctl", "bootout", domain+"/"+Label) // it may not be loaded
	var err error
	for try := 0; try < 5; try++ {
		if err = l.Run("launchctl", "bootstrap", domain, l.path()); err == nil {
			return nil
		}
		if l.Wait != nil {
			l.Wait(time.Second)
		}
	}
	return err
}

func (l launchAgent) Installed() (bool, error) { return exists(l.path()) }

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(out))
	}
	return nil
}
