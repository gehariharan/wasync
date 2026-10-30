package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type call struct{ args []string }

type harness struct {
	t      *testing.T
	calls  []call
	reply  func(args []string) (string, int)
	starts int
}

// session feeds the lines to a fresh server and returns every JSON line it wrote.
func (h *harness) session(lines ...string) []map[string]any {
	h.t.Helper()
	var out bytes.Buffer
	s := &Server{
		In: strings.NewReader(strings.Join(lines, "\n") + "\n"), Out: &out, Version: "9.9.9",
		Run: func(_ context.Context, args []string) (string, int) {
			h.calls = append(h.calls, call{args})
			if h.reply != nil {
				return h.reply(args)
			}
			return "ok", 0
		},
		StartSetup: func(context.Context) (string, error) { h.starts++; return "opened", nil },
	}
	if err := s.Serve(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	var msgs []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			h.t.Fatalf("stdout carried something that is not JSON-RPC: %q", l)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func rpc(id int, method string, params any) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return string(b)
}

func callTool(id int, name string, args map[string]any) string {
	return rpc(id, "tools/call", map[string]any{"name": name, "arguments": args})
}

func result(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	r, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", m)
	}
	return r
}

func text(t *testing.T, m map[string]any) (string, bool) {
	t.Helper()
	r := result(t, m)
	content := r["content"].([]any)[0].(map[string]any)
	return content["text"].(string), r["isError"].(bool)
}

func TestInitializeNegotiatesAndAnnouncesOnlyTools(t *testing.T) {
	h := &harness{t: t}
	msgs := h.session(
		rpc(1, "initialize", map[string]any{"protocolVersion": "2025-03-26"}),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		rpc(2, "initialize", map[string]any{"protocolVersion": "1999-01-01"}),
		rpc(3, "ping", nil),
	)
	if len(msgs) != 3 {
		t.Fatalf("a notification must get no reply; got %d messages", len(msgs))
	}
	first := result(t, msgs[0])
	if first["protocolVersion"] != "2025-03-26" {
		t.Fatalf("did not echo a supported version: %v", first)
	}
	caps := first["capabilities"].(map[string]any)
	if len(caps) != 1 || caps["tools"] == nil {
		t.Fatalf("capabilities: %v", caps)
	}
	if info := first["serverInfo"].(map[string]any); info["name"] != "whatsapp-axi" || info["version"] != "9.9.9" {
		t.Fatalf("serverInfo %v", info)
	}
	if result(t, msgs[1])["protocolVersion"] != "2025-06-18" {
		t.Fatal("an unknown version should be answered with the newest supported one")
	}
}

func TestProtocolErrors(t *testing.T) {
	h := &harness{t: t}
	msgs := h.session(`{not json`, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, rpc(2, "resources/list", nil), callTool(3, "no_such_tool", nil), rpc(4, "tools/call", map[string]any{}))
	want := []float64{-32700, -32600, -32601, -32602, -32602}
	if len(msgs) != len(want) {
		t.Fatalf("got %d replies", len(msgs))
	}
	for i, m := range msgs {
		if code := m["error"].(map[string]any)["code"].(float64); code != want[i] {
			t.Errorf("reply %d: code %v, want %v", i, code, want[i])
		}
	}
	if len(h.calls) != 0 {
		t.Fatal("a protocol error ran a command")
	}
}

// The menu is the security contract: nothing here approves, grants, revokes,
// changes the access mode or reveals a secret.
func TestToolMenuHasNoWayToApproveOrGrant(t *testing.T) {
	h := &harness{t: t}
	msgs := h.session(rpc(1, "tools/list", nil))
	list := result(t, msgs[0])["tools"].([]any)
	var names []string
	readOnly := map[string]bool{}
	for _, raw := range list {
		tl := raw.(map[string]any)
		name := tl["name"].(string)
		names = append(names, name)
		ann := tl["annotations"].(map[string]any)
		readOnly[name] = ann["readOnlyHint"].(bool)
		schema := tl["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s: schema %v", name, schema)
		}
		if tl["description"].(string) == "" || ann["title"].(string) == "" {
			t.Errorf("%s: missing description or title", name)
		}
	}
	sort.Strings(names)
	want := []string{"whatsapp_access_status", "whatsapp_changes", "whatsapp_doctor", "whatsapp_edit", "whatsapp_list_chats", "whatsapp_message", "whatsapp_react", "whatsapp_read_messages", "whatsapp_reply", "whatsapp_request_access", "whatsapp_search", "whatsapp_start_setup", "whatsapp_status", "whatsapp_thread"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tool menu changed:\n got %v\nwant %v", names, want)
	}
	for _, name := range []string{"whatsapp_status", "whatsapp_doctor", "whatsapp_list_chats", "whatsapp_read_messages", "whatsapp_search", "whatsapp_thread", "whatsapp_message", "whatsapp_changes", "whatsapp_access_status"} {
		if !readOnly[name] {
			t.Errorf("%s should be marked read-only", name)
		}
	}
	for _, name := range []string{"whatsapp_reply", "whatsapp_react", "whatsapp_edit", "whatsapp_request_access", "whatsapp_start_setup"} {
		if readOnly[name] {
			t.Errorf("%s changes something and must not be marked read-only", name)
		}
	}
}

func TestToolsRunTheMatchingCommandLine(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want []string
	}{
		{"whatsapp_status", nil, []string{"--brief"}},
		{"whatsapp_list_chats", map[string]any{"match": "fam", "limit": 5}, []string{"chats", "--match", "fam", "--limit", "5"}},
		{"whatsapp_read_messages", map[string]any{"chat": "Family", "limit": 10, "cursor": "abc"}, []string{"messages", "--limit", "10", "--cursor", "abc", "--", "Family"}},
		{"whatsapp_search", map[string]any{"text": "--json --help", "chat": "Family", "context": 2}, []string{"search", "--chat", "Family", "--context", "2", "--", "--json --help"}},
		{"whatsapp_thread", map[string]any{"ref": "m1_x", "before": 3}, []string{"thread", "--before", "3", "--", "m1_x"}},
		{"whatsapp_message", map[string]any{"ref": "m1_x", "full": true}, []string{"message", "--full", "--", "m1_x"}},
		{"whatsapp_changes", map[string]any{"consumer": "codex", "limit": 50}, []string{"observe", "--consumer", "codex", "--limit", "50"}},
		{"whatsapp_reply", map[string]any{"ref": "m1_x", "text": "--text sneaky"}, []string{"reply", "--text", "--text sneaky", "--", "m1_x"}},
		{"whatsapp_react", map[string]any{"ref": "m1_x", "emoji": "👍"}, []string{"react", "--", "m1_x", "👍"}},
		{"whatsapp_react", map[string]any{"ref": "m1_x", "remove": true}, []string{"react", "--remove", "--", "m1_x"}},
		{"whatsapp_edit", map[string]any{"ref": "m1_x", "text": "fixed"}, []string{"edit", "--text", "fixed", "--", "m1_x"}},
		{"whatsapp_request_access", map[string]any{"match": "Mum", "send": true}, []string{"access", "request", "--match", "Mum", "--send"}},
		{"whatsapp_request_access", map[string]any{"chat": "1@g.us"}, []string{"access", "request", "--chat", "1@g.us"}},
		{"whatsapp_access_status", map[string]any{"request_id": strings.Repeat("a", 32)}, []string{"access", "status", strings.Repeat("a", 32)}},
	}
	for _, c := range cases {
		h := &harness{t: t}
		msgs := h.session(callTool(1, c.tool, c.args))
		if got, isErr := text(t, msgs[0]); isErr || got != "ok" {
			t.Errorf("%s: %q error=%v", c.tool, got, isErr)
			continue
		}
		if len(h.calls) != 1 || !reflect.DeepEqual(h.calls[0].args, c.want) {
			t.Errorf("%s ran %q, want %q", c.tool, h.calls, c.want)
		}
	}
}

func TestBadArgumentsNeverReachACommand(t *testing.T) {
	long := strings.Repeat("x", 16001)
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"whatsapp_read_messages", nil},
		{"whatsapp_read_messages", map[string]any{"chat": 5}},
		{"whatsapp_read_messages", map[string]any{"chat": "a", "limit": 999}},
		{"whatsapp_read_messages", map[string]any{"chat": "a", "limit": 1.5}},
		{"whatsapp_read_messages", map[string]any{"chat": "a", "extra": "x"}},
		{"whatsapp_status", map[string]any{"approve": true}},
		{"whatsapp_reply", map[string]any{"ref": "m", "text": long}},
		{"whatsapp_reply", map[string]any{"ref": "m", "text": ""}},
		{"whatsapp_react", map[string]any{"ref": "m"}},
		{"whatsapp_react", map[string]any{"ref": "m", "emoji": "👍", "remove": true}},
		{"whatsapp_request_access", map[string]any{}},
		{"whatsapp_request_access", map[string]any{"chat": "a", "match": "b"}},
		{"whatsapp_request_access", map[string]any{"match": "a", "edit": true}},
		{"whatsapp_search", map[string]any{"text": "a\x00b"}},
	}
	for _, c := range cases {
		h := &harness{t: t}
		msgs := h.session(callTool(1, c.tool, c.args))
		got, isErr := text(t, msgs[0])
		if !isErr || !strings.HasPrefix(got, "invalid arguments") || len(h.calls) != 0 {
			t.Errorf("%s %v: %q error=%v calls=%d", c.tool, c.args, got, isErr, len(h.calls))
		}
	}
}

func TestACommandFailureIsAToolErrorAndLongOutputIsCut(t *testing.T) {
	h := &harness{t: t, reply: func(args []string) (string, int) { return "error: forbidden\nhelp: ask a human", 1 }}
	msgs := h.session(callTool(1, "whatsapp_list_chats", nil))
	if got, isErr := text(t, msgs[0]); !isErr || !strings.Contains(got, "forbidden") {
		t.Fatalf("%q %v", got, isErr)
	}
	h = &harness{t: t, reply: func([]string) (string, int) { return strings.Repeat("a", 300<<10), 0 }}
	msgs = h.session(callTool(1, "whatsapp_search", map[string]any{"text": "x"}))
	got, isErr := text(t, msgs[0])
	if isErr || len(got) > maxResult+200 || !strings.Contains(got, "output cut") {
		t.Fatalf("len=%d error=%v", len(got), isErr)
	}
}

func TestStartSetupRunsNoCommandAndTakesNoArguments(t *testing.T) {
	h := &harness{t: t}
	msgs := h.session(callTool(1, "whatsapp_start_setup", nil), callTool(2, "whatsapp_start_setup", map[string]any{"url": "x"}))
	if got, isErr := text(t, msgs[0]); isErr || got != "opened" {
		t.Fatalf("%q %v", got, isErr)
	}
	if h.starts != 1 || len(h.calls) != 0 {
		t.Fatalf("starts=%d commands=%d", h.starts, len(h.calls))
	}
	if got, isErr := text(t, msgs[1]); h.starts != 1 || !isErr || !strings.HasPrefix(got, "invalid arguments") {
		t.Fatalf("an argument to a tool that takes none must be refused: starts=%d %q", h.starts, got)
	}
}

func TestLauncherRunsOnlyWasyncSetupAndRespectsTheCooldown(t *testing.T) {
	var spawned [][]string
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	l := &Launcher{
		Bin: func() (string, error) { return "/opt/wasync", nil },
		Spawn: func(bin string, args []string) error {
			spawned = append(spawned, append([]string{bin}, args...))
			return nil
		},
		Now: func() time.Time { return now },
	}
	msg, err := l.Start(context.Background())
	if err != nil || !strings.Contains(msg, "scan the QR code") {
		t.Fatalf("%q %v", msg, err)
	}
	for _, secret := range []string{"http", "t=", "token", "wsa_"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("the reply may leak a link or credential: contains %q", secret)
		}
	}
	if !reflect.DeepEqual(spawned, [][]string{{"/opt/wasync", "setup"}}) {
		t.Fatalf("spawned %v", spawned)
	}
	now = now.Add(10 * time.Second)
	if msg, _ = l.Start(context.Background()); len(spawned) != 1 || !strings.Contains(msg, "a moment ago") {
		t.Fatalf("cooldown: %d spawns, %q", len(spawned), msg)
	}
	now = now.Add(cooldown)
	if _, err = l.Start(context.Background()); err != nil || len(spawned) != 2 {
		t.Fatalf("after the cooldown: %v %d", err, len(spawned))
	}
}

func TestLauncherReportsMissingWasyncAndSpawnFailures(t *testing.T) {
	l := &Launcher{Bin: func() (string, error) { return "", errors.New("wasync is not on PATH") }}
	if _, err := l.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Fatalf("%v", err)
	}
	l = &Launcher{Bin: func() (string, error) { return "/x", nil }, Spawn: func(string, []string) error { return errors.New("boom") }}
	if _, err := l.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("%v", err)
	}
	if !l.last.IsZero() {
		t.Fatal("a failed start must not begin the cooldown")
	}
}

// A slow tool must not stop the server answering ping, and output from
// concurrent writers must stay one JSON message per line.
func TestOutputStaysOneMessagePerLine(t *testing.T) {
	var mu sync.Mutex
	var out bytes.Buffer
	pr, pw := io.Pipe()
	s := &Server{In: pr, Out: writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return out.Write(p) }), Run: func(context.Context, []string) (string, int) { return "line one\nline two", 0 }}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	io.WriteString(pw, callTool(1, "whatsapp_status", nil)+"\n")
	io.WriteString(pw, rpc(2, "ping", nil)+"\n")
	pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("a tool's own line breaks leaked into the protocol stream: %q", out.String())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
