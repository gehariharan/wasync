package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scriptedRunner answers commands by name and records them.
type scriptedRunner struct {
	calls  [][]string
	envs   [][]string
	answer func(name string, args []string) (stdout, stderr string, err error)
}

func (s *scriptedRunner) run(_ context.Context, name string, args []string, env []string) ([]byte, []byte, error) {
	s.calls = append(s.calls, append([]string{name}, args...))
	s.envs = append(s.envs, env)
	out, errOut, err := s.answer(name, args)
	return []byte(out), []byte(errOut), err
}

func (s *scriptedRunner) ran(name string) [][]string {
	var got [][]string
	for _, c := range s.calls {
		if filepath.Base(c[0]) == name {
			got = append(got, c)
		}
	}
	return got
}

func TestMacProbeNeedsADeveloperSignature(t *testing.T) {
	signed := &scriptedRunner{answer: func(name string, args []string) (string, string, error) {
		switch name {
		case "codesign":
			return "", "Executable=/Applications/wasync.app/Contents/MacOS/wasync\nTeamIdentifier=57V379V642\n", nil
		case "plutil":
			return "com.gehariharan.wasync\n", "", nil
		}
		return "", "", errors.New("unexpected " + name)
	}}
	ch := Channel{Kind: KindMacApp, OneClick: true, Target: "/Applications/wasync.app"}
	Mac{Run: signed.run}.Probe(context.Background(), &ch)
	if !ch.OneClick || ch.Team != "57V379V642" || ch.BundleID != "com.gehariharan.wasync" {
		t.Fatalf("signed app: %+v", ch)
	}
	adhoc := &scriptedRunner{answer: func(string, []string) (string, string, error) {
		return "", "Signature=adhoc\nTeamIdentifier=not set\n", nil
	}}
	ch = Channel{Kind: KindMacApp, OneClick: true, Target: "/Applications/wasync.app"}
	Mac{Run: adhoc.run}.Probe(context.Background(), &ch)
	if ch.OneClick || !strings.Contains(ch.Note, "not signed") {
		t.Fatalf("ad hoc app: %+v", ch)
	}
}

// macImage fakes hdiutil and ditto: attaching "mounts" a folder holding apps.
func macImage(t *testing.T, apps []string, codesignOK bool, bundleID string) *scriptedRunner {
	t.Helper()
	return &scriptedRunner{answer: func(name string, args []string) (string, string, error) {
		switch name {
		case "hdiutil":
			if args[0] == "attach" {
				mount := args[len(args)-2]
				for _, a := range apps {
					if err := os.MkdirAll(filepath.Join(mount, a, "Contents"), 0o700); err != nil {
						return "", "", err
					}
				}
			}
			return "", "", nil
		case "ditto":
			return "", "", os.CopyFS(args[1], os.DirFS(args[0]))
		case "codesign":
			if !codesignOK {
				return "", "", errors.New("code failed to satisfy specified code requirement(s)")
			}
			return "", "", nil
		case "spctl":
			return "", "", nil
		case "plutil":
			return bundleID, "", nil
		}
		return "", "", errors.New("unexpected " + name)
	}}
}

var macChannel = Channel{Kind: KindMacApp, OneClick: true, Target: "/Applications/wasync.app", Arch: "arm64", Team: "57V379V642", BundleID: "com.gehariharan.wasync"}

func TestMacPrepareCopiesTheAppOutAndChecksItsTeam(t *testing.T) {
	staging := t.TempDir()
	run := macImage(t, []string{"wasync.app"}, true, "com.gehariharan.wasync")
	staged, err := Mac{Run: run.run}.Prepare(context.Background(), macChannel, filepath.Join(staging, "u.dmg"), staging)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Path != filepath.Join(staging, "app", "wasync.app") || staged.Requirement != `notarized and anchor apple generic and certificate leaf[subject.OU] = "57V379V642"` {
		t.Fatalf("staged %+v", staged)
	}
	if _, err := os.Stat(staged.Path); err != nil {
		t.Fatal(err)
	}
	cs := run.ran("codesign")
	if len(cs) != 1 || cs[0][len(cs[0])-2] != "-R="+staged.Requirement || cs[0][len(cs[0])-1] != staged.Path {
		t.Fatalf("codesign calls %v", cs)
	}
	if d := run.ran("hdiutil"); len(d) != 2 || d[1][1] != "detach" {
		t.Fatalf("the image must be detached: %v", d)
	}
}

func TestMacPrepareRefusesWhatIsNotTheSameSignedApp(t *testing.T) {
	cases := map[string]*scriptedRunner{
		"wrong team":    macImage(t, []string{"wasync.app"}, false, "com.gehariharan.wasync"),
		"other app":     macImage(t, []string{"wasync.app"}, true, "com.example.other"),
		"two apps":      macImage(t, []string{"wasync.app", "other.app"}, true, "com.gehariharan.wasync"),
		"no app at all": macImage(t, nil, true, "com.gehariharan.wasync"),
	}
	for name, run := range cases {
		staging := t.TempDir()
		if _, err := (Mac{Run: run.run}).Prepare(context.Background(), macChannel, filepath.Join(staging, "u.dmg"), staging); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if d := run.ran("hdiutil"); len(d) < 2 || d[len(d)-1][1] != "detach" && d[len(d)-1][2] != "detach" {
			t.Errorf("%s: the image must be detached: %v", name, d)
		}
	}
	if _, err := (Mac{Run: macImage(t, []string{"wasync.app"}, true, "x").run}).Prepare(context.Background(), Channel{Kind: KindMacApp}, "u.dmg", t.TempDir()); err == nil {
		t.Error("an unknown team must be refused before anything runs")
	}
}

func TestMacInstallSwapsTheAppAndKeepsTheOldOneOnFailure(t *testing.T) {
	apps := t.TempDir()
	target := filepath.Join(apps, "wasync.app")
	staged := filepath.Join(t.TempDir(), "wasync.app")
	for dir, version := range map[string]string{target: "old", staged: "new"} {
		if err := os.MkdirAll(filepath.Join(dir, "Contents"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Contents", "v"), []byte(version), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := macImage(t, nil, true, "")
	if err := (Mac{Run: run.run}).Install(context.Background(), Plan{Target: target, Staged: staged, Requirement: "req"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "Contents", "v")); string(b) != "new" {
		t.Fatalf("installed %q", b)
	}
	if entries, _ := os.ReadDir(apps); len(entries) != 1 {
		t.Fatalf("left behind %v", entries)
	}
	refused := macImage(t, nil, false, "")
	if err := (Mac{Run: refused.run}).Install(context.Background(), Plan{Target: target, Staged: staged, Requirement: "req"}); err == nil {
		t.Fatal("an app that fails its signature check at install time must be refused")
	}
	if b, _ := os.ReadFile(filepath.Join(target, "Contents", "v")); string(b) != "new" {
		t.Fatalf("a refused install changed the app: %q", b)
	}
}

func TestWindowsMatchesTheSignerOnlyWhenTheRunningCopyIsSigned(t *testing.T) {
	sig := map[string]string{}
	run := &scriptedRunner{}
	run.answer = func(name string, args []string) (string, string, error) {
		if !strings.HasSuffix(filepath.ToSlash(name), "WindowsPowerShell/v1.0/powershell.exe") {
			return "", "", nil
		}
		env := run.envs[len(run.envs)-1]
		file := strings.TrimPrefix(env[0], "WASYNC_UPDATE_FILE=")
		return sig[file], "", nil
	}
	w := Windows{Run: run.run, Getenv: func(string) string { return `C:\Windows` }}
	exe, setup := `C:\wasync\wasync.exe`, filepath.Join(t.TempDir(), "setup.exe")
	if err := os.WriteFile(setup, []byte("setup"), 0o600); err != nil {
		t.Fatal(err)
	}

	sig[exe], sig[setup] = "NotSigned\n", "NotSigned\n"
	ch := Channel{Kind: KindWindowsSetup, OneClick: true, Target: exe}
	w.Probe(context.Background(), &ch)
	if !ch.OneClick || ch.Signer != "" {
		t.Fatalf("unsigned preview: %+v", ch)
	}
	staged, err := w.Prepare(context.Background(), ch, setup, filepath.Dir(setup))
	if err != nil || staged.SHA256 != sha([]byte("setup")) {
		t.Fatalf("unsigned preview prepare: %+v %v", staged, err)
	}
	for _, c := range run.calls {
		for _, a := range c {
			if strings.Contains(a, setup) || strings.Contains(a, exe) {
				t.Fatalf("a path must reach PowerShell only through the environment: %v", c)
			}
		}
	}

	sig[exe] = "Valid\r\nCN=Hariharan Ragunathan"
	ch = Channel{Kind: KindWindowsSetup, OneClick: true, Target: exe}
	w.Probe(context.Background(), &ch)
	if ch.Signer != "CN=Hariharan Ragunathan" {
		t.Fatalf("signed copy: %+v", ch)
	}
	if _, err := w.Prepare(context.Background(), ch, setup, filepath.Dir(setup)); err == nil {
		t.Fatal("an unsigned update for a signed copy must be refused")
	}
	sig[setup] = "Valid\nCN=Someone Else"
	if _, err := w.Prepare(context.Background(), ch, setup, filepath.Dir(setup)); err == nil {
		t.Fatal("an update signed by another publisher must be refused")
	}
	sig[setup] = "Valid\nCN=Hariharan Ragunathan"
	if _, err := w.Prepare(context.Background(), ch, setup, filepath.Dir(setup)); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsInstallRunsSetupSilentlyOnlyIfUnchanged(t *testing.T) {
	run := &scriptedRunner{answer: func(string, []string) (string, string, error) { return "", "", nil }}
	w := Windows{Run: run.run}
	setup := filepath.Join(t.TempDir(), "setup.exe")
	if err := os.WriteFile(setup, []byte("setup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.Install(context.Background(), Plan{Staged: setup, SHA256: sha([]byte("other"))}); err == nil || len(run.calls) != 0 {
		t.Fatalf("a changed Setup must not run: %v %v", err, run.calls)
	}
	if err := w.Install(context.Background(), Plan{Staged: setup, SHA256: sha([]byte("setup"))}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(run.calls[0], " ")
	for _, want := range []string{setup, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/CLOSEAPPLICATIONS"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Setup ran as %q, missing %s", got, want)
		}
	}
}
