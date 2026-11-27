package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/msgref"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
)

const queryUsage = `Usage: wasync [--data-dir PATH] query COMMAND

  groups [--enabled] [--json]
  chats [--enabled] [--json]
  messages --chat CHAT_ID [--since RFC3339] [--until RFC3339]
           [--sender SENDER_ID] [--limit 50] [--cursor CURSOR] [--json]
  search "phrase" [--chat CHAT_ID] [--since RFC3339] [--until RFC3339]
           [--sender SENDER_ID] [--limit 50] [--cursor CURSOR] [--json]
  status [--json]
  version [--json]
  message --ref REF [--json]
  thread --ref REF [--before 5] [--after 5] [--json]
  changes [--after CURSOR] [--limit 100] [--json]
  checkpoint --consumer NAME [--json]
  doctor [--json]

All results are JSON. Queries read the existing archive without connecting to
WhatsApp, requiring an API, or stopping the TUI. Dates are inclusive --since and
exclusive --until; results are newest first. Pass next_cursor to --cursor with
the same filters for the next page. An empty items array is a successful result.
Status describes persisted observations; current network connectivity is unknown.
Global flags precede query. Exit codes: 0 success, 1 error, 130 cancellation.

--v1 wraps groups, chats, messages, search, status and message results in the
versioned v1 envelope; version, thread, changes, checkpoint and doctor always
use it. v1 only: chats --match QUERY resolves a chat by JID or name, and
search --context N (0 to 5) adds messages around each hit. message and thread
take stable m1_ references; changes reads the change journal (c1_ cursors) and
checkpoint reads a consumer checkpoint (commit through the owner's API).
Use wasync help query COMMAND for details.
`

// queryCommands lists every query subcommand runQuery dispatches; each must
// appear in queryUsage and the command catalog (see help_test.go).
var queryCommands = []string{"groups", "chats", "messages", "search", "status", "message", "thread", "version", "doctor", "changes", "checkpoint"}

// queryMessageUsage is the full reference for `query message`, appended to its
// catalog help.
const queryMessageUsage = `Usage: wasync [--data-dir PATH] query message --ref REF [--json]

Prints one archived message by its stable, account-scoped reference (m1_...).
References survive restarts, purge and re-capture; local row ids do not.
Expired content is never returned; revoked messages return their tombstone.
With --v1 the data is the v1 message shape (legacy fields plus ref and chat_kind).
`

// queryThreadUsage documents `query thread`, which always emits the v1 envelope.
const queryThreadUsage = `Usage: wasync [--data-dir PATH] query thread --ref REF [--before 5] [--after 5] [--json]

Prints the message REF as "anchor" with up to --before earlier and --after later
messages (0 to 20 each) from the same chat, oldest first, as a v1 envelope.
Expired content is never returned; revoked messages return their tombstone.
`

func (a *App) runQuery(ctx context.Context, cfg config.Config, args []string) (result error) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(a.Out, queryUsage)
		return err
	}
	command := args[0]
	if command == "version" {
		return a.runQueryVersion(ctx, cfg, args[1:])
	}
	if command == "doctor" {
		return a.runQueryDoctor(ctx, cfg, args[1:])
	}
	if command == "changes" || command == "checkpoint" {
		return a.runQueryJournal(ctx, cfg, command, args[1:])
	}
	// --v1 is detected before parsing so argument errors also use the envelope.
	v1 := slices.Contains(args[1:], "--v1") || slices.Contains(args[1:], "--v1=true") || command == "thread"
	defer func() { result = a.v1Failure(v1, result) }()
	if !slices.Contains(queryCommands, command) {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("unknown query command %q; use wasync query --help", command))
	}
	fs := a.flags("query " + command)
	fs.Usage = func() { _ = a.writeHelp(fs.Output(), "query "+command) }
	jsonOutput := fs.Bool("json", true, "JSON output (default; required for query commands)")
	fs.BoolVar(&v1, "v1", v1, "emit the versioned v1 envelope")
	var enabled bool
	var group, since, until, sender, cursor, match string
	limit, around, before, after := 50, 0, query.DefaultThread, query.DefaultThread
	var refFlag string
	if command == "message" || command == "thread" {
		fs.StringVar(&refFlag, "ref", "", "stable message reference (m1_...)")
	}
	if command == "thread" {
		fs.IntVar(&before, "before", before, "earlier messages, 0 to 20")
		fs.IntVar(&after, "after", after, "later messages, 0 to 20")
	}
	if command == "groups" || command == "chats" {
		fs.BoolVar(&enabled, "enabled", false, "only groups with capture enabled")
	}
	if command == "chats" {
		fs.StringVar(&match, "match", "", "v1 only: resolve a chat by JID or name")
	}
	if command == "search" {
		fs.IntVar(&around, "context", 0, "v1 only: messages before and after each hit, 0 to 5")
	}
	if command == "messages" || command == "search" {
		fs.StringVar(&group, "group", "", "WhatsApp group ID (legacy alias for --chat)")
		fs.StringVar(&group, "chat", "", "group or direct chat ID")
		fs.StringVar(&since, "since", "", "inclusive RFC3339 timestamp")
		fs.StringVar(&until, "until", "", "exclusive RFC3339 timestamp")
		fs.StringVar(&sender, "sender", "", "exact sender ID")
		fs.StringVar(&cursor, "cursor", "", "cursor from a previous result page")
		fs.IntVar(&limit, "limit", 50, "page size, 1 to 200")
	}
	rest := args[1:]
	search := ""
	// Accept both search "phrase" --flags and search --flags "phrase".
	if command == "search" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		search = rest[0]
		rest = rest[1:]
	}
	if err := a.parseFlags(fs, rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return queryErr(query.CodeInvalidArgument, err)
	}
	positional := fs.Args()
	if command == "search" && search == "" && len(positional) == 1 {
		search = positional[0]
		positional = nil
	}
	if len(positional) != 0 {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("unexpected arguments; use wasync query --help"))
	}
	if !*jsonOutput {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("query commands always return JSON; omit --json=false"))
	}
	v1 = v1 || command == "thread" // thread has no legacy shape
	var v1Only []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "match" || f.Name == "context" {
			v1Only = append(v1Only, "--"+f.Name)
		}
	})
	if len(v1Only) > 0 && !v1 {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("%s requires --v1", strings.Join(v1Only, " and ")))
	}
	if command == "chats" && slices.Contains(v1Only, "--match") && (strings.TrimSpace(match) == "" || len(match) > 256) {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("--match must contain 1 to 256 bytes"))
	}
	if around < 0 || around > query.MaxSearchContext {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("--context must be between 0 and %d", query.MaxSearchContext))
	}
	if before < 0 || before > query.MaxThreadWindow || after < 0 || after > query.MaxThreadWindow {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("--before and --after must be between 0 and %d", query.MaxThreadWindow))
	}
	if command == "search" && (strings.TrimSpace(search) == "" || len(search) > 1024) {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("search requires a phrase of 1 to 1024 bytes"))
	}
	if command == "messages" && group == "" {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("messages requires --chat CHAT_ID (or --group); list IDs with wasync query chats"))
	}
	var ref msgref.Ref
	if command == "message" || command == "thread" {
		if refFlag == "" {
			return queryErr(query.CodeInvalidArgument, fmt.Errorf("%s requires --ref REF", command))
		}
		var err error
		if ref, err = msgref.Decode(refFlag); err != nil {
			return queryErr(query.CodeInvalidArgument, err)
		}
	}
	if limit < 1 || limit > 200 {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("--limit must be between 1 and 200"))
	}
	q := storage.MessageQuery{Limit: limit, Search: strings.TrimSpace(search), Sender: sender}
	for name, entry := range map[string]struct {
		value  string
		target **time.Time
	}{"--since": {since, &q.From}, "--until": {until, &q.To}} {
		if entry.value != "" {
			t, err := time.Parse(time.RFC3339Nano, entry.value)
			if err != nil {
				return queryErr(query.CodeInvalidArgument, fmt.Errorf("%s must be RFC3339, for example 2026-10-01T00:00:00Z", name))
			}
			t = t.UTC()
			*entry.target = &t
		}
	}
	if q.From != nil && q.To != nil && !q.From.Before(*q.To) {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("--since must be before --until"))
	}
	store, err := storage.OpenReadOnly(ctx, filepath.Join(cfg.DataDir, "archive.db"))
	if err != nil {
		var schema *storage.SchemaError
		if errors.As(err, &schema) {
			return queryErr(query.CodeSchemaIncompatible, err)
		}
		return queryErr(query.CodeArchiveUnavailable, err)
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	account, err := store.Account(ctx)
	if err != nil {
		return fmt.Errorf("read archive account: %w", err)
	}
	if account == nil {
		return queryErr(query.CodeNoAccount, fmt.Errorf("no account in the archive; start wasync and pair first"))
	}
	if command == "message" || command == "thread" {
		m, err := store.MessageByRef(ctx, account.ID, account.JID, ref)
		if errors.Is(err, storage.ErrMessageNotFound) {
			return queryErr(query.CodeNotFound, fmt.Errorf("message is not in the current account's archive; it may have expired, been purged, or belong to another account"))
		}
		if err != nil {
			return fmt.Errorf("query message: %w", err)
		}
		if command == "thread" {
			thread, err := query.ThreadFor(ctx, store, account, m, before, after)
			if err != nil {
				return fmt.Errorf("query thread: %w", err)
			}
			return a.queryOutput(true, thread, nil)
		}
		if v1 {
			return a.queryOutput(true, query.FormatV1Message(account.JID, account.ID, m), nil)
		}
		return a.queryOutput(v1, query.MessageByRef{AccountID: account.ID, Ref: query.RefFor(account.JID, m), Item: query.FormatMessage(account.ID, m)}, nil)
	}
	if command == "groups" || command == "chats" || command == "status" {
		groups, err := store.GroupSummaries(ctx, account.ID)
		if err != nil {
			return err
		}
		if command == "chats" && v1 {
			list, ambiguous := query.V1Chats(account.ID, groups, enabled, match, nil)
			if ambiguous != nil {
				return &queryError{query.CodeChatAmbiguous, ambiguous, query.ChatAmbiguousHelp, ambiguous}
			}
			return a.queryOutput(true, list, nil)
		}
		if command == "groups" || command == "chats" {
			items := make([]query.Group, 0, len(groups))
			for _, g := range groups {
				if (command == "chats" || storage.ChatKind(g.ID) == "group") && (!enabled || g.Enabled) {
					items = append(items, query.FormatGroup(g))
				}
			}
			return a.queryOutput(v1, map[string]any{"account_id": account.ID, "items": items}, nil)
		}
		status, err := store.ArchiveStatus(ctx, account.ID)
		if err != nil {
			return err
		}
		status.ChatCount = len(groups)
		for _, g := range groups {
			if g.Enabled {
				status.EnabledChats++
			}
			if storage.ChatKind(g.ID) == "group" {
				status.GroupCount++
				if g.Enabled {
					status.EnabledGroups++
				}
			}
			status.MessageCount += g.MessageCount
		}
		return a.queryOutput(v1, status, nil)
	}
	if group != "" {
		groups, err := store.Groups(ctx, account.ID)
		if err != nil {
			return err
		}
		found := false
		for _, g := range groups {
			if g.ID == group {
				found = true
				break
			}
		}
		if !found {
			return queryErr(query.CodeNotFound, fmt.Errorf("group is not in the current account's archive; list IDs with wasync query groups"))
		}
	}
	scope := queryScope(account.ID, group, q)
	if v1 { // v1 and legacy cursors are not interchangeable.
		digest := sha256.Sum256([]byte("v1\x00" + scope))
		scope = hex.EncodeToString(digest[:])
	}
	if cursor != "" {
		q.Before, err = decodeQueryCursor(cursor, scope)
		if err != nil {
			return queryErr(query.CodeInvalidCursor, err)
		}
	}
	page, err := store.Messages(ctx, account.ID, group, q)
	if err != nil {
		return fmt.Errorf("query messages: %w", err)
	}
	next := ""
	if page.Next.ID != 0 {
		next = encodeQueryCursor(page.Next, scope)
	}
	if v1 {
		list, err := query.V1Messages(ctx, store, account, page.Items, around, nil)
		if err != nil {
			return fmt.Errorf("query context: %w", err)
		}
		total, err := store.CountMessages(ctx, account.ID, group, q, nil)
		if err != nil {
			return fmt.Errorf("query total: %w", err)
		}
		list.Total = &total
		return a.queryOutput(true, list, &query.V1Page{NextCursor: next, HasMore: next != ""})
	}
	items := make([]query.Message, 0, len(page.Items))
	for _, m := range page.Items {
		items = append(items, query.FormatMessage(account.ID, m))
	}
	return json.NewEncoder(a.Out).Encode(map[string]any{"account_id": account.ID, "items": items, "next_cursor": next})
}

// queryError carries a v1 error code; its message is the legacy error text.
type queryError struct {
	code    string
	err     error
	help    string
	details any
}

func (e *queryError) Error() string         { return e.err.Error() }
func (e *queryError) Unwrap() error         { return e.err }
func queryErr(code string, err error) error { return &queryError{code: code, err: err} }

// ReportedError marks a failure already written to stdout as a v1 envelope;
// callers exit with status 1 without printing it again.
type ReportedError struct{ Err error }

func (e *ReportedError) Error() string { return e.Err.Error() }
func (e *ReportedError) Unwrap() error { return e.Err }

func (a *App) queryOutput(v1 bool, data any, page *query.V1Page) error {
	if !v1 {
		return json.NewEncoder(a.Out).Encode(data)
	}
	return json.NewEncoder(a.Out).Encode(query.V1Response{RequestID: query.NewRequestID(), Data: data, Page: page})
}

func (a *App) v1Failure(v1 bool, err error) error {
	if !v1 || err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	e := query.V1Error{RequestID: query.NewRequestID(), ErrorCode: query.CodeInternal, Error: err.Error()}
	var coded *queryError
	if errors.As(err, &coded) {
		e.ErrorCode, e.Help, e.Details = coded.code, coded.help, coded.details
	}
	if writeErr := json.NewEncoder(a.Out).Encode(e); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return &ReportedError{err}
}

func (a *App) runQueryVersion(ctx context.Context, cfg config.Config, args []string) (result error) {
	defer func() { result = a.v1Failure(true, result) }()
	fs := a.flags("query version")
	jsonOutput := fs.Bool("json", true, "JSON output (default)")
	fs.Bool("v1", true, "v1 envelope (always)")
	if err := a.parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return queryErr(query.CodeInvalidArgument, err)
	}
	if fs.NArg() != 0 || !*jsonOutput {
		return queryErr(query.CodeInvalidArgument, fmt.Errorf("usage: wasync query version [--json]"))
	}
	info := query.Version()
	version, exists, err := storage.ArchiveSchemaVersion(ctx, filepath.Join(cfg.DataDir, "archive.db"))
	if err != nil {
		return queryErr(query.CodeArchiveUnavailable, err)
	}
	if exists {
		info.ArchiveSchemaVersion = &version
	}
	return a.queryOutput(true, info, nil)
}

type queryCursor struct {
	Version int            `json:"v"`
	Before  storage.Cursor `json:"before"`
	Scope   string         `json:"scope"`
}

func queryScope(account int64, group string, q storage.MessageQuery) string {
	data, _ := json.Marshal([]any{account, group, q.Search, q.Sender, q.From, q.To})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func encodeQueryCursor(before storage.Cursor, scope string) string {
	data, _ := json.Marshal(queryCursor{1, before, scope})
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeQueryCursor(value, scope string) (storage.Cursor, error) {
	var c queryCursor
	if len(value) > 1024 {
		return storage.Cursor{}, fmt.Errorf("invalid cursor or filters changed; start again without --cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Version != 1 || c.Before.ID <= 0 || c.Scope != scope {
		return storage.Cursor{}, fmt.Errorf("invalid cursor or filters changed; start again without --cursor")
	}
	return c.Before, nil
}
