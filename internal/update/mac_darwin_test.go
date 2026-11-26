//go:build darwin

package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAppImage builds a disk image holding an ad hoc signed app, with the
// real hdiutil and codesign, as the release's disk image would hold wasync.
func fakeAppImage(t *testing.T) (dmg, app string) {
	t.Helper()
	dir := t.TempDir()
	app = filepath.Join(dir, "src", "wasync.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.ReadFile("/bin/echo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "wasync"), exe, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.example.wasync-update-test</string><key>CFBundleExecutable</key><string>wasync</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("codesign", "-s", "-", "-f", app).CombinedOutput(); err != nil {
		t.Skipf("codesign is not usable here: %v %s", err, out)
	}
	dmg = filepath.Join(dir, "wasync.dmg")
	if out, err := exec.Command("hdiutil", "create", "-quiet", "-srcfolder", filepath.Dir(app), "-volname", "wasync", "-format", "UDZO", dmg).CombinedOutput(); err != nil {
		t.Skipf("hdiutil cannot make disk images here: %v %s", err, out)
	}
	return dmg, app
}

func mounted(path string) bool {
	// mount lists real paths, such as /private/tmp for /tmp.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	out, _ := exec.Command("mount").Output()
	return strings.Contains(string(out), " on "+path+" ")
}

// TestRealMacTools runs the disk image steps with the Mac's own tools. One
// image serves both checks, since making one takes seconds.
func TestRealMacTools(t *testing.T) {
	dmg, app := fakeAppImage(t)
	t.Run("an ad hoc signed app is refused", func(t *testing.T) { refuseAdHoc(t, dmg, app) })
	t.Run("the app is copied out with its signature", func(t *testing.T) { copyOut(t, dmg) })
}

func refuseAdHoc(t *testing.T, dmg, app string) {
	m := Mac{Run: ExecRunner}
	ch := Channel{Kind: KindMacApp, OneClick: true, Target: app}
	m.Probe(context.Background(), &ch)
	if ch.OneClick || !strings.Contains(ch.Note, "not signed") {
		t.Fatalf("an ad hoc signed running app must not update itself: %+v", ch)
	}
	staging := t.TempDir()
	ch = Channel{Kind: KindMacApp, OneClick: true, Target: "/Applications/wasync.app", Team: "ABCDE12345", BundleID: "com.example.wasync-update-test"}
	_, err := m.Prepare(context.Background(), ch, dmg, staging)
	if err == nil || !strings.Contains(err.Error(), "not notarized and signed by wasync's developer") {
		t.Fatalf("an ad hoc signed update must be refused: %v", err)
	}
	if mounted(filepath.Join(staging, "mount")) {
		t.Fatal("the disk image was left attached")
	}
}

func copyOut(t *testing.T, dmg string) {
	m := Mac{Run: ExecRunner}
	staging := t.TempDir()
	mount := filepath.Join(staging, "mount")
	if err := os.Mkdir(mount, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Run(context.Background(), "hdiutil", []string{"attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mount, dmg}, nil); err != nil {
		t.Skipf("hdiutil cannot attach here: %v", err)
	}
	if !mounted(mount) {
		t.Fatal("the attached image is not listed as mounted, so the detach checks prove nothing")
	}
	app, err := m.copyApp(context.Background(), mount, filepath.Join(staging, "app"), "wasync.app")
	if detachErr := m.detach(mount); detachErr != nil {
		t.Fatal(detachErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		t.Fatalf("the copied app lost its signature: %v %s", err, out)
	}
	if id, err := m.bundleID(context.Background(), app); err != nil || id != "com.example.wasync-update-test" {
		t.Fatalf("bundle id %q %v", id, err)
	}
	if mounted(mount) {
		t.Fatal("the disk image was left attached")
	}
}
