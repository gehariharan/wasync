package service

import (
	"os"
	"path/filepath"
	"strings"
)

// The wasync app's own login item. It opens the desktop app at login with no
// window, in the menu bar (macOS) or the notification area (Windows); the app
// then starts wasync itself. It is separate from the `wasync serve` item above,
// so a person who set that up from a terminal keeps it.

// AppLabel is the macOS login item that opens the wasync app.
const AppLabel = "com.wasync.app"

// AppRunName is the value under the current user's Run key that opens the wasync app.
const AppRunName = "wasync-app"

// AppBundleID is the wasync app's bundle identifier. It is the same identifier
// the wasync command is signed with, so the Keychain trusts the app and the
// command for the same approval credential. packaging/macos/Info.plist must match.
const AppBundleID = "com.gehariharan.wasynctuicli"

// BackgroundFlag starts the app in the menu bar or notification area only, as at login.
const BackgroundFlag = "--background"

// AppArgs are the arguments the login item passes to the app: no window, and
// the data directory the person set up.
func AppArgs(dataDir string) []string { return []string{BackgroundFlag, "--data-dir", dataDir} }

// appAgent is the macOS LaunchAgent that opens the app at login. Unlike the
// serve item it is not kept alive: quitting the app from its menu keeps it quit.
type appAgent struct{ Home string }

func (appAgent) Where() string { return "a macOS login item" }

func (a appAgent) path() string {
	return filepath.Join(a.Home, "Library", "LaunchAgents", AppLabel+".plist")
}

func appPlist(s Spec) string {
	var args strings.Builder
	for _, arg := range append([]string{s.Exe}, AppArgs(s.DataDir)...) {
		args.WriteString("\t\t<string>" + escape(arg) + "</string>\n")
	}
	// System Settings, Login Items, shows the item under the app's name and icon
	// when it names the app's bundle.
	associated := ""
	if strings.Contains(s.Exe, ".app/Contents/MacOS/") {
		associated = "\t<key>AssociatedBundleIdentifiers</key><array><string>" + AppBundleID + "</string></array>\n"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + AppLabel + `</string>
	<key>ProgramArguments</key>
	<array>
` + args.String() + `	</array>
	<key>RunAtLoad</key><true/>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
	<key>ProcessType</key><string>Interactive</string>
` + associated + `</dict>
</plist>
`
}

// Install writes the item. launchd reads it at the next login; the app is
// already running, so nothing is started now.
func (a appAgent) Install(s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	return writeAtomic(a.path(), appPlist(s), 0o644)
}

// Uninstall removes the item. It does not unload it: when the app was opened
// by this item, unloading it would quit the app the person is using.
func (a appAgent) Uninstall() error {
	if err := os.Remove(a.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (a appAgent) Installed() (bool, error) { return exists(a.path()) }

// AppRunCommand is the line Windows runs at login to open the app. The app is
// a windowed program, so it needs no hidden console around it.
func AppRunCommand(s Spec) string {
	parts := []string{windowsArg(s.Exe)}
	for _, arg := range AppArgs(s.DataDir) {
		parts = append(parts, windowsArg(arg))
	}
	return strings.Join(parts, " ")
}

// windowsArg quotes one argument the way Windows programs split a command line
// (the rules of syscall.EscapeArg, which exists only on Windows).
func windowsArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			slashes++
		case '"':
			// Backslashes before a quote are doubled, and the quote escaped.
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(s[i])
	}
	// Backslashes before the closing quote are doubled too.
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	}
	return false, err
}

// writeAtomic replaces path with content in one step, so a login item is never
// left half written.
func writeAtomic(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wasync-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
