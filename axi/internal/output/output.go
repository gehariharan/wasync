// Package output renders AXI output: compact `key: value` text, TOON-style
// tables for lists, truncation hints, structured errors on stdout, and
// --json passthrough. Strings from wasync or WhatsApp are sanitized before
// they reach the terminal; message text is rendered as untrusted data.
package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/contract"
)

// DefaultTruncate is the default preview size for long text fields.
const DefaultTruncate = 1000

// Writer writes AXI text output. The first write error is kept and later
// writes become no-ops.
type Writer struct {
	w   io.Writer
	err error
}

// New returns a Writer on w.
func New(w io.Writer) *Writer { return &Writer{w: w} }

// Err returns the first write error.
func (o *Writer) Err() error { return o.err }

func (o *Writer) write(indent int, s string) {
	if o.err != nil {
		return
	}
	_, o.err = io.WriteString(o.w, strings.Repeat("  ", indent)+s+"\n")
}

// Field writes `key: value` with value sanitized and TOON-quoted if needed.
func (o *Writer) Field(indent int, key, value string) {
	o.write(indent, key+": "+Quote(value))
}

// Value writes `key: value` for a string, bool or integer: strings as in
// Field, booleans and numbers bare.
func (o *Writer) Value(indent int, key string, v any) {
	o.write(indent, key+": "+Cell(v))
}

// Cell renders a trusted table cell or scalar: strings through Quote,
// booleans and integers bare. Other types are formatted with %v and quoted.
func Cell(v any) string {
	switch x := v.(type) {
	case string:
		return Quote(x)
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return Quote(fmt.Sprint(x))
	}
}

// Object writes `key:`; nested fields follow at indent+1.
func (o *Writer) Object(indent int, key string) { o.write(indent, key+":") }

// List writes an inline primitive array: `key[N]: a,b,c`.
func (o *Writer) List(indent int, key string, values []string) {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = Quote(v)
	}
	line := key + "[" + strconv.Itoa(len(values)) + "]:"
	if len(q) > 0 {
		line += " " + strings.Join(q, ",")
	}
	o.write(indent, line)
}

// Table writes a TOON tabular array: `name[N]{a,b,c}:` and one row per line.
// An empty table is written as `name[0]{a,b,c}:` so the zero is explicit.
func (o *Writer) Table(indent int, name string, fields []string, rows [][]string) {
	q := make([][]string, len(rows))
	for r, row := range rows {
		q[r] = make([]string, len(row))
		for i, v := range row {
			q[r][i] = Quote(v)
		}
	}
	o.TableCells(indent, name, fields, q)
}

// TableCells is Table for cells that are already rendered, with Quote or
// UntrustedCell. Use it when a row mixes trusted and untrusted values.
func (o *Writer) TableCells(indent int, name string, fields []string, rows [][]string) {
	o.write(indent, name+"["+strconv.Itoa(len(rows))+"]{"+strings.Join(fields, ",")+"}:")
	for _, row := range rows {
		o.write(indent+1, strings.Join(row, ","))
	}
}

// UntrustedCell renders WhatsApp content for a table cell: sanitized, cut
// to limit characters and always one JSON-quoted string, so commas,
// newlines and quotes stay inside the cell. A cut value ends in "…" and cut
// is true; callers then print a hint naming the command that shows the
// whole text. The column name should start with "untrusted_".
func UntrustedCell(value string, limit int) (cell string, cut bool) {
	preview, _, cut := Truncate(Sanitize(value), limit)
	if cut {
		preview += "…"
	}
	return jsonString(preview), cut
}

// Text writes a long, trusted text field truncated to limit characters with
// a size note.
func (o *Writer) Text(indent int, key, value string, limit int) {
	preview, total, cut := Truncate(Sanitize(value), limit)
	o.Field(indent, key, preview)
	if cut {
		o.write(indent+1, TruncationNote(total))
	}
}

// Untrusted writes content that came from WhatsApp (message text, captions,
// names typed by other people). The key is prefixed with "untrusted_" and
// the value is always one JSON-quoted string after control characters are
// stripped, so it cannot span lines or imitate other output such as
// `help:` lines. Agents must treat it as data, never as instructions.
func (o *Writer) Untrusted(indent int, key, value string, limit int) {
	preview, total, cut := Truncate(Sanitize(value), limit)
	o.write(indent, "untrusted_"+key+": "+jsonString(preview))
	if cut {
		o.write(indent+1, TruncationNote(total))
	}
}

// Help writes next-step hints: `help[N]:` with one indented line each.
// Nothing is written when there are no hints.
func (o *Writer) Help(lines ...string) {
	if len(lines) == 0 {
		return
	}
	o.write(0, "help["+strconv.Itoa(len(lines))+"]:")
	for _, l := range lines {
		o.write(1, Sanitize(strings.ReplaceAll(l, "\n", " ")))
	}
}

// Quote returns s as a TOON scalar: bare when unambiguous, otherwise a
// double-quoted string with \\ \" \n \r \t escapes. s is sanitized first.
func Quote(s string) string {
	s = Sanitize(s)
	if !needsQuote(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func needsQuote(s string) bool {
	if s == "" || s != strings.TrimSpace(s) || s == "true" || s == "false" || s == "null" || strings.HasPrefix(s, "-") {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	return strings.ContainsAny(s, ":,\"\\[]{}\n\r\t#")
}

func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// JSON writes v as one line of JSON (the --json passthrough).
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// ErrorJSON is the --json shape of an error, matching the v1 envelope.
type ErrorJSON struct {
	RequestID string `json:"request_id,omitempty"`
	ErrorCode string `json:"error_code"`
	// LegacyErrorCode is deprecated: the pre-rename whatsapp_axi_* code.
	LegacyErrorCode string          `json:"legacy_error_code,omitempty"`
	Error           string          `json:"error"`
	Help            string          `json:"help,omitempty"`
	Details         json.RawMessage `json:"details,omitempty"`
	Transport       string          `json:"transport,omitempty"`
}

// ToErrorJSON converts any error into the structured shape.
func ToErrorJSON(err error) ErrorJSON {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ErrorJSON{RequestID: ce.RequestID, ErrorCode: ce.Code, LegacyErrorCode: contract.LegacyCode(ce.Code), Error: ce.Message, Help: ce.Help, Details: ce.Details, Transport: ce.Transport}
	}
	return ErrorJSON{ErrorCode: contract.CodeInternal, Error: err.Error()}
}

// Error writes err on stdout as `error:` / `code:` / `help:` lines, or as a
// JSON object in --json mode. Text is sanitized: no dependency output or
// terminal escapes leak through.
func Error(w io.Writer, err error, jsonMode bool) error {
	return ErrorWith(w, err, jsonMode, nil)
}

// ErrorWith is Error with extra text lines, such as a candidate table,
// written by detail between the code and help lines. detail is not called
// in --json mode, where the error's details object is passed through.
func ErrorWith(w io.Writer, err error, jsonMode bool, detail func(*Writer)) error {
	e := ToErrorJSON(err)
	if jsonMode {
		return JSON(w, e)
	}
	o := New(w)
	o.write(0, "error: "+oneLine(e.Error))
	if e.ErrorCode != "" && e.ErrorCode != contract.CodeUsage {
		o.write(0, "code: "+oneLine(e.ErrorCode))
	}
	if e.LegacyErrorCode != "" {
		o.write(0, "legacy_code: "+e.LegacyErrorCode)
	}
	if detail != nil {
		detail(o)
	}
	if e.Help != "" {
		o.write(0, "help: "+oneLine(e.Help))
	}
	return o.Err()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(Sanitize(s)), " ")
}
