package update

import (
	"path/filepath"
	"strings"
)

// The kinds of install wasync can tell apart. Only KindWindowsSetup and
// KindMacApp update in one click; the others update the way they were installed.
const (
	KindWindowsSetup = "windows-setup"
	KindMacApp       = "mac-app"
	KindHomebrew     = "homebrew"
	KindLinux        = "linux"
	KindStore        = "microsoft-store"
	KindWindowsZIP   = "windows-zip"
	KindMacCommand   = "mac-command"
	KindDevelopment  = "development"
	KindUnsupported  = "unsupported"
)

// Channel is how this copy of wasync was installed, and so how it updates.
type Channel struct {
	Kind string `json:"kind"`
	// OneClick is set when wasync can install an update by itself.
	OneClick bool `json:"one_click"`
	// Note says, in plain words, how to update when OneClick is not set.
	Note string `json:"note,omitempty"`
	// Target is what one click replaces: the .app bundle on macOS, the
	// running wasync.exe on Windows.
	Target string `json:"-"`
	// Arch is the CPU the downloads are for: amd64 or arm64.
	Arch string `json:"-"`
	// Team is the Apple team that signed the running app; an update must be
	// signed by the same team.
	Team string `json:"-"`
	// BundleID is the running app's identifier; an update must keep it.
	BundleID string `json:"-"`
	// Signer is the Authenticode subject of the running wasync.exe, or ""
	// when it is unsigned (Windows previews are).
	Signer string `json:"-"`
}

// AssetNames are the release files that update this channel, best first. The
// Windows name is the Setup program scripts/package-windows.ps1 builds
// ("wasync_VERSION_windows_amd64-setup.exe"); the macOS name follows the same
// "wasync_VERSION_OS_ARCH" pattern for the drag-to-Applications disk image.
func (c Channel) AssetNames(version string) []string {
	switch c.Kind {
	case KindWindowsSetup:
		return []string{"wasync_" + version + "_windows_" + c.Arch + "-setup.exe"}
	case KindMacApp:
		return []string{"wasync_" + version + "_darwin_" + c.Arch + ".dmg", "wasync_" + version + "_darwin_universal.dmg"}
	}
	return nil
}

// Host is what Detect looks at.
type Host struct {
	GOOS, GOARCH string
	// Exe is the running program, as os.Executable reports it.
	Exe string
	// Resolved is Exe with symbolic links followed ("" when that failed).
	Resolved string
	// Distribution and Version come from internal/buildinfo.
	Distribution, Version string
	// Exists reports whether a file exists.
	Exists func(string) bool
	// Writable reports whether this user may create and rename entries in a folder.
	Writable func(string) bool
}

const releasePage = "https://github.com/gehariharan/wasync/releases"

// Detect works out how this copy was installed from where it runs. It does
// not run anything; Platform.Probe adds what needs a command (such as the
// signing team) for a one-click channel.
func Detect(h Host) Channel {
	if !ValidVersion(h.Version) {
		return Channel{Kind: KindDevelopment, Note: "This is a development build (version " + h.Version + "), so it does not update itself."}
	}
	resolved := h.Resolved
	if resolved == "" {
		resolved = h.Exe
	}
	if homebrew(filepath.ToSlash(resolved)) || homebrew(filepath.ToSlash(h.Exe)) {
		if h.GOOS == "darwin" {
			return Channel{Kind: KindHomebrew, Note: "wasync was installed with Homebrew. Update it with: brew upgrade --cask gehariharan/tap/wasync"}
		}
		return Channel{Kind: KindHomebrew, Note: "wasync was installed with Homebrew. Update it with: brew upgrade gehariharan/tap/wasync"}
	}
	switch h.GOOS {
	case "windows":
		if h.Distribution == "msix" {
			return Channel{Kind: KindStore, Note: "Update wasync from the Microsoft Store."}
		}
		dir := filepath.Dir(h.Exe)
		// Setup always leaves its uninstaller beside the program it installed.
		if h.GOARCH == "amd64" && h.Exists != nil && h.Exists(filepath.Join(dir, "unins000.exe")) {
			return Channel{Kind: KindWindowsSetup, OneClick: true, Target: h.Exe, Arch: h.GOARCH}
		}
		return Channel{Kind: KindWindowsZIP, Note: "This copy was unpacked from a ZIP file, so it cannot replace itself. Download the new Setup or ZIP from " + releasePage + "."}
	case "darwin":
		exe := filepath.ToSlash(h.Exe)
		i := strings.Index(exe, ".app/")
		if i < 0 {
			return Channel{Kind: KindMacCommand, Note: "This copy was installed as a command-line program. Download the new wasync from " + releasePage + "."}
		}
		bundle := filepath.FromSlash(exe[:i+len(".app")])
		if strings.Contains(exe, "/AppTranslocation/") {
			return Channel{Kind: KindMacApp, Note: "macOS is running wasync from a temporary copy. Move wasync to your Applications folder, open it from there, then update."}
		}
		if h.Writable == nil || !h.Writable(filepath.Dir(bundle)) {
			return Channel{Kind: KindMacApp, Note: "wasync cannot replace itself in " + filepath.Dir(bundle) + ". Download the new disk image from " + releasePage + " and drag wasync to Applications."}
		}
		return Channel{Kind: KindMacApp, OneClick: true, Target: bundle, Arch: h.GOARCH}
	case "linux":
		return Channel{Kind: KindLinux, Note: "Download the new Linux archive from " + releasePage + " and run its install.sh."}
	}
	return Channel{Kind: KindUnsupported, Note: "Download the new wasync from " + releasePage + "."}
}

// homebrew reports whether a path is inside a Homebrew installation.
func homebrew(p string) bool {
	for _, part := range []string{"/Cellar/", "/Caskroom/", "/opt/homebrew/", "/home/linuxbrew/.linuxbrew/", "/usr/local/Homebrew/"} {
		if strings.Contains(p, part) {
			return true
		}
	}
	return false
}
