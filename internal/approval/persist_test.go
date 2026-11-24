package approval

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

func access(agent, query string) Access {
	return Access{Agent: agent, AgentID: "id-" + agent, Scopes: []string{"read.messages"}, Query: query, Candidates: []Candidate{{Chat: "one@g.us", Name: "One"}}}
}

func TestRequestsWaitThirtyMinutes(t *testing.T) {
	q := &Queue{}
	r, err := q.Add(1, Input{Kind: "purge-chat", Chat: "one@g.us"})
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(r.Expires); left < 29*time.Minute || left > 31*time.Minute {
		t.Fatalf("a request waits %v, want about 30 minutes", left)
	}
	a, err := q.AddAccess(1, access("helper", "family"))
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(a.Expires); left < 29*time.Minute || left > 31*time.Minute {
		t.Fatalf("an access request waits %v, want about 30 minutes", left)
	}
}

func TestWaitingRequestsSurviveARestartButNothingElseDoes(t *testing.T) {
	dir := privateDir(t)
	q := &Queue{}
	if err := q.Persist(dir); err != nil {
		t.Fatal(err)
	}
	waiting, _ := q.Add(1, Input{Kind: "purge-chat", Chat: "one@g.us"})
	asked, err := q.AddAccess(1, access("helper", "family"))
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Decide(1, asked.ID, "one@g.us", HistoryNow); err != nil {
		t.Fatal(err)
	}
	running, _ := q.Add(1, Input{Kind: "clear-message", Chat: "one@g.us", Message: "m1"})
	if _, err = q.Take(1, running.ID); err != nil {
		t.Fatal(err)
	}
	rejected, _ := q.Add(1, Input{Kind: "purge-chat", Chat: "two@g.us"})
	if err = q.Reject(1, rejected.ID); err != nil {
		t.Fatal(err)
	}
	stale, _ := q.Add(1, Input{Kind: "purge-chat", Chat: "three@g.us"})
	q.mu.Lock()
	for i := range q.requests {
		if q.requests[i].ID == stale.ID {
			q.requests[i].Expires = time.Now().Add(-time.Second)
		}
	}
	q.save()
	q.mu.Unlock()

	again := &Queue{}
	if err = again.Persist(dir); err != nil {
		t.Fatal(err)
	}
	list := again.PendingList(1)
	if len(list) != 2 || list[0].ID != waiting.ID || list[1].ID != asked.ID {
		t.Fatalf("restored %+v; only the two waiting requests should come back", list)
	}
	if list[1].Access.Chosen != "one@g.us" || list[1].Access.Agent != "helper" {
		t.Fatalf("the access request lost its details: %+v", list[1].Access)
	}
	for _, gone := range []string{running.ID, rejected.ID, stale.ID} {
		if again.Get(1, gone) != nil {
			t.Fatalf("request %s should not have been restored", gone)
		}
	}
	if _, err = again.Take(1, waiting.ID); err != nil {
		t.Fatalf("a restored request must still be answerable: %v", err)
	}
	again.Finish(waiting.ID, "completed", nil)
	third := &Queue{}
	if err = third.Persist(dir); err != nil || len(third.PendingList(1)) != 1 {
		t.Fatalf("an answered request must not come back: %v %+v", err, third.PendingList(1))
	}
}

func TestADamagedOrOpenFileRestoresNothing(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, PendingFileName)
	for name, content := range map[string]string{
		"garbage":       "{not json",
		"wrong version": `{"version":9,"requests":[]}`,
		"invalid chat":  `{"version":1,"requests":[{"id":"00000000000000000000000000000001","account_id":1,"kind":"purge-chat","chat":"not a chat","expires_at":"2999-01-01T00:00:00Z","status":"pending_human_confirmation"}]}`,
		"bad id":        `{"version":1,"requests":[{"id":"1","account_id":1,"kind":"purge-chat","chat":"one@g.us","expires_at":"2999-01-01T00:00:00Z","status":"pending_human_confirmation"}]}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		q := &Queue{}
		_ = q.Persist(dir)
		if got := q.PendingList(1); len(got) != 0 {
			t.Fatalf("%s: restored %+v", name, got)
		}
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"requests":[{"id":"00000000000000000000000000000001","account_id":1,"kind":"purge-chat","chat":"one@g.us","expires_at":"2999-01-01T00:00:00Z","status":"pending_human_confirmation"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if q := (&Queue{}); q.Persist(dir) != nil || len(q.PendingList(1)) != 1 {
		t.Fatal("a valid file was not restored")
	}
	if runtime.GOOS == "windows" {
		return // permission bits mean nothing there; the private-file check reads the real access list
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	q := &Queue{}
	if err := q.Persist(dir); err == nil || len(q.PendingList(1)) != 0 {
		t.Fatalf("a file others can write was trusted: %v", err)
	}
}

func TestAQueueWithoutAFolderKeepsNothing(t *testing.T) {
	q := &Queue{}
	if err := q.Persist(""); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(1, Input{Kind: "purge-chat", Chat: "one@g.us"}); err != nil {
		t.Fatal(err)
	}
	var none *Queue
	if err := none.Persist(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
