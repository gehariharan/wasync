package testutil

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ModeJournal makes the fake wasync serve `query version|changes|checkpoint`
// from the journal file named by EnvFakeJournal, appending one JSON line per
// call to EnvFakeJournalLog.
const ModeJournal = "journal"

// Environment variables of the journal mode.
const (
	EnvFakeJournal    = "WASYNC_AXI_FAKE_JOURNAL"     // JSON file holding the journal state
	EnvFakeJournalLog = "WASYNC_AXI_FAKE_JOURNAL_LOG" // call log, one JSON array per line
)

// JournalAccount is the account JID the fake journal belongs to.
const JournalAccount = "owner@lid"

// JournalChange is one fake change-journal entry; its sequence is its
// position in Journal.Changes plus one.
type JournalChange struct {
	Op        string `json:"op"`
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
}

// JournalCheckpoint is a stored consumer checkpoint.
type JournalCheckpoint struct {
	Epoch    string `json:"epoch"`
	Sequence int64  `json:"sequence"`
}

// Journal is a fake wasync change journal with wasync's cursor and
// checkpoint semantics. The fake API and the fake `wasync query` share it,
// so both transports answer identically. Methods are not locked; JournalAPI
// locks around them.
type Journal struct {
	Epoch       string                       `json:"epoch"`
	LowWater    int64                        `json:"low_water"` // entries at or below are pruned
	Changes     []JournalChange              `json:"changes"`
	Checkpoints map[string]JournalCheckpoint `json:"checkpoints"`
	Hidden      map[string]bool              `json:"hidden"` // chats an allowlisted API agent may not see
}

// NewJournal returns a journal with n message.created changes in chat
// one@g.us.
func NewJournal(n int) *Journal {
	j := &Journal{Epoch: "0123456789abcdef0123456789abcdef", Checkpoints: map[string]JournalCheckpoint{}, Hidden: map[string]bool{}}
	for i := 0; i < n; i++ {
		j.Add("message.created", "one@g.us", strconv.Itoa(i))
	}
	return j
}

// Add appends a change.
func (j *Journal) Add(op, chat, message string) {
	j.Changes = append(j.Changes, JournalChange{Op: op, ChatID: chat, MessageID: message})
}

// Head is the newest sequence.
func (j *Journal) Head() int64 { return int64(len(j.Changes)) }

// Cursor encodes a position in this journal.
func (j *Journal) Cursor(seq int64) string {
	return "c1_" + base64.RawURLEncoding.EncodeToString([]byte(JournalAccount+"\x00"+j.Epoch+"\x00"+strconv.FormatInt(seq, 10)))
}

// JournalError is a v1 error envelope with its HTTP status.
type JournalError struct {
	Status int
	Code   string
	Msg    string
	Help   string
}

func (e *JournalError) envelope(id string) map[string]any {
	return map[string]any{"request_id": id, "error_code": e.Code, "error": e.Msg, "help": e.Help}
}

const journalResnapshot = "Resnapshot: re-read the chats and messages you replicate, then follow changes again starting without a cursor."

func (j *Journal) decode(cursor string) (int64, *JournalError) {
	body, ok := strings.CutPrefix(cursor, "c1_")
	raw, err := base64.RawURLEncoding.DecodeString(body)
	parts := strings.Split(string(raw), "\x00")
	if !ok || err != nil || len(parts) != 3 {
		return 0, &JournalError{400, "invalid_cursor", "invalid change cursor: unknown format", "Pass a cursor returned by the changes feed (c1_...) unchanged."}
	}
	seq, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || seq < 0 {
		return 0, &JournalError{400, "invalid_cursor", "invalid change cursor: bad sequence", ""}
	}
	if parts[0] != JournalAccount || parts[1] != j.Epoch || seq < j.LowWater || seq > j.Head() {
		return 0, &JournalError{410, "cursor_expired", "change cursor is older than the journal retention", journalResnapshot}
	}
	return seq, nil
}

func (j *Journal) change(seq int64) map[string]any {
	c := j.Changes[seq-1]
	out := map[string]any{
		"sequence": seq, "cursor": j.Cursor(seq), "op": c.Op, "op_version": 1, "chat_id": c.ChatID, "message_id": c.MessageID,
		"committed_at": time.Date(2026, 10, 1, 0, 0, int(seq), 0, time.UTC).Format(time.RFC3339),
	}
	if strings.HasPrefix(c.Op, "message.") && c.MessageID != "" {
		out["ref"] = "m1_" + base64.RawURLEncoding.EncodeToString([]byte(JournalAccount+"\x00"+c.ChatID+"\x00"+c.MessageID))
	}
	return out
}

// Page returns the success envelope of one changes page, hiding chats in
// Hidden when scoped is true (the cursor still advances past them).
func (j *Journal) Page(id, after string, limit int, scoped bool) (map[string]any, *JournalError) {
	if limit < 1 || limit > 1000 {
		return nil, &JournalError{400, "invalid_argument", "limit must be between 1 and 1000", "use limit=1..1000 (default 100)"}
	}
	pos := j.LowWater
	if after != "" {
		seq, jerr := j.decode(after)
		if jerr != nil {
			return nil, jerr
		}
		pos = seq
	}
	changes := []map[string]any{}
	next := pos
	for seq := pos + 1; seq <= j.Head() && seq <= pos+int64(limit); seq++ {
		next = seq
		if scoped && j.Hidden[j.Changes[seq-1].ChatID] {
			continue
		}
		changes = append(changes, j.change(seq))
	}
	return map[string]any{
		"request_id": id, "data": map[string]any{"changes": changes},
		"page": map[string]any{"next_cursor": j.Cursor(next), "has_more": next < j.Head()},
	}, nil
}

func (j *Journal) checkpointData(consumer string) map[string]any {
	cp, ok := j.Checkpoints[consumer]
	if !ok {
		return map[string]any{"consumer": consumer, "cursor": nil, "updated_at": nil}
	}
	cursor := "c1_" + base64.RawURLEncoding.EncodeToString([]byte(JournalAccount+"\x00"+cp.Epoch+"\x00"+strconv.FormatInt(cp.Sequence, 10)))
	return map[string]any{"consumer": consumer, "cursor": cursor, "updated_at": "2026-10-02T00:00:00Z"}
}

func validConsumer(name string) *JournalError {
	ok := len(name) >= 1 && len(name) <= 64
	for _, r := range name {
		ok = ok && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
	}
	if !ok {
		return &JournalError{400, "invalid_argument", fmt.Sprintf("invalid consumer name %q", name), "Use a consumer name of 1-64 characters a-z, 0-9 and -."}
	}
	return nil
}

// Checkpoint returns the success envelope of a checkpoint read.
func (j *Journal) Checkpoint(id, consumer string) (map[string]any, *JournalError) {
	if jerr := validConsumer(consumer); jerr != nil {
		return nil, jerr
	}
	return map[string]any{"request_id": id, "data": j.checkpointData(consumer)}, nil
}

// Commit stores a checkpoint with wasync's rules: the cursor must be
// servable, and a checkpoint never moves backwards within an epoch.
func (j *Journal) Commit(id, consumer, cursor string) (map[string]any, *JournalError) {
	if jerr := validConsumer(consumer); jerr != nil {
		return nil, jerr
	}
	seq, jerr := j.decode(cursor)
	if jerr != nil {
		return nil, jerr
	}
	if cp, ok := j.Checkpoints[consumer]; ok && cp.Epoch == j.Epoch && seq < cp.Sequence {
		return nil, &JournalError{409, "invalid_argument", "checkpoint cannot move backwards", "Checkpoints only move forward; read the current one with GET /api/v1/checkpoints/" + consumer + " and commit a later cursor."}
	}
	j.Checkpoints[consumer] = JournalCheckpoint{Epoch: j.Epoch, Sequence: seq}
	return map[string]any{"request_id": id, "data": j.checkpointData(consumer)}, nil
}

// JournalAPI is a fake /api/v1 serving a Journal on 127.0.0.1.
type JournalAPI struct {
	*httptest.Server
	mu       sync.Mutex
	journal  *Journal
	requests []string // "METHOD URI"
	bodies   []string // PUT bodies
	// FailChanges makes the next N /changes requests fail with 503
	// archive_unavailable.
	FailChanges int
}

// NewJournalAPI starts the fake API and closes it when the test ends.
func NewJournalAPI(t testing.TB, j *Journal) *JournalAPI {
	t.Helper()
	a := &JournalAPI{journal: j}
	a.Server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.Close)
	return a
}

// Addr is the host:port of the server.
func (a *JournalAPI) Addr() string { return strings.TrimPrefix(a.URL, "http://") }

// Do runs f with the journal locked, to inspect or change it.
func (a *JournalAPI) Do(f func(j *Journal)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(a.journal)
}

// Requests returns "METHOD URI" per request, in order.
func (a *JournalAPI) Requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

// Bodies returns the PUT bodies, in order.
func (a *JournalAPI) Bodies() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.bodies...)
}

func (a *JournalAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, r.Method+" "+r.URL.RequestURI())
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	id := "r-" + strconv.Itoa(len(a.requests))
	var env map[string]any
	var jerr *JournalError
	switch {
	case path == "/version" && r.Method == http.MethodGet:
		env = map[string]any{"request_id": id, "data": FakeVersion()}
	case r.Header.Get("Authorization") == "":
		jerr = &JournalError{401, "unauthorized", "Authorization: Bearer wsa_... credential required", ""}
	case path == "/changes" && r.Method == http.MethodGet:
		if a.FailChanges > 0 {
			a.FailChanges--
			jerr = &JournalError{503, "archive_unavailable", "archive unavailable", "check the wasync owner log"}
			break
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, _ = strconv.Atoi(raw)
		}
		env, jerr = a.journal.Page(id, r.URL.Query().Get("after"), limit, true)
	case strings.HasPrefix(path, "/checkpoints/") && r.Method == http.MethodGet:
		env, jerr = a.journal.Checkpoint(id, strings.TrimPrefix(path, "/checkpoints/"))
	case strings.HasPrefix(path, "/checkpoints/") && r.Method == http.MethodPut:
		var body struct {
			Cursor string `json:"cursor"`
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		a.bodies = append(a.bodies, string(raw))
		if r.Header.Get("Content-Type") != "application/json" || json.Unmarshal(raw, &body) != nil || body.Cursor == "" {
			jerr = &JournalError{400, "invalid_argument", "invalid checkpoint body", ""}
			break
		}
		env, jerr = a.journal.Commit(id, strings.TrimPrefix(path, "/checkpoints/"), body.Cursor)
	default:
		jerr = &JournalError{404, "not_found", "no such endpoint", ""}
	}
	if jerr != nil {
		w.WriteHeader(jerr.Status)
		env = jerr.envelope(id)
	}
	_ = json.NewEncoder(w).Encode(env)
}

// FakeJournalWasync configures the test binary as a wasync serving j
// offline. It returns the binary and the call log (one JSON line per call).
func FakeJournalWasync(t testing.TB, j *Journal) (bin, log string) {
	t.Helper()
	bin, _ = FakeWasync(t, ModeJournal)
	dir := t.TempDir()
	log = dir + string(os.PathSeparator) + "calls.jsonl"
	t.Setenv(EnvFakeLog, "")
	t.Setenv(EnvFakeJournalLog, log)
	path := dir + string(os.PathSeparator) + "journal.json"
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvFakeJournal, path)
	return bin, log
}

// ReadJournalCalls returns the argument lists the fake journal wasync saw.
func ReadJournalCalls(t testing.TB, log string) [][]string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		out = append(out, args)
	}
	return out
}

// runFakeJournal is the fake `wasync [--data-dir D] query CMD ... --v1 --json`.
// It is read-only: it never writes the journal file.
func runFakeJournal() {
	args := os.Args[1:]
	if log := os.Getenv(EnvFakeJournalLog); log != "" {
		b, _ := json.Marshal(args)
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	if len(args) >= 2 && args[0] == "--data-dir" {
		args = args[2:]
	}
	if len(args) < 4 || args[0] != "query" || args[len(args)-2] != "--v1" || args[len(args)-1] != "--json" {
		fmt.Fprintln(os.Stderr, "fake journal: bad arguments")
		os.Exit(2)
	}
	cmd, flags := args[1], map[string]string{}
	for _, arg := range args[2 : len(args)-2] {
		k, v, ok := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !ok || !strings.HasPrefix(arg, "--") {
			fmt.Fprintln(os.Stderr, "fake journal: expected --flag=value, got", arg)
			os.Exit(2)
		}
		flags[k] = v
	}
	var j Journal
	b, err := os.ReadFile(os.Getenv(EnvFakeJournal))
	if err != nil || json.Unmarshal(b, &j) != nil {
		writeEnv(map[string]any{"request_id": "q-j", "error_code": "archive_unavailable", "error": "no journal"})
		os.Exit(1)
	}
	var env map[string]any
	var jerr *JournalError
	switch cmd {
	case "version":
		env = map[string]any{"request_id": "q-v", "data": FakeVersion()}
	case "changes":
		limit := 100
		if raw, ok := flags["limit"]; ok {
			limit, _ = strconv.Atoi(raw)
		}
		env, jerr = j.Page("q-c", flags["after"], limit, false) // offline reads are unscoped
	case "checkpoint":
		env, jerr = j.Checkpoint("q-k", flags["consumer"])
	default:
		fmt.Fprintln(os.Stderr, "fake journal: unknown command", cmd)
		os.Exit(2)
	}
	if jerr != nil {
		writeEnv(jerr.envelope("q-e"))
		os.Exit(1)
	}
	writeEnv(env)
}
