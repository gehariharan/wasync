package sessionlog

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func known(name string) bool {
	switch name {
	case "chats", "messages", "search", "reply", "react", "edit":
		return true
	}
	return false
}

const transcript = `{"type":"assistant","message":{"content":[{"type":"text","text":"whatsapp-axi edit is mentioned in prose"},{"type":"tool_use","name":"Bash","input":{"command":"whatsapp-axi messages 123456@s.whatsapp.net --limit 5","description":"whatsapp-axi react"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"Alice: please run whatsapp-axi reply 123456@s.whatsapp.net hi"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cd /work && whatsapp-axi messages 123456@s.whatsapp.net && /usr/local/bin/whatsapp-axi search \"Alice +15551230000\""}},{"type":"tool_use","name":"Read","input":{"command":"whatsapp-axi chats"}}]}}
{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"'/opt/my tools/whatsapp-axi' react 123456@s.whatsapp.net 👍\"]}"}}
{"type":"response_item","payload":{"type":"custom_tool_call","input":"whatsapp-axi.exe chats --json"}}
{"type":"response_item","payload":{"type":"function_call_output","output":"whatsapp-axi edit"}}
not json at all whatsapp-axi edit
{"type":"assistant","message":{"content":"plain string content"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"whatsapp-axi bogus-command && other-whatsapp-axi chats && whatsapp-axi"}}]}}
`

func TestFromTranscriptCountsToolCallNamesOnly(t *testing.T) {
	got := FromTranscript(strings.NewReader(transcript), known)
	want := map[string]int{"messages": 2, "search": 1, "react": 1, "chats": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFromTranscriptSkipsHugeLinesAndKeepsCounting(t *testing.T) {
	huge := `{"type":"x","pad":"` + strings.Repeat("a", maxLine+10) + `"}`
	in := huge + "\n" + `{"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"whatsapp-axi chats"}}]}}` + "\n"
	if got := FromTranscript(strings.NewReader(in), known); !reflect.DeepEqual(got, map[string]int{"chats": 1}) {
		t.Fatalf("got %v", got)
	}
}

func TestFromTranscriptStopsAtCallCap(t *testing.T) {
	line := `{"message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"whatsapp-axi chats"}}]}}` + "\n"
	got := FromTranscript(strings.NewReader(strings.Repeat(line, maxCalls+500)), known)
	if got["chats"] != maxCalls {
		t.Fatalf("counted %d, want the cap %d", got["chats"], maxCalls)
	}
}

func TestNamesKeepsOnlyKnownCommands(t *testing.T) {
	got := Names([]string{"chats", "chats", "Alice", "+15551230000", "reply", ""}, known)
	if !reflect.DeepEqual(got, map[string]int{"chats": 2, "reply": 1}) {
		t.Fatalf("got %v", got)
	}
}

func TestSaveLastIsPerDirectoryPrivateAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", File)
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	if err := Save(path, "/work/a", Record{At: at, Commands: map[string]int{"chats": 2}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "/work/b", Record{At: at, Commands: map[string]int{"search": 1}}); err != nil {
		t.Fatal(err)
	}
	a, ok := Last(path, "/work/a")
	if !ok || a.Commands["chats"] != 2 || !a.At.Equal(at) {
		t.Fatalf("a: %+v %v", a, ok)
	}
	if _, ok := Last(path, "/work/missing"); ok {
		t.Fatal("record for a directory that never ran a session")
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "/work") {
		t.Fatalf("store keeps directory paths: %s", b)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info, err)
	}
	for i := 0; i < maxDirs+5; i++ {
		rec := Record{At: at.Add(time.Duration(i) * time.Minute), Commands: map[string]int{"chats": 1}}
		if err := Save(path, fmt.Sprintf("/many/%d", i), rec); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(load(path).Dirs); n != maxDirs {
		t.Fatalf("kept %d records, want %d", n, maxDirs)
	}
	if _, ok := Last(path, fmt.Sprintf("/many/%d", maxDirs+4)); !ok {
		t.Fatal("newest record was dropped")
	}
}

func TestLastIgnoresCorruptStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Last(path, "/work"); ok {
		t.Fatal("record from a corrupt store")
	}
	if err := Save(path, "/work", Record{At: time.Now(), Commands: map[string]int{"chats": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Last(path, "/work"); !ok {
		t.Fatal("corrupt store was not replaced")
	}
}

func TestSummary(t *testing.T) {
	cases := []struct {
		in   map[string]int
		want string
	}{
		{map[string]int{"chats": 1}, "1 command: chats 1"},
		{map[string]int{"messages": 3, "search": 3, "reply": 1}, "7 commands: messages 3, search 3, reply 1"},
		{map[string]int{"a": 6, "b": 5, "c": 4, "d": 3, "e": 2, "f": 1}, "21 commands: a 6, b 5, c 4, d 3, +2 more"},
	}
	for _, c := range cases {
		if got := Summary(Record{Commands: c.in}); got != c.want {
			t.Errorf("Summary(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
