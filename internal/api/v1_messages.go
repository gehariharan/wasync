package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/msgref"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
)

// W-03b/W-03c: chats, messages, search with context, message refs and threads.
func init() {
	for _, route := range []v1Route{
		{Pattern: "/api/v1/chats", Scope: agentauth.ScopeReadChats, Chat: nil, Handle: (*handler).v1Chats},
		{Pattern: "/api/v1/chats/{chat}", Scope: agentauth.ScopeReadChats, Chat: pathChat("chat"), Handle: (*handler).v1Chat},
		{Pattern: "/api/v1/chats/{chat}/messages", Scope: agentauth.ScopeReadMessages, Chat: pathChat("chat"), Handle: (*handler).v1ChatMessages},
		{Pattern: "/api/v1/messages/search", Scope: agentauth.ScopeReadMessages, Chat: queryChat("chat"), Handle: (*handler).v1Search},
		{Pattern: "/api/v1/messages/{ref}", Scope: agentauth.ScopeReadMessages, Chat: refChat("ref"), Handle: (*handler).v1Message},
		{Pattern: "/api/v1/messages/{ref}/thread", Scope: agentauth.ScopeReadMessages, Chat: refChat("ref"), Handle: (*handler).v1Thread},
	} {
		handle := route.Handle
		route.Handle = func(h *handler, w http.ResponseWriter, r *http.Request, id string) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				v1Fail(w, id, 405, query.CodeInvalidArgument, "method not allowed", "use GET")
				return
			}
			handle(h, w, r, id)
		}
		registerV1(route)
	}
}

// queryChat reads the chat JID from a query parameter such as ?chat=.
func queryChat(name string) func(*http.Request) string {
	return func(r *http.Request) string { return r.URL.Query().Get(name) }
}

func v1OK(w http.ResponseWriter, id string, data any, page *query.V1Page) {
	writeJSON(w, 200, query.V1Response{RequestID: id, Data: data, Page: page})
}

func (h *handler) v1Groups(w http.ResponseWriter, r *http.Request, id string, a *storage.Account) ([]storage.Group, bool) {
	groups, err := h.store.GroupSummaries(r.Context(), a.ID)
	if err != nil {
		v1Fail(w, id, 503, query.CodeArchiveUnavailable, "archive query failed", "check wasync status")
		return nil, false
	}
	return groups, true
}

// v1FindChat writes not_found unless chat is in the account's archive.
func (h *handler) v1FindChat(w http.ResponseWriter, r *http.Request, id string, a *storage.Account, chat string) *storage.Group {
	groups, ok := h.v1Groups(w, r, id, a)
	if !ok {
		return nil
	}
	for _, g := range groups {
		if g.ID == chat {
			return &g
		}
	}
	v1Fail(w, id, 404, query.CodeNotFound, "chat is not in this account's archive", "list chat ids with GET /api/v1/chats")
	return nil
}

func (h *handler) v1Chats(w http.ResponseWriter, r *http.Request, id string) {
	values := r.URL.Query()
	enabled := false
	if v := values.Get("enabled"); v != "" {
		var err error
		if enabled, err = strconv.ParseBool(v); err != nil {
			v1Fail(w, id, 400, query.CodeInvalidArgument, "enabled must be true or false", "")
			return
		}
	}
	match, hasMatch := values.Get("match"), values.Has("match")
	if hasMatch && (strings.TrimSpace(match) == "" || len(match) > 256) {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "match must contain 1 to 256 bytes", "")
		return
	}
	a := h.v1Account(w, r, id)
	if a == nil {
		return
	}
	groups, ok := h.v1Groups(w, r, id, a)
	if !ok {
		return
	}
	list, ambiguous := query.V1Chats(a.ID, groups, enabled, match, v1Client(r).AllowsChat)
	if ambiguous != nil {
		writeJSON(w, 409, query.V1Error{RequestID: id, ErrorCode: query.CodeChatAmbiguous, Error: ambiguous.Error(), Help: query.ChatAmbiguousHelp, Details: ambiguous})
		return
	}
	v1OK(w, id, list, nil)
}

func (h *handler) v1Chat(w http.ResponseWriter, r *http.Request, id string) {
	if a := h.v1Account(w, r, id); a != nil {
		if g := h.v1FindChat(w, r, id, a, r.PathValue("chat")); g != nil {
			v1OK(w, id, query.FormatGroup(*g), nil)
		}
	}
}

func (h *handler) v1ChatMessages(w http.ResponseWriter, r *http.Request, id string) {
	if a := h.v1Account(w, r, id); a != nil && h.v1FindChat(w, r, id, a, r.PathValue("chat")) != nil {
		h.v1List(w, r, id, a, r.PathValue("chat"), "", 0)
	}
}

func (h *handler) v1Search(w http.ResponseWriter, r *http.Request, id string) {
	values := r.URL.Query()
	q := strings.TrimSpace(values.Get("q"))
	if q == "" || len(q) > 1024 {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "q must contain 1 to 1024 bytes", "")
		return
	}
	around, ok := v1Int(w, id, values, "context", 0, query.MaxSearchContext)
	if !ok {
		return
	}
	a := h.v1Account(w, r, id)
	if a == nil {
		return
	}
	chat := values.Get("chat")
	if chat != "" && h.v1FindChat(w, r, id, a, chat) == nil {
		return
	}
	h.v1List(w, r, id, a, chat, q, around)
}

// v1Int parses an optional integer parameter within [0, hi]; absent yields def.
func v1Int(w http.ResponseWriter, id string, values url.Values, name string, def, hi int) (int, bool) {
	v := values.Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > hi {
		v1Fail(w, id, 400, query.CodeInvalidArgument, name+" must be between 0 and "+strconv.Itoa(hi), "")
		return 0, false
	}
	return n, true
}

// v1List serves message pages for one chat or a search. Cursors are signed
// like legacy cursors but under a v1 scope, so neither kind is accepted by
// the other. Without a chat, rows in chats outside the allowlist are dropped,
// so a page may be shorter than limit while has_more is still true.
func (h *handler) v1List(w http.ResponseWriter, r *http.Request, id string, a *storage.Account, chat, search string, around int) {
	values := r.URL.Query()
	q := storage.MessageQuery{Limit: 50, Search: search, Sender: values.Get("sender_id")}
	if v := values.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			v1Fail(w, id, 400, query.CodeInvalidArgument, "limit must be between 1 and 200", "")
			return
		}
		q.Limit = n
	}
	for _, p := range []struct {
		name   string
		target **time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		if v := values.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				v1Fail(w, id, 400, query.CodeInvalidArgument, p.name+" must be RFC3339", "for example 2026-10-01T00:00:00Z")
				return
			}
			t = t.UTC()
			*p.target = &t
		}
	}
	if q.From != nil && q.To != nil && !q.From.Before(*q.To) {
		v1Fail(w, id, 400, query.CodeInvalidArgument, "from must be before to", "")
		return
	}
	scope, _ := json.Marshal([]any{"v1", a.ID, chat, search, q.Sender, q.From, q.To})
	if cursor := values.Get("cursor"); cursor != "" {
		before, err := h.decodeCursor(cursor, scope)
		if err != nil {
			v1Fail(w, id, 400, query.CodeInvalidCursor, "invalid cursor or filters changed", "repeat the request without cursor")
			return
		}
		q.Before = before
	}
	page, err := h.store.Messages(r.Context(), a.ID, chat, q)
	if err != nil {
		v1Fail(w, id, 503, query.CodeArchiveUnavailable, "archive query failed", "check wasync status")
		return
	}
	list, err := query.V1Messages(r.Context(), h.store, a, page.Items, around, v1Client(r).AllowsChat)
	if err != nil {
		v1Fail(w, id, 503, query.CodeArchiveUnavailable, "archive query failed", "check wasync status")
		return
	}
	var total int64
	if client := v1Client(r); !client.Restricted() {
		total, err = h.store.CountMessages(r.Context(), a.ID, chat, q, nil)
	} else if allowed := client.AllowedChats(); len(allowed) > 0 {
		total, err = h.store.CountMessages(r.Context(), a.ID, chat, q, allowed)
	}
	if err != nil {
		v1Fail(w, id, 503, query.CodeArchiveUnavailable, "archive count failed", "check wasync status")
		return
	}
	list.Total = &total
	next := ""
	if page.Next.ID != 0 {
		next = h.encodeCursor(page.Next, scope)
	}
	v1OK(w, id, list, &query.V1Page{NextCursor: next, HasMore: next != ""})
}

// v1Anchor resolves the {ref} path value within the current account.
func (h *handler) v1Anchor(w http.ResponseWriter, r *http.Request, id string) (*storage.Account, *storage.Message) {
	ref, err := msgref.Decode(r.PathValue("ref"))
	if err != nil {
		v1Fail(w, id, 400, query.CodeInvalidArgument, err.Error(), "pass a ref (m1_...) from a v1 message")
		return nil, nil
	}
	a := h.v1Account(w, r, id)
	if a == nil {
		return nil, nil
	}
	m, err := h.store.MessageByRef(r.Context(), a.ID, a.JID, ref)
	if errors.Is(err, storage.ErrMessageNotFound) {
		v1Fail(w, id, 404, query.CodeNotFound, "message is not in the current account's archive", "it may have expired, been purged, or belong to another account")
		return nil, nil
	}
	if err != nil {
		v1Fail(w, id, 503, query.CodeArchiveUnavailable, "archive query failed", "check wasync status")
		return nil, nil
	}
	return a, &m
}

func (h *handler) v1Message(w http.ResponseWriter, r *http.Request, id string) {
	if a, m := h.v1Anchor(w, r, id); m != nil {
		v1OK(w, id, query.FormatV1Message(a.JID, a.ID, *m), nil)
	}
}

func (h *handler) v1Thread(w http.ResponseWriter, r *http.Request, id string) {
	values := r.URL.Query()
	before, ok := v1Int(w, id, values, "before", query.DefaultThread, query.MaxThreadWindow)
	if !ok {
		return
	}
	after, ok := v1Int(w, id, values, "after", query.DefaultThread, query.MaxThreadWindow)
	if !ok {
		return
	}
	a, m := h.v1Anchor(w, r, id)
	if m == nil {
		return
	}
	thread, err := query.ThreadFor(r.Context(), h.store, a, *m, before, after)
	if err != nil {
		v1Fail(w, id, 503, query.CodeArchiveUnavailable, "archive query failed", "check wasync status")
		return
	}
	v1OK(w, id, thread, nil)
}
