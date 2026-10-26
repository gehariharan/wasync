package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/msgref"
	"github.com/gehariharan/wasync/internal/query"
)

func isV1(path string) bool { return path == "/api/v1" || strings.HasPrefix(path, "/api/v1/") }

// serveV1 runs after the shared Host and Origin checks; legacy auth and error
// shapes never apply to /api/v1.
func (h *handler) serveV1(w http.ResponseWriter, r *http.Request) {
	id := query.NewRequestID()
	w.Header().Set("X-Request-Id", id)
	if r.URL.Path == "/api/v1/version" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			v1Fail(w, id, 405, query.CodeInvalidArgument, "method not allowed", "use GET /api/v1/version")
			return
		}
		writeJSON(w, 200, query.V1Response{RequestID: id, Data: query.Version()})
		return
	}
	h.v1Once.Do(h.buildV1)
	h.v1Mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), v1IDKey{}, id)))
}

type v1IDKey struct{}

// v1Route is one versioned endpoint. Scope names the agent permission the
// route needs; Chat, when set, extracts the chat JID the request touches so
// chat allowlists can be enforced before the handler runs. Mutation marks a
// write: its authorization is audited even when allowed, and refused when the
// audit log cannot be written.
type v1Route struct {
	Pattern, Scope string
	Chat           func(*http.Request) string
	Mutation       bool
	Handle         func(h *handler, w http.ResponseWriter, r *http.Request, id string)
}

// pathChat reads the chat JID from a path value such as {chat}.
func pathChat(name string) func(*http.Request) string {
	return func(r *http.Request) string { return r.PathValue(name) }
}

// refChat reads the chat JID from a message ref path value; invalid refs yield "".
func refChat(name string) func(*http.Request) string {
	return func(r *http.Request) string {
		ref, err := msgref.Decode(r.PathValue(name))
		if err != nil {
			return ""
		}
		return ref.Chat
	}
}

// v1Routes is filled by feature files in init, keeping endpoints independent.
var v1Routes []v1Route

func registerV1(route v1Route) { v1Routes = append(v1Routes, route) }

func (h *handler) buildV1() {
	h.v1Mux = http.NewServeMux()
	for _, route := range v1Routes {
		h.v1Mux.HandleFunc(route.Pattern, func(w http.ResponseWriter, r *http.Request) {
			id, _ := r.Context().Value(v1IDKey{}).(string)
			chat := ""
			if route.Chat != nil {
				chat = route.Chat(r)
			}
			if !h.v1Authorize(w, r, id, route.Scope, chat, route.Mutation) {
				return
			}
			route.Handle(h, w, r, id)
		})
	}
	h.v1Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(v1IDKey{}).(string)
		if !h.v1Authorize(w, r, id, "", "", false) {
			return
		}
		v1Fail(w, id, 404, query.CodeNotFound, "unknown v1 endpoint", "see GET /api/v1/version for supported features")
	})
}

type v1ClientKey struct{}

// v1Client returns the authenticated agent for a v1 request.
func v1Client(r *http.Request) *agentauth.Client {
	c, _ := r.Context().Value(v1ClientKey{}).(*agentauth.Client)
	return c
}

// v1Authorize is the single v1 credential decision and writes the failure
// itself. Only scoped agent credentials are accepted; legacy tokens never are.
// Reads are audited on denial, mutations always; the authenticated client is
// stored on r.
func (h *handler) v1Authorize(w http.ResponseWriter, r *http.Request, id, scope, chat string, mutation bool) bool {
	if h.agents == nil {
		v1Fail(w, id, 503, query.CodeInternal, "agent authorization unavailable", "restart wasync")
		return false
	}
	c, err := h.agents.Authenticate(r.Header.Get("Authorization"))
	if err == nil && scope != "" {
		err = h.agents.AuthorizeAndAudit(c, scope, chat, r.PathValue("ref"), id, mutation)
	} else if err != nil {
		_ = h.agents.Audit(agentauth.AuditEntry{Time: time.Now(), Event: "request", Scope: scope, Chat: chat, Outcome: agentauth.OutcomeDenied, Code: query.CodeUnauthorized, RequestID: id})
	}
	if err != nil {
		var denied *agentauth.Error
		if !errors.As(err, &denied) {
			v1Fail(w, id, 500, query.CodeInternal, "agent registry unavailable", "check permissions of agents.json in the wasync data directory")
			return false
		}
		if denied.Status == 401 {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		if denied.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(denied.RetryAfter.Seconds()))))
		}
		v1Fail(w, id, denied.Status, denied.Code, denied.Message, denied.Help)
		return false
	}
	*r = *r.WithContext(context.WithValue(r.Context(), v1ClientKey{}, c))
	return true
}

func v1Fail(w http.ResponseWriter, id string, code int, errorCode, message, help string) {
	writeJSON(w, code, query.V1Error{RequestID: id, ErrorCode: errorCode, Error: message, Help: help})
}

func init() { query.RegisterFeature("auth.scopes") }
