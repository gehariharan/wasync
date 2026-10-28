package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/accessmode"
	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/storage"
)

// signed sends a request the way the CLI does, signed with token.
func (f accessFixture) signed(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	rand.Read(nonce)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("X-Wasync-Time", stamp)
	req.Header.Set("X-Wasync-Nonce", hex.EncodeToString(nonce))
	req.Header.Set("X-Wasync-Signature", SignCommand(token, stamp, hex.EncodeToString(nonce), method, path, b))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

func (f accessFixture) setMode(t *testing.T, body any, want int) {
	t.Helper()
	if code, out := f.signed(t, "POST", accessModePath, f.approver, body); code != want {
		t.Fatalf("set mode %v: got %d want %d: %s", body, code, want, out)
	}
}

func TestAccessModeIsHumanOnlyAndValidated(t *testing.T) {
	f := setupAccess(t)
	for name, token := range map[string]string{"agent credential": f.jit, "none": ""} {
		if c, _ := f.do(t, "POST", accessModePath, token, map[string]string{"mode": "all", "history": "now"}); c != 401 {
			t.Fatalf("%s changed the access mode: %d", name, c)
		}
	}
	sendToken, _ := loadNamedToken(f.dir, "send.token")
	if c, _ := f.signed(t, "POST", accessModePath, sendToken, map[string]string{"mode": "all", "history": "now"}); c != 401 {
		t.Fatalf("send credential changed the access mode: %d", c)
	}
	for _, bad := range []any{map[string]string{}, map[string]string{"mode": "all"}, map[string]string{"mode": "all", "history": "forever"}, map[string]string{"mode": "ask", "history": "now"}, map[string]string{"mode": "x"}, map[string]any{"mode": "ask", "extra": 1}} {
		f.setMode(t, bad, 400)
	}
	if _, err := os.Stat(filepath.Join(f.dir, accessmode.FileName)); !os.IsNotExist(err) {
		t.Fatal("an invalid request saved a mode")
	}
	code, out := f.signed(t, "GET", accessModePath, f.approver, nil)
	if code != 200 || !strings.Contains(string(out), `"mode":"ask"`) || !strings.Contains(string(out), `"chosen":false`) {
		t.Fatalf("default %d %s", code, out)
	}
}

func TestSyncAllEnablesChatsAndAutoApprovesReadSendReplyReactButNotEdit(t *testing.T) {
	f := setupAccess(t)
	rich, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "rich", Scopes: []string{"read.chats,read.messages,send,react,edit.own"}, OnDemand: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Ask each time (the default): even a clean match waits for the owner.
	waiting := f.ask(t, f.jit, map[string]any{"chat": familyChat}, 202)
	if waiting.Status != "pending" || f.group(t, familyChat).Enabled {
		t.Fatalf("default mode %+v", waiting)
	}
	if err = f.session.RejectAction(f.account, waiting.RequestID); err != nil {
		t.Fatal(err)
	}

	f.setMode(t, map[string]string{"mode": "all", "history": "now"}, 200)
	for _, id := range []string{familyChat, phoneChat, otherChat} {
		if g := f.group(t, id); !g.Enabled || g.HistoryRequest != "" {
			t.Fatalf("sync all (from now on) left %s as %+v", id, g)
		}
	}
	// A read request for the one chat it matched is approved at once, still as a recorded request.
	got := f.ask(t, f.jit, map[string]any{"match": "family"}, 200)
	if got.Status != "approved" || got.Chat != familyChat || got.History != "now" || strings.Join(got.Access, ",") != "read" {
		t.Fatalf("auto read %+v", got)
	}
	recorded := f.queue.Get(f.account, got.RequestID)
	if recorded == nil || recorded.Status != "completed" || !recorded.Access.Auto || recorded.Access.Chosen != familyChat {
		t.Fatalf("recorded request %+v", recorded)
	}
	if c, out := f.do(t, "GET", "/api/v1/chats/"+familyChat+"/messages", f.jit, nil); c != 200 || !strings.Contains(string(out), "synthetic family text") {
		t.Fatalf("read after auto approval %d %s", c, out)
	}
	if err = agentauth.NewAuthorizer(f.dir).Authorize(mustClient(t, f.dir, "jit"), agentauth.ScopeSend, familyChat); err == nil {
		t.Fatal("a read request was auto approved for send")
	}
	// Send (send, reply, react) is automatic too, within the scopes the credential holds; edit.own is not.
	sent := f.ask(t, rich, map[string]any{"chat": phoneChat, "send": true}, 200)
	if sent.Status != "approved" || strings.Join(sent.Access, ",") != "read,send" {
		t.Fatalf("auto send %+v", sent)
	}
	authz, client := agentauth.NewAuthorizer(f.dir), mustClient(t, f.dir, "rich")
	for _, scope := range []string{agentauth.ScopeReadMessages, agentauth.ScopeSend, agentauth.ScopeReact} {
		if err = authz.Authorize(client, scope, phoneChat); err != nil {
			t.Fatalf("%s after auto send approval: %v", scope, err)
		}
	}
	if err = authz.Authorize(client, agentauth.ScopeEditOwn, phoneChat); err == nil {
		t.Fatal("edit.own was approved automatically")
	}
	edit := f.ask(t, rich, map[string]any{"chat": otherChat, "send": true, "edit": true}, 202)
	if edit.Status != "pending" || strings.Join(edit.Access, ",") != "read,send,edit" {
		t.Fatalf("edit must wait: %+v", edit)
	}
	if err = authz.Authorize(client, agentauth.ScopeReadMessages, otherChat); err == nil {
		t.Fatal("a request waiting for the owner granted access")
	}
	if c := f.approve(t, edit.RequestID, map[string]string{"chat": otherChat, "history": "now"}); c != 200 {
		t.Fatalf("manual approval of edit: %d", c)
	}
	if err = authz.Authorize(mustClient(t, f.dir, "rich"), agentauth.ScopeEditOwn, otherChat); err != nil {
		t.Fatalf("edit after the owner approved: %v", err)
	}
	// Nothing matched, or several matched: the owner still decides.
	if none := f.ask(t, f.jit, map[string]any{"match": "nobody by that name"}, 202); none.Status != "pending" {
		t.Fatalf("no match %+v", none)
	}
	if err = f.store.SaveDiscoveredChat(context.Background(), f.account, storage.Group{ID: "120363000000000004@g.us", Name: "Family Two"}); err != nil {
		t.Fatal(err)
	}
	if many := f.ask(t, f.jit, map[string]any{"match": "family"}, 202); many.Status != "pending" {
		t.Fatalf("not a single match %+v", many)
	}
	audit, _ := os.ReadFile(filepath.Join(f.dir, agentauth.AuditFileName))
	if strings.Count(string(audit), `"event":"access_grant_auto"`) != 2 || strings.Count(string(audit), `"event":"access_grant"`) != 1 || !strings.Contains(string(audit), `"event":"access_mode"`) {
		t.Fatalf("audit %s", audit)
	}
	// Switching back stops new automatic approvals but keeps grants already given.
	f.setMode(t, map[string]string{"mode": "ask"}, 200)
	if again := f.ask(t, rich, map[string]any{"chat": familyChat}, 202); again.Status != "pending" {
		t.Fatalf("auto approval continued after switching back: %+v", again)
	}
	if c, _ := f.do(t, "GET", "/api/v1/chats/"+familyChat+"/messages", f.jit, nil); c != 200 {
		t.Fatalf("switching back revoked access: %d", c)
	}
}

func TestSyncAllFullHistoryAndDamagedSettingFallsBackToAsk(t *testing.T) {
	f := setupAccess(t)
	f.setMode(t, map[string]string{"mode": "all", "history": "full"}, 200)
	if g := f.group(t, phoneChat); !g.Enabled || g.HistoryRequest != "queued" {
		t.Fatalf("full history: %+v", g)
	}
	code, out := f.signed(t, "GET", accessModePath, f.approver, nil)
	if code != 200 || !strings.Contains(string(out), `"history":"full"`) || !strings.Contains(string(out), `"chosen":true`) {
		t.Fatalf("get %d %s", code, out)
	}
	if err := os.WriteFile(filepath.Join(f.dir, accessmode.FileName), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.ask(t, f.jit, map[string]any{"chat": familyChat}, 202); got.Status != "pending" {
		t.Fatalf("a damaged setting auto approved: %+v", got)
	}
}

func TestSyncAllKeepsAnEarlierPendingRequestPendingWhenItsCandidatesDiffer(t *testing.T) {
	f := setupAccess(t)
	earlier := f.ask(t, f.jit, map[string]any{"match": "family"}, 202)
	if earlier.Status != "pending" {
		t.Fatalf("earlier %+v", earlier)
	}
	if err := f.store.SaveGroups(context.Background(), f.account, []storage.Group{{ID: familyChat, Name: "Aunts"}, {ID: otherChat, Name: "Family Club"}}); err != nil {
		t.Fatal(err)
	}
	f.setMode(t, map[string]string{"mode": "all", "history": "now"}, 200)
	got := f.ask(t, f.jit, map[string]any{"match": "family"}, 202)
	if got.Status != "pending" || got.RequestID != earlier.RequestID || got.ExpiresAt == nil || got.ApproveWith != "wasync actions approve "+earlier.RequestID || got.Chat != "" {
		t.Fatalf("frozen candidates %+v", got)
	}
	if r := f.queue.Get(f.account, earlier.RequestID); r == nil || r.Status != approval.StatusPending || r.Access.Chosen != "" {
		t.Fatalf("request after sync all %+v", r)
	}
	if err := agentauth.NewAuthorizer(f.dir).Authorize(mustClient(t, f.dir, "jit"), agentauth.ScopeReadMessages, otherChat); err == nil {
		t.Fatal("a stale pending request granted a chat that was never chosen")
	}
}
