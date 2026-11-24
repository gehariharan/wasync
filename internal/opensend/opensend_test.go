package opensend

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSwitchLastsOneHourThenTurnsItselfOff(t *testing.T) {
	dir := privateDir(t)
	if _, on := ActiveNow(dir, time.Now()); on {
		t.Fatal("open sending was on with no file")
	}
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	s, err := Start(dir, now)
	if err != nil || !s.Until.Equal(now.Add(time.Hour)) {
		t.Fatalf("start: %+v %v", s, err)
	}
	if _, on := ActiveNow(dir, now.Add(59*time.Minute)); !on {
		t.Fatal("switched off early")
	}
	if _, on := ActiveNow(dir, now.Add(time.Hour)); on {
		t.Fatal("still on after the hour")
	}
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the switch file must be private: %v %v", info, err)
	}
}

func TestStopIsHarmlessTwiceAndDamageMeansOff(t *testing.T) {
	dir := privateDir(t)
	if _, err := Start(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := Stop(dir); err != nil || Stop(dir) != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, on := ActiveNow(dir, time.Now()); on {
		t.Fatal("on after stop")
	}
	for name, content := range map[string]string{"garbage": "{not json", "extra field": `{"until":"2999-01-01T00:00:00Z","everyone":true}`, "empty": ""} {
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, on := ActiveNow(dir, time.Now()); on {
			t.Fatalf("%s: a damaged file switched open sending on", name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"until":"2999-01-01T00:00:00Z"}`), 0o666); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(dir, FileName), 0o666)
	if _, on := ActiveNow(dir, time.Now()); on {
		t.Fatal("a world-writable file switched open sending on")
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
