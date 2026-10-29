// Package sessionlog is whatsapp-axi's session-end capture (AXI principle 7):
// one small record per working directory of which whatsapp-axi commands an
// agent session ran and how often. A record holds command names and counts
// only, never arguments, chat names, message text or phone numbers, so the
// next session's `--brief` can say what the last one did without carrying
// anything from the archive.
package sessionlog

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// File is the record store inside the config directory.
	File = "last-session.json"

	maxDirs       = 32       // records kept, oldest dropped first
	maxLine       = 1 << 20  // longest transcript line parsed; longer ones are skipped
	maxTranscript = 64 << 20 // bytes of transcript read
	maxCalls      = 10000    // command invocations counted per session
)

// Record is one session's summary for a directory.
type Record struct {
	At       time.Time      `json:"at"`
	Commands map[string]int `json:"commands"`
}

type store struct {
	Version int               `json:"version"`
	Dirs    map[string]Record `json:"dirs"`
}

// dirKey names a directory without storing its path.
func dirKey(dir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return hex.EncodeToString(sum[:8])
}

func load(path string) store {
	s := store{Version: 1, Dirs: map[string]Record{}}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &s) != nil || s.Dirs == nil {
		return store{Version: 1, Dirs: map[string]Record{}}
	}
	return s
}

// Save stores rec as dir's record in the store at path, keeping the newest
// maxDirs records. The file is written atomically with mode 0600.
func Save(path, dir string, rec Record) error {
	s := load(path)
	s.Dirs[dirKey(dir)] = rec
	for len(s.Dirs) > maxDirs {
		oldest := ""
		for k, r := range s.Dirs {
			if oldest == "" || r.At.Before(s.Dirs[oldest].At) {
				oldest = k
			}
		}
		delete(s.Dirs, oldest)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

// Last returns dir's record from the store at path, if there is one.
func Last(path, dir string) (Record, bool) {
	r, ok := load(path).Dirs[dirKey(dir)]
	if !ok || len(r.Commands) == 0 {
		return Record{}, false
	}
	return r, true
}

func writeAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".last-session-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}

// invocation finds `whatsapp-axi <word>` in a shell command: the program
// name, optionally quoted or with .exe, then the first argument. Only the
// word is kept; known() decides whether it is a real command name.
var invocation = regexp.MustCompile(`(?:^|[\s"'/\;&|(])whatsapp-axi(?:\.exe)?["']?\s+([a-z][a-z-]*)`)

// Count tallies whatsapp-axi invocations by command name in a shell command
// line, keeping only names known() accepts.
func Count(command string, known func(string) bool, into map[string]int) {
	for _, m := range invocation.FindAllStringSubmatch(command, -1) {
		if known(m[1]) {
			into[m[1]]++
		}
	}
}

// FromTranscript counts whatsapp-axi commands in an agent transcript (JSON
// lines). It looks only at the command a tool call ran, never at tool
// output or messages, and keeps only command names. Claude Code records a
// Bash tool_use block; Codex records a function, custom-tool or shell call.
// Lines it cannot read are skipped.
func FromTranscript(r io.Reader, known func(string) bool) map[string]int {
	counts := map[string]int{}
	br := bufio.NewReaderSize(io.LimitReader(r, maxTranscript), 64<<10)
	for {
		line, tooLong, err := readLine(br)
		if !tooLong && len(bytes.TrimSpace(line)) > 0 {
			for _, call := range toolCalls(line) {
				Count(call, known, counts)
			}
		}
		if total(counts) >= maxCalls || err != nil {
			return counts
		}
	}
}

// readLine returns the next line, or reports it as too long (draining it).
func readLine(br *bufio.Reader) (line []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return line, tooLong, err
		}
		if len(line)+len(chunk) > maxLine {
			tooLong = true
		}
		if !tooLong {
			line = append(line, chunk...)
		}
		if !isPrefix {
			return line, tooLong, nil
		}
	}
}

// toolCalls returns the command text of each tool call on one transcript
// line.
func toolCalls(line []byte) []string {
	var rec struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Payload struct {
			Type      string          `json:"type"`
			Arguments json.RawMessage `json:"arguments"`
			Input     json.RawMessage `json:"input"`
			Action    json.RawMessage `json:"action"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return nil
	}
	var out []string
	var blocks []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(rec.Message.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "tool_use" && (b.Name == "Bash" || b.Name == "PowerShell") {
				var in struct {
					Command string `json:"command"`
				}
				if json.Unmarshal(b.Input, &in) == nil {
					out = append(out, in.Command)
				}
			}
		}
	}
	switch rec.Payload.Type {
	case "function_call", "custom_tool_call", "local_shell_call":
		for _, raw := range []json.RawMessage{rec.Payload.Arguments, rec.Payload.Input, rec.Payload.Action} {
			if len(raw) > 0 {
				out = append(out, string(raw))
			}
		}
	}
	return out
}

func total(m map[string]int) int {
	n := 0
	for _, c := range m {
		n += c
	}
	return n
}

// Names filters names to those known() accepts and counts them, for
// integrations that report command names directly.
func Names(names []string, known func(string) bool) map[string]int {
	counts := map[string]int{}
	for _, n := range names {
		if known(n) && total(counts) < maxCalls {
			counts[n]++
		}
	}
	return counts
}

// Summary renders a record as "5 commands: messages 3, search 2", at most
// four names (most used first), with the rest folded into "+N more".
func Summary(r Record) string {
	type kv struct {
		k string
		n int
	}
	var all []kv
	sum := 0
	for k, n := range r.Commands {
		all = append(all, kv{k, n})
		sum += n
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].k < all[j].k
	})
	noun := " commands"
	if sum == 1 {
		noun = " command"
	}
	var parts []string
	for i, e := range all {
		if i == 4 {
			parts = append(parts, "+"+strconv.Itoa(len(all)-4)+" more")
			break
		}
		parts = append(parts, e.k+" "+strconv.Itoa(e.n))
	}
	return strconv.Itoa(sum) + noun + ": " + strings.Join(parts, ", ")
}
