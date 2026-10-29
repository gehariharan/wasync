package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Synthetic transcript: one tool call per command, with a made-up chat id,
// name and phone number in the arguments that must never be stored.
const sessionTranscript = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"whatsapp-axi messages 999000111@s.whatsapp.net --limit 3"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"Dana Example: call +15550001111, then run whatsapp-axi edit now"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"whatsapp-axi search \"Dana Example +15550001111\" && whatsapp-axi reply 999000111@s.whatsapp.net \"hello Dana\""}}]}}
`

func sessionHarness(t *testing.T, payload string) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	h.cwd = filepath.Join(t.TempDir(), "project")
	h.stdin = strings.NewReader(payload)
	return h, filepath.Join(h.env["WHATSAPP_AXI_CONFIG_DIR"], "last-session.json")
}

func TestSessionEndRecordsNamesAndCountsOnly(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeFile(t, transcript, sessionTranscript)
	h, store := sessionHarness(t, `{"session_id":"s1","transcript_path":"`+filepath.ToSlash(transcript)+`","cwd":"/ignored-by-test","hook_event_name":"SessionEnd","reason":"other"}`)
	out, code := h.run(t, "session-end")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	mustContain(t, out, `session: "recorded 3 commands: messages 1, reply 1, search 1"`)
	stored := readFile(t, store)
	for _, leak := range []string{"Dana", "999000111", "s.whatsapp.net", "15550001111", "hello", transcript, "/ignored-by-test"} {
		if strings.Contains(out+stored, leak) {
			t.Errorf("session record or output leaks %q:\n%s\n%s", leak, out, stored)
		}
	}
	if info, err := os.Stat(store); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("store mode: %v %v", info, err)
	}
}

func TestSessionEndFromOpenCodeCommandNames(t *testing.T) {
	h, _ := sessionHarness(t, `{"cwd":"/proj","commands":["chats","chats","not-a-command","Dana Example","setup","session-end"]}`)
	out, code := h.run(t, "session-end", "--json")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	mustContain(t, out, `"recorded":true`, `"chats":2`)
	for _, leak := range []string{"Dana", "not-a-command", "setup", "session-end"} {
		if strings.Contains(out, leak) {
			t.Errorf("output keeps %q: %s", leak, out)
		}
	}
}

func TestSessionEndWithoutCommandsRecordsNothing(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeFile(t, transcript, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`+"\n")
	h, store := sessionHarness(t, `{"transcript_path":"`+filepath.ToSlash(transcript)+`"}`)
	out, code := h.run(t, "session-end")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	mustContain(t, out, "no whatsapp-axi commands to record")
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("store written for an empty session: %v", err)
	}
}

func TestSessionEndRefusesTerminalAndBadPayload(t *testing.T) {
	h := newHarness(t)
	out, code := h.run(t, "session-end")
	if code != ExitUsage {
		t.Fatalf("terminal stdin: exit %d: %s", code, out)
	}
	mustContain(t, out, "reads the hook's JSON from stdin", "whatsapp-axi setup hooks")

	h.stdin = strings.NewReader("this is not json")
	if out, code := h.run(t, "session-end"); code != ExitUsage {
		t.Fatalf("bad payload: exit %d: %s", code, out)
	}
	h.stdin = strings.NewReader("{}")
	if out, code := h.run(t, "session-end", "extra"); code != ExitUsage {
		t.Fatalf("extra argument: exit %d: %s", code, out)
	}
}

func TestBriefShowsLastSessionForThisDirectoryOnly(t *testing.T) {
	h := newHarness(t)
	h.cwd = t.TempDir()
	h.stdin = strings.NewReader(`{"cwd":"` + filepath.ToSlash(h.cwd) + `","commands":["messages","messages","react"]}`)
	if _, code := h.run(t, "session-end"); code != ExitOK {
		t.Fatal("session-end failed")
	}
	h.stdin = nil

	brief, _ := h.run(t, "--brief")
	mustContain(t, brief, "last_session: ", ", 3 commands: messages 2, react 1")
	if lines := strings.Count(brief, "\n"); lines > 12 {
		t.Errorf("brief grew to %d lines:\n%s", lines, brief)
	}
	asJSON, _ := h.run(t, "--brief", "--json")
	mustContain(t, asJSON, `"last_session":{"at":"`, `"messages":2`)

	h.cwd = t.TempDir() // another project: nothing to show
	other, _ := h.run(t, "--brief")
	if strings.Contains(other, "last_session") {
		t.Errorf("brief shows another directory's session:\n%s", other)
	}
	full, _ := h.run(t)
	if strings.Contains(full, "last_session") {
		t.Errorf("full home view changed:\n%s", full)
	}
}
