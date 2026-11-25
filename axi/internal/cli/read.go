package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/output"
	"github.com/gehariharan/wasync/axi/internal/transport"
)

// Read commands compose wasync v1 reads and format them. Matching, search,
// paging, scopes and windows are decided by wasync; wasync-axi only picks
// defaults for how much to print.

const (
	defaultPageLimit  = 20  // messages and search results per page
	defaultChatLimit  = 50  // chats printed by `chats` in text mode
	homeChatLimit     = 5   // recent chats in the home view
	listTextLimit     = 200 // characters of message text in lists
	offlineNote       = "offline read of the local archive; not scoped by an agent grant"
	fullTextHintFmt   = "Text over %d chars is cut and ends in …; run `wasync-axi message <ref> --full` for a whole message"
	threadHint        = "Run `wasync-axi thread <ref>` to read a message with its neighbours"
	ambiguityHelpText = "Several chats match; rerun with one candidate id, e.g. "
)

var (
	limitFlag  = flagSpec{name: "--limit", value: "N", description: "results per page (default 20); wasync enforces its own maximum"}
	cursorFlag = flagSpec{name: "--cursor", value: "CURSOR", description: "continue from the next_cursor of a previous page with the same filters"}
)

var chatsCommand = &command{
	name:        "chats",
	usage:       "wasync-axi chats [--match TEXT] [--limit N] [--fields LIST] [--json]",
	description: "List archived chats, or find one by JID or name",
	flags: []flagSpec{
		{name: "--match", value: "TEXT", description: "find chats by JID or name (wasync matches exact id, then exact name, name prefix, substring)"},
		{name: "--limit", value: "N", description: "chats to print (default 50, 0 prints all); --json always returns every chat"},
		listFieldsFlag(chatFields, defaultChatFields), jsonFlag,
	},
	examples: []string{"wasync-axi chats", "wasync-axi chats --match family", "wasync-axi chats --json"},
	run:      (*App).runChats,
}

var messagesCommand = &command{
	name:        "messages",
	usage:       "wasync-axi messages CHAT [--limit N] [--cursor CURSOR] [--fields LIST] [--json]",
	description: "Read a chat's messages, newest first; CHAT is a chat JID or name",
	flags:       []flagSpec{limitFlag, cursorFlag, listFieldsFlag(messageFields, defaultMessageFields), jsonFlag},
	minArgs:     1,
	maxArgs:     1,
	argsName:    "CHAT",
	examples:    []string{"wasync-axi messages 120363000000000000@g.us", "wasync-axi messages family --limit 50"},
	run:         (*App).runMessages,
}

var searchCommand = &command{
	name:        "search",
	usage:       "wasync-axi search TEXT [--chat CHAT] [--context N] [--limit N] [--cursor CURSOR] [--fields LIST] [--json]",
	description: "Search message text, newest first, optionally with the messages around each hit",
	flags: []flagSpec{
		{name: "--chat", value: "CHAT", description: "only this chat (JID or name)"},
		{name: "--context", value: "N", description: "also show N messages before and after each hit"},
		limitFlag, cursorFlag, listFieldsFlag(append([]string{"hit", "role", "chat"}, messageFields...), []string{"chat", "ref", "time", "untrusted_text"}), jsonFlag,
	},
	minArgs:  1,
	maxArgs:  1,
	argsName: "TEXT",
	examples: []string{`wasync-axi search "dinner plans"`, "wasync-axi search invoice --chat family --context 2", "wasync-axi search -- -5%"},
	run:      (*App).runSearch,
}

var threadCommand = &command{
	name:        "thread",
	usage:       "wasync-axi thread REF [--before N] [--after N] [--fields LIST] [--json]",
	description: "Show a message with the messages before and after it in its chat, oldest first",
	flags: []flagSpec{
		{name: "--before", value: "N", description: "earlier messages (wasync default 5)"},
		{name: "--after", value: "N", description: "later messages (wasync default 5)"},
		listFieldsFlag(append([]string{"role"}, messageFields...), []string{"role", "ref", "sender", "untrusted_text"}), jsonFlag,
	},
	minArgs:  1,
	maxArgs:  1,
	argsName: "REF",
	examples: []string{"wasync-axi thread m1_...", "wasync-axi thread m1_... --before 10 --after 2"},
	run:      (*App).runThread,
}

var messageCommand = &command{
	name:        "message",
	usage:       "wasync-axi message REF [--full] [--json]",
	description: "Show one message by its ref (m1_...)",
	flags: []flagSpec{
		{name: "--full", description: "print the whole text and media metadata instead of the first 1000 chars"},
		jsonFlag,
	},
	minArgs:  1,
	maxArgs:  1,
	argsName: "REF",
	examples: []string{"wasync-axi message m1_...", "wasync-axi message m1_... --full"},
	run:      (*App).runMessage,
}

// readTransport returns the transport a data command may use, or the error
// that explains why there is none.
func (d detection) readTransport() (transport.Transport, error) {
	if d.sel.Transport == nil {
		switch {
		case d.compatErr != nil:
			return nil, d.compatErr
		case d.state == StateNotInstalled:
			return nil, contract.Errorf(contract.CodeWasyncNotFound, transport.InstallHelp, "wasync is not running and is not installed")
		case d.sel.QueryErr != nil:
			return nil, d.sel.QueryErr
		default:
			return nil, contract.Errorf(contract.CodeOwnerOffline, "start wasync (TUI or `wasync serve`)", "wasync is not reachable")
		}
	}
	if d.compatErr != nil {
		return nil, d.compatErr
	}
	if d.sel.Name() == transport.NameAPI && d.credErr != nil {
		return nil, d.credErr
	}
	return d.sel.Transport, nil
}

func (a *App) connect(ctx context.Context, cmd string) (transport.Transport, error) {
	return a.detect(ctx, cmd).readTransport()
}

// get reads path and decodes the data into out (when not nil).
func get(ctx context.Context, t transport.Transport, path string, q url.Values, out any) (contract.Response, error) {
	var resp contract.Response
	if err := t.Get(ctx, path, q, &resp); err != nil {
		return resp, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Data, out); err != nil {
			return resp, &contract.Error{RequestID: resp.RequestID, Code: contract.CodeInvalidResponse, Message: "wasync response data has an unexpected shape: " + err.Error(), Transport: t.Name()}
		}
	}
	return resp, nil
}

// result is the --json shape of every read command: wasync's data and page
// unchanged, plus the transport that served them.
type result struct {
	Transport string          `json:"transport"`
	Chat      json.RawMessage `json:"chat,omitempty"`
	Data      json.RawMessage `json:"data"`
	Page      *contract.Page  `json:"page,omitempty"`
}

func writeTransport(o *output.Writer, t transport.Transport) {
	o.Field(0, "transport", t.Name())
	if t.Name() == transport.NameQuery {
		o.Field(0, "note", offlineNote)
	}
}

// intFlag parses a non-negative integer flag; ranges are wasync's to check.
func intFlag(inv invocation, name string) (string, bool, error) {
	v, ok := inv.flag(name)
	if !ok {
		return "", false, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return "", false, usageErr("pass a whole number, e.g. "+name+" 5", "%s must be a whole number >= 0, got %q", name, v)
	}
	return strconv.Itoa(n), true, nil
}

// shellArg quotes s for a copy-pasteable command hint.
func shellArg(s string) string {
	safe := s != "" && !strings.HasPrefix(s, "-") && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./_-", r))
	}) < 0
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commandLine builds a copy-pasteable hint from wasync-axi arguments.
// Arguments that look like flags (--name) are kept bare; callers put a
// positional value that starts with "-" after "--" (see searchArgs).
func commandLine(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if isFlagToken(a) {
			q[i] = a
		} else {
			q[i] = shellArg(a)
		}
	}
	return "`wasync-axi " + strings.Join(q, " ") + "`"
}

func isFlagToken(s string) bool {
	return s == "--" || len(s) > 2 && strings.HasPrefix(s, "--") && strings.IndexFunc(s[2:], func(r rune) bool { return !(r >= 'a' && r <= 'z' || r == '-') }) < 0
}

// searchArgs is a `search` argument list that keeps text positional even
// when it starts with "-".
func searchArgs(text string, flags ...string) []string {
	if strings.HasPrefix(text, "-") {
		return append(append([]string{"search"}, flags...), "--", text)
	}
	return append([]string{"search", text}, flags...)
}

// withAmbiguityHelp replaces wasync's API-oriented chat_ambiguous help with
// a command the agent can run; example builds it from a candidate id.
func withAmbiguityHelp(err error, example func(id string) string) error {
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != contract.CodeChatAmbiguous {
		return err
	}
	var amb contract.ChatAmbiguity
	if json.Unmarshal(ce.Details, &amb) != nil || len(amb.Candidates) == 0 {
		return err
	}
	c := *ce
	c.Help = ambiguityHelpText + example(amb.Candidates[0].ID)
	return &c
}

// writeError renders err; a chat_ambiguous error also lists its candidates.
func writeError(w io.Writer, err error, jsonMode bool) error {
	var ce *contract.Error
	var amb contract.ChatAmbiguity
	if !errors.As(err, &ce) || ce.Code != contract.CodeChatAmbiguous || json.Unmarshal(ce.Details, &amb) != nil {
		return output.Error(w, err, jsonMode)
	}
	return output.ErrorWith(w, err, jsonMode, func(o *output.Writer) {
		rows := make([][]string, 0, len(amb.Candidates))
		for _, c := range amb.Candidates {
			name, _ := output.UntrustedCell(c.Name, listTextLimit)
			rows = append(rows, []string{output.Cell(c.ID), output.Cell(c.Kind), name})
		}
		o.TableCells(0, "candidates", []string{"id", "kind", "untrusted_name"}, rows)
	})
}

// resolveChat finds exactly one chat for a JID or name through wasync's
// ?match= resolution. It returns the chat and its raw JSON item.
func resolveChat(ctx context.Context, t transport.Transport, match string, example func(id string) string) (contract.Chat, json.RawMessage, error) {
	var raw struct {
		Items []json.RawMessage `json:"items"`
	}
	resp, err := get(ctx, t, "/chats", url.Values{"match": {match}}, &raw)
	if err != nil {
		return contract.Chat{}, nil, withAmbiguityHelp(err, example)
	}
	switch len(raw.Items) {
	case 0:
		return contract.Chat{}, nil, &contract.Error{RequestID: resp.RequestID, Code: contract.CodeNotFound, Message: "no chat matches " + strconv.Quote(match), Help: "run `wasync-axi chats` to list chats, or `wasync-axi chats --match <part of a name>`", Transport: t.Name()}
	case 1:
		var c contract.Chat
		if err := json.Unmarshal(raw.Items[0], &c); err != nil {
			return c, nil, &contract.Error{RequestID: resp.RequestID, Code: contract.CodeInvalidResponse, Message: "wasync chat has an unexpected shape: " + err.Error(), Transport: t.Name()}
		}
		return c, raw.Items[0], nil
	default:
		// wasync answers chat_ambiguous instead; never pick one here.
		return contract.Chat{}, nil, &contract.Error{RequestID: resp.RequestID, Code: contract.CodeInvalidResponse, Message: "wasync returned several chats for one match without chat_ambiguous", Transport: t.Name()}
	}
}

func chatRow(c contract.Chat) []string {
	name, _ := output.UntrustedCell(c.Name, listTextLimit)
	return []string{output.Cell(c.ID), output.Cell(c.Kind), output.Cell(c.MessageCount), output.Cell(c.LastMessageAt), output.Cell(c.SyncEnabled), name}
}

var chatFields = []string{"id", "kind", "messages", "last_message_at", "sync", "untrusted_name"}

var defaultChatFields = []string{"id", "kind", "messages", "untrusted_name"}
var defaultMessageFields = []string{"ref", "time", "sender", "untrusted_text"}

func (a *App) runChats(ctx context.Context, inv invocation) error {
	cols, err := selectColumns(inv, chatFields, defaultChatFields)
	if err != nil {
		return err
	}

	limit := defaultChatLimit
	if v, ok, err := intFlag(inv, "--limit"); err != nil {
		return err
	} else if ok {
		limit, _ = strconv.Atoi(v)
	}
	t, err := a.connect(ctx, "chats")
	if err != nil {
		return err
	}
	q := url.Values{}
	match, hasMatch := inv.flag("--match")
	if hasMatch {
		q.Set("match", match)
	}
	var list contract.ChatList
	resp, err := get(ctx, t, "/chats", q, &list)
	if err != nil {
		return withAmbiguityHelp(err, func(id string) string { return commandLine("messages", id) })
	}
	if inv.json {
		return output.JSON(a.Out, result{Transport: t.Name(), Data: resp.Data, Page: resp.Page})
	}
	o := output.New(a.Out)
	writeTransport(o, t)
	shown := list.Items
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	chatAggregates(o, 0, list.Items)
	rows := make([][]string, 0, len(shown))
	for _, c := range shown {
		rows = append(rows, chatRow(c))
	}
	cols.table(o, 0, "chats", rows)
	var hints []string
	switch {
	case len(list.Items) == 0 && hasMatch:
		hints = append(hints, "No chat matches "+strconv.Quote(match)+"; run `wasync-axi chats` to list every chat")
	case len(list.Items) == 0:
		hints = append(hints, "The archive has no chats yet; ask the user to pair wasync and enable capture for some chats")
	default:
		if len(shown) < len(list.Items) {
			hints = append(hints, "Showing "+strconv.Itoa(len(shown))+" of "+strconv.Itoa(len(list.Items))+" chats; run `wasync-axi chats --limit 0` for all, or `wasync-axi chats --match <name>` to narrow")
		}
		hints = append(hints, "Run `wasync-axi messages <id>` to read a chat, `wasync-axi search <text>` to search")
	}
	o.Help(hints...)
	return o.Err()
}

// messageRow renders one message for a table. sender is "me" for the
// account's own messages.
func messageRow(m contract.Message) ([]string, bool) {
	text, cut := output.UntrustedCell(m.Text, listTextLimit)
	return []string{output.Cell(m.Ref), output.Cell(m.Timestamp), output.Cell(sender(m)), output.Cell(m.Type), output.Cell(state(m)), text}, cut
}

var messageFields = []string{"ref", "time", "sender", "type", "state", "untrusted_text"}

func sender(m contract.Message) string {
	if m.FromMe {
		return "me"
	}
	return m.SenderID
}

// state names the wasync tombstone and edit markers of a message.
func state(m contract.Message) string {
	switch {
	case m.DeletedAt != "":
		return "deleted"
	case m.EditedAt != "":
		return "edited"
	}
	return ""
}

func pageParams(inv invocation, q url.Values) error {
	limit, ok, err := intFlag(inv, "--limit")
	if err != nil {
		return err
	}
	if !ok {
		limit = strconv.Itoa(defaultPageLimit)
	}
	q.Set("limit", limit)
	if c, ok := inv.flag("--cursor"); ok {
		q.Set("cursor", c)
	}
	return nil
}

// nextPageHint repeats a command with the next cursor (replacing any old
// one); args turns the paging flags into the full argument list.
func nextPageHint(what string, page *contract.Page, inv invocation, args func(flags ...string) []string) string {
	if page == nil || !page.HasMore || page.NextCursor == "" {
		return ""
	}
	var flags []string
	if v, ok := inv.flag("--fields"); ok {
		flags = append(flags, "--fields", v)
	}
	if v, ok := inv.flag("--limit"); ok {
		flags = append(flags, "--limit", v)
	}
	flags = append(flags, "--cursor", page.NextCursor)
	return what + ": run " + commandLine(args(flags...)...)
}

func (a *App) runMessages(ctx context.Context, inv invocation) error {
	cols, err := selectColumns(inv, messageFields, defaultMessageFields)
	if err != nil {
		return err
	}

	q := url.Values{}
	if err := pageParams(inv, q); err != nil {
		return err
	}
	t, err := a.connect(ctx, "messages")
	if err != nil {
		return err
	}
	chat, rawChat, err := resolveChat(ctx, t, inv.args[0], func(id string) string { return commandLine("messages", id) })
	if err != nil {
		return err
	}
	var list contract.MessageList
	resp, err := get(ctx, t, "/chats/"+url.PathEscape(chat.ID)+"/messages", q, &list)
	if err != nil {
		return err
	}
	if inv.json {
		return output.JSON(a.Out, result{Transport: t.Name(), Chat: rawChat, Data: resp.Data, Page: resp.Page})
	}
	o := output.New(a.Out)
	writeTransport(o, t)
	o.Object(0, "chat")
	o.Field(1, "id", chat.ID)
	o.Field(1, "kind", chat.Kind)
	o.Value(1, "messages", chat.MessageCount)
	o.Value(1, "sync", chat.SyncEnabled)
	o.Untrusted(1, "name", chat.Name, listTextLimit)
	o.Field(0, "order", "newest first")
	rows, cut := messageRows(list.Items)
	total := list.Total
	writeResultCount(o, len(list.Items), total, resp.Page)
	cols.table(o, 0, "messages", rows)
	var hints []string
	if h := nextPageHint("Older messages", resp.Page, inv, func(flags ...string) []string { return append([]string{"messages", chat.ID}, flags...) }); h != "" {
		hints = append(hints, h)
	}
	if len(list.Items) == 0 && len(hints) == 0 {
		if total != nil && *total > 0 && !inv.has("--cursor") {
			hints = append(hints, "This page has no messages; the archive may have changed while reading")
		} else if inv.has("--cursor") {
			hints = append(hints, "No older messages; this is the start of the archived chat")
		} else {
			hints = append(hints, "No messages are archived for this chat")
		}
	}
	hints = append(hints, listHints(len(list.Items) > 0, cut && cols.has("untrusted_text"))...)
	o.Help(hints...)
	return o.Err()
}

func messageRows(ms []contract.Message) ([][]string, bool) {
	rows := make([][]string, 0, len(ms))
	anyCut := false
	for _, m := range ms {
		row, cut := messageRow(m)
		rows = append(rows, row)
		anyCut = anyCut || cut
	}
	return rows, anyCut
}

func listHints(nonEmpty, cut bool) []string {
	var h []string
	if cut {
		h = append(h, strings.Replace(fullTextHintFmt, "%d", strconv.Itoa(listTextLimit), 1))
	}
	if nonEmpty {
		h = append(h, threadHint)
	}
	return h
}

func (a *App) runSearch(ctx context.Context, inv invocation) error {
	around, hasContext, err := intFlag(inv, "--context")
	if err != nil {
		return err
	}
	available := append([]string{"chat"}, messageFields...)
	defaults := []string{"chat", "ref", "time", "untrusted_text"}
	if hasContext && around != "0" {
		available = append([]string{"hit", "role", "chat"}, messageFields...)
		defaults = []string{"hit", "role", "ref", "untrusted_text"}
	}
	cols, err := selectColumns(inv, available, defaults)
	if err != nil {
		return err
	}

	text := inv.args[0]
	q := url.Values{"q": {text}}
	if err := pageParams(inv, q); err != nil {
		return err
	}
	if hasContext {
		q.Set("context", around)
	}
	t, err := a.connect(ctx, "search")
	if err != nil {
		return err
	}
	var hintFlags []string
	chatArg, hasChat := inv.flag("--chat")
	var chat contract.Chat
	if hasChat {
		chat, _, err = resolveChat(ctx, t, chatArg, func(id string) string { return commandLine(searchArgs(text, "--chat", id)...) })
		if err != nil {
			return err
		}
		q.Set("chat", chat.ID)
		hintFlags = append(hintFlags, "--chat", chat.ID)
	}
	if hasContext {
		hintFlags = append(hintFlags, "--context", around)
	}
	var list contract.SearchList
	resp, err := get(ctx, t, "/messages/search", q, &list)
	if err != nil {
		return err
	}
	if inv.json {
		return output.JSON(a.Out, result{Transport: t.Name(), Data: resp.Data, Page: resp.Page})
	}
	o := output.New(a.Out)
	writeTransport(o, t)
	o.Field(0, "query", text)
	if hasChat {
		o.Field(0, "chat", chat.ID)
	}
	writeResultCount(o, len(list.Items), list.Total, resp.Page)
	o.Field(0, "order", "newest first")
	anyCut := false
	if hasContext && around != "0" {
		var rows [][]string
		add := func(hit int, role string, m contract.Message) {
			row, cut := messageRow(m)
			anyCut = anyCut || cut
			rows = append(rows, append([]string{output.Cell(hit), output.Cell(role), output.Cell(m.ChatID)}, row...))
		}
		for i, h := range list.Items {
			for _, m := range h.Before {
				add(i+1, "before", m)
			}
			add(i+1, "hit", h.Message)
			for _, m := range h.After {
				add(i+1, "after", m)
			}
		}
		cols.table(o, 0, "results", rows)
	} else {
		rows := make([][]string, 0, len(list.Items))
		for _, h := range list.Items {
			row, cut := messageRow(h.Message)
			anyCut = anyCut || cut
			rows = append(rows, append([]string{output.Cell(h.ChatID)}, row...))
		}
		cols.table(o, 0, "results", rows)
	}
	var hints []string
	if h := nextPageHint("More results", resp.Page, inv, func(flags ...string) []string { return searchArgs(text, append(hintFlags, flags...)...) }); h != "" {
		hints = append(hints, h)
	}
	if len(list.Items) == 0 && len(hints) == 0 {
		if list.Total != nil && *list.Total > 0 && !inv.has("--cursor") {
			hints = append(hints, "This page has no visible matches; the archive may have changed while reading")
		} else if inv.has("--cursor") {
			hints = append(hints, "No more results")
		} else if hasChat {
			hints = append(hints, "No messages in this chat match; try other words or drop --chat")
		} else {
			hints = append(hints, "No messages match; try other or fewer words")
		}
	}
	hints = append(hints, listHints(len(list.Items) > 0, anyCut && cols.has("untrusted_text"))...)
	o.Help(hints...)
	return o.Err()
}

func refArg(inv invocation) (string, error) {
	ref := inv.args[0]
	if !contract.IsMessageRef(ref) {
		return "", usageErr("pass the ref column (m1_...) from `wasync-axi messages` or `wasync-axi search`", "%q is not a message ref", ref)
	}
	return ref, nil
}

func (a *App) runThread(ctx context.Context, inv invocation) error {
	cols, err := selectColumns(inv, append([]string{"role"}, messageFields...), []string{"role", "ref", "sender", "untrusted_text"})
	if err != nil {
		return err
	}

	ref, err := refArg(inv)
	if err != nil {
		return err
	}
	q := url.Values{}
	for _, name := range []string{"before", "after"} {
		v, ok, err := intFlag(inv, "--"+name)
		if err != nil {
			return err
		}
		if ok {
			q.Set(name, v)
		}
	}
	t, err := a.connect(ctx, "thread")
	if err != nil {
		return err
	}
	var th contract.Thread
	resp, err := get(ctx, t, "/messages/"+url.PathEscape(ref)+"/thread", q, &th)
	if err != nil {
		return err
	}
	if inv.json {
		return output.JSON(a.Out, result{Transport: t.Name(), Data: resp.Data, Page: resp.Page})
	}
	o := output.New(a.Out)
	writeTransport(o, t)
	o.Field(0, "chat", th.Anchor.ChatID)
	o.Field(0, "order", "oldest first")
	var rows [][]string
	anyCut := false
	add := func(role string, m contract.Message) {
		row, cut := messageRow(m)
		anyCut = anyCut || cut
		rows = append(rows, append([]string{output.Cell(role)}, row...))
	}
	for _, m := range th.Before {
		add("before", m)
	}
	add("anchor", th.Anchor)
	for _, m := range th.After {
		add("after", m)
	}
	cols.table(o, 0, "thread", rows)
	var hints []string
	if anyCut && cols.has("untrusted_text") {
		hints = append(hints, strings.Replace(fullTextHintFmt, "%d", strconv.Itoa(listTextLimit), 1))
	}
	wider := []string{"thread", ref, "--before", "10", "--after", "10"}
	if selected, ok := inv.flag("--fields"); ok {
		wider = append(wider, "--fields", selected)
	}
	hints = append(hints, "For a wider window run "+commandLine(wider...))
	o.Help(hints...)
	return o.Err()
}

func (a *App) runMessage(ctx context.Context, inv invocation) error {
	ref, err := refArg(inv)
	if err != nil {
		return err
	}
	t, err := a.connect(ctx, "message")
	if err != nil {
		return err
	}
	var m contract.Message
	resp, err := get(ctx, t, "/messages/"+url.PathEscape(ref), nil, &m)
	if err != nil {
		return err
	}
	if inv.json {
		return output.JSON(a.Out, result{Transport: t.Name(), Data: resp.Data, Page: resp.Page})
	}
	limit := output.DefaultTruncate
	if inv.has("--full") {
		limit = 0
	}
	o := output.New(a.Out)
	writeTransport(o, t)
	o.Object(0, "message")
	o.Field(1, "ref", m.Ref)
	o.Field(1, "chat", m.ChatID)
	o.Field(1, "chat_kind", m.ChatKind)
	o.Field(1, "time", m.Timestamp)
	o.Field(1, "sender", m.SenderID)
	o.Value(1, "from_me", m.FromMe)
	o.Field(1, "type", m.Type)
	for _, f := range []struct{ key, value string }{{"reply_to", m.ReplyTo}, {"edited_at", m.EditedAt}, {"deleted_at", m.DeletedAt}, {"expires_at", m.ExpiresAt}} {
		if f.value != "" {
			o.Field(1, f.key, f.value)
		}
	}
	_, _, textCut := output.Truncate(output.Sanitize(m.Text), limit)
	o.Untrusted(1, "text", m.Text, limit)
	mediaCut := false
	if media := compactJSON(m.Media); media != "" && media != "null" {
		_, _, mediaCut = output.Truncate(output.Sanitize(media), limit)
		o.Untrusted(1, "media", media, limit)
	}
	var hints []string
	if textCut || mediaCut {
		hints = append(hints, "Run "+commandLine("message", ref, "--full")+" for the whole message")
	}
	hints = append(hints, "Run "+commandLine("thread", ref)+" for the messages around it")
	o.Help(hints...)
	return o.Err()
}

// homeChats fetches the chat list for the home view. It returns nil and no
// error when the home view should not try (no usable transport or no
// credential, which the home hints already explain).
func (a *App) homeChats(ctx context.Context, d detection) (*contract.ChatList, error) {
	t, err := d.readTransport()
	if err != nil || (t.Name() == transport.NameAPI && d.cred == nil) {
		return nil, nil
	}
	if d.sel.Version == nil || a.Compat.Check("chats", *d.sel.Version) != nil {
		return nil, nil
	}
	var list contract.ChatList
	if _, err := get(ctx, t, "/chats", nil, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// homeSendList fetches the agent's approved recipient list for the home view.
// It returns nil when there is none to show or it cannot be read: the list is
// a convenience here, and `send` reports any refusal itself.
func (a *App) homeSendList(ctx context.Context, d detection) *contract.SendList {
	if d.sel.Name() != transport.NameAPI || d.cred == nil || d.sel.Version == nil || a.Compat.Check("send.list", *d.sel.Version) != nil {
		return nil
	}
	var list contract.SendList
	if _, err := get(ctx, d.sel.Transport, "/send-list", nil, &list); err != nil || !list.Active {
		return nil
	}
	return &list
}

// sendListLine is the home view's one-line summary of an approved list.
func sendListLine(l *contract.SendList) string {
	s := fmt.Sprintf("%d of %d new numbers and %d of %d messages left this hour; %d numbers, %d not messaged yet", l.NewNumbersLeft, l.NewNumbersPerHour, l.MessagesLeft, l.MessagesPerHour, l.Numbers, l.NotMessaged)
	if l.Stopped > 0 {
		s += fmt.Sprintf(", %d removed after STOP", l.Stopped)
	}
	if l.NextNewAt != nil {
		s += "; next new number at " + *l.NextNewAt
	}
	if l.ExpiresAt != nil {
		s += "; ends " + *l.ExpiresAt
	}
	return s
}

// compactJSON returns raw JSON on one line, or "" when it is empty.
func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return strings.TrimSpace(string(raw))
	}
	return b.String()
}
