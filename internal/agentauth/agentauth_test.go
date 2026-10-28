package agentauth

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

const (
	chatA = "120363000000000001@g.us"
	chatB = "120363000000000002@g.us"
)

func grant(t *testing.T, dir string, req GrantRequest) (string, Client) {
	t.Helper()
	cred, c, err := Grant(dir, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return cred, c
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return "other:" + err.Error()
	}
	return ""
}

func TestCredentialFormatAndParse(t *testing.T) {
	for i := 0; i < 200; i++ { // secrets often contain '_' and '-'
		id, _ := newClientID()
		secret, _ := newSecret()
		cred := FormatCredential(id, secret)
		if len(cred) != credentialLen || !strings.HasPrefix(cred, "wsa_") {
			t.Fatalf("bad credential %q", cred)
		}
		gotID, gotSecret, err := ParseCredential(cred)
		if err != nil || gotID != id || gotSecret != secret {
			t.Fatalf("round trip failed: %v", err)
		}
	}
	secret, _ := newSecret()
	for _, bad := range []string{"", "wsa_", "wsa_ABCDEF01_" + secret, "wsa_0123456_x" + secret, "wsa_01234567-" + secret, "wsx_01234567_" + secret, "wsa_01234567_" + secret + "A", "wsa_01234567_" + secret[:42] + "=", strings.Repeat("a", 64)} {
		if _, _, err := ParseCredential(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if !IsAgentCredential("Bearer wsa_x") || IsAgentCredential("Bearer "+strings.Repeat("a", 64)) {
		t.Fatal("IsAgentCredential")
	}
}

func TestNameAndScopeValidation(t *testing.T) {
	for _, ok := range []string{"a", "claude-axi", "agent-01", strings.Repeat("a", 32)} {
		if ValidName(ok) != nil {
			t.Fatalf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"", "Agent", "a_b", "a b", "a.b", strings.Repeat("a", 33), "é"} {
		if ValidName(bad) == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	scopes, err := ParseScopes()
	if err != nil || strings.Join(scopes, ",") != "read.chats,read.messages" {
		t.Fatalf("default scopes %v %v", scopes, err)
	}
	if _, err = ParseScopes("destructive"); err == nil {
		t.Fatal("granted destructive")
	}
	if _, err = ParseScopes("read.chats,admin"); err == nil {
		t.Fatal("granted unknown scope")
	}
	scopes, _ = ParseScopes("send,read.chats", "send")
	if strings.Join(scopes, ",") != "read.chats,send" {
		t.Fatalf("canonical order %v", scopes)
	}
	dir := privateDir(t)
	for _, s := range []string{ScopeSend, ScopeReact, ScopeEditOwn, ScopeChatState, ScopeGroupAdmin, ScopeCaptureManage} {
		if _, _, err = Grant(dir, GrantRequest{Name: "x", Scopes: []string{s}}, time.Now()); err == nil {
			t.Fatalf("%s granted without allowlist", s)
		}
	}
	if _, _, err = Grant(dir, GrantRequest{Name: "x", Chats: []string{"not a jid"}}, time.Now()); err == nil {
		t.Fatal("accepted invalid JID")
	}
	if _, _, err = Grant(dir, GrantRequest{Name: "x", RatePerMinute: MaxRatePerMinute + 1}, time.Now()); err == nil {
		t.Fatal("accepted huge rate")
	}
	grant(t, dir, GrantRequest{Name: "x"})
	if _, _, err = Grant(dir, GrantRequest{Name: "x"}, time.Now()); err == nil {
		t.Fatal("duplicate active name")
	}
	if _, err = Revoke(dir, "x", time.Now()); err != nil {
		t.Fatal(err)
	}
	grant(t, dir, GrantRequest{Name: "x"}) // name reusable after revoke
}

func TestHashOnlyStorageAndFormat(t *testing.T) {
	dir := privateDir(t)
	cred, c := grant(t, dir, GrantRequest{Name: "axi", Scopes: []string{"read.chats,send"}, Chats: []string{chatA}, Expires: time.Hour})
	_, secret, _ := ParseCredential(cred)
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), cred) {
		t.Fatal("secret written to disk")
	}
	if !strings.Contains(string(data), hashSecret(secret)) {
		t.Fatal("hash missing")
	}
	var reg struct {
		Version int              `json:"version"`
		Clients []map[string]any `json:"clients"`
	}
	if err = json.Unmarshal(data, &reg); err != nil || reg.Version != 1 || len(reg.Clients) != 1 {
		t.Fatalf("format %v %s", err, data)
	}
	for _, key := range []string{"id", "name", "scopes", "chats", "rate_per_minute", "created_at", "expires_at", "secret_sha256"} {
		if _, ok := reg.Clients[0][key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
	if c.RatePerMinute != 60 || c.ExpiresAt == nil {
		t.Fatalf("defaults %+v", c)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatal("temp file left behind")
		}
	}
	clients, _ := List(dir)
	b, _ := json.Marshal(clients)
	if strings.Contains(string(b), hashSecret(secret)) || strings.Contains(string(b), "secret") {
		t.Fatal("List exposes hash")
	}
}

func TestPrivatePermissionEnforcement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ACL-based on Windows")
	}
	dir := privateDir(t)
	cred, _ := grant(t, dir, GrantRequest{Name: "axi"})
	path := filepath.Join(dir, FileName)
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(dir); err == nil {
		t.Fatal("loaded a world-readable registry")
	}
	a := NewAuthorizer(dir)
	if _, err := a.Authenticate("Bearer " + cred); err == nil {
		t.Fatal("authenticated against a public registry")
	}
	if _, _, err := Grant(dir, GrantRequest{Name: "other"}, time.Now()); err == nil {
		t.Fatal("updated a public registry")
	}
	os.Chmod(path, 0600)
	if _, err := a.Authenticate("Bearer " + cred); err != nil {
		t.Fatal(err)
	}
	// Symlinked registries are refused.
	os.Rename(path, path+".real")
	os.Symlink(path+".real", path)
	if _, err := List(dir); err == nil {
		t.Fatal("followed symlink")
	}
}

func TestAuthenticate(t *testing.T) {
	dir := privateDir(t)
	cred, c := grant(t, dir, GrantRequest{Name: "axi"})
	a := NewAuthorizer(dir)
	got, err := a.Authenticate("Bearer " + cred)
	if err != nil || got.ID != c.ID || got.Name != "axi" {
		t.Fatalf("%v %+v", err, got)
	}
	legacy := strings.Repeat("ab", 32) // shape of api.token, send.token, approve.token
	for _, h := range []string{"", cred, "Basic " + cred, "Bearer " + legacy, "Bearer wsa_" + legacy, "bearer " + cred, "Bearer " + cred[:len(cred)-1] + "A", "Bearer " + strings.Replace(cred, c.ID, "00000000", 1)} {
		if h == "Bearer "+cred[:len(cred)-1]+"A" && strings.HasSuffix(cred, "A") {
			continue
		}
		_, err := a.Authenticate(h)
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeUnauthorized || e.Status != 401 || e.Help == "" {
			t.Fatalf("header %q: %v", h, err)
		}
		if strings.Contains(e.Message+e.Help, cred) {
			t.Fatal("credential echoed")
		}
	}
	_, err = a.Authenticate("Bearer " + legacy)
	var e *Error
	errors.As(err, &e)
	if !strings.Contains(e.Message, "legacy") {
		t.Fatalf("legacy message %q", e.Message)
	}
}

func TestRevokedAndExpiredRejected(t *testing.T) {
	dir := privateDir(t)
	cred, _ := grant(t, dir, GrantRequest{Name: "axi"})
	short, _ := grant(t, dir, GrantRequest{Name: "short", Expires: time.Hour})
	a := NewAuthorizer(dir)
	if _, err := a.Authenticate("Bearer " + short); err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := a.Authenticate("Bearer " + short); code(err) != CodeUnauthorized {
		t.Fatalf("expired accepted: %v", err)
	}
	a.now = time.Now
	c, err := a.Authenticate("Bearer " + cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Revoke(dir, "axi", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Authenticate("Bearer " + cred); code(err) != CodeUnauthorized {
		t.Fatalf("revoked accepted: %v", err)
	}
	// A client authenticated before revocation loses access immediately.
	if err = a.Authorize(c, ScopeReadChats, ""); code(err) != CodeUnauthorized {
		t.Fatalf("revoked client authorized: %v", err)
	}
	if _, err = Revoke(dir, "axi", time.Now()); err == nil {
		t.Fatal("double revoke")
	}
}

func TestEachScopeAllowedAndDenied(t *testing.T) {
	dir := privateDir(t)
	a := NewAuthorizer(dir)
	for i, scope := range AllScopes {
		name := "c" + string(rune('a'+i))
		req := GrantRequest{Name: name, Scopes: []string{scope}}
		if RequiresAllowlist(scope) {
			req.Chats = []string{chatA}
		}
		cred, _ := grant(t, dir, req)
		c, err := a.Authenticate("Bearer " + cred)
		if err != nil {
			t.Fatal(err)
		}
		if err = a.Authorize(c, scope, chatA); err != nil {
			t.Fatalf("%s denied: %v", scope, err)
		}
		for _, other := range AllScopes {
			if other == scope {
				continue
			}
			err = a.Authorize(c, other, chatA)
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeForbidden || e.Status != 403 {
				t.Fatalf("%s granted %s: %v", scope, other, err)
			}
			if !strings.Contains(e.Help, other) || !strings.Contains(e.Help, "wasync agents grant "+name) {
				t.Fatalf("help %q", e.Help)
			}
		}
		if err = a.Authorize(c, "destructive", chatA); code(err) != CodeForbidden {
			t.Fatal("destructive authorized")
		}
	}
}

func TestAllowlistEnforcement(t *testing.T) {
	dir := privateDir(t)
	a := NewAuthorizer(dir)
	cred, _ := grant(t, dir, GrantRequest{Name: "writer", Scopes: []string{"read.chats,read.messages,send"}, Chats: []string{chatA}})
	c, _ := a.Authenticate("Bearer " + cred)
	if err := a.Authorize(c, ScopeSend, chatA); err != nil {
		t.Fatal(err)
	}
	if err := a.Authorize(c, ScopeSend, chatB); code(err) != CodeForbidden {
		t.Fatalf("send outside allowlist: %v", err)
	}
	var e *Error
	errors.As(a.Authorize(c, ScopeSend, chatB), &e)
	if !strings.Contains(e.Help, "--chat "+chatB) || !strings.Contains(e.Help, "--chat "+chatA) {
		t.Fatalf("help %q", e.Help)
	}
	if err := a.Authorize(c, ScopeSend, ""); code(err) != CodeForbidden {
		t.Fatal("send without chat")
	}
	// Reads are restricted to the allowlist when one is set.
	if err := a.Authorize(c, ScopeReadMessages, chatB); code(err) != CodeForbidden {
		t.Fatal("read outside allowlist")
	}
	if err := a.Authorize(c, ScopeReadMessages, chatA); err != nil {
		t.Fatal(err)
	}
	if err := a.Authorize(c, ScopeReadChats, ""); err != nil {
		t.Fatal("list reads must be permitted and filtered by the handler")
	}
	if c.AllowsChat(chatB) || !c.AllowsChat(chatA) {
		t.Fatal("AllowsChat")
	}
	// Without an allowlist, reads cover every chat.
	reader, _ := grant(t, dir, GrantRequest{Name: "reader"})
	r, _ := a.Authenticate("Bearer " + reader)
	if err := a.Authorize(r, ScopeReadMessages, chatB); err != nil || !r.AllowsChat(chatB) {
		t.Fatal(err)
	}
}

func TestRateLimit(t *testing.T) {
	dir := privateDir(t)
	a := NewAuthorizer(dir)
	now := time.Now()
	a.now = func() time.Time { return now }
	cred, _ := grant(t, dir, GrantRequest{Name: "slow", RatePerMinute: 3})
	other, _ := grant(t, dir, GrantRequest{Name: "other", RatePerMinute: 3})
	c, _ := a.Authenticate("Bearer " + cred)
	o, _ := a.Authenticate("Bearer " + other)
	for i := 0; i < 3; i++ {
		if err := a.Authorize(c, ScopeReadChats, ""); err != nil {
			t.Fatal(err)
		}
	}
	err := a.Authorize(c, ScopeReadChats, "")
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeRateLimited || e.Status != 429 || e.RetryAfter <= 0 || e.RetryAfter > 20*time.Second || !strings.Contains(e.Help, "--rate 6") {
		t.Fatalf("%v %+v", err, e)
	}
	if err = a.Authorize(o, ScopeReadChats, ""); err != nil {
		t.Fatal("rate limit is per client")
	}
	now = now.Add(21 * time.Second)
	if err = a.Authorize(c, ScopeReadChats, ""); err != nil {
		t.Fatalf("bucket did not refill: %v", err)
	}
	if err = a.Authorize(c, ScopeReadChats, ""); code(err) != CodeRateLimited {
		t.Fatal("refilled too much")
	}
}

func readAudit(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	s := bufio.NewScanner(f)
	for s.Scan() {
		var m map[string]any
		if err := json.Unmarshal(s.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestAuditContentAndRotation(t *testing.T) {
	dir := privateDir(t)
	a := NewAuthorizer(dir)
	cred, _ := grant(t, dir, GrantRequest{Name: "writer", Scopes: []string{"read.messages,send"}, Chats: []string{chatA}})
	c, _ := a.Authenticate("Bearer " + cred)
	if err := a.AuthorizeAndAudit(c, ScopeSend, chatA, "MSGREF1", "req-1", true); err != nil {
		t.Fatal(err)
	}
	if err := a.AuthorizeAndAudit(c, ScopeSend, chatB, "", "req-2", true); code(err) != CodeForbidden {
		t.Fatal(err)
	}
	if err := a.AuthorizeAndAudit(c, ScopeReadMessages, chatA, "", "req-3", false); err != nil {
		t.Fatal(err)
	}
	if err := a.AuthorizeAndAudit(c, ScopeReadMessages, chatB, "", "req-4", false); code(err) != CodeForbidden {
		t.Fatal(err)
	}
	path := filepath.Join(dir, AuditFileName)
	entries := readAudit(t, path)
	if len(entries) != 3 {
		t.Fatalf("want 3 entries (allowed mutation, two denials), got %v", entries)
	}
	first := entries[0]
	if first["client_id"] != c.ID || first["client_name"] != "writer" || first["scope"] != "send" || first["chat"] != chatA || first["ref"] != "MSGREF1" || first["outcome"] != "allowed" || first["request_id"] != "req-1" {
		t.Fatalf("entry %v", first)
	}
	if ts, _ := time.Parse(time.RFC3339, first["time"].(string)); ts.IsZero() || !strings.HasSuffix(first["time"].(string), "Z") {
		t.Fatalf("time %v", first["time"])
	}
	if entries[1]["outcome"] != "denied" || entries[1]["error_code"] != "forbidden" || entries[2]["request_id"] != "req-4" {
		t.Fatalf("denials %v", entries[1:])
	}
	_, secret, _ := ParseCredential(cred)
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), hashSecret(secret)) || strings.Contains(string(raw), "body") || strings.Contains(string(raw), "text") {
		t.Fatal("audit leaks secrets or bodies")
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
			t.Fatal("audit log not private")
		}
	}
	log := NewAuditLog(dir)
	log.maxBytes = 1024
	for i := 0; i < 60; i++ {
		if err := log.Append(AuditEntry{ClientName: "writer", Scope: "send", Chat: chatA, Ref: strings.Repeat("r", 1000), Outcome: OutcomeAllowed}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{AuditFileName, AuditFileName + ".1", AuditFileName + ".2"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() > 1024 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, AuditFileName+".3")); err == nil {
		t.Fatal("kept more than two old files")
	}
	if entries := readAudit(t, filepath.Join(dir, AuditFileName)); len(entries[0]["ref"].(string)) != 256 {
		t.Fatal("fields not truncated")
	}
	if err := log.Append(AuditEntry{Outcome: "maybe"}); err == nil {
		t.Fatal("accepted invalid outcome")
	}
}

func TestReloadOnChange(t *testing.T) {
	dir := privateDir(t)
	a := NewAuthorizer(dir)
	if _, err := a.Authenticate("Bearer " + FormatCredential("01234567", strings.Repeat("A", 43))); code(err) != CodeUnauthorized {
		t.Fatalf("missing registry: %v", err)
	}
	cred, _ := grant(t, dir, GrantRequest{Name: "axi"})
	c, err := a.Authenticate("Bearer " + cred)
	if err != nil {
		t.Fatalf("grant not picked up: %v", err)
	}
	if err = a.Authorize(c, ScopeObserve, ""); code(err) != CodeForbidden {
		t.Fatal("observe granted by default")
	}
	// Replace with broader scopes: the same cached *Client sees new scopes.
	Revoke(dir, "axi", time.Now())
	cred2, _ := grant(t, dir, GrantRequest{Name: "axi", Scopes: []string{"read.chats,observe"}})
	if _, err = a.Authenticate("Bearer " + cred); code(err) != CodeUnauthorized {
		t.Fatal("old credential still valid")
	}
	c2, err := a.Authenticate("Bearer " + cred2)
	if err != nil || a.Authorize(c2, ScopeObserve, "") != nil {
		t.Fatalf("reload: %v", err)
	}
}

func TestConcurrentGrantAndReload(t *testing.T) {
	dir := privateDir(t)
	a := NewAuthorizer(dir)
	var wg sync.WaitGroup
	creds := make([]string, 8)
	for i := range creds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cred, _, err := Grant(dir, GrantRequest{Name: "c" + string(rune('a'+i))}, time.Now())
			if err != nil {
				t.Error(err)
			}
			creds[i] = cred
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Authenticate("Bearer " + FormatCredential("01234567", strings.Repeat("A", 43)))
		}()
	}
	wg.Wait()
	clients, err := List(dir)
	if err != nil || len(clients) != len(creds) {
		t.Fatalf("lost updates: %d %v", len(clients), err)
	}
	for _, cred := range creds {
		if _, err := a.Authenticate("Bearer " + cred); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMalformedRegistryFailsClosed(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, FileName)
	for _, body := range []string{"{", `{"version":2,"clients":[]}`, `{"version":1,"clients":[{"id":"zz","name":"x"}]}`, `{"version":1,"clients":[],"extra":1}`} {
		os.WriteFile(path, []byte(body), 0600)
		if _, err := List(dir); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOnDemandClientStartsWithNoChatsAndGainsOnlyApprovedAccess(t *testing.T) {
	dir := privateDir(t)
	cred, c := grant(t, dir, GrantRequest{Name: "jit", Scopes: []string{"read.chats,read.messages,send"}, OnDemand: true})
	if !c.OnDemand || !c.Restricted() || c.AllowsChat(chatA) || len(c.AllowedChats()) != 0 {
		t.Fatalf("fresh client %+v", c)
	}
	if _, _, err := Grant(dir, GrantRequest{Name: "plain", Scopes: []string{"send"}}, time.Now()); err == nil {
		t.Fatal("write scope without a chat and without --on-demand")
	}
	a := NewAuthorizer(dir)
	client, err := a.Authenticate("Bearer " + cred)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{ScopeReadMessages, ScopeSend} {
		err = a.Authorize(client, scope, chatA)
		var e *Error
		if !errors.As(err, &e) || e.Status != 403 || !strings.Contains(e.Help, "whatsapp-axi access request --chat "+chatA) {
			t.Fatalf("%s: %v", scope, err)
		}
	}
	if err = a.Authorize(client, ScopeReadChats, ""); err != nil {
		t.Fatalf("listing chats without a chat: %v", err)
	}
	if _, err = GrantChatAccess(dir, c.ID, chatA, []string{ScopeReadChats, ScopeReadMessages}, "req1", time.Now()); err != nil {
		t.Fatal(err)
	}
	// The running authorizer sees the grant without restarting, for read only.
	if err = a.Authorize(client, ScopeReadMessages, chatA); err != nil {
		t.Fatalf("read after grant: %v", err)
	}
	if code(a.Authorize(client, ScopeSend, chatA)) != CodeForbidden || code(a.Authorize(client, ScopeReadMessages, chatB)) != CodeForbidden {
		t.Fatal("grant reached send or another chat")
	}
	// Send is added separately and merges with the earlier read grant.
	updated, err := GrantChatAccess(dir, c.ID, chatA, []string{ScopeSend}, "req2", time.Now())
	if err != nil || len(updated.Grants) != 1 || strings.Join(updated.Grants[0].Scopes, ",") != "read.chats,read.messages,send" {
		t.Fatalf("merged %+v %v", updated, err)
	}
	if err = a.Authorize(client, ScopeSend, chatA); err != nil {
		t.Fatal(err)
	}
	if got := mustActive(t, dir, "jit").AllowedChats(); len(got) != 1 || got[0] != chatA {
		t.Fatalf("allowed %v", got)
	}
}

func TestGrantChatAccessRefusesWhatTheCredentialCannotDo(t *testing.T) {
	dir := privateDir(t)
	_, c := grant(t, dir, GrantRequest{Name: "reader", OnDemand: true})
	for _, scopes := range [][]string{{ScopeSend}, {ScopeCaptureManage}, {ScopeExport}, {}} {
		if _, err := GrantChatAccess(dir, c.ID, chatA, scopes, "r", time.Now()); err == nil {
			t.Fatalf("granted %v", scopes)
		}
	}
	if _, err := GrantChatAccess(dir, c.ID, "bad jid", []string{ScopeReadChats}, "r", time.Now()); err == nil {
		t.Fatal("granted an invalid chat")
	}
	if _, err := GrantChatAccess(dir, "deadbeef", chatA, []string{ScopeReadChats}, "r", time.Now()); err == nil {
		t.Fatal("granted to an unknown client")
	}
	if _, err := Revoke(dir, "reader", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := GrantChatAccess(dir, c.ID, chatA, []string{ScopeReadChats}, "r", time.Now()); err == nil {
		t.Fatal("granted to a revoked client")
	}
	_, e := grant(t, dir, GrantRequest{Name: "short", OnDemand: true, Expires: time.Minute})
	if _, err := GrantChatAccess(dir, e.ID, chatA, []string{ScopeReadChats}, "r", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("granted to an expired client")
	}
}

func TestAccessScopesStayWithinTheCredential(t *testing.T) {
	read := &Client{Scopes: []string{ScopeReadChats, ScopeReadMessages, ScopeSend, ScopeCaptureManage}}
	got, err := read.AccessScopes(false)
	if err != nil || strings.Join(got, ",") != "read.chats,read.messages" {
		t.Fatalf("read %v %v", got, err)
	}
	got, err = read.AccessScopes(true)
	if err != nil || strings.Join(got, ",") != "read.chats,read.messages,send" {
		t.Fatalf("send %v %v", got, err)
	}
	if _, err = (&Client{Scopes: []string{ScopeReadChats}}).AccessScopes(true); err == nil {
		t.Fatal("send requested without holding a send scope")
	}
	if _, err = (&Client{Scopes: []string{ScopeSend}}).AccessScopes(false); err == nil {
		t.Fatal("read requested without holding a read scope")
	}
}

func TestPreApprovedChatsKeepEveryScopeAndRateLimitCoversRequests(t *testing.T) {
	dir := privateDir(t)
	cred, _ := grant(t, dir, GrantRequest{Name: "mixed", Scopes: []string{"read.messages,send"}, Chats: []string{chatA}, OnDemand: true, RatePerMinute: 2})
	a := NewAuthorizer(dir)
	client, _ := a.Authenticate("Bearer " + cred)
	if a.Authorize(client, ScopeSend, chatA) != nil || a.Authorize(client, ScopeReadMessages, chatA) != nil {
		t.Fatal("pre-approved chat lost a scope")
	}
	if code(a.AllowAccessRequest(client)) != CodeRateLimited {
		t.Fatal("access requests are not rate limited")
	}
}

func mustActive(t *testing.T, dir, name string) *Client {
	t.Helper()
	c, err := ActiveByName(dir, name)
	if err != nil || c == nil {
		t.Fatalf("no client %s: %v", name, err)
	}
	return c
}
