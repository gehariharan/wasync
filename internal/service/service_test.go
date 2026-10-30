package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func spec(t *testing.T) Spec {
	t.Helper()
	return Spec{Exe: filepath.Join(t.TempDir(), "bin", "wasync"), DataDir: filepath.Join(t.TempDir(), "data & more")}
}

func TestLaunchAgentInstallWritesAFileAndNothingElse(t *testing.T) {
	home := t.TempDir()
	var ran []string
	l := launchAgent{Home: home, Run: func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}}
	s := spec(t)
	if on, err := l.Installed(); err != nil || on {
		t.Fatalf("before install: %v %v", on, err)
	}
	if err := l.Install(s); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("installing must not start anything now: %v", ran)
	}
	b, err := os.ReadFile(l.path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"<string>" + Label + "</string>", "<string>serve</string>", "<string>--data-dir</string>", "data &amp; more", "<key>RunAtLoad</key><true/>"} {
		if !strings.Contains(text, want) {
			t.Errorf("plist lacks %q:\n%s", want, text)
		}
	}
	if info, _ := os.Stat(l.path()); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", info.Mode())
	}
	if on, _ := l.Installed(); !on {
		t.Fatal("not reported installed")
	}
	if err = l.Install(s); err != nil {
		t.Fatalf("a second install should replace the first: %v", err)
	}
}

func TestLaunchAgentUninstallStopsAndRemoves(t *testing.T) {
	home := t.TempDir()
	var ran []string
	l := launchAgent{Home: home, Run: func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}}
	if err := l.Uninstall(); err != nil {
		t.Fatalf("uninstall when nothing is installed: %v", err)
	}
	if err := l.Install(spec(t)); err != nil {
		t.Fatal(err)
	}
	ran = nil
	if err := l.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || !strings.Contains(ran[0], "launchctl bootout gui/") || !strings.HasSuffix(ran[0], "/"+Label) {
		t.Fatalf("ran %v", ran)
	}
	if on, _ := l.Installed(); on {
		t.Fatal("still installed")
	}
}

func TestInstallRefusesRelativePaths(t *testing.T) {
	l := launchAgent{Home: t.TempDir()}
	for _, s := range []Spec{{Exe: "wasync", DataDir: "/data"}, {Exe: "/bin/wasync", DataDir: "data"}} {
		if err := l.Install(s); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

func TestRunCommandKeepsOddPathsInsideTheirQuotes(t *testing.T) {
	got := RunCommand(Spec{Exe: `C:\Users\O'Brien\wasync.exe`, DataDir: `C:\Users\O'Brien\AppData\Roaming\wasync`})
	for _, want := range []string{"-WindowStyle Hidden", `O''Brien\wasync.exe`, "serve"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if strings.Count(got, `"`) != 2 && !strings.Contains(got, `\"`) {
		t.Fatalf("data folder not quoted: %s", got)
	}
}
