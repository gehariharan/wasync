package uninstall

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gehariharan/wasync/internal/buildinfo"
	"github.com/gehariharan/wasync/internal/update"
)

// uninstallerName is the uninstaller Setup leaves beside the program it
// installed (packaging/windows/wasync.iss). Settings > Apps runs the same one.
const uninstallerName = "unins000.exe"

// DetectHandoff works out how this copy's program files are removed, from
// where it runs. exe is os.Executable() of the running wasync. It tells
// installs apart the way updates do (update.Detect).
func DetectHandoff(exe string) Handoff {
	return handoffFor(update.Detect(update.ThisHost(exe, buildinfo.Version, buildinfo.Distribution)), exe, runtime.GOOS)
}

// handoffFor turns how this copy was installed into how it is removed.
func handoffFor(ch update.Channel, exe, goos string) Handoff {
	switch ch.Kind {
	case update.KindWindowsSetup:
		// Detect found the uninstaller beside the running wasync.exe.
		return Handoff{Kind: KindWindowsSetup, Target: filepath.Join(filepath.Dir(ch.Target), uninstallerName)}
	case update.KindMacApp:
		if ch.Target != "" {
			return Handoff{Kind: KindMacApp, Target: ch.Target}
		}
		if strings.Contains(filepath.ToSlash(exe), "/AppTranslocation/") {
			return manual("macOS ran wasync from a temporary copy, so wasync cannot find where you keep it. Drag wasync from that folder (usually Applications) to the Bin.")
		}
		// Detect gives no target when this user may not change the folder the
		// app is in; Finder asks for an administrator's password there.
		return manual("wasync cannot move itself out of " + filepath.Dir(appBundle(exe)) + ". Drag wasync from there to the Bin; macOS asks for an administrator's password.")
	case update.KindHomebrew:
		if goos == "darwin" {
			return manual("wasync was installed with Homebrew. Remove the program in Terminal with: brew uninstall --cask gehariharan/tap/wasync (and brew uninstall gehariharan/tap/wasync-axi if you added it).")
		}
		return manual("wasync was installed with Homebrew. Remove the program in Terminal with: brew uninstall gehariharan/tap/wasync-axi gehariharan/tap/wasync")
	case update.KindLinux:
		return manual("Delete wasync and wasync-axi from the folder install.sh put them in (~/.local/bin unless you chose another).")
	case update.KindStore:
		return manual("Uninstall wasync from the Start menu or Settings > Apps, like any app from the Microsoft Store.")
	case update.KindWindowsZIP:
		return manual("This copy was unpacked from a ZIP file. Delete the folder you unpacked it into (" + filepath.Dir(exe) + ").")
	case update.KindMacCommand:
		return manual("wasync was installed with the macOS package. Remove the program in Terminal with: sudo rm -f /usr/local/bin/wasync /usr/local/bin/wasync-axi /usr/local/bin/whatsapp-axi && sudo rm -rf /usr/local/share/wasync")
	case update.KindDevelopment:
		return manual("This is a development build of wasync. Delete the program files you built or copied (" + exe + ").")
	}
	return manual("Delete the wasync program files (" + exe + ").")
}

func manual(note string) Handoff { return Handoff{Kind: KindManual, Note: note} }

// appBundle is the .app bundle that holds exe, or exe itself when none does.
func appBundle(exe string) string {
	p := filepath.ToSlash(exe)
	if i := strings.Index(p, ".app/"); i >= 0 {
		return filepath.FromSlash(p[:i+len(".app")])
	}
	return exe
}

// equalFold compares file names the way Windows does, ignoring case.
func equalFold(a, b string) bool { return strings.EqualFold(a, b) }
