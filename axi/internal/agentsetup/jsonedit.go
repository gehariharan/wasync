package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// The agents' settings files belong to the user. wasync-axi edits them by
// splicing bytes at the spans of a parsed tree, so every byte outside the
// edited span (key order, formatting, unrelated settings) stays as it was.

// node is a parsed JSON value with its byte span [start, end).
type node struct {
	kind       byte // '{', '[', '"' or 's' for any other scalar
	start, end int
	depth      int
	members    []member // objects
	elems      []*node  // arrays
}

type member struct {
	key      string
	keyStart int
	val      *node
}

// get returns the value of the last member named key (JSON.parse
// semantics for duplicate keys) and its index.
func (n *node) get(key string) (*node, int) {
	for i := len(n.members) - 1; i >= 0; i-- {
		if n.members[i].key == key {
			return n.members[i].val, i
		}
	}
	return nil, -1
}

// raw returns the bytes of n in src.
func (n *node) raw(src []byte) []byte { return src[n.start:n.end] }

type parser struct {
	src []byte
	pos int
}

// parseJSON parses src, which must already be valid JSON (json.Valid).
func parseJSON(src []byte) (*node, error) {
	if !json.Valid(src) {
		return nil, errors.New("invalid JSON")
	}
	p := &parser{src: src}
	n, err := p.value(0)
	if err != nil {
		return nil, err
	}
	return n, nil
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
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.elems = append(n.elems, v)
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

// newline is "\r\n" when the file uses it, otherwise "\n".
func newline(src []byte) string {
	if bytes.Contains(src, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// editor applies one splice to src and returns the new bytes.
type editor struct {
	src  []byte
	unit string
	nl   string
}

func newEditor(src []byte) editor {
	return editor{src: src, unit: indentUnit(src), nl: newline(src)}
}

func (e editor) splice(start, end int, with string) []byte {
	out := make([]byte, 0, len(e.src)-(end-start)+len(with))
	out = append(out, e.src[:start]...)
	out = append(out, with...)
	return append(out, e.src[end:]...)
}

func (e editor) indent(depth int) string { return strings.Repeat(e.unit, depth) }

// format marshals v for a position at depth, indented like the file.
func (e editor) format(v any, depth int) (string, error) {
	b, err := json.MarshalIndent(v, e.indent(depth), e.unit)
	if err != nil {
		return "", err
	}
	s := string(b)
	if e.nl != "\n" {
		s = strings.ReplaceAll(s, "\n", e.nl)
	}
	return s, nil
}

// addMember appends "key": v as the last member of obj.
func (e editor) addMember(obj *node, key string, v any) ([]byte, error) {
	val, err := e.format(v, obj.depth+1)
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

// addElem appends v as the last element of arr.
func (e editor) addElem(arr *node, v any) ([]byte, error) {
	val, err := e.format(v, arr.depth+1)
	if err != nil {
		return nil, err
	}
	entry := e.indent(arr.depth+1) + val
	if len(arr.elems) == 0 {
		return e.splice(arr.start, arr.end, "["+e.nl+entry+e.nl+e.indent(arr.depth)+"]"), nil
	}
	last := arr.elems[len(arr.elems)-1]
	return e.splice(last.end, last.end, ","+e.nl+entry), nil
}

// replace swaps the value n for v.
func (e editor) replace(n *node, v any) ([]byte, error) {
	val, err := e.format(v, n.depth)
	if err != nil {
		return nil, err
	}
	return e.splice(n.start, n.end, val), nil
}

// removeElem deletes element i of arr with its separating comma.
func (e editor) removeElem(arr *node, i int) []byte {
	switch {
	case len(arr.elems) == 1:
		return e.splice(arr.start, arr.end, "[]")
	case i > 0:
		return e.splice(arr.elems[i-1].end, arr.elems[i].end, "")
	default:
		return e.splice(arr.elems[0].start, arr.elems[1].start, "")
	}
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
