// Package api exposes the shared archive to authenticated local applications.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

type Server struct {
	HTTP   *http.Server
	URL    string
	Errors <-chan error
}

// ValidateAddr permits only literal loopback addresses, avoiding DNS rebinding.
func ValidateAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 0 || number > 65535 || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("API address must be a literal loopback address, e.g. 127.0.0.1:8787")
	}
	return nil
}

func loadToken(dir string) (token string, result error) { return loadNamedToken(dir, "api.token") }
func loadNamedToken(dir, name string) (token string, result error) {
	f, err := localfs.OpenFile(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	defer func() { result = errors.Join(result, f.Close()) }()
	b, err := io.ReadAll(io.LimitReader(f, 1024))
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return "", err
		}
		token = hex.EncodeToString(key)
		if _, err = f.WriteString(token + "\n"); err != nil {
			return "", err
		}
		if err = f.Sync(); err != nil {
			return "", err
		}
		return token, nil
	}
	token = strings.TrimSpace(string(b))
	key, err := hex.DecodeString(token)
	if err != nil || len(key) != 32 {
		return "", fmt.Errorf("invalid %s; remove it with wasync stopped to generate a replacement", name)
	}
	return token, nil
}

// Start shares the caller's store; it never opens a WhatsApp session.
func Start(addr, dir string, store *storage.Store, status func() whatsapp.CaptureStatus, sender ...whatsapp.Sender) (*Server, error) {
	return StartWithApprovals(addr, dir, store, status, nil, sender...)
}
func StartWithApprovals(addr, dir string, store *storage.Store, status func() whatsapp.CaptureStatus, approvals *approval.Queue, sender ...whatsapp.Sender) (*Server, error) {
	return StartControlled(addr, dir, store, status, approvals, nil, nil, sender...)
}

// StartControlled shares one device owner across the TUI and CLI command channel.
func StartControlled(addr, dir string, store *storage.Store, status func() whatsapp.CaptureStatus, approvals *approval.Queue, approve func(int64, string) error, syncer whatsapp.Syncer, sender ...whatsapp.Sender) (*Server, error) {
	return StartMediaControlled(addr, dir, store, status, approvals, approve, syncer, nil, sender...)
}

func StartMediaControlled(addr, dir string, store *storage.Store, status func() whatsapp.CaptureStatus, approvals *approval.Queue, approve func(int64, string) error, syncer whatsapp.Syncer, media whatsapp.MediaDownloader, sender ...whatsapp.Sender) (*Server, error) {
	return StartOperations(addr, dir, store, status, approvals, approve, syncer, media, whatsapp.Operations{}, sender...)
}

// StartOperations also serves reply, reaction and edit through ops, which are
// nil while the owner has no live WhatsApp client.
func StartOperations(addr, dir string, store *storage.Store, status func() whatsapp.CaptureStatus, approvals *approval.Queue, approve func(int64, string) error, syncer whatsapp.Syncer, media whatsapp.MediaDownloader, ops whatsapp.Operations, sender ...whatsapp.Sender) (*Server, error) {
	if err := ValidateAddr(addr); err != nil {
		return nil, err
	}
	token, err := loadToken(dir)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("start local API (change --api-addr or use --no-api): %w", err)
	}
	h := &handler{dataDir: dir, agents: agentauth.NewAuthorizer(dir), mediaDownload: media, store: store, token: token, status: status, authority: listener.Addr().String(), approvals: approvals, approve: approve, syncer: syncer, ops: ops}
	if len(sender) > 0 {
		h.sender = sender[0]
	}
	{ // Policy configuration is available even when the media owner is offline.
		h.sendToken, err = loadNamedToken(dir, "send.token")
		if err != nil {
			listener.Close()
			return nil, err
		}
	}
	if approve != nil {
		h.approveToken, err = loadNamedToken(dir, "approve.token")
		if err != nil {
			listener.Close()
			return nil, err
		}
	}
	srv := &http.Server{Handler: h.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() {
		err := srv.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
		close(done)
	}()
	return &Server{HTTP: srv, URL: "http://" + listener.Addr().String(), Errors: done}, nil
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.HTTP.Shutdown(ctx); err != nil {
		return errors.Join(err, s.HTTP.Close())
	}
	return nil
}

type handler struct {
	dataDir          string
	v1Once           sync.Once
	v1Mux            *http.ServeMux
	agents           *agentauth.Authorizer
	mediaDownload    whatsapp.MediaDownloader
	approve          func(int64, string) error
	approveToken     string
	syncer           whatsapp.Syncer
	approvals        *approval.Queue
	sendToken        string
	sendAuthMu       sync.Mutex
	sendNonces       map[string]time.Time
	sender           whatsapp.Sender
	ops              whatsapp.Operations
	store            *storage.Store
	token, authority string
	status           func() whatsapp.CaptureStatus
}

func (h *handler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := h.store.Ping(r.Context()); err != nil {
			fail(w, 503, "archive unavailable")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/status", h.getStatus)
	mux.HandleFunc("POST /api/send", h.send)
	mux.HandleFunc("POST /api/reply", h.reply)
	mux.HandleFunc("POST /api/react", h.react)
	mux.HandleFunc("POST /api/edit", h.edit)
	mux.HandleFunc("POST /api/media/policy", h.mediaControl)
	mux.HandleFunc("POST /api/media/download", h.mediaControl)
	mux.HandleFunc("POST /api/sync", h.syncNow)
	mux.HandleFunc("POST /api/actions/{id}/approve", h.approveAction)
	mux.HandleFunc("POST /api/groups/enable-all", h.enableAll)
	mux.HandleFunc("POST /api/groups/disable-all", h.enableAll)
	mux.HandleFunc("POST /api/chats/enable-all", h.enableAll)
	mux.HandleFunc("POST /api/chats/disable-all", h.enableAll)
	mux.HandleFunc("POST /api/actions", h.requestAction)
	mux.HandleFunc("GET /api/access/mode", h.getAccessMode)
	mux.HandleFunc("POST /api/access/mode", h.setAccessMode)
	mux.HandleFunc("GET /api/actions", h.listActions)
	mux.HandleFunc("GET /api/actions/{id}", h.actionStatus)
	mux.HandleFunc("GET /api/groups", h.groups)
	mux.HandleFunc("GET /api/chats", h.groups)
	mux.HandleFunc("GET /api/chats/{id}", h.group)
	mux.HandleFunc("POST /api/chats/{id}/enable", h.enable)
	mux.HandleFunc("POST /api/chats/{id}/disable", h.enable)
	mux.HandleFunc("POST /api/chats/{id}/history", h.history)
	mux.HandleFunc("GET /api/chats/{id}/messages", h.messages)
	mux.HandleFunc("GET /api/groups/{id}", h.group)
	mux.HandleFunc("POST /api/groups/{id}/enable", h.enable)
	mux.HandleFunc("POST /api/groups/{id}/disable", h.enable)
	mux.HandleFunc("POST /api/groups/{id}/history", h.history)
	mux.HandleFunc("GET /api/groups/{id}/messages", h.messages)
	mux.HandleFunc("GET /api/messages/search", h.search)
	mux.HandleFunc("GET /api/messages/{id}", h.message)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		deny := fail
		if isV1(r.URL.Path) {
			deny = func(w http.ResponseWriter, code int, message string) {
				id := query.NewRequestID()
				w.Header().Set("X-Request-Id", id)
				v1Fail(w, id, code, query.CodeForbidden, message, "connect to the loopback address wasync listens on")
			}
		}
		if r.Host != h.authority {
			deny(w, 403, "invalid host")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Scheme != "http" || u.Host != h.authority {
				deny(w, 403, "foreign origin")
				return
			}
		}
		if isV1(r.URL.Path) {
			h.serveV1(w, r)
			return
		}
		requiredToken := h.token
		if (isWritePath(r.URL.Path) || r.URL.Path == "/api/sync") || strings.HasPrefix(r.URL.Path, "/api/actions") || strings.HasPrefix(r.URL.Path, "/api/media/") || r.URL.Path == accessModePath {
			if r.URL.Path == "/api/send" && h.sender == nil || strings.HasPrefix(r.URL.Path, "/api/actions") && h.approvals == nil {
				fail(w, 503, "sending unavailable")
				return
			}
			requiredToken = h.sendToken
			if strings.HasSuffix(r.URL.Path, "/approve") || r.URL.Path == accessModePath {
				if h.approve == nil || h.approveToken == "" {
					fail(w, 503, "interactive approval unavailable")
					return
				}
				requiredToken = h.approveToken
			}
			if requiredToken == "" || r.Header.Get("X-Wasync-Signature") == "" {
				fail(w, 401, "signed request required")
				return
			}
			if !h.authenticateSend(w, r, requiredToken) {
				return
			}
			requiredToken = ""
		}
		if requiredToken != "" && r.Header.Get("X-Wasync-Signature") != "" {
			if !h.authenticateSend(w, r, requiredToken) {
				return
			}
			requiredToken = ""
		}
		if requiredToken != "" && r.URL.Path != "/health" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+requiredToken)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "bearer token required")
			return
		}
		// An explicitly requested attachment upload may take longer than the
		// ordinary command deadline. The caller's context still cancels on disconnect.
		if isWritePath(r.URL.Path) {
			if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
				fail(w, 500, "could not configure transfer response")
				return
			}
			mux.ServeHTTP(w, r)
			return
		}
		timeout := 10 * time.Second
		if r.URL.Path == "/api/sync" || strings.HasSuffix(r.URL.Path, "/approve") || r.URL.Path == accessModePath {
			timeout = 90 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

func (h *handler) account(w http.ResponseWriter, r *http.Request) *storage.Account {
	a, err := h.store.Account(r.Context())
	if err != nil {
		fail(w, 500, "archive unavailable")
		return nil
	}
	if a == nil {
		fail(w, 409, "no paired account in archive")
		return nil
	}
	return a
}
