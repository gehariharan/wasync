package uninstall

import (
	"context"
	"errors"
	"os"
	"strings"
)

// The macOS package (packaging/macos/distribution.xml) installs wasync-axi as
// its own component, with this receipt, into /usr/local/bin, which only an
// administrator may change.
const (
	axiReceipt = "com.gehariharan.wasynctuicli.axi"
	axiCommand = "/usr/local/bin/wasync-axi"
	// axiAlias is the old command name; the package's postinstall makes it a
	// link to wasync-axi.
	axiAlias = "/usr/local/bin/whatsapp-axi"
)

// removeAxiScript removes the package's wasync-axi, its old-name link and its
// receipt. It checks again, as administrator, that wasync-axi is a plain file
// and not a link another installation method owns (Homebrew), and removes the
// old name only while it is the link the package made.
const removeAxiScript = `set -u
if [ -f ` + axiCommand + ` ] && [ ! -L ` + axiCommand + ` ]; then rm -f ` + axiCommand + `; fi
if [ -L ` + axiAlias + ` ] && [ "$(readlink ` + axiAlias + `)" = wasync-axi ]; then rm -f ` + axiAlias + `; fi
/usr/sbin/pkgutil --forget ` + axiReceipt + ` >/dev/null
`

// statAxi looks at the package's wasync-axi without following a link; tests
// replace it.
var statAxi = func() (os.FileInfo, error) { return os.Lstat(axiCommand) }

const removeAxiPrompt = "wasync wants to remove wasync-axi, the command for AI agents that its macOS package put in /usr/local/bin."

// removeMacApp moves the wasync app to the Bin, then removes the package's
// wasync-axi if that package installed it. Moving the app needs no prompt:
// the person may change the folder it is in (update.Detect checked that).
func (s System) removeMacApp(ctx context.Context, app string, log *helperLog) error {
	var trashErr error
	switch {
	case s.Trash == nil:
		trashErr = errors.New("this copy of wasync cannot use the Bin")
	default:
		trashErr = s.Trash(app)
	}
	if trashErr != nil {
		log.printf("Could not move %s to the Bin: %v. Drag it to the Bin yourself.", app, trashErr)
	} else {
		log.printf("Moved %s to the Bin.", app)
	}
	s.removePackageAxi(ctx, log)
	return trashErr
}

// removePackageAxi removes /usr/local/bin/wasync-axi when the macOS package
// installed it there and it is still that package's plain file. It asks for an
// administrator's password with the system's own dialog; cancelling it leaves
// the command where it is.
func (s System) removePackageAxi(ctx context.Context, log *helperLog) {
	if s.Run == nil {
		return
	}
	if _, _, err := s.Run(ctx, "/usr/sbin/pkgutil", []string{"--pkg-info", axiReceipt}, nil); err != nil {
		return // the package did not install it
	}
	info, err := statAxi()
	switch {
	case errors.Is(err, os.ErrNotExist):
		return // already gone; the receipt alone is not worth a password
	case err != nil:
		log.printf("Could not check %s: %v", axiCommand, err)
		return
	case !info.Mode().IsRegular():
		log.printf("Left %s alone: another installation method (such as Homebrew) owns it.", axiCommand)
		return
	}
	files, _, err := s.Run(ctx, "/usr/sbin/pkgutil", []string{"--files", axiReceipt}, nil)
	if err != nil || !listsFile(string(files), strings.TrimPrefix(axiCommand, "/")) {
		log.printf("Left %s alone: the package's receipt does not list it.", axiCommand)
		return
	}
	// The script and the prompt go to AppleScript as arguments, so nothing in
	// them is read as AppleScript.
	args := []string{
		"-e", "on run argv",
		"-e", "do shell script (item 1 of argv) with prompt (item 2 of argv) with administrator privileges",
		"-e", "end run",
		removeAxiScript, removeAxiPrompt,
	}
	if _, _, err := s.Run(ctx, "/usr/bin/osascript", args, nil); err != nil {
		log.printf("Did not remove %s (%v). Remove it in Terminal with: sudo rm %s && sudo pkgutil --forget %s", axiCommand, err, axiCommand, axiReceipt)
		return
	}
	log.printf("Removed %s, which the macOS package installed.", axiCommand)
}

func listsFile(list, name string) bool {
	for _, line := range strings.Split(list, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}
