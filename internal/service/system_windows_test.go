//go:build windows

package service

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// This writes a real value under the current user's Run key, with a name no
// real install uses, and removes it again. It needs no administrator rights.
// The value under wasync's real name, if any, must be left exactly as it was.
func TestWindowsStartupItemIsWrittenReadAndRemoved(t *testing.T) {
	want, err := readRunValue(RunName)
	if errors.Is(err, registry.ErrNotExist) {
		want = "wasync-test-stand-in"
		if err = writeRunValue(RunName, want); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if got, _ := readRunValue(RunName); got == want {
				_ = deleteRunValue(RunName)
			}
		})
	} else if err != nil {
		t.Fatal(err)
	}
	t.Run("test-only name", func(t *testing.T) {
		name := "wasync-test-" + strings.ReplaceAll(t.Name(), "/", "-")
		valueName = name
		t.Cleanup(func() { _ = deleteRunValue(name); valueName = RunName })
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
		if got, err := readRunValue(name); err != nil || got != RunCommand(spec) {
			t.Fatalf("stored command %q, want %q (%v)", got, RunCommand(spec), err)
		}
		if err := m.Install(spec); err != nil {
			t.Fatalf("a second install should replace the first: %v", err)
		}
		if err := m.Uninstall(); err != nil {
			t.Fatal(err)
		}
		if on, _ := m.Installed(); on {
			t.Fatal("still installed after uninstall")
		}
		if err := m.Uninstall(); err != nil {
			t.Fatalf("uninstall when nothing is installed: %v", err)
		}
		if err := m.Install(Spec{Exe: "wasync.exe", DataDir: spec.DataDir}); err == nil {
			t.Fatal("a relative program path was accepted")
		}
	})
	if got, err := readRunValue(RunName); err != nil || got != want {
		t.Fatalf("the startup item under the real name became %q, want %q (%v)", got, want, err)
	}
}

func readRunValue(name string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	s, _, err := k.GetStringValue(name)
	return s, err
}

func writeRunValue(name, data string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, data)
}

func deleteRunValue(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.DeleteValue(name)
}
