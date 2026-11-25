package bench

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/axi/internal/cli"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/testutil"
	"github.com/gehariharan/wasync/axi/internal/transport"
)

func TestMain(m *testing.M) {
	testutil.MaybeRunFakeWasync()
	os.Exit(m.Run())
}

// Corpus sizes: the report uses the full corpus; -short a small one.
const (
	fullChats, fullPerChat   = 40, 150
	shortChats, shortPerChat = 8, 30
)

// rawCall is one `wasync query <cmd> --v1 --json` an agent would run.
type rawCall struct {
	path string
	q    url.Values
}

// task is a typical agent task done both ways: wasync-axi commands, or
// the raw wasync reads that give the agent the same information.
type task struct {
	name, goal string
	axi        [][]string
	raw        []rawCall
}

func tasks(c corpus) []task {
	target, changed := c.find(targetChat), c.busiestChangedChat()
	limit := fmt.Sprint(pageLimit)
	return []task{
		{
			name: "orient", goal: "check that WhatsApp data is reachable and see the chats",
			axi: [][]string{{}},
			raw: []rawCall{{"/version", nil}, {"/chats", nil}},
		},
		{
			name: "list_chats", goal: "list every chat with its id, kind, size and last activity",
			axi: [][]string{{"chats", "--fields", "id,kind,messages,last_message_at"}},
			raw: []rawCall{{"/chats", nil}},
		},
		{
			name: "read_chat", goal: "find the chat named \"" + targetChat + "\" and read its last 20 messages",
			axi: [][]string{{"messages", targetChat}},
			raw: []rawCall{{"/chats", url.Values{"match": {targetChat}}}, {"/chats/" + target.ID + "/messages", url.Values{"limit": {limit}}}},
		},
		{
			name: "search_context", goal: "find the 20 newest messages mentioning \"" + searchTerm + "\" with 2 messages of context each",
			axi: [][]string{{"search", searchTerm, "--context", fmt.Sprint(contextSize)}},
			raw: []rawCall{{"/messages/search", url.Values{"q": {searchTerm}, "limit": {limit}, "context": {fmt.Sprint(contextSize)}}}},
		},
		{
			name: "follow_changes", goal: "read the next 100 changes after a cursor, then the newest messages of the busiest changed chat",
			axi: [][]string{{"observe", "--after", c.cursor(changeLimit)}, {"messages", changed.ID}},
			raw: []rawCall{
				{"/changes", url.Values{"after": {c.cursor(changeLimit)}, "limit": {fmt.Sprint(changeLimit)}}},
				{"/chats/" + changed.ID + "/messages", url.Values{"limit": {limit}}},
			},
		},
	}
}

// approxTokens estimates LLM tokens without a model tokenizer: a run of
// letters counts one token per 4 characters (rounded up), a run of digits
// one per 3, and every other non-space character one. On English prose it
// lands near the usual 4-characters-per-token rule; it charges JSON
// punctuation and opaque ids (base64 refs, cursors, JIDs) more, as BPE
// tokenizers do. Both sides are measured with the same function.
func approxTokens(s string) int {
	n := 0
	for _, m := range tokenPattern.FindAllString(s, -1) {
		switch c := m[0]; {
		case c >= '0' && c <= '9':
			n += (len(m) + 2) / 3
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
			n += (len(m) + 3) / 4
		default:
			n++
		}
	}
	return n
}

var tokenPattern = regexp.MustCompile(`[A-Za-z]+|[0-9]+|[^\sA-Za-z0-9]`)

type side struct {
	Calls  int // commands the agent runs
	Bytes  int
	Tokens int
	Output string
}

type result struct {
	Task      task
	AXI, Raw  side
	AXIWasync int // wasync subprocess calls wasync-axi made for the task
}

type env struct {
	bin, log, dir, addr string
}

// setup writes the corpus fixtures and points the fake wasync at them.
func setup(tb testing.TB, c corpus) env {
	tb.Helper()
	dir := tb.TempDir()
	b, err := json.Marshal(c.fixtures())
	if err != nil {
		tb.Fatal(err)
	}
	path := filepath.Join(dir, "fixtures.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		tb.Fatal(err)
	}
	bin, log := testutil.FakeFixtures(tb, path)
	return env{bin: bin, log: log, dir: dir, addr: testutil.ClosedAddr(tb)}
}

// runAXI runs one wasync-axi command in process against the fake wasync,
// with no API listening on e.addr, so it reads through `wasync query`.
func (e env) runAXI(tb testing.TB, args []string) string {
	tb.Helper()
	vars := map[string]string{
		"WASYNC_BIN": e.bin, "WASYNC_API_ADDR": e.addr,
		"WASYNC_AXI_CONFIG_DIR": e.dir, "WASYNC_AXI_AGENT_HOME": e.dir,
	}
	var out bytes.Buffer
	a := cli.New(&out)
	a.Getenv = func(k string) string { return vars[k] }
	a.HomeDir = func() (string, error) { return e.dir, nil }
	a.Executable = func() (string, error) { return "wasync-axi", nil }
	a.ProbeTimeout = 2 * time.Second
	if code := a.Run(context.Background(), args); code != cli.ExitOK {
		tb.Fatalf("wasync-axi %v: exit %d\n%s", args, code, out.String())
	}
	return out.String()
}

// runRaw runs the fake wasync exactly as an agent would run wasync.
func (e env) runRaw(tb testing.TB, call rawCall) string {
	tb.Helper()
	args, err := transport.QueryArgs("", call.path, call.q)
	if err != nil {
		tb.Fatal(err)
	}
	out, err := exec.Command(e.bin, args...).Output()
	if err != nil {
		tb.Fatalf("wasync %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (e env) logLines(tb testing.TB) int {
	b, err := os.ReadFile(e.log)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		tb.Fatal(err)
	}
	return bytes.Count(b, []byte("\n"))
}

func measure(tb testing.TB, c corpus) ([]result, side) {
	tb.Helper()
	e := setup(tb, c)
	var results []result
	for _, t := range tasks(c) {
		r := result{Task: t}
		before := e.logLines(tb)
		for _, args := range t.axi {
			r.AXI.Output += e.runAXI(tb, args)
			r.AXI.Calls++
		}
		r.AXIWasync = e.logLines(tb) - before
		for _, call := range t.raw {
			r.Raw.Output += e.runRaw(tb, call)
			r.Raw.Calls++
		}
		for _, s := range []*side{&r.AXI, &r.Raw} {
			s.Bytes, s.Tokens = len(s.Output), approxTokens(s.Output)
		}
		results = append(results, r)
	}
	brief := side{Output: e.runAXI(tb, []string{"--brief"}), Calls: 1}
	brief.Bytes, brief.Tokens = len(brief.Output), approxTokens(brief.Output)
	if dir := os.Getenv("WASYNC_AXI_BENCH_DUMP"); dir != "" {
		for _, r := range results {
			_ = os.WriteFile(filepath.Join(dir, r.Task.name+".axi.txt"), []byte(r.AXI.Output), 0o600)
			_ = os.WriteFile(filepath.Join(dir, r.Task.name+".raw.json"), []byte(r.Raw.Output), 0o600)
		}
		_ = os.WriteFile(filepath.Join(dir, "brief.axi.txt"), []byte(brief.Output), 0o600)
	}
	return results, brief
}

func saved(axi, raw int) string {
	if raw == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*(1-float64(axi)/float64(raw)))
}

func report(results []result, brief side, chats, perChat int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Corpus: %d chats x %d messages (synthetic). Transport: offline `wasync query` (fake).\n\n", chats, perChat)
	b.WriteString("| Task | Agent calls (axi / raw) | wasync calls by axi | Bytes (axi / raw) | Bytes saved | ~Tokens (axi / raw) | Tokens saved |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	var ta, tr side
	for _, r := range results {
		fmt.Fprintf(&b, "| %s | %d / %d | %d | %d / %d | %s | %d / %d | %s |\n", r.Task.name, r.AXI.Calls, r.Raw.Calls, r.AXIWasync,
			r.AXI.Bytes, r.Raw.Bytes, saved(r.AXI.Bytes, r.Raw.Bytes), r.AXI.Tokens, r.Raw.Tokens, saved(r.AXI.Tokens, r.Raw.Tokens))
		ta.Calls, tr.Calls = ta.Calls+r.AXI.Calls, tr.Calls+r.Raw.Calls
		ta.Bytes, tr.Bytes = ta.Bytes+r.AXI.Bytes, tr.Bytes+r.Raw.Bytes
		ta.Tokens, tr.Tokens = ta.Tokens+r.AXI.Tokens, tr.Tokens+r.Raw.Tokens
	}
	fmt.Fprintf(&b, "| **total** | %d / %d | | %d / %d | %s | %d / %d | %s |\n", ta.Calls, tr.Calls,
		ta.Bytes, tr.Bytes, saved(ta.Bytes, tr.Bytes), ta.Tokens, tr.Tokens, saved(ta.Tokens, tr.Tokens))
	fmt.Fprintf(&b, "\nSession hook (`wasync-axi --brief`): %d bytes, ~%d tokens per session.\n", brief.Bytes, brief.Tokens)
	return b.String()
}

func TestBenchmarkReport(t *testing.T) {
	chats, perChat := fullChats, fullPerChat
	if testing.Short() {
		chats, perChat = shortChats, shortPerChat
	}
	results, brief := measure(t, newCorpus(chats, perChat))
	for _, r := range results {
		if r.AXI.Calls > r.Raw.Calls {
			t.Errorf("%s: wasync-axi needs more calls (%d) than raw wasync (%d)", r.Task.name, r.AXI.Calls, r.Raw.Calls)
		}
		if r.AXI.Tokens >= r.Raw.Tokens || r.AXI.Bytes >= r.Raw.Bytes {
			t.Errorf("%s: wasync-axi output is not smaller: %d/%d bytes, %d/%d tokens", r.Task.name, r.AXI.Bytes, r.Raw.Bytes, r.AXI.Tokens, r.Raw.Tokens)
		}
		if strings.Contains(r.AXI.Output, "error:") || r.AXI.Bytes == 0 || r.Raw.Bytes == 0 {
			t.Errorf("%s: empty or failed output", r.Task.name)
		}
		if r.Task.name == "list_chats" {
			var raw struct {
				Data contract.ChatList `json:"data"`
			}
			if err := json.Unmarshal([]byte(r.Raw.Output), &raw); err != nil {
				t.Fatal(err)
			}
			header := fmt.Sprintf("chats[%d]{id,kind,messages,last_message_at}:\n", chats)
			_, table, found := strings.Cut(r.AXI.Output, header)
			if !found || len(raw.Data.Items) != chats {
				t.Fatal("list_chats did not return all required fields and chats")
			}
			rows := strings.Split(table, "\n")
			for i, chat := range raw.Data.Items {
				if i >= len(rows) {
					t.Fatal("list_chats omitted rows")
				}
				got, err := csv.NewReader(strings.NewReader(strings.TrimSpace(rows[i]))).Read()
				want := []string{chat.ID, chat.Kind, fmt.Sprint(chat.MessageCount), chat.LastMessageAt}
				if err != nil || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
					t.Fatalf("list_chats row %d: %v (%v), want %v", i, got, err, want)
				}
			}
		}
	}
	// The read_chat answer is the same messages both ways.
	first := results[2]
	if !strings.Contains(first.AXI.Output, "messages[20]") || strings.Count(first.Raw.Output, `"text_content"`) != 20 {
		t.Errorf("read_chat did not return 20 messages both ways:\n%s", first.AXI.Output)
	}
	if brief.Tokens > 200 {
		t.Errorf("session hook output is %d tokens; keep it small", brief.Tokens)
	}
	t.Log("\n" + report(results, brief, chats, perChat))
}

func TestApproxTokens(t *testing.T) {
	for s, want := range map[string]int{
		"":                       0,
		"hello world":            4,
		`{"a":1}`:                7,
		"m1_bWVAbGlk":            5,
		"2026-10-19T08:00:00Z":   13,
		"Please send the slides": 6,
	} {
		if got := approxTokens(s); got != want {
			t.Errorf("%q: %d, want %d", s, got, want)
		}
	}
}

// BenchmarkTasks times each task through wasync-axi (process-local CLI,
// subprocess fake wasync) and reports the output sizes as metrics.
func BenchmarkTasks(b *testing.B) {
	c := newCorpus(fullChats, fullPerChat)
	e := setup(b, c)
	for _, t := range tasks(c) {
		b.Run(t.name, func(b *testing.B) {
			var out string
			for b.Loop() {
				out = ""
				for _, args := range t.axi {
					out += e.runAXI(b, args)
				}
			}
			var raw string
			for _, call := range t.raw {
				raw += e.runRaw(b, call)
			}
			b.ReportMetric(float64(len(out)), "axi-bytes")
			b.ReportMetric(float64(len(raw)), "raw-bytes")
			b.ReportMetric(float64(approxTokens(out)), "axi-tokens")
			b.ReportMetric(float64(approxTokens(raw)), "raw-tokens")
		})
	}
}
