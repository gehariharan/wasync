package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// Label is the macOS login item's name.
const Label = "com.wasync.serve"

// launchAgent is a macOS per-user login item: a property list in
// ~/Library/LaunchAgents that starts `wasync serve` at login. Installing only
// writes the file; it starts at the next login, so it never collides with a
// wasync the person is already running.
type launchAgent struct {
	Home string
	// Run runs a command; tests replace it.
	Run func(name string, args ...string) error
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
	if err := os.MkdirAll(filepath.Dir(l.path()), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path()), ".wasync-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.WriteString(plist(s)); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.path())
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

func (l launchAgent) Installed() (bool, error) {
	_, err := os.Stat(l.path())
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	}
	return false, err
}

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(out))
	}
	return nil
}
