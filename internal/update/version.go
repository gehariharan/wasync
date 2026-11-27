// Package update finds a newer published wasync release, downloads the
// installer for this computer, checks it, and hands it to a short-lived helper
// that installs it once the running wasync has stopped, then starts wasync
// again. Only the owner can start it, from the web app, after the operating
// system's own confirmation; see internal/api/update.go.
//
// What one click does on each kind of install:
//
//   - Windows Setup installs: download wasync_VERSION_windows_amd64-setup.exe,
//     check its SHA-256 against the release's checksums.txt (and GitHub's own
//     digest), check its Authenticode signer matches the running copy's when
//     the running copy is signed, then run it silently.
//   - The macOS app in /Applications: download wasync_VERSION_darwin_ARCH.dmg,
//     check its SHA-256 the same way, copy the app out of the disk image and
//     require that it is notarized and signed by the same Apple team as the
//     running app, then put it in place of the running app and open it again.
//   - Homebrew, the Linux archive, the Microsoft Store, a Windows ZIP and the
//     macOS command-line package update the way they were installed: wasync
//     only says how.
package update

import (
	"strconv"
	"strings"
)

// version is a parsed semantic version such as 0.1.0-preview.20261126105548.
type version struct {
	core [3]uint64
	pre  []string
}

// parseVersion reads MAJOR.MINOR.PATCH with an optional -PRERELEASE and an
// optional leading "v". Build metadata (+...) is ignored, as semver says.
func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v version
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil || p == "" || (len(p) > 1 && p[0] == '0') {
			return version{}, false
		}
		v.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return version{}, false
		}
		v.pre = strings.Split(pre, ".")
		for _, id := range v.pre {
			if id == "" {
				return version{}, false
			}
		}
	}
	return v, true
}

// ValidVersion reports whether s is a version wasync can compare.
func ValidVersion(s string) bool {
	_, ok := parseVersion(s)
	return ok
}

// Newer reports whether candidate is a later version than current. A version
// that cannot be read is never newer, and nothing is newer than one that
// cannot be read (such as a development build's "dev").
func Newer(candidate, current string) bool {
	a, okA := parseVersion(candidate)
	b, okB := parseVersion(current)
	return okA && okB && compare(a, b) > 0
}

func compare(a, b version) int {
	for i := range a.core {
		switch {
		case a.core[i] > b.core[i]:
			return 1
		case a.core[i] < b.core[i]:
			return -1
		}
	}
	// A release is newer than any pre-release of the same version.
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := compareIdent(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) > len(b.pre):
		return 1
	case len(a.pre) < len(b.pre):
		return -1
	}
	return 0
}

func compareIdent(a, b string) int {
	na, errA := strconv.ParseUint(a, 10, 64)
	nb, errB := strconv.ParseUint(b, 10, 64)
	switch {
	case errA == nil && errB == nil:
		switch {
		case na > nb:
			return 1
		case na < nb:
			return -1
		}
		return 0
	case errA == nil: // numeric identifiers sort before alphanumeric ones
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}
