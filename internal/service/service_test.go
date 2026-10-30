package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	got, err := parsePlist(b)
	if err != nil {
		t.Fatalf("the plist is not well-formed: %v\n%s", err, b)
	}
	if got["Label"] != Label {
		t.Errorf("Label = %v", got["Label"])
	}
	if args, want := got["ProgramArguments"], []any{s.Exe, "--data-dir", s.DataDir, "serve"}; !reflect.DeepEqual(args, want) {
		t.Errorf("ProgramArguments = %#v, want %#v", args, want)
	}
	if got["RunAtLoad"] != true {
		t.Errorf("RunAtLoad = %v", got["RunAtLoad"])
	}
	if keep, want := got["KeepAlive"], map[string]any{"SuccessfulExit": false}; !reflect.DeepEqual(keep, want) {
		t.Errorf("KeepAlive = %#v, want %#v", keep, want)
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
	want := `powershell.exe -NoProfile -NonInteractive -WindowStyle Hidden -Command "Start-Process -WindowStyle Hidden -FilePath 'C:\Users\O''Brien\wasync.exe' -ArgumentList '--data-dir \"C:\Users\O''Brien\AppData\Roaming\wasync\" serve'"`
	if got != want {
		t.Fatalf("Run command\n got: %s\nwant: %s", got, want)
	}
}

// parsePlist reads an XML property list into maps, slices, strings, bools and
// ints, the way launchd reads it.
func parsePlist(b []byte) (map[string]any, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			v, err := plistValue(d, se)
			if err != nil {
				return nil, err
			}
			return v.(map[string]any), nil
		}
	}
}

func plistValue(d *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "true", "false":
		return start.Name.Local == "true", d.Skip()
	case "string", "key":
		var s string
		err := d.DecodeElement(&s, &start)
		return s, err
	case "integer":
		var n int
		err := d.DecodeElement(&n, &start)
		return n, err
	case "array", "dict":
		var items []any
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch tok := tok.(type) {
			case xml.StartElement:
				v, err := plistValue(d, tok)
				if err != nil {
					return nil, err
				}
				items = append(items, v)
			case xml.EndElement:
				if start.Name.Local == "array" {
					return items, nil
				}
				if len(items)%2 != 0 {
					return nil, fmt.Errorf("dict has a key without a value")
				}
				m := map[string]any{}
				for i := 0; i < len(items); i += 2 {
					k, ok := items[i].(string)
					if !ok {
						return nil, fmt.Errorf("dict key %v is not a string", items[i])
					}
					m[k] = items[i+1]
				}
				return m, nil
			}
		}
	}
	return nil, fmt.Errorf("unknown plist element <%s>", start.Name.Local)
}
