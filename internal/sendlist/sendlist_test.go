package sendlist

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestParseTakesOneNumberPerEntryAndRefusesAnythingElse(t *testing.T) {
	chats, bad := Parse("\uFEFFnumber\n+15551230000")
	if bad != "number" || chats != nil {
		t.Fatalf("a header line must refuse the list: %v %q", chats, bad)
	}
	chats, bad = Parse(" +1 555 123 0000\r\n\n\"15551230001\";15551230002\t+1 (555) 123-0000,\n")
	if bad != "" || len(chats) != 3 || chats[0] != "15551230000@s.whatsapp.net" || chats[2] != "15551230002@s.whatsapp.net" {
		t.Fatalf("parse: %v %q", chats, bad)
	}
	if chats, bad = Parse(" \n , "); bad != "" || len(chats) != 0 {
		t.Fatalf("blank list: %v %q", chats, bad)
	}
}

func TestPutKeepsWhatWasSentAndRemoveForgetsTheList(t *testing.T) {
	dir := privateDir(t)
	at := time.Now().UTC().Truncate(time.Second)
	first := List{AgentID: "a1", Agent: "helper", GrantedAt: at, Numbers: []Number{{Chat: "15551230000@s.whatsapp.net", FirstSentAt: &at, LastSentAt: &at}}}
	if err := Put(dir, first); err != nil {
		t.Fatal(err)
	}
	if err := Put(dir, List{AgentID: "b2", Agent: "other", GrantedAt: at, Numbers: []Number{{Chat: "15551239999@s.whatsapp.net"}}}); err != nil {
		t.Fatal(err)
	}
	again := List{AgentID: "a1", Agent: "helper", GrantedAt: at, Numbers: []Number{{Chat: "15551230000@s.whatsapp.net"}, {Chat: "15551230001@s.whatsapp.net"}}}
	if err := Put(dir, again); err != nil {
		t.Fatal(err)
	}
	l, ok := For(dir, "a1", time.Now())
	if !ok || len(l.Numbers) != 2 || l.Find("15551230000@s.whatsapp.net").LastSentAt == nil || l.NotMessaged() != 1 {
		t.Fatalf("approving again forgot what was sent: %+v", l)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(dir, FileName)); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("the file is not private: %v %v", info, err)
		}
	}
	if err := Remove(dir, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, ok = For(dir, "a1", time.Now()); ok {
		t.Fatal("a removed list is still in force")
	}
	if _, ok = For(dir, "b2", time.Now()); !ok {
		t.Fatal("removing one list removed another")
	}
	if err := Update(dir, "a1", time.Now(), func(*List) error { return nil }); err != ErrNoList {
		t.Fatalf("update of a missing list: %v", err)
	}
}

func TestAnExpiredOrDamagedListIsNotInForce(t *testing.T) {
	dir := privateDir(t)
	past := time.Now().Add(-time.Minute)
	if err := Put(dir, List{AgentID: "a1", Agent: "helper", Until: &past, Numbers: []Number{{Chat: "15551230000@s.whatsapp.net"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := For(dir, "a1", time.Now()); ok {
		t.Fatal("an expired list is in force")
	}
	for _, content := range []string{`not json`, `{"lists":[{"agent_id":"a1","numbers":[{"chat":"one@g.us"}]}]}`, `{"lists":[],"extra":1}`, `{"lists":[{"agent_id":"a1","per_hour":50,"numbers":[{"chat":"15551230000@s.whatsapp.net"}]}]}`} {
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Fatalf("%q loaded", content)
		}
		if _, ok := For(dir, "a1", time.Now()); ok {
			t.Fatalf("%q: a damaged file gave a list", content)
		}
	}
	if Put(dir, List{AgentID: "a1"}) == nil {
		t.Fatal("an empty list was saved")
	}
}

func TestDropCountsStoppedNumbers(t *testing.T) {
	l := List{Numbers: []Number{{Chat: "1@s.whatsapp.net"}, {Chat: "2@s.whatsapp.net"}, {Chat: "3@s.whatsapp.net"}}}
	if n := l.Drop(map[string]bool{"2@s.whatsapp.net": true, "9@s.whatsapp.net": true}); n != 1 || l.Stopped != 1 || len(l.Numbers) != 2 || l.Find("2@s.whatsapp.net") != nil {
		t.Fatalf("drop: %d %+v", n, l)
	}
}

func TestARemovedListsSendHistoryComesBackWhenTheSameNumbersAreApprovedAgain(t *testing.T) {
	dir := privateDir(t)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	sent, fresh := "15551230000@s.whatsapp.net", "15551230001@s.whatsapp.net"
	l := List{AgentID: "a1", Agent: "helper", GrantedAt: at, Numbers: []Number{{Chat: sent, FirstSentAt: &at, LastSentAt: &at}, {Chat: fresh}}}
	if err := Put(dir, l); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, "a1"); err != nil {
		t.Fatal(err)
	}
	// Another agent's list does not inherit it.
	if err := Put(dir, List{AgentID: "b2", Agent: "other", Numbers: []Number{{Chat: sent}}}); err != nil {
		t.Fatal(err)
	}
	if o, _ := For(dir, "b2", time.Now()); o.NotMessaged() != 1 {
		t.Fatalf("another agent inherited the history: %+v", o)
	}
	if err := Put(dir, List{AgentID: "a1", Agent: "helper", Numbers: []Number{{Chat: sent}, {Chat: fresh}}}); err != nil {
		t.Fatal(err)
	}
	got, ok := For(dir, "a1", time.Now())
	if n := got.Find(sent); !ok || n == nil || n.FirstSentAt == nil || !n.FirstSentAt.Equal(at) || !n.LastSentAt.Equal(at) || got.NotMessaged() != 1 {
		t.Fatalf("approving again after removing forgot who was messaged: %+v", got)
	}
	// A number the new list leaves out is remembered for the next approval.
	if err := Put(dir, List{AgentID: "a1", Agent: "helper", Numbers: []Number{{Chat: fresh}}}); err != nil {
		t.Fatal(err)
	}
	if err := Put(dir, List{AgentID: "a1", Agent: "helper", Numbers: []Number{{Chat: sent}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ = For(dir, "a1", time.Now()); got.NotMessaged() != 0 {
		t.Fatalf("a number left out of one approval lost its history: %+v", got)
	}
}

func TestRemovedListHistoryIsForgottenAfterThirtyDays(t *testing.T) {
	dir := privateDir(t)
	old := time.Now().Add(-HistoryKept - time.Hour).UTC().Format(time.RFC3339)
	content := `{"lists":[],"sent":[{"agent_id":"a1","chat":"15551230000@s.whatsapp.net","first_sent_at":"` + old + `","last_sent_at":"` + old + `","removed_at":"` + old + `"}]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Put(dir, List{AgentID: "a1", Agent: "helper", Numbers: []Number{{Chat: "15551230000@s.whatsapp.net"}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := For(dir, "a1", time.Now()); got.NotMessaged() != 1 {
		t.Fatalf("history older than %v was used: %+v", HistoryKept, got)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(data), `"sent"`) {
		t.Fatalf("old history was kept in the file:\n%s", data)
	}
}

// A number that replied STOP stays off every list after its send history is
// forgotten, and is counted once however often it is approved again.
func TestAStopOutlivesTheSendHistory(t *testing.T) {
	dir := privateDir(t)
	x, y := "15551230000@s.whatsapp.net", "15551230001@s.whatsapp.net"
	old := time.Now().Add(-HistoryKept - time.Hour).UTC().Format(time.RFC3339)
	content := `{"lists":[],"sent":[{"agent_id":"a1","chat":"` + x + `","first_sent_at":"` + old + `","removed_at":"` + old + `"}],"opted_out":["` + x + `"]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"a1", "b2", "a1"} {
		if err := Put(dir, List{AgentID: agent, Agent: agent, Numbers: []Number{{Chat: x}, {Chat: y}}}); err != nil {
			t.Fatal(err)
		}
		if got, ok := For(dir, agent, time.Now()); !ok || got.Find(x) != nil || got.Find(y) == nil || got.Stopped != 1 {
			t.Fatalf("%s: %+v", agent, got)
		}
	}
	if first, err := FirstSent(dir); err != nil || len(first) != 0 {
		t.Fatalf("forgotten history is still used: %v %v", first, err)
	}
}

func TestOptOutDropsTheNumberFromEveryListOnce(t *testing.T) {
	dir := privateDir(t)
	x, y := "15551230000@s.whatsapp.net", "15551230001@s.whatsapp.net"
	for _, agent := range []string{"a1", "b2"} {
		if err := Put(dir, List{AgentID: agent, Agent: agent, Numbers: []Number{{Chat: x}, {Chat: y}}}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := OptOut(dir, map[string]bool{x: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range []string{"a1", "b2"} {
		if got, _ := For(dir, agent, time.Now()); got.Find(x) != nil || got.Stopped != 1 || len(got.Numbers) != 1 {
			t.Fatalf("%s: %+v", agent, got)
		}
	}
}

func TestAPausedListStaysInForceAndResumes(t *testing.T) {
	dir := privateDir(t)
	if err := Put(dir, List{AgentID: "a1", Agent: "helper", Numbers: []Number{{Chat: "15551230000@s.whatsapp.net"}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := Pause(dir, "a1", true, now); err != nil {
		t.Fatal(err)
	}
	l, ok := For(dir, "a1", now)
	if !ok || !l.Paused() || !Listed(dir, "15551230000@s.whatsapp.net", now) {
		t.Fatalf("a paused list must stay in force, listed and marked paused: %+v %v", l, ok)
	}
	first := *l.PausedAt
	if err := Pause(dir, "a1", true, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if l, _ = For(dir, "a1", now); !l.PausedAt.Equal(first) {
		t.Fatalf("pausing again moved the pause time: %v", l.PausedAt)
	}
	if err := Pause(dir, "a1", false, now); err != nil {
		t.Fatal(err)
	}
	if l, _ = For(dir, "a1", now); l.Paused() {
		t.Fatal("the list did not resume")
	}
	if err := Pause(dir, "nobody", true, now); err != ErrNoList {
		t.Fatalf("pausing a missing list: %v", err)
	}
}

// The file must stay readable however much is remembered: load refuses a file
// over maxFileBytes, which would stop every list. The oldest history goes first.
func TestHistoryNeverGrowsTheFilePastWhatLoadReads(t *testing.T) {
	dir := privateDir(t)
	stopped := "15559990000@s.whatsapp.net"
	if err := OptOut(dir, map[string]bool{stopped: true}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	full := func(i int) List {
		l := List{AgentID: fmt.Sprintf("a%d", i), Agent: fmt.Sprintf("agent %d", i), GrantedAt: at}
		for j := range MaxNumbers {
			l.Numbers = append(l.Numbers, Number{Chat: fmt.Sprintf("1555%d%06d@s.whatsapp.net", i, j), FirstSentAt: &at, LastSentAt: &at})
		}
		return l
	}
	for i := range 5 {
		if err := Put(dir, full(i)); err != nil {
			t.Fatalf("list %d: %v", i, err)
		}
		if i < 3 {
			if err := Remove(dir, fmt.Sprintf("a%d", i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if info, err := os.Stat(filepath.Join(dir, FileName)); err != nil || info.Size() > maxFileBytes {
		t.Fatalf("file size: %v %v", info, err)
	}
	if lists, err := Load(dir); err != nil || len(lists) != 2 {
		t.Fatalf("the saved file must load: %d %v", len(lists), err)
	}
	if err := Put(dir, List{AgentID: "a2", Agent: "agent 2", Numbers: []Number{{Chat: "15552000999@s.whatsapp.net"}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := For(dir, "a2", time.Now()); got.NotMessaged() != 0 {
		t.Fatalf("the newest history was forgotten: %+v", got)
	}
	if err := Put(dir, List{AgentID: "a5", Agent: "agent 5", Numbers: []Number{{Chat: stopped}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := For(dir, "a5", time.Now()); got.Find(stopped) != nil {
		t.Fatalf("trimming the history forgot a STOP: %+v", got)
	}
}
