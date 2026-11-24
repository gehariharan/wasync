package sentlog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAppendThenRecentReturnsNewestFirstWithoutMessageText(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	for i, e := range []Entry{
		{Time: at, Actor: Owner, Kind: "send", Chat: "a@g.us", MessageID: "m1", Outcome: OutcomeSent},
		{Time: at.Add(time.Minute), Actor: "whatsapp-axi", Kind: "reply", Chat: "b@g.us", MessageID: "m2", Outcome: OutcomeUnknown, Code: "send_failed"},
		{Time: at.Add(2 * time.Minute), Actor: "whatsapp-axi", Kind: "send", Chat: "c@g.us", Outcome: OutcomeFailed},
	} {
		if err := l.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	got, err := l.Recent(2)
	if err != nil || len(got) != 2 {
		t.Fatalf("recent: %v %v", got, err)
	}
	if got[0].Chat != "c@g.us" || got[1].Actor != "whatsapp-axi" || got[1].Outcome != OutcomeUnknown {
		t.Fatalf("order or content wrong: %+v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(strings.ToLower(string(raw)), "text") {
		t.Fatalf("the log has a field that could hold message text: %s", raw)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(dir, FileName)); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("the log is not private: %v %v", info, err)
		}
	}
}

func TestAppendRefusesAnIncompleteEntry(t *testing.T) {
	l := New(t.TempDir())
	for _, e := range []Entry{{Kind: "send", Chat: "a@g.us", Outcome: OutcomeSent}, {Actor: Owner, Chat: "a@g.us", Outcome: OutcomeSent}, {Actor: Owner, Kind: "send", Outcome: OutcomeSent}, {Actor: Owner, Kind: "send", Chat: "a@g.us"}} {
		if l.Append(e) == nil {
			t.Fatalf("accepted %+v", e)
		}
	}
}

func TestRotationKeepsOneOlderFileAndRecentReadsBoth(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	l.maxBytes = 300
	for i := 0; i < 12; i++ {
		if err := l.Append(Entry{Actor: Owner, Kind: "send", Chat: "a@g.us", MessageID: string(rune('a' + i)), Outcome: OutcomeSent}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, FileName+".1")); err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	got, err := l.Recent(50)
	if err != nil || len(got) < 3 || got[0].MessageID != "l" {
		t.Fatalf("recent after rotation: %d entries, %+v, err %v", len(got), got, err)
	}
	for _, e := range got {
		if e.MessageID == "a" {
			t.Fatalf("an entry older than the kept file survived: %+v", got)
		}
	}
}

func TestRecentSkipsLinesThatDoNotParseAndIsEmptyWithoutAFile(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	if got, err := l.Recent(5); err != nil || len(got) != 0 {
		t.Fatalf("no file: %v %v", got, err)
	}
	if err := l.Append(Entry{Actor: Owner, Kind: "send", Chat: "a@g.us", Outcome: OutcomeSent}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(filepath.Join(dir, FileName), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("not json\n")
	f.Close()
	if got, err := l.Recent(5); err != nil || len(got) != 1 {
		t.Fatalf("garbage line: %v %v", got, err)
	}
}
