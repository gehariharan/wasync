package output

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Sanitize removes terminal control sequences from text that did not come
// from wasync-axi (WhatsApp messages, names, wasync strings): ANSI/VT
// escape sequences (CSI, OSC, DCS, ...), C0 controls other than newline and
// tab, DEL, C1 controls and bidirectional override/isolate characters.
// Invalid UTF-8 becomes U+FFFD.
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == 0x1b:
			i = skipEscape(s, i+size)
			continue
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			// drop C0 controls (incl. CR, BEL, BS) and DEL
		case r >= 0x80 && r <= 0x9f:
			// drop C1 controls (8-bit CSI/OSC introducers)
		case isBidiControl(r):
			// drop bidi overrides and isolates
		default:
			b.WriteRune(r) // RuneError for invalid bytes writes U+FFFD
		}
		i += size
	}
	return b.String()
}

// skipEscape returns the index just past the escape sequence whose ESC ends
// right before i.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameters and intermediates, then a final byte 0x40-0x7e
		for i++; i < len(s); i++ {
			if c := s[i]; c >= 0x40 && c <= 0x7e {
				return i + 1
			}
		}
		return i
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: until BEL or ST
		for i++; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return i
	default: // two-byte sequence (ESC c, ESC 7, ...) or charset designation
		if s[i] == '(' || s[i] == ')' || s[i] == '*' || s[i] == '+' || s[i] == '#' || s[i] == '%' {
			if i+1 < len(s) {
				return i + 2
			}
			return len(s)
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		return i + size
	}
}

func isBidiControl(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x061c
}

// TruncationNote is the line printed under truncated content.
func TruncationNote(total int) string {
	return "... (truncated, " + strconv.Itoa(total) + " chars total)"
}

// Truncate shortens s to at most limit characters (runes). It returns the
// preview, the total number of characters and whether s was truncated.
// limit <= 0 means no limit.
func Truncate(s string, limit int) (preview string, total int, truncated bool) {
	total = utf8.RuneCountInString(s)
	if limit <= 0 || total <= limit {
		return s, total, false
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i], total, true
		}
		n++
	}
	return s, total, false
}
