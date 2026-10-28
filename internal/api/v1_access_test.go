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

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/tui"
)

const (
	familyChat = "120363000000000003@g.us"
	phoneChat  = "15551234567@s.whatsapp.net"
	otherChat  = "15557654321@s.whatsapp.net"
)

// accessFixture is a real owner: archive, approval queue, and a session that
// applies approvals, with synthetic chats that no agent is allowed yet.
type accessFixture struct {
	store    *storage.Store
	account  int64
	queue    *approval.Queue
	session  *tui.Session
	server   *Server
	dir      string
	approver string // approve.token
	jit      string // on-demand credential: read.chats, read.messages, send
	other    string // a second on-demand credential
}

func setupAccess(t *testing.T) accessFixture {
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
	account, err := store.UseAccount(ctx, "synthetic@lid")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveGroups(ctx, account, []storage.Group{{ID: familyChat, Name: "Family Group"}, {ID: phoneChat, Name: phoneChat}, {ID: otherChat, Name: "+1∙∙∙∙21"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetEnabled(ctx, account, []string{familyChat}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Apply(ctx, account, []storage.Message{{GroupID: familyChat, MessageID: "m1", SenderID: "alice@lid", Timestamp: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Type: "text", Text: "synthetic family text", Source: "live"}}); err != nil {
		t.Fatal(err)
	}
	// Capture is switched off again so approving must turn it back on.
	if err = store.SetEnabled(ctx, account, []string{familyChat}, false); err != nil {
		t.Fatal(err)
	}
	queue := &approval.Queue{}
	session := tui.NewSession(ctx, store, nil)
	session.Approvals, session.DataDir = queue, dir
	server, err := StartControlled("127.0.0.1:0", dir, store, nil, queue, session.ApproveAction, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	approver, err := loadNamedToken(dir, "approve.token")
	if err != nil {
		t.Fatal(err)
	}
	jit, _, err := agentauth.Grant(dir, agentauth.GrantRequest{Name: "jit", Scopes: []string{"read.chats,read.messages,send"}, OnDemand: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := agentauth.Grant(dir, agentauth.GrantRequest{Name: "other", OnDemand: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return accessFixture{store, account, queue, session, server, dir, approver, jit, other}
}

func (f accessFixture) do(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.server.URL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

// approve is what `wasync actions approve` sends: a signed request with the choice.
func (f accessFixture) approve(t *testing.T, id string, body any) int {
	t.Helper()
	b := []byte(nil)
	if body != nil {
		b, _ = json.Marshal(body)
	}
	path := "/api/actions/" + id + "/approve"
	req, err := http.NewRequest("POST", f.server.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	rand.Read(nonce)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("X-Wasync-Time", stamp)
	req.Header.Set("X-Wasync-Nonce", hex.EncodeToString(nonce))
	req.Header.Set("X-Wasync-Signature", SignCommand(f.approver, stamp, hex.EncodeToString(nonce), "POST", path, b))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

type accessData struct {
	Data accessView `json:"data"`
}

func (f accessFixture) ask(t *testing.T, token string, body any, want int) accessView {
	t.Helper()
	code, out := f.do(t, "POST", "/api/v1/access-requests", token, body)
	if code != want {
		t.Fatalf("access request: got %d want %d: %s", code, want, out)
	}
	var got accessData
	_ = json.Unmarshal(out, &got)
	return got.Data
}

func (f accessFixture) status(t *testing.T, token, id string) (int, accessView) {
	t.Helper()
	code, out := f.do(t, "GET", "/api/v1/access-requests/"+id, token, nil)
	var got accessData
	_ = json.Unmarshal(out, &got)
	return code, got.Data
}

func (f accessFixture) group(t *testing.T, id string) storage.Group {
	t.Helper()
	groups, err := f.store.GroupSummaries(context.Background(), f.account)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("chat %s missing", id)
	return storage.Group{}
}

func TestAccessRequestGrantsOneChatOnlyAfterHumanApproval(t *testing.T) {
	f := setupAccess(t)
	// Nothing is allowed up front, and the denial points at the request command.
	code, body := f.do(t, "GET", "/api/v1/chats/"+familyChat+"/messages", f.jit, nil)
	var denied query.V1Error
	_ = json.Unmarshal(body, &denied)
	if code != 403 || !strings.Contains(denied.Help, "whatsapp-axi access request --chat "+familyChat) {
		t.Fatalf("denial %d %s", code, body)
	}
	// Asking by name queues a request and tells the agent nothing about the match.
	asked := f.ask(t, f.jit, map[string]any{"match": "family"}, 202)
	if asked.Status != "pending" || len(asked.RequestID) != 32 || strings.Join(asked.Access, ",") != "read" || asked.ApproveWith != "wasync actions approve "+asked.RequestID {
		t.Fatalf("asked %+v", asked)
	}
	_, raw := f.do(t, "GET", "/api/v1/access-requests/"+asked.RequestID, f.jit, nil)
	if strings.Contains(string(raw), "Family") || strings.Contains(string(raw), familyChat) {
		t.Fatalf("pending status leaked the matched chat: %s", raw)
	}
	if same := f.ask(t, f.jit, map[string]any{"match": " FAMILY "}, 202); same.RequestID != asked.RequestID {
		t.Fatal("repeat request prompted the human twice")
	}
	pending := f.queue.Pending(f.account)
	if pending == nil || pending.ID != asked.RequestID || pending.Access.Agent != "jit" || len(pending.Access.Candidates) != 1 || pending.Access.Candidates[0].Chat != familyChat {
		t.Fatalf("pending %+v", pending)
	}
	// Still nothing granted, capture stays off, and other agents cannot see the request.
	if c, _ := f.do(t, "GET", "/api/v1/chats/"+familyChat+"/messages", f.jit, nil); c != 403 {
		t.Fatalf("read before approval: %d", c)
	}
	if f.group(t, familyChat).Enabled {
		t.Fatal("asking enabled capture")
	}
	if c, _ := f.status(t, f.other, asked.RequestID); c != 404 {
		t.Fatalf("another agent saw the request: %d", c)
	}
	// Approving needs the signed human channel and a valid choice.
	if c, _ := f.do(t, "POST", "/api/actions/"+asked.RequestID+"/approve", f.jit, map[string]string{"chat": familyChat, "history": "full"}); c != 401 {
		t.Fatalf("agent credential approved its own request: %d", c)
	}
	if c := f.approve(t, asked.RequestID, nil); c != 400 {
		t.Fatalf("approval without a choice: %d", c)
	}
	if c := f.approve(t, asked.RequestID, map[string]string{"chat": otherChat, "history": "full"}); c != 409 {
		t.Fatalf("chat outside the candidates: %d", c)
	}
	if c := f.approve(t, asked.RequestID, map[string]string{"chat": familyChat, "history": "bogus"}); c != 409 {
		t.Fatalf("bad history depth: %d", c)
	}
	if f.queue.Pending(f.account) == nil || f.group(t, familyChat).Enabled {
		t.Fatal("a refused approval changed state")
	}
	if c := f.approve(t, asked.RequestID, map[string]string{"chat": familyChat, "history": "full"}); c != 200 {
		t.Fatalf("approval: %d", c)
	}
	if c := f.approve(t, asked.RequestID, map[string]string{"chat": familyChat, "history": "full"}); c == 200 {
		t.Fatal("approval replayed")
	}
	// Capture is on with a history request queued, and the agent can now read that chat only.
	g := f.group(t, familyChat)
	if !g.Enabled || g.HistoryRequest != "queued" {
		t.Fatalf("after full approval: enabled=%v history=%q", g.Enabled, g.HistoryRequest)
	}
	if code, st := f.status(t, f.jit, asked.RequestID); code != 200 || st.Status != "approved" || st.Chat != familyChat || st.History != "full" {
		t.Fatalf("status %d %+v", code, st)
	}
	if c, out := f.do(t, "GET", "/api/v1/chats/"+familyChat+"/messages", f.jit, nil); c != 200 || !strings.Contains(string(out), "synthetic family text") {
		t.Fatalf("read after approval: %d %s", c, out)
	}
	if c, _ := f.do(t, "GET", "/api/v1/chats/"+phoneChat+"/messages", f.jit, nil); c != 403 {
		t.Fatalf("grant leaked to another chat: %d", c)
	}
	_, list := f.do(t, "GET", "/api/v1/chats", f.jit, nil)
	if !strings.Contains(string(list), familyChat) || strings.Contains(string(list), phoneChat) {
		t.Fatalf("chat list %s", list)
	}
	if c, _ := f.do(t, "GET", "/api/v1/chats/"+familyChat+"/messages", f.other, nil); c != 403 {
		t.Fatalf("grant leaked to another agent: %d", c)
	}
	// Read was requested, so send must still be refused for that chat.
	clients, err := agentauth.List(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range clients {
		if c.Name == "jit" && (len(c.Grants) != 1 || strings.Join(c.Grants[0].Scopes, ",") != "read.chats,read.messages" || len(c.Chats) != 0) {
			t.Fatalf("grants %+v", c)
		}
	}
	if err = agentauth.NewAuthorizer(f.dir).Authorize(mustClient(t, f.dir, "jit"), agentauth.ScopeSend, familyChat); err == nil {
		t.Fatal("read approval allowed send")
	}
	audit, _ := io.ReadAll(mustOpen(t, filepath.Join(f.dir, agentauth.AuditFileName)))
	for _, event := range []string{"access_request", "access_grant"} {
		if !strings.Contains(string(audit), event) {
			t.Fatalf("audit lacks %s: %s", event, audit)
		}
	}
	if strings.Contains(string(audit), "synthetic family text") {
		t.Fatal("audit recorded message content")
	}
}

func TestAccessRequestByPhoneWithSendAndNowOnly(t *testing.T) {
	f := setupAccess(t)
	asked := f.ask(t, f.jit, map[string]any{"match": "+1 (555) 123-4567", "send": true}, 202)
	if strings.Join(asked.Access, ",") != "read,send" {
		t.Fatalf("access %v", asked.Access)
	}
	if c := f.approve(t, asked.RequestID, map[string]string{"chat": phoneChat, "history": "now"}); c != 200 {
		t.Fatalf("approval: %d", c)
	}
	g := f.group(t, phoneChat)
	if !g.Enabled || g.HistoryRequest != "" {
		t.Fatalf("from-now-on approval: enabled=%v history=%q", g.Enabled, g.HistoryRequest)
	}
	authz := agentauth.NewAuthorizer(f.dir)
	client := mustClient(t, f.dir, "jit")
	for _, scope := range []string{agentauth.ScopeReadMessages, agentauth.ScopeSend} {
		if err := authz.Authorize(client, scope, phoneChat); err != nil {
			t.Fatalf("%s after send approval: %v", scope, err)
		}
	}
	if err := authz.Authorize(client, agentauth.ScopeSend, otherChat); err == nil {
		t.Fatal("send approval reached another chat")
	}
	// A number that is not archived yet still resolves to a direct chat.
	fresh := f.ask(t, f.jit, map[string]any{"match": "447700900123"}, 202)
	pending := f.queue.Get(f.account, fresh.RequestID)
	if pending == nil || len(pending.Access.Candidates) != 1 || pending.Access.Candidates[0].Chat != "447700900123@s.whatsapp.net" {
		t.Fatalf("unarchived number: %+v", pending)
	}
	if c := f.approve(t, fresh.RequestID, map[string]string{"chat": "447700900123@s.whatsapp.net", "history": "now"}); c != 200 {
		t.Fatalf("approval of a new number: %d", c)
	}
	if !f.group(t, "447700900123@s.whatsapp.net").Enabled {
		t.Fatal("new direct chat not enabled")
	}
}

func TestAccessRequestRejectionAndLimits(t *testing.T) {
	f := setupAccess(t)
	asked := f.ask(t, f.jit, map[string]any{"chat": familyChat}, 202)
	if err := f.session.RejectAction(f.account, asked.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, st := f.status(t, f.jit, asked.RequestID); st.Status != "rejected" {
		t.Fatalf("status %+v", st)
	}
	if f.group(t, familyChat).Enabled {
		t.Fatal("rejection enabled capture")
	}
	if c := f.approve(t, asked.RequestID, map[string]string{"chat": familyChat, "history": "now"}); c == 200 {
		t.Fatal("rejected request approved")
	}
	if c, _ := f.do(t, "GET", "/api/v1/chats/"+familyChat+"/messages", f.jit, nil); c != 403 {
		t.Fatalf("read after rejection: %d", c)
	}
	// A request that matched nothing looks exactly like any other pending one,
	// so the agent learns nothing about chats it was not given.
	miss := f.ask(t, f.jit, map[string]any{"match": "nobody by that name"}, 202)
	if miss.Status != "pending" || len(miss.RequestID) != 32 || miss.ApproveWith == "" || miss.ExpiresAt == nil {
		t.Fatalf("no-match answer %+v", miss)
	}
	gone := f.ask(t, f.jit, map[string]any{"chat": "missing@g.us"}, 202)
	for _, v := range []accessView{miss, gone} {
		if code, st := f.status(t, f.jit, v.RequestID); code != 200 || st.Status != "pending" || st.Chat != "" {
			t.Fatalf("no-match status %d %+v", code, st)
		}
	}
	pendingMiss := f.queue.Get(f.account, miss.RequestID)
	if pendingMiss == nil || !pendingMiss.NoMatch() {
		t.Fatalf("queued %+v", pendingMiss)
	}
	if c := f.approve(t, miss.RequestID, map[string]string{"chat": familyChat, "history": "now"}); c == 200 {
		t.Fatal("a request that matched nothing was approved")
	}
	if f.group(t, familyChat).Enabled {
		t.Fatal("approving a no-match request enabled capture")
	}
	if err := f.session.RejectAction(f.account, miss.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, st := f.status(t, f.jit, miss.RequestID); st.Status != "rejected" {
		t.Fatalf("rejected no-match status %+v", st)
	}
	if err := f.session.RejectAction(f.account, gone.RequestID); err != nil {
		t.Fatal(err)
	}
	for _, body := range []any{map[string]any{}, map[string]any{"chat": familyChat, "match": "x"}, map[string]any{"chat": "not a jid"}, map[string]any{"match": "x", "extra": 1}} {
		f.ask(t, f.jit, body, 400)
	}
	// An agent may hold only a few unanswered requests.
	for _, n := range []string{"family", phoneChat, "nothing like it"} {
		f.ask(t, f.jit, map[string]any{"match": n}, 202)
	}
	f.ask(t, f.jit, map[string]any{"match": otherChat}, 429)
	// Already-allowed chats and unrestricted or unscoped credentials get plain answers.
	open, _, _ := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "everything"}, time.Now())
	f.ask(t, open, map[string]any{"chat": familyChat}, 400)
	narrow, _, _ := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "readonly", OnDemand: true}, time.Now())
	f.ask(t, narrow, map[string]any{"chat": familyChat, "send": true}, 403)
	pre, _, _ := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "pre", Chats: []string{familyChat}}, time.Now())
	if v := f.ask(t, pre, map[string]any{"chat": familyChat}, 200); v.Status != "already_allowed" {
		t.Fatalf("already allowed: %+v", v)
	}
	// Legacy tokens never reach the route, and the destructive endpoint cannot mint access requests.
	if c, _ := f.do(t, "POST", "/api/v1/access-requests", "", map[string]any{"chat": familyChat}); c != 401 {
		t.Fatalf("anonymous request: %d", c)
	}
	for _, r := range f.queue.PendingList(f.account) {
		if err := f.session.RejectAction(f.account, r.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLegacyActionEndpointRejectsAccessKind(t *testing.T) {
	in := approval.Input{Kind: approval.KindChatAccess, Chat: familyChat}
	if err := in.Validate(); err == nil {
		t.Fatal("destructive validation accepted an access request")
	}
	if _, err := (&approval.Queue{}).Add(1, in); err == nil {
		t.Fatal("Add queued an access request")
	}
}

func mustClient(t *testing.T, dir, name string) *agentauth.Client {
	t.Helper()
	c, err := agentauth.ActiveByName(dir, name)
	if err != nil || c == nil {
		t.Fatalf("client %s: %v", name, err)
	}
	return c
}

func mustOpen(t *testing.T, path string) io.Reader {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b)
}

// Direct chats are named after the saved contact (see the chat name field), so
// an agent can ask for a person by that name; the owner sees the candidate.
func TestAccessRequestFindsADirectChatByItsContactName(t *testing.T) {
	f := setupAccess(t)
	const uncle = "15559876543@s.whatsapp.net"
	if err := f.store.SaveDiscoveredChat(context.Background(), f.account, storage.Group{ID: uncle, Name: "RK Uncle"}); err != nil {
		t.Fatal(err)
	}
	asked := f.ask(t, f.jit, map[string]any{"match": "rk uncle"}, 202)
	pending := f.queue.Get(f.account, asked.RequestID)
	if pending == nil || len(pending.Access.Candidates) != 1 || pending.Access.Candidates[0] != (approval.Candidate{Chat: uncle, Name: "RK Uncle"}) {
		t.Fatalf("candidates %+v", pending)
	}
	if c := f.approve(t, asked.RequestID, map[string]string{"chat": uncle, "history": "now"}); c != 200 {
		t.Fatalf("approval: %d", c)
	}
	if !f.group(t, uncle).Enabled {
		t.Fatal("approved contact chat not enabled")
	}
}
