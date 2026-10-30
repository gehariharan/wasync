//go:build windows

package service

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// This writes a real value under the current user's Run key, with a name no
// real install uses, and removes it again. It needs no administrator rights.
func TestWindowsStartupItemIsWrittenReadAndRemoved(t *testing.T) {
	valueName = "wasync-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	t.Cleanup(func() { valueName = RunName; _ = (runValue{}).Uninstall() })
	m := System()
	if on, err := m.Installed(); err != nil || on {
		t.Fatalf("before: %v %v", on, err)
	}
	spec := Spec{Exe: `C:\Users\Test User\AppData\Local\Programs\wasync\wasync.exe`, DataDir: `C:\Users\Test User\AppData\Roaming\wasync`}
	if err := m.Install(spec); err != nil {
		t.Fatal(err)
	}
	if on, err := m.Installed(); err != nil || !on {
		t.Fatalf("after install: %v %v", on, err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := k.GetStringValue(valueName)
	k.Close()
	if err != nil || got != RunCommand(spec) {
		t.Fatalf("stored command %q, want %q (%v)", got, RunCommand(spec), err)
	}
	if err = m.Install(spec); err != nil {
		t.Fatalf("a second install should replace the first: %v", err)
	}
	if err = m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if on, _ := m.Installed(); on {
		t.Fatal("still installed after uninstall")
	}
	if err = m.Uninstall(); err != nil {
		t.Fatalf("uninstall when nothing is installed: %v", err)
	}
	if err = m.Install(Spec{Exe: "wasync.exe", DataDir: spec.DataDir}); err == nil {
		t.Fatal("a relative program path was accepted")
	}
}
