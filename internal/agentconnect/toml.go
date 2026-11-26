package agentconnect

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Codex's config.toml is edited line by line: wasync removes the tables of
// its own entry ([mcp_servers.whatsapp] and any [mcp_servers.whatsapp.*])
// and appends its table at the end. Every other line keeps its bytes. The
// result is decoded again and must equal the old file with only that entry
// changed; anything else (the entry written inline or with dotted keys, a
// table header inside a multi-line string) is refused rather than guessed.

var tomlHeader = regexp.MustCompile(`^\s*(\[\[?)([^\[\]]+)(\]\]?)\s*(?:#.*)?$`)

// tomlKey splits a table header's key into its parts, or returns nil when
// it is not a key wasync understands.
func tomlKey(s string) []string {
	var parts []string
	var cur strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"' && c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			i++
			cur.WriteByte(s[i])
			continue
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == 0 && c == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if quote != 0 {
		return nil
	}
	parts = append(parts, strings.TrimSpace(cur.String()))
	for i, p := range parts {
		switch {
		case len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"':
			u, err := strconv.Unquote(p)
			if err != nil {
				return nil
			}
			parts[i] = u
		case len(p) >= 2 && p[0] == '\'' && p[len(p)-1] == '\'':
			parts[i] = p[1 : len(p)-1]
		case p == "":
			return nil
		}
	}
	return parts
}

// isEntryHeader reports whether a header names wasync's entry or a table under it.
func isEntryHeader(line string) (header, entry bool) {
	m := tomlHeader.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil || (m[1] == "[" && m[3] != "]") || (m[1] == "[[" && m[3] != "]]") {
		return false, false
	}
	key := tomlKey(m[2])
	return true, len(key) >= 2 && key[0] == "mcp_servers" && key[1] == ServerName
}

func blankOrComment(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// cutTOMLEntry removes the entry's tables and returns the rest of the file
// and the text it took out.
func cutTOMLEntry(content string) (rest, removed string) {
	lines := strings.SplitAfter(content, "\n")
	var keep, cut []string
	for i := 0; i < len(lines); {
		header, entry := isEntryHeader(lines[i])
		if !header || !entry {
			keep = append(keep, lines[i])
			i++
			continue
		}
		end := i + 1
		for end < len(lines) {
			if h, _ := isEntryHeader(lines[end]); h {
				break
			}
			end++
		}
		// Comments and blank lines just above the next table belong to it.
		last := end
		for last > i+1 && blankOrComment(lines[last-1]) {
			last--
		}
		if end == len(lines) {
			// At the end of the file: take the trailing blank lines too, and
			// the blank lines that separated this table from the one before.
			for last < end && strings.TrimSpace(lines[last]) == "" {
				last++
			}
			for len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" && last == end {
				keep = keep[:len(keep)-1]
			}
		}
		cut = append(cut, lines[i:last]...)
		keep = append(keep, lines[last:end]...)
		i = end
	}
	return strings.Join(keep, ""), strings.Join(cut, "")
}

// appendTOML adds block at the end of content, after a blank line.
func appendTOML(content, block, nl string) string {
	if content == "" {
		return block
	}
	if !strings.HasSuffix(content, "\n") {
		content += nl
	}
	if strings.TrimSpace(content) != "" && !strings.HasSuffix(content, "\n\n") && !strings.HasSuffix(content, "\n\r\n") {
		content += nl
	}
	return content + block
}

// tomlEntry renders e as wasync's table.
func tomlEntry(e Entry, nl string) string {
	var b strings.Builder
	b.WriteString("[mcp_servers." + ServerName + "]" + nl)
	b.WriteString("# Added by wasync (Connect an agent). Disconnect it in wasync to remove it." + nl)
	b.WriteString("command = " + tomlString(e.Command) + nl)
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		args[i] = tomlString(a)
	}
	b.WriteString("args = [" + strings.Join(args, ", ") + "]" + nl)
	if len(e.Env) > 0 {
		var env []string
		for _, k := range e.envKeys() {
			env = append(env, k+" = "+tomlString(e.Env[k]))
		}
		b.WriteString("env = { " + strings.Join(env, ", ") + " }" + nl)
	}
	return b.String()
}

// tomlString is s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func decodeTOML(content string) (map[string]any, error) {
	m := map[string]any{}
	if _, err := toml.Decode(content, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// splitEntry removes wasync's entry from the servers table of m and returns
// what it held. An empty servers table is dropped too, so a file that had
// none compares equal to one where only wasync's entry was added.
func splitEntry(m map[string]any, servers string) any {
	s, ok := m[servers].(map[string]any)
	if !ok {
		return nil
	}
	v := s[ServerName]
	delete(s, ServerName)
	if len(s) == 0 {
		delete(m, servers)
	}
	return v
}

func newline(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// editTOML returns content with wasync's entry replaced by block ("" only
// removes it) and the text it took out. It refuses an edit it cannot make
// exactly.
func editTOML(content, block string) (out, removed string, err error) {
	before, err := decodeTOML(content)
	if err != nil {
		return "", "", fmt.Errorf("it is not valid TOML: %w", err)
	}
	rest, removed := cutTOMLEntry(content)
	if block != "" {
		out = appendTOML(rest, block, newline(content))
	} else {
		out = rest
	}
	after, err := decodeTOML(out)
	if err != nil {
		return "", "", errors.New("wasync could not edit it exactly")
	}
	splitEntry(before, "mcp_servers")
	got := splitEntry(after, "mcp_servers")
	if !reflect.DeepEqual(before, after) {
		return "", "", errors.New("its " + ServerName + " server is written in a form wasync does not edit")
	}
	if block != "" {
		want, err := decodeTOML(block)
		if err != nil {
			return "", "", err
		}
		if !reflect.DeepEqual(splitEntry(want, "mcp_servers"), got) {
			return "", "", errors.New("wasync could not edit it exactly")
		}
	} else if got != nil {
		return "", "", errors.New("its " + ServerName + " server is written in a form wasync does not edit")
	}
	return out, removed, nil
}

// tomlCurrent returns the entry the file now has, or nil.
func tomlCurrent(content string) (any, error) {
	m, err := decodeTOML(content)
	if err != nil {
		return nil, err
	}
	return splitEntry(m, "mcp_servers"), nil
}

// tomlTable reports whether the file has a top-level table path, such as
// marketplaces.wasync.
func tomlTable(content string, path ...string) bool {
	m, err := decodeTOML(content)
	if err != nil {
		return false
	}
	var cur any = m
	for _, p := range path {
		t, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = t[p]; !ok {
			return false
		}
	}
	return true
}
