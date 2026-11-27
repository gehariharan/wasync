package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ThisHost describes the running program for Detect.
func ThisHost(exe, version, distribution string) Host {
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = ""
	}
	return Host{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Exe: exe, Resolved: resolved,
		Distribution: distribution, Version: version,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		Writable: writable,
	}
}

// DefaultPlatform is the Platform for this operating system.
func DefaultPlatform() Platform {
	switch runtime.GOOS {
	case "darwin":
		return Mac{Run: ExecRunner}
	case "windows":
		return Windows{Run: ExecRunner}
	}
	return unsupportedPlatform{}
}

// LaunchHelper starts `wasync --data-dir DIR update-apply` detached from this
// process, so it outlives it. exe is the running wasync.
func LaunchHelper(exe, dataDir string) error {
	helper, err := helperExe(exe, dataDir)
	if err != nil {
		return err
	}
	return StartDetached(helper, []string{"--data-dir", dataDir, "update-apply"}, filepath.Join(Folder(dataDir), "helper.log"))
}

// StartAgain starts wasync again as r says. loginItem starts the login item.
func StartAgain(r Relaunch, dataDir string, loginItem func() error) error {
	switch r.How {
	case RelaunchLoginItem:
		return loginItem()
	case RelaunchCommand:
		return StartDetached(r.Exe, r.Args, filepath.Join(Folder(dataDir), "restart.log"))
	case RelaunchApp:
		return exec.Command("/usr/bin/open", r.App).Run()
	}
	return nil
}
