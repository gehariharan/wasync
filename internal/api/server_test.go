package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

type fixture struct {
	store   *storage.Store
	account int64
	server  *Server
	token   string
	dir     string
	agent   string // read-only agent credential (default scopes)
}

func setup(t *testing.T) fixture { return setupOps(t, whatsapp.Operations{}) }

// setupOps is setup with the owner's WhatsApp writes replaced by fakes.
func setupOps(t *testing.T, ops whatsapp.Operations) fixture {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	account, err := store.UseAccount(ctx, "test@lid")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveGroups(ctx, account, []storage.Group{{ID: "one@g.us", Name: "First"}, {ID: "two@g.us", Name: "Second"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetEnabled(ctx, account, []string{"one@g.us", "two@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"a", "b", "c"} {
		sender := "alice@lid"
		group := "one@g.us"
		if i == 0 {
			sender = "bob@lid"
		}
		if i == 2 {
			group = "two@g.us"
		}
		_, err = store.Apply(ctx, account, []storage.Message{{GroupID: group, MessageID: id, SenderID: sender, Timestamp: stamp, Type: "text", Text: "hello archive " + id, Source: "live"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	srv, err := StartOperations("127.0.0.1:0", dir, store, nil, nil, nil, nil, nil, ops)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
	})
	token, err := loadToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := agentauth.Grant(dir, agentauth.GrantRequest{Name: "test-agent"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return fixture{store, account, srv, token, dir, agent}
}
func (f fixture) request(t *testing.T, method, path, token string, code int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != code {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, res.StatusCode, code, body)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	return body
}
func TestAuthenticationAndOrigin(t *testing.T) {
	f := setup(t)
	f.request(t, "GET", "/health", "", 200)
	for _, path := range []string{"/api/status", "/api/groups", "/api/messages/search?q=hello", "/api/groups/one@g.us/messages", "/api/messages/1"} {
		for _, token := range []string{"", "wrong"} {
			body := f.request(t, "GET", path, token, 401)
			if strings.Contains(string(body), "hello archive") {
				t.Fatal("unauthenticated content leak")
			}
		}
	}
	f.request(t, "GET", "/api/status", f.token, 200)
	h := (&handler{store: f.store, token: f.token, authority: "127.0.0.1:8787"}).routes()
	for _, tc := range []struct{ host, origin string }{{"attacker.invalid:8787", ""}, {"127.0.0.1:8787", "https://attacker.invalid"}, {"127.0.0.1:8787", "null"}} {
		req := httptest.NewRequest("GET", "http://127.0.0.1:8787/api/groups", nil)
		req.Host = tc.host
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Authorization", "Bearer "+f.token)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != 403 {
			t.Fatalf("accepted host/origin: %+v", tc)
		}
	}
}
func TestQueriesPaginationAndFilters(t *testing.T) {
	f := setup(t)
	var page struct {
		Items []messageJSON `json:"items"`
		Next  string        `json:"next_cursor"`
	}
	body := f.request(t, "GET", "/api/groups/one@g.us/messages?limit=1", f.token, 200)
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].MessageID != "b" || page.Next == "" {
		t.Fatalf("bad first page %s", body)
	}
	cursor := page.Next
	body = f.request(t, "GET", "/api/groups/one@g.us/messages?limit=1&cursor="+url.QueryEscape(cursor), f.token, 200)
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].MessageID != "a" || page.Next != "" {
		t.Fatalf("bad next page %s", body)
	}
	for _, path := range []string{
		"/api/groups/two@g.us/messages?cursor=" + cursor,
		"/api/groups/one@g.us/messages?sender_id=alice@lid&cursor=" + cursor,
		"/api/groups/one@g.us/messages?cursor=" + cursor + "x",
		"/api/groups/one@g.us/messages?limit=201",
		"/api/groups/one@g.us/messages?limit=0",
		"/api/groups/one@g.us/messages?from=invalid",
		"/api/groups/one@g.us/messages?from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z",
		"/api/messages/search?q=",
	} {
		f.request(t, "GET", path, f.token, 400)
	}
	for _, tc := range []struct {
		path  string
		count int
	}{
		{"/api/messages/search?q=hello", 3},
		{"/api/messages/search?q=hello&group_id=one@g.us", 2},
		{"/api/messages/search?q=hello&sender_id=alice@lid", 2},
		{"/api/groups/one@g.us/messages?sender_id=bob@lid", 1},
		{"/api/groups/one@g.us/messages?from=2026-10-01T12:00:00Z&to=2026-10-02T00:00:00Z", 2},
		{"/api/groups/one@g.us/messages?to=2026-10-01T12:00:00Z", 0},
		{"/api/groups/one@g.us/messages?from=2026-10-02T00:00:00Z", 0},
	} {
		body = f.request(t, "GET", tc.path, f.token, 200)
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != tc.count {
			t.Fatalf("%s: %s", tc.path, body)
		}
	}
	f.request(t, "GET", "/api/messages/1", f.token, 200)
	f.request(t, "GET", "/api/messages/9999", f.token, 404)
	f.request(t, "GET", "/api/groups/unknown/messages", f.token, 404)
}
func TestPreferencesSharedStoreAndIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.request(t, "POST", "/api/groups/one@g.us/disable", f.token, 200)
	enabled, err := f.store.IsEnabled(ctx, f.account, "one@g.us")
	if err != nil || enabled {
		t.Fatalf("disable not applied: %v", err)
	}
	f.request(t, "POST", "/api/groups/one@g.us/history", f.token, 409)
	f.request(t, "POST", "/api/groups/one@g.us/enable", f.token, 200)
	jobs, err := f.store.NextHistory(ctx, f.account)
	if err != nil || jobs == nil {
		t.Fatalf("history not queued: %v", err)
	}
	// Updates committed by the capture path are immediately visible through HTTP.
	_, err = f.store.Apply(ctx, f.account, []storage.Message{{GroupID: "one@g.us", MessageID: "new", Timestamp: time.Now(), Text: "fresh", Type: "text", Source: "live"}})
	if err != nil {
		t.Fatal(err)
	}
	body := f.request(t, "GET", "/api/messages/search?q=fresh", f.token, 200)
	if !strings.Contains(string(body), `"message_id":"new"`) {
		t.Fatal(string(body))
	}
	if _, err = f.store.UseAccount(ctx, "other@lid"); err != nil {
		t.Fatal(err)
	}
	f.request(t, "GET", "/api/messages/1", f.token, 404)
	body = f.request(t, "GET", "/api/messages/search?q=hello", f.token, 200)
	if !strings.Contains(string(body), `"items":[]`) {
		t.Fatal("cross-account search leak")
	}
}
func TestConcurrentQueriesAndCapture(t *testing.T) {
	f := setup(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				f.request(t, "GET", "/api/groups/one@g.us/messages", f.token, 200)
			}
		}()
	}
	for i := 0; i < 10; i++ {
		if _, err := f.store.Apply(context.Background(), f.account, []storage.Message{{GroupID: "one@g.us", MessageID: "concurrent", Timestamp: time.Now(), Type: "text", Text: "concurrent", Source: "live"}}); err != nil {
			t.Error(err)
		}
	}
	wg.Wait()
}
func TestTokenAndLoopbackValidation(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8787", "[::]:8787", "example.com:8787", "localhost:8787", ":8787", "127.0.0.1"} {
		if ValidateAddr(addr) == nil {
			t.Fatalf("accepted %s", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8787", "[::1]:8787"} {
		if err := ValidateAddr(addr); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	token, err := loadToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := loadToken(dir)
	if err != nil || again != token || len(token) != 64 {
		t.Fatal("token did not persist")
	}
	err = localfs.CheckPrivateFile(filepath.Join(dir, "api.token"))
	if err != nil {
		t.Fatal("token permissions")
	}
	if err = os.WriteFile(filepath.Join(dir, "api.token"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadToken(dir); err == nil {
		t.Fatal("corrupt token accepted")
	}
}
func TestNoAccountAndClosedArchive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(context.Background(), filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv, err := Start("127.0.0.1:0", dir, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	token, err := loadToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{store: store, server: srv, token: token}
	f.request(t, "GET", "/api/status", token, 200)
	f.request(t, "GET", "/api/groups", token, 409)
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	f.request(t, "GET", "/api/groups", token, 500)
	f.request(t, "GET", "/health", "", 503)
}

func TestQueriesFollowEditsRevokesExpiryAndPurge(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := time.Now()
	expired := now.Add(-time.Hour)
	mutations := []storage.Message{
		{GroupID: "one@g.us", MessageID: "a", Kind: "delete", MutationAt: now, Source: "live"},
		{GroupID: "one@g.us", MessageID: "b", Kind: "edit", MutationAt: now, Text: "replacement words", Type: "text", Source: "live"},
		{GroupID: "one@g.us", MessageID: "pending", Kind: "edit", MutationAt: now, Text: "pending secret", Type: "text", Source: "live"},
		{GroupID: "one@g.us", MessageID: "expired", Timestamp: now.Add(-2 * time.Hour), Text: "expired secret", ExpiresAt: &expired, Type: "text", Source: "live"},
	}
	if _, err := f.store.Apply(ctx, f.account, mutations); err != nil {
		t.Fatal(err)
	}
	body := f.request(t, "GET", "/api/groups/one@g.us/messages", f.token, 200)
	var page struct {
		Items []messageJSON `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("pending/expired content appeared: %s", body)
	}
	for _, m := range page.Items {
		if m.MessageID == "a" && (m.Text != "" || m.DeletedAt == nil) {
			t.Fatal("revoked content retained")
		}
		if m.MessageID == "b" && m.Text != "replacement words" {
			t.Fatal("edit not visible")
		}
	}
	body = f.request(t, "GET", "/api/messages/search?q=hello&group_id=one@g.us", f.token, 200)
	if !strings.Contains(string(body), `"items":[]`) {
		t.Fatal("old text retained in search")
	}
	if err := f.store.PurgeGroup(ctx, f.account, "one@g.us"); err != nil {
		t.Fatal(err)
	}
	f.request(t, "GET", "/api/messages/1", f.token, 404)
	body = f.request(t, "GET", "/api/groups/one@g.us/messages", f.token, 200)
	if !strings.Contains(string(body), `"items":[]`) {
		t.Fatal("purged content retained")
	}
}
