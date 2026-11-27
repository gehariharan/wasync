package agentconnect

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
)

// The Claude apps' JSON files are edited by splicing bytes at the spans of
// a parsed tree, so every byte outside wasync's entry (key order,
// formatting, unrelated settings) stays as it was. This is the approach
// wasync-axi takes for agent hooks (axi/internal/agentsetup/jsonedit.go);
// the two modules cannot share code.

// node is a parsed JSON value with its byte span [start, end).
type node struct {
	kind       byte // '{', '[', '"' or 's' for any other scalar
	start, end int
	depth      int
	members    []member
}

type member struct {
	key      string
	keyStart int
	val      *node
}

// find returns the indexes of the members named key.
func (n *node) find(key string) []int {
	var out []int
	for i, m := range n.members {
		if m.key == key {
			out = append(out, i)
		}
	}
	return out
}

type parser struct {
	src []byte
	pos int
}

// parseJSON parses src, which must be valid JSON.
func parseJSON(src []byte) (*node, error) {
	if !json.Valid(src) {
		return nil, errors.New("it is not valid JSON")
	}
	p := &parser{src: src}
	return p.value(0)
}

func (p *parser) ws() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value(depth int) (*node, error) {
	p.ws()
	if p.pos >= len(p.src) {
		return nil, errors.New("unexpected end of JSON")
	}
	n := &node{start: p.pos, depth: depth}
	switch c := p.src[p.pos]; c {
	case '{':
		n.kind = '{'
		p.pos++
		p.ws()
		if p.src[p.pos] == '}' {
			p.pos++
			break
		}
		for {
			p.ws()
			keyStart := p.pos
			if err := p.str(); err != nil {
				return nil, err
			}
			var key string
			if err := json.Unmarshal(p.src[keyStart:p.pos], &key); err != nil {
				return nil, err
			}
			p.ws()
			p.pos++ // ':'
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.members = append(n.members, member{key: key, keyStart: keyStart, val: v})
			p.ws()
			if p.src[p.pos] == ',' {
				p.pos++
				continue
			}
			p.pos++ // '}'
			break
		}
	case '[':
		n.kind = '['
		p.pos++
		p.ws()
		if p.src[p.pos] == ']' {
			p.pos++
			break
		}
		for {
			if _, err := p.value(depth + 1); err != nil {
				return nil, err
			}
			p.ws()
			if p.src[p.pos] == ',' {
				p.pos++
				continue
			}
			p.pos++ // ']'
			break
		}
	case '"':
		n.kind = '"'
		if err := p.str(); err != nil {
			return nil, err
		}
	default:
		n.kind = 's'
		for p.pos < len(p.src) && !strings.ContainsRune(",}] \t\r\n", rune(p.src[p.pos])) {
			p.pos++
		}
	}
	n.end = p.pos
	return n, nil
}

// str advances over a string literal starting at p.pos.
func (p *parser) str() error {
	if p.pos >= len(p.src) || p.src[p.pos] != '"' {
		return errors.New("expected a string")
	}
	for i := p.pos + 1; i < len(p.src); i++ {
		switch p.src[i] {
		case '\\':
			i++
		case '"':
			p.pos = i + 1
			return nil
		}
	}
	return errors.New("unterminated string")
}

// indentUnit guesses the file's indentation from its first indented line,
// defaulting to two spaces.
func indentUnit(src []byte) string {
	for i := 0; i < len(src); i++ {
		if src[i] != '\n' {
			continue
		}
		j := i + 1
		for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
			j++
		}
		if j > i+1 && j < len(src) && src[j] != '\n' && src[j] != '\r' {
			return string(src[i+1 : j])
		}
	}
	return "  "
}

type editor struct {
	src  []byte
	unit string
	nl   string
}

func newEditor(src []byte) editor {
	return editor{src: src, unit: indentUnit(src), nl: newline(string(src))}
}

func (e editor) splice(start, end int, with string) []byte {
	out := make([]byte, 0, len(e.src)-(end-start)+len(with))
	out = append(out, e.src[:start]...)
	out = append(out, with...)
	return append(out, e.src[end:]...)
}

func (e editor) indent(depth int) string { return strings.Repeat(e.unit, depth) }

// reindent moves raw JSON (as formatted by MarshalIndent with no prefix)
// to depth, in the file's own indentation and line endings.
func (e editor) reindent(raw []byte, depth int) (string, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, e.indent(depth), e.unit); err != nil {
		return "", err
	}
	s := buf.String()
	if e.nl != "\n" {
		s = strings.ReplaceAll(s, "\n", e.nl)
	}
	return s, nil
}

// addMember appends "key": raw as the last member of obj.
func (e editor) addMember(obj *node, key string, raw []byte) ([]byte, error) {
	val, err := e.reindent(raw, obj.depth+1)
	if err != nil {
		return nil, err
	}
	entry := e.indent(obj.depth+1) + strconv.Quote(key) + ": " + val
	if len(obj.members) == 0 {
		return e.splice(obj.start, obj.end, "{"+e.nl+entry+e.nl+e.indent(obj.depth)+"}"), nil
	}
	last := obj.members[len(obj.members)-1].val
	return e.splice(last.end, last.end, ","+e.nl+entry), nil
}

// removeMember deletes member i of obj with its separating comma.
func (e editor) removeMember(obj *node, i int) []byte {
	switch {
	case len(obj.members) == 1:
		return e.splice(obj.start, obj.end, "{}")
	case i > 0:
		return e.splice(obj.members[i-1].val.end, obj.members[i].val.end, "")
	default:
		return e.splice(obj.members[0].keyStart, obj.members[1].keyStart, "")
	}
}

// jsonServers finds the root object and its mcpServers object (nil when absent).
func jsonServers(src []byte) (root, servers *node, err error) {
	root, err = parseJSON(src)
	if err != nil {
		return nil, nil, err
	}
	if root.kind != '{' {
		return nil, nil, errors.New("it is not a JSON object")
	}
	idx := root.find("mcpServers")
	switch {
	case len(idx) > 1:
		return nil, nil, errors.New(`it has "mcpServers" more than once`)
	case len(idx) == 0:
		return root, nil, nil
	}
	servers = root.members[idx[0]].val
	if servers.kind != '{' {
		return nil, nil, errors.New(`its "mcpServers" is not an object`)
	}
	if len(servers.find(ServerName)) > 1 {
		return nil, nil, errors.New(`it has the "` + ServerName + `" server more than once`)
	}
	return root, servers, nil
}

// editJSON returns src with wasync's entry set to raw (nil only removes it)
// and the raw value it replaced or removed ("" when there was none).
func editJSON(src, raw []byte) (out []byte, removed string, err error) {
	if len(bytes.TrimSpace(src)) == 0 {
		if raw == nil {
			return src, "", nil
		}
		src = []byte("{}\n")
	}
	root, servers, err := jsonServers(src)
	if err != nil {
		return nil, "", err
	}
	e := newEditor(src)
	if servers != nil {
		if idx := servers.find(ServerName); len(idx) == 1 {
			v := servers.members[idx[0]].val
			removed = string(src[v.start:v.end])
			src = e.removeMember(servers, idx[0])
			if root, servers, err = jsonServers(src); err != nil {
				return nil, "", err
			}
			e = newEditor(src)
		}
	}
	if raw != nil {
		if servers == nil {
			wrapped, err := json.Marshal(map[string]json.RawMessage{ServerName: raw})
			if err != nil {
				return nil, "", err
			}
			src, err = e.addMember(root, "mcpServers", wrapped)
			if err != nil {
				return nil, "", err
			}
		} else if src, err = e.addMember(servers, ServerName, raw); err != nil {
			return nil, "", err
		}
	}
	return src, removed, nil
}

// checkJSON verifies that out equals before with only wasync's entry set to
// want (nil: absent).
func checkJSON(before, out []byte, want []byte) error {
	var b, a map[string]any
	if len(bytes.TrimSpace(before)) == 0 {
		before = []byte("{}")
	}
	if err := json.Unmarshal(before, &b); err != nil {
		return err
	}
	if err := json.Unmarshal(out, &a); err != nil {
		return errors.New("wasync could not edit it exactly")
	}
	splitEntry(b, "mcpServers")
	got := splitEntry(a, "mcpServers")
	if !reflect.DeepEqual(b, a) {
		return errors.New("wasync could not edit it exactly")
	}
	var w any
	if want != nil {
		if err := json.Unmarshal(want, &w); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(w, got) {
		return errors.New("wasync could not edit it exactly")
	}
	return nil
}

// jsonCurrent returns the entry the file now has, or nil.
func jsonCurrent(src []byte) (any, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(src, &m); err != nil {
		return nil, err
	}
	return splitEntry(m, "mcpServers"), nil
}
