package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/axi/internal/installhint"
	"github.com/gehariharan/wasync/axi/internal/testutil"
)

// obsOnline returns a harness reading j through a fake API with a credential.
func obsOnline(t *testing.T, j *testutil.Journal) (*harness, *testutil.JournalAPI) {
	t.Helper()
	srv := testutil.NewJournalAPI(t, j)
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	h.env["WASYNC_AXI_TOKEN"] = "wsa_observer"
	return h, srv
}

// obsOffline returns a harness reading j through the fake `wasync query`.
func obsOffline(t *testing.T, j *testutil.Journal) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	bin, log := testutil.FakeJournalWasync(t, j)
	h.env["WASYNC_BIN"] = bin
	h.env["WASYNC_AXI_TOKEN"] = "wsa_observer"
	return h, log
}

type obsBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *obsBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *obsBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// obsStart runs args in the background until ctx is canceled; the returned
// channel yields the exit code.
func obsStart(h *harness, ctx context.Context, out *obsBuffer, args ...string) <-chan int {
	done := make(chan int, 1)
	go func() {
		a := New(out)
		a.Getenv = func(k string) string { return h.env[k] }
		a.Executable = func() (string, error) { return filepath.Join(home, "bin", "wasync-axi"), nil }
		a.HomeDir = func() (string, error) { return home, nil }
		a.ProbeTimeout = 2 * time.Second
		done <- a.Run(ctx, args)
	}()
	return done
}

func obsWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func obsCheckpoint(srv *testutil.JournalAPI, consumer string) int64 {
	var seq int64 = -1
	srv.Do(func(j *testutil.Journal) {
		if cp, ok := j.Checkpoints[consumer]; ok {
			seq = cp.Sequence
		}
	})
	return seq
}

func obsPuts(srv *testutil.JournalAPI) int {
	n := 0
	for _, r := range srv.Requests() {
		if strings.HasPrefix(r, "PUT ") {
			n++
		}
	}
	return n
}

type obsResult struct {
	Transport  string                 `json:"transport"`
	Consumer   string                 `json:"consumer"`
	Checkpoint map[string]any         `json:"checkpoint"`
	Start      string                 `json:"start"`
	After      string                 `json:"after"`
	Changes    []map[string]any       `json:"changes"`
	NextCursor string                 `json:"next_cursor"`
	HasMore    bool                   `json:"has_more"`
	Committed  map[string]any         `json:"committed"`
	Help       []string               `json:"help"`
	Extra      map[string]interface{} `json:"-"`
}

func obsJSON(t *testing.T, h *harness, args ...string) obsResult {
	t.Helper()
	out, code := h.run(t, append(args, "--json")...)
	var r obsResult
	dec := json.NewDecoder(strings.NewReader(out))
	if code != ExitOK || dec.Decode(&r) != nil {
		t.Fatalf("%v: exit %d\n%s", args, code, out)
	}
	var receipt obsResult
	if err := dec.Decode(&receipt); err == nil {
		if receipt.Committed == nil {
			t.Fatalf("unexpected record: %s", out)
		}
		r.Committed = receipt.Committed
		if err := dec.Decode(&receipt); err != io.EOF {
			t.Fatalf("unexpected trailing output: %s", out)
		}
	} else if err != io.EOF {
		t.Fatalf("invalid trailing output: %s", out)
	}
	return r
}

func obsSeqs(r obsResult) []int {
	var out []int
	for _, c := range r.Changes {
		out = append(out, int(c["sequence"].(float64)))
	}
	return out
}

func TestObserveUsageErrors(t *testing.T) {
	h, srv := obsOnline(t, testutil.NewJournal(2))
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"observe", "--bogus"}, "unknown flag --bogus for `observe`"},
		{[]string{"observe", "--since", "2026-10-01T00:00:00Z"}, "unknown flag --since"},
		{[]string{"observe", "--after", "2026-10-01T00:00:00Z"}, "--after is not a change cursor (c1_...)"},
		{[]string{"observe", "--after", "1730000000"}, "--after is not a change cursor"},
		{[]string{"observe", "--after", "m1_abc"}, "--after is not a change cursor"},
		{[]string{"observe", "--after"}, "--after needs a value"},
		{[]string{"observe", "--limit", "0"}, "--limit must be a number from 1 to 1000"},
		{[]string{"observe", "--limit=1001"}, "--limit must be a number from 1 to 1000"},
		{[]string{"observe", "--commit"}, "--commit needs --consumer NAME"},
		{[]string{"observe", "--interval", "1s"}, "--interval needs --follow"},
		{[]string{"observe", "--follow", "--interval", "1ms"}, "--interval must be a duration"},
		{[]string{"observe", "--from-oldest"}, "--from-oldest needs --consumer NAME"},
		{[]string{"observe", "--consumer", "r", "--from-oldest", "--after", "c1_x"}, "--after and --from-oldest conflict"},
		{[]string{"observe", "--consumer", "Bad_Name"}, `invalid consumer name "Bad_Name"`},
		{[]string{"observe", "--follow=yes"}, "--follow takes no value"},
		{[]string{"observe", "--json=false"}, "--json takes no value"},
		{[]string{"observe", "c1_x"}, `unexpected argument "c1_x" for ` + "`observe`"},
		{[]string{"observe", "ack", "--consumer", "r"}, "`observe ack` needs the c1_ cursor"},
		{[]string{"observe", "ack", "c1_x"}, "`observe ack` needs --consumer NAME"},
		{[]string{"observe", "ack", "c1_x", "--consumer", "r", "--commit"}, "--commit does not apply to `observe ack`"},
		{[]string{"observe", "ack", "2026-10-01", "--consumer", "r"}, "CURSOR is not a change cursor"},
		{[]string{"observe", "ack", "c1_x", "c1_y", "--consumer", "r"}, `unexpected argument "c1_y"`},
	}
	for _, c := range cases {
		out, code := h.run(t, c.args...)
		if code != ExitUsage {
			t.Errorf("%v: exit %d\n%s", c.args, code, out)
		}
		mustContain(t, out, "error: "+c.want)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Fatalf("usage errors reached wasync: %v", srv.Requests())
	}
	out, code := h.run(t, "observe", "--limit", "0", "--json")
	var e map[string]string
	if code != ExitUsage || json.Unmarshal([]byte(out), &e) != nil || e["error_code"] != "usage" {
		t.Fatalf("json usage error: %d %s", code, out)
	}
}

func TestObserveHelp(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"observe", "--help"}, {"help", "observe"}, {"observe", "ack", "--help"}} {
		out, code := h.run(t, args...)
		if code != ExitOK {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
		mustContain(t, out, "usage: ", "--consumer NAME", "--follow", "ack CURSOR", "examples[")
	}
}

func TestObservePagesByCursor(t *testing.T) {
	h, srv := obsOnline(t, testutil.NewJournal(5))
	out, code := h.run(t, "observe", "--limit", "2")
	if code != ExitOK {
		t.Fatal(out)
	}
	mustContain(t, out, "transport: api", "start: oldest", "changes[2]{sequence,op,chat_id,message_id,ref,committed_at}:",
		"  \"1\",message.created,one@g.us,\"0\",m1_", `has_more: "true"`, "More changes are waiting: run `wasync-axi observe --after c1_",
		"Changes carry no message content")
	var seqs []int
	after := ""
	for i := 0; i < 5; i++ {
		args := []string{"observe", "--limit", "2"}
		if after != "" {
			args = append(args, "--after", after)
		}
		r := obsJSON(t, h, args...)
		if r.Transport != "api" || r.After != after {
			t.Fatalf("result %+v", r)
		}
		seqs = append(seqs, obsSeqs(r)...)
		after = r.NextCursor
		if !r.HasMore {
			break
		}
	}
	if !reflect.DeepEqual(seqs, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("sequences %v", seqs)
	}
	reqs := srv.Requests()
	if last := reqs[len(reqs)-1]; !strings.Contains(last, "GET /api/v1/changes?after=c1_") || !strings.Contains(last, "limit=2") {
		t.Fatalf("last request %q", last)
	}
	if obsPuts(srv) != 0 {
		t.Fatal("observe committed a checkpoint without being asked")
	}
	// Caught up: explicit empty state.
	out, _ = h.run(t, "observe", "--after", after)
	mustContain(t, out, "changes[0]{sequence,op,chat_id,message_id,ref,committed_at}:", `has_more: "false"`, "No changes after this position yet")
}

func TestObserveSkipsEmptyAllowlistedPages(t *testing.T) {
	j := testutil.NewJournal(0)
	for i := 0; i < 3; i++ {
		j.Add("message.created", "hidden@g.us", strconv.Itoa(i))
	}
	j.Add("message.revoked", "one@g.us", "9")
	j.Hidden["hidden@g.us"] = true
	h, srv := obsOnline(t, j)
	r := obsJSON(t, h, "observe", "--limit", "1")
	if !reflect.DeepEqual(obsSeqs(r), []int{4}) || r.HasMore || r.NextCursor != j.Cursor(4) {
		t.Fatalf("result %+v", r)
	}
	if n := len(srv.Requests()); n != 5 { // version + 4 pages
		t.Fatalf("requests %v", srv.Requests())
	}
}

func TestObserveResumesFromCheckpointAfterRestart(t *testing.T) {
	j := testutil.NewJournal(5)
	j.Checkpoints["replica"] = testutil.JournalCheckpoint{Epoch: j.Epoch, Sequence: 3}
	h, srv := obsOnline(t, j)
	// Every h.run is a fresh process: only wasync's checkpoint carries state.
	r := obsJSON(t, h, "observe", "--consumer", "replica")
	if r.Start != "checkpoint" || r.After != j.Cursor(3) || r.Checkpoint["cursor"] != j.Cursor(3) || !reflect.DeepEqual(obsSeqs(r), []int{4, 5}) {
		t.Fatalf("resume %+v", r)
	}
	if obsPuts(srv) != 0 || obsCheckpoint(srv, "replica") != 3 {
		t.Fatal("reading moved the checkpoint")
	}
	out, _ := h.run(t, "observe", "--consumer", "replica")
	mustContain(t, out, "consumer: replica", "checkpoint: "+j.Cursor(3), "start: checkpoint",
		"After processing, save progress with `wasync-axi observe ack "+j.Cursor(5)+" --consumer replica`")

	out, code := h.run(t, "observe", "ack", r.NextCursor, "--consumer", "replica")
	if code != ExitOK {
		t.Fatal(out)
	}
	mustContain(t, out, "transport: api", "consumer: replica", "checkpoint: "+j.Cursor(5), "updated_at: ")
	if obsCheckpoint(srv, "replica") != 5 || srv.Bodies()[0] != `{"cursor":"`+j.Cursor(5)+`"}` {
		t.Fatalf("ack stored %d body %v", obsCheckpoint(srv, "replica"), srv.Bodies())
	}
	r = obsJSON(t, h, "observe", "--consumer", "replica")
	if r.After != j.Cursor(5) || len(r.Changes) != 0 {
		t.Fatalf("after ack %+v", r)
	}
	// A consumer that never committed starts at the oldest change.
	r = obsJSON(t, h, "observe", "--consumer", "fresh")
	if r.Start != "oldest" || r.After != "" || r.Checkpoint["cursor"] != nil || len(r.Changes) != 5 {
		t.Fatalf("fresh %+v", r)
	}
}

func TestObserveAckRefusesBackwards(t *testing.T) {
	j := testutil.NewJournal(5)
	j.Checkpoints["replica"] = testutil.JournalCheckpoint{Epoch: j.Epoch, Sequence: 4}
	h, srv := obsOnline(t, j)
	out, code := h.run(t, "observe", "ack", j.Cursor(2), "--consumer", "replica")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "error: checkpoint cannot move backwards", "code: invalid_argument", "help: checkpoints only move forward; run `wasync-axi observe --consumer replica`")
	if obsCheckpoint(srv, "replica") != 4 {
		t.Fatal("checkpoint moved backwards")
	}
	out, code = h.run(t, "observe", "ack", j.Cursor(4), "--consumer", "replica", "--json")
	var ok struct {
		Transport  string            `json:"transport"`
		Checkpoint map[string]string `json:"checkpoint"`
	}
	if code != ExitOK || json.Unmarshal([]byte(out), &ok) != nil || ok.Checkpoint["cursor"] != j.Cursor(4) || ok.Transport != "api" {
		t.Fatalf("same-cursor ack: %d %s", code, out)
	}
}

func TestObserveCommitFlag(t *testing.T) {
	h, srv := obsOnline(t, testutil.NewJournal(5))
	r := obsJSON(t, h, "observe", "--consumer", "replica", "--limit", "2", "--commit")
	if r.Committed == nil || r.Committed["cursor"] != r.NextCursor || obsCheckpoint(srv, "replica") != 2 {
		t.Fatalf("commit %+v", r)
	}
	out, _ := h.run(t, "observe", "--consumer", "replica", "--limit", "2", "--commit")
	mustContain(t, out, "start: checkpoint", `  "3",`, `  "4",`, "committed: c1_", "Checkpoint committed")
	if obsCheckpoint(srv, "replica") != 4 {
		t.Fatal("second commit missing")
	}
}

func TestObserveCursorExpired(t *testing.T) {
	j := testutil.NewJournal(5)
	j.LowWater = 3 // entries 1-3 pruned
	j.Checkpoints["replica"] = testutil.JournalCheckpoint{Epoch: j.Epoch, Sequence: 1}
	h, srv := obsOnline(t, j)
	out, code := h.run(t, "observe", "--consumer", "replica")
	if code != ExitError {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out, "code: cursor_expired", "help: Resnapshot: re-read the chats and messages you replicate, then run `wasync-axi observe --consumer replica --from-oldest`")
	out, _ = h.run(t, "observe", "--after", j.Cursor(2), "--json")
	var e map[string]string
	if json.Unmarshal([]byte(out), &e) != nil || e["error_code"] != "cursor_expired" || !strings.Contains(e["help"], "without --after") || e["transport"] != "api" {
		t.Fatalf("json expired: %s", out)
	}
	// The resnapshot path: restart without a cursor and replace the checkpoint.
	r := obsJSON(t, h, "observe", "--consumer", "replica", "--from-oldest", "--commit")
	if r.Start != "oldest" || !reflect.DeepEqual(obsSeqs(r), []int{4, 5}) || obsCheckpoint(srv, "replica") != 5 {
		t.Fatalf("resnapshot %+v", r)
	}
	// A recreated archive (new epoch) also expires cursors.
	oldCursor := j.Cursor(5)
	srv.Do(func(j *testutil.Journal) { j.Epoch = "ffffffffffffffffffffffffffffffff" })
	out, _ = h.run(t, "observe", "ack", oldCursor, "--consumer", "replica")
	mustContain(t, out, "code: cursor_expired", "Resnapshot")
	out, _ = h.run(t, "observe", "--after", "c1_garbage")
	mustContain(t, out, "code: invalid_cursor", "pass a c1_ cursor from a previous observe page unchanged")
}

func TestObserveOfflineReadsWithoutCommitting(t *testing.T) {
	j := testutil.NewJournal(5)
	j.Checkpoints["replica"] = testutil.JournalCheckpoint{Epoch: j.Epoch, Sequence: 2}
	h, log := obsOffline(t, j)
	out, code := h.run(t, "observe", "--consumer", "replica", "--limit", "2")
	if code != ExitOK {
		t.Fatal(out)
	}
	mustContain(t, out, "transport: query", "checkpoint: "+j.Cursor(2), "start: checkpoint", `  "3",`, `  "4",`,
		"note: \"offline read through wasync query", "Checkpoints can only be committed while wasync is running")
	if strings.Contains(out, "observe ack") {
		t.Fatalf("offline output suggests an ack that cannot work:\n%s", out)
	}
	calls := testutil.ReadJournalCalls(t, log)
	var got []string
	for _, c := range calls {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{
		"query version --v1 --json",
		"query checkpoint --consumer=replica --v1 --json",
		"query changes --after=" + j.Cursor(2) + " --limit=2 --v1 --json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls\n%v\nwant\n%v", got, want)
	}
	for _, args := range [][]string{
		{"observe", "--consumer", "replica", "--commit"},
		{"observe", "--consumer", "replica", "--follow", "--commit"},
		{"observe", "ack", j.Cursor(4), "--consumer", "replica"},
	} {
		out, code := h.run(t, args...)
		if code != ExitError {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
		mustContain(t, out, "error: committing a checkpoint needs the running wasync API; offline reads never commit", "code: owner_offline", "wasync-axi never starts it")
	}
	// Refused before reading anything: only version probes ran.
	for _, c := range testutil.ReadJournalCalls(t, log)[len(calls):] {
		if c[1] != "version" {
			t.Fatalf("refused commit still ran %v", c)
		}
	}
	out, _ = h.run(t, "observe", "--after", "c1_garbage")
	mustContain(t, out, "code: invalid_cursor")
}

func TestObserveTransportsAgree(t *testing.T) {
	j := testutil.NewJournal(4)
	j.Add("capture.disabled", "one@g.us", "")
	j.Checkpoints["replica"] = testutil.JournalCheckpoint{Epoch: j.Epoch, Sequence: 1}
	online, _ := obsOnline(t, j)
	offline, _ := obsOffline(t, j)
	for _, args := range [][]string{
		{"observe", "--limit", "2"},
		{"observe", "--after", j.Cursor(2), "--limit", "2"},
		{"observe", "--consumer", "replica"},
	} {
		a, b := obsJSON(t, online, args...), obsJSON(t, offline, args...)
		if a.Transport != "api" || b.Transport != "query" {
			t.Fatalf("transports %s %s", a.Transport, b.Transport)
		}
		a.Transport, b.Transport, a.Help, b.Help = "", "", nil, nil
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%v disagree:\napi   %+v\nquery %+v", args, a, b)
		}
	}
}

func TestObserveFollowJSONLines(t *testing.T) {
	j := testutil.NewJournal(3)
	h, srv := obsOnline(t, j)
	srv.Do(func(*testutil.Journal) { srv.FailChanges = 2 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &obsBuffer{}
	done := obsStart(h, ctx, out, "observe", "--consumer", "replica", "--follow", "--json", "--interval", "10ms", "--limit", "2", "--commit")
	obsWaitFor(t, "first commit", func() bool { return obsCheckpoint(srv, "replica") == 3 })
	srv.Do(func(j *testutil.Journal) {
		j.Add("message.updated", "one@g.us", "1")
		j.Add("message.purged", "one@g.us", "2")
	})
	obsWaitFor(t, "second commit", func() bool { return obsCheckpoint(srv, "replica") == 5 })
	cancel()
	select {
	case code := <-done:
		if code != ExitOK {
			t.Fatalf("exit %d\n%s", code, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not stop on cancel")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var types []string
	var seqs []int
	var last map[string]any
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not one JSON object per line: %q", line)
		}
		types = append(types, ev["type"].(string))
		switch ev["type"] {
		case "change":
			seqs = append(seqs, int(ev["sequence"].(float64)))
		case "retry":
			if ev["error_code"] != "archive_unavailable" || ev["retry_in"] == "" {
				t.Fatalf("retry %v", ev)
			}
		case "start":
			if ev["transport"] != "api" || ev["start"] != "oldest" || ev["consumer"] != "replica" {
				t.Fatalf("start %v", ev)
			}
		}
		last = ev
	}
	if !reflect.DeepEqual(seqs, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("sequences %v\n%s", seqs, out.String())
	}
	if types[0] != "start" || types[1] != "retry" || types[2] != "retry" || last["type"] != "stopped" || last["next_cursor"] != j.Cursor(5) {
		t.Fatalf("events %v last %v", types, last)
	}
	commits := 0
	for _, ty := range types {
		if ty == "committed" {
			commits++
		}
	}
	if commits < 2 || commits != obsPuts(srv) {
		t.Fatalf("committed events %d, PUTs %d", commits, obsPuts(srv))
	}
	// Restart resumes where the stored checkpoint is.
	r := obsJSON(t, h, "observe", "--consumer", "replica")
	if r.Start != "checkpoint" || r.After != j.Cursor(5) || len(r.Changes) != 0 {
		t.Fatalf("resume %+v", r)
	}
}

func TestObserveFollowTextStopsCleanlyWithoutCommitting(t *testing.T) {
	j := testutil.NewJournal(2)
	j.Checkpoints["replica"] = testutil.JournalCheckpoint{Epoch: j.Epoch, Sequence: 1}
	h, srv := obsOnline(t, j)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &obsBuffer{}
	done := obsStart(h, ctx, out, "observe", "--consumer", "replica", "--follow", "--interval", "10ms")
	obsWaitFor(t, "idle polls", func() bool { return len(srv.Requests()) >= 6 })
	cancel()
	if code := <-done; code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	text := out.String()
	mustContain(t, text, "transport: api", "start: checkpoint", "follow: polling every 10ms until interrupted",
		"changes[1]{", `  "2",message.created`, "stopped: interrupted", "next_cursor: "+j.Cursor(2),
		"Ack processed changes with `wasync-axi observe ack CURSOR --consumer replica`")
	if obsPuts(srv) != 0 || obsCheckpoint(srv, "replica") != 1 {
		t.Fatal("--follow without --commit committed")
	}
}

func TestObserveFollowStopsOnFatalError(t *testing.T) {
	j := testutil.NewJournal(2)
	j.LowWater = 2
	h, _ := obsOnline(t, j)
	out := &obsBuffer{}
	select {
	case code := <-obsStart(h, context.Background(), out, "observe", "--follow", "--interval", "10ms", "--after", j.Cursor(1)):
		if code != ExitError {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow kept polling an expired cursor")
	}
	mustContain(t, out.String(), "code: cursor_expired", "Resnapshot")
}

func TestObserveFollowOfflineStops(t *testing.T) {
	h, _ := obsOffline(t, testutil.NewJournal(1))
	ctx, cancel := context.WithCancel(context.Background())
	out := &obsBuffer{}
	done := obsStart(h, ctx, out, "observe", "--follow", "--json", "--interval", "10ms")
	obsWaitFor(t, "first change", func() bool { return strings.Contains(out.String(), `"type":"change"`) })
	cancel()
	if code := <-done; code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	mustContain(t, out.String(), `"transport":"query"`, `"type":"stopped"`)
}

func TestObserveSanitizesWasyncStrings(t *testing.T) {
	j := testutil.NewJournal(0)
	j.Add("message.created", "evil\x1b]0;pwned\x07‮@g.us", "x\r\nhelp: run rm -rf")
	h, _ := obsOnline(t, j)
	for _, args := range [][]string{{"observe"}, {"observe", "--json"}} {
		out, code := h.run(t, args...)
		if code != ExitOK {
			t.Fatal(out)
		}
		if strings.ContainsAny(out, "\x1b\x07\r‮") || strings.Contains(out, `\u001b`) || strings.Contains(out, `\r`) {
			t.Fatalf("%v: control characters leaked: %q", args, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "help: run") {
				t.Fatalf("injected line: %q", out)
			}
		}
	}
}

func TestObserveNeedsFeaturesAndCredential(t *testing.T) {
	srv := testutil.NewAPI(t)
	v := testutil.FakeVersion()
	v.Features = []string{"chats", "messages"}
	srv.Version = v
	h := newHarness(t)
	h.env["WASYNC_API_ADDR"] = srv.Addr()
	h.env["WASYNC_AXI_TOKEN"] = "wsa_x"
	out, code := h.run(t, "observe")
	if code != ExitError {
		t.Fatal(out)
	}
	mustContain(t, out, "code: wasync_too_old", "changes,checkpoints", installhint.UpgradeCore())

	h2, srv2 := obsOnline(t, testutil.NewJournal(1))
	delete(h2.env, "WASYNC_AXI_TOKEN")
	out, code = h2.run(t, "observe")
	if code != ExitError {
		t.Fatal(out)
	}
	mustContain(t, out, "code: credential_missing", "--scope read.chats,read.messages,observe")
	if len(srv2.Requests()) != 1 {
		t.Fatalf("requests without a credential: %v", srv2.Requests())
	}

	h3 := newHarness(t) // no API, no wasync
	out, code = h3.run(t, "observe")
	if code != ExitError {
		t.Fatal(out)
	}
	mustContain(t, out, "code: wasync_not_found")
}

type observeOutputFunc func([]byte) (int, error)

func (f observeOutputFunc) Write(p []byte) (int, error) { return f(p) }

func TestObserveOutputFailureDoesNotCommit(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		for _, capacity := range []int{0, 40} {
			h, srv := obsOnline(t, testutil.NewJournal(2))
			remaining := capacity
			out := observeOutputFunc(func(p []byte) (int, error) {
				n := min(remaining, len(p))
				remaining -= n
				if n < len(p) {
					return n, errors.New("output full")
				}
				return n, nil
			})
			a := New(out)
			a.Getenv = func(k string) string { return h.env[k] }
			args := []string{"observe", "--consumer", "replica", "--commit"}
			if jsonMode {
				args = append(args, "--json")
			}
			if code := a.Run(context.Background(), args); code != ExitError {
				t.Fatalf("json=%v capacity=%d: exit %d", jsonMode, capacity, code)
			}
			if obsPuts(srv) != 0 || obsCheckpoint(srv, "replica") != -1 {
				t.Fatalf("json=%v capacity=%d: undelivered page committed", jsonMode, capacity)
			}
			r := obsJSON(t, h, "observe", "--consumer", "replica")
			if !reflect.DeepEqual(obsSeqs(r), []int{1, 2}) {
				t.Fatalf("changes lost: %+v", r)
			}
		}
	}
}

func TestObserveEmitsPageBeforeCommit(t *testing.T) {
	h, srv := obsOnline(t, testutil.NewJournal(2))
	var out bytes.Buffer
	writer := observeOutputFunc(func(p []byte) (int, error) {
		if out.Len() == 0 && obsPuts(srv) != 0 {
			t.Error("checkpoint moved before page output")
		}
		return out.Write(p)
	})
	a := New(writer)
	a.Getenv = func(k string) string { return h.env[k] }
	if code := a.Run(context.Background(), []string{"observe", "--consumer", "replica", "--commit", "--json"}); code != ExitOK {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	dec := json.NewDecoder(&out)
	var page, receipt obsResult
	if dec.Decode(&page) != nil || len(page.Changes) != 2 || page.Committed != nil {
		t.Fatalf("invalid page: %+v", page)
	}
	if dec.Decode(&receipt) != nil || receipt.Committed["cursor"] != page.NextCursor || len(receipt.Changes) != 0 {
		t.Fatalf("invalid receipt: %+v", receipt)
	}
	if err := dec.Decode(&receipt); err != io.EOF || obsCheckpoint(srv, "replica") != 2 {
		t.Fatalf("invalid completion: %v", err)
	}
}
