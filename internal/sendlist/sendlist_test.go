package sendlist

import (
	"os"
	"path/filepath"
	"runtime"
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
	for _, content := range []string{`not json`, `{"lists":[{"agent_id":"a1","numbers":[{"chat":"one@g.us"}]}]}`, `{"lists":[],"extra":1}`} {
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
