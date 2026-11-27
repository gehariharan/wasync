package service

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAppLoginItemOpensTheAppWithNoWindowAndIsNotKeptAlive(t *testing.T) {
	home := t.TempDir()
	a := appAgent{Home: home}
	// An absolute path on every OS, written with the forward slashes macOS uses.
	exe := filepath.ToSlash(filepath.Join(t.TempDir(), "Applications", "wasync.app", "Contents", "MacOS", "wasync-desktop"))
	s := Spec{Exe: exe, DataDir: filepath.Join(t.TempDir(), "data & more")}
	if on, err := a.Installed(); err != nil || on {
		t.Fatalf("before install: %v %v", on, err)
	}
	if err := a.Install(s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(a.path())
	if err != nil {
		t.Fatal(err)
	}
	got, err := parsePlist(b)
	if err != nil {
		t.Fatalf("the plist is not well-formed: %v\n%s", err, b)
	}
	if got["Label"] != AppLabel {
		t.Errorf("Label = %v", got["Label"])
	}
	if args, want := got["ProgramArguments"], []any{s.Exe, "--background", "--data-dir", s.DataDir}; !reflect.DeepEqual(args, want) {
		t.Errorf("ProgramArguments = %#v, want %#v", args, want)
	}
	if got["RunAtLoad"] != true {
		t.Errorf("RunAtLoad = %v", got["RunAtLoad"])
	}
	if _, kept := got["KeepAlive"]; kept {
		t.Error("the app must stay quit when the person quits it")
	}
	if ids, want := got["AssociatedBundleIdentifiers"], []any{AppBundleID}; !reflect.DeepEqual(ids, want) {
		t.Errorf("AssociatedBundleIdentifiers = %#v, want %#v", ids, want)
	}
	if on, _ := a.Installed(); !on {
		t.Fatal("not reported installed")
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", Label+".plist")); !os.IsNotExist(err) {
		t.Fatal("the app's item must not touch the wasync serve item")
	}
	if err := a.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if on, _ := a.Installed(); on {
		t.Fatal("still installed after uninstall")
	}
	if err := a.Uninstall(); err != nil {
		t.Fatalf("uninstall when nothing is installed: %v", err)
	}
}

func TestAppLoginItemOutsideABundleNamesNoBundle(t *testing.T) {
	a := appAgent{Home: t.TempDir()}
	data := filepath.Join(t.TempDir(), "data")
	if err := a.Install(Spec{Exe: filepath.Join(t.TempDir(), "build", "wasync-desktop"), DataDir: data}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.path())
	got, err := parsePlist(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["AssociatedBundleIdentifiers"]; ok {
		t.Fatal("a program outside an app bundle cannot name one")
	}
	if err := a.Install(Spec{Exe: "wasync-desktop", DataDir: data}); err == nil {
		t.Fatal("a relative program path was accepted")
	}
}

func TestAppRunCommandQuotesLikeWindows(t *testing.T) {
	s := Spec{Exe: `C:\Users\Test User\AppData\Local\Programs\wasync\wasync-desktop.exe`, DataDir: `C:\Users\Test User\AppData\Roaming\wasync\`}
	want := `"C:\Users\Test User\AppData\Local\Programs\wasync\wasync-desktop.exe" --background --data-dir "C:\Users\Test User\AppData\Roaming\wasync\\"`
	if got := AppRunCommand(s); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	for in, want := range map[string]string{
		"":          `""`,
		`plain`:     `plain`,
		`a b`:       `"a b"`,
		`say "hi"`:  `"say \"hi\""`,
		`a\"b c`:    `"a\\\"b c"`,
		`tab	here`:  "\"tab\there\"",
		`ends in\ `: `"ends in\ "`,
	} {
		if got := windowsArg(in); got != want {
			t.Errorf("windowsArg(%q) = %s, want %s", in, got, want)
		}
	}
}

// The app's bundle identifier is written into its Info.plist when it is
// packaged; the login item names the same one.
func TestAppBundleIDMatchesThePackagedInfoPlist(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "packaging", "macos", "app", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	plist, err := parsePlist(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := plist["CFBundleIdentifier"]; got != AppBundleID {
		t.Fatalf("Info.plist CFBundleIdentifier = %v, want %q", got, AppBundleID)
	}
}
