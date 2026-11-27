package datadir

import (
	"path/filepath"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestStandardPerOperatingSystem(t *testing.T) {
	// Absolute on whichever system runs the test, as the XDG rules require.
	root := t.TempDir()
	home := filepath.Join(root, "home", "ana")
	local := filepath.Join("C:", "Users", "ana", "AppData", "Local")
	roaming := filepath.Join("C:", "Users", "ana", "AppData", "Roaming")
	profile := filepath.Join("C:", "Users", "ana")
	xdgData := filepath.Join(root, "data")
	xdgConfig := filepath.Join(root, "config")
	for _, tc := range []struct {
		name, goos, distribution string
		vars                     map[string]string
		want                     Location
	}{
		{"macOS keeps Application Support", "darwin", "standalone", map[string]string{"HOME": home},
			Location{Dir: filepath.Join(home, "Library", "Application Support", "wasync")}},
		{"Windows uses local app data and moves from roaming", "windows", "standalone", map[string]string{"LOCALAPPDATA": local, "APPDATA": roaming},
			Location{Dir: filepath.Join(local, "wasync"), Legacy: filepath.Join(roaming, "wasync")}},
		{"Windows Store keeps its profile folder", "windows", "msix", map[string]string{"USERPROFILE": profile, "LOCALAPPDATA": local, "APPDATA": roaming},
			Location{Dir: filepath.Join(profile, ".wasync")}},
		{"Linux uses the XDG data folder and moves from the config folder", "linux", "standalone", map[string]string{"HOME": home},
			Location{Dir: filepath.Join(home, ".local", "share", "wasync"), Legacy: filepath.Join(home, ".config", "wasync")}},
		{"Linux follows XDG variables", "linux", "standalone", map[string]string{"HOME": home, "XDG_DATA_HOME": xdgData, "XDG_CONFIG_HOME": xdgConfig},
			Location{Dir: filepath.Join(xdgData, "wasync"), Legacy: filepath.Join(xdgConfig, "wasync")}},
		{"Linux ignores relative XDG variables", "linux", "standalone", map[string]string{"HOME": home, "XDG_DATA_HOME": "data", "XDG_CONFIG_HOME": "config"},
			Location{Dir: filepath.Join(home, ".local", "share", "wasync")}},
		{"one XDG folder for both moves nothing", "linux", "standalone", map[string]string{"HOME": home, "XDG_DATA_HOME": xdgData, "XDG_CONFIG_HOME": xdgData},
			Location{Dir: filepath.Join(xdgData, "wasync")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Standard(tc.goos, tc.distribution, env(tc.vars))
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
	for goos, vars := range map[string]map[string]string{"darwin": {}, "windows": {"APPDATA": roaming}, "linux": {}} {
		if _, err := Standard(goos, "standalone", env(vars)); err == nil {
			t.Errorf("%s without its base folder: no error", goos)
		}
	}
	if _, err := Standard("windows", "msix", env(map[string]string{"LOCALAPPDATA": local})); err == nil {
		t.Error("Store build without a profile folder: no error")
	}
}

func TestInUseFollowsTheData(t *testing.T) {
	base := t.TempDir()
	l := Location{Dir: filepath.Join(base, "new", "wasync"), Legacy: filepath.Join(base, "old", "wasync")}
	if l.InUse() != l.Dir {
		t.Fatal("a fresh computer must use the new folder")
	}
	write(t, filepath.Join(l.Dir, "device.lock"), "")
	write(t, filepath.Join(l.Legacy, "archive.db"), "x")
	if l.InUse() != l.Legacy {
		t.Fatal("data only in the old folder must keep it in use until it is moved")
	}
	write(t, filepath.Join(l.Dir, "archive.db"), "x")
	if l.InUse() != l.Dir {
		t.Fatal("once the new folder holds data it is the one in use")
	}
	if !l.Names(l.Legacy) || !l.Names(l.Dir+string(filepath.Separator)) || l.Names(filepath.Join(base, "other")) {
		t.Fatal("Names must match exactly the two default folders")
	}
	if (Location{Dir: l.Dir}).InUse() != l.Dir {
		t.Fatal("without an old folder the standard one is used")
	}
}
