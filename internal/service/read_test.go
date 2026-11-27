package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLaunchAgentsAreReadBackAsTheProgramAndDataDirectoryTheyStart(t *testing.T) {
	home := t.TempDir()
	s := spec(t)
	for name, m := range map[string]interface {
		Manager
		Reader
	}{
		"serve": launchAgent{Home: home},
		"app":   appAgent{Home: home},
	} {
		if _, err := m.Read(); !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("%s: reading a missing item gave %v, want ErrNotInstalled", name, err)
		}
		if err := m.Install(s); err != nil {
			t.Fatal(err)
		}
		got, err := Read(m)
		if err != nil || got != s {
			t.Fatalf("%s: read %+v %v, want %+v", name, got, err, s)
		}
	}
}

func TestALaunchAgentWithoutADataDirectoryIsNotReadAsOne(t *testing.T) {
	home := t.TempDir()
	l := launchAgent{Home: home}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
	<key>Label</key><string>com.wasync.serve</string>
	<key>EnvironmentVariables</key><dict><key>ProgramArguments</key><string>decoy</string></dict>
	<key>ProgramArguments</key><array><string>/usr/local/bin/wasync</string><string>serve</string></array>
</dict></plist>`
	if err := os.MkdirAll(filepath.Dir(l.path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.path(), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := l.Read(); err == nil {
		t.Fatalf("an item with no --data-dir must not be read as one: %+v", got)
	}
	args, err := programArguments([]byte(plist))
	if err != nil || len(args) != 2 || args[0] != "/usr/local/bin/wasync" {
		t.Fatalf("only the top-level ProgramArguments counts: %q %v", args, err)
	}
}

func TestAManagerThatCannotReadItsItemSaysSo(t *testing.T) {
	if _, err := Read(unsupported{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}

func TestWindowsStartupLinesAreReadBack(t *testing.T) {
	for _, s := range []Spec{
		{Exe: `C:\Users\Test User\AppData\Local\Programs\wasync\wasync.exe`, DataDir: `C:\Users\Test User\AppData\Local\wasync`},
		{Exe: `C:\Users\O'Brien\wasync\wasync.exe`, DataDir: `D:\data\O'Brien's wasync`},
		{Exe: `C:\w\wasync.exe`, DataDir: `C:\w\data\`},
	} {
		got, err := ParseRunCommand(RunCommand(s))
		if err != nil || got != s {
			t.Errorf("serve line %q read as %+v %v, want %+v", RunCommand(s), got, err, s)
		}
		got, err = ParseAppRunCommand(AppRunCommand(s))
		if err != nil || got != s {
			t.Errorf("app line %q read as %+v %v, want %+v", AppRunCommand(s), got, err, s)
		}
	}
	for _, bad := range []string{"", "wasync.exe serve", `powershell.exe -Command "Start-Process -FilePath 'C:\w.exe' -ArgumentList 'serve'"`} {
		if got, err := ParseRunCommand(bad); err == nil {
			t.Errorf("%q read as %+v", bad, got)
		}
	}
}
