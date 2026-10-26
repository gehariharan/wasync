package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/msgref"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// secretBody must never reach the audit log or an error message.
const secretBody = "SECRET-BODY-4417"

func writerRef(chat, id string) string { return msgref.Encode("test@lid", chat, id) }

// grantWriter returns a credential holding scopes for chats.
func (f fixture) grantWriter(t *testing.T, name string, scopes []string, rate int, chats ...string) string {
	t.Helper()
	credential, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: name, Scopes: scopes, Chats: chats, RatePerMinute: rate}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func (f fixture) auditLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, agentauth.AuditFileName))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// recorder is a fake owner that records what it was asked to write.
type recorder struct {
	mu    sync.Mutex
	calls []any
	err   error
}

func (r *recorder) record(in any) (whatsapp.SendResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, in)
	if r.err != nil {
		return whatsapp.SendResult{Status: "unknown"}, r.err
	}
	return whatsapp.SendResult{ChatID: "one@g.us", MessageID: "NEW1", Status: "server_acknowledged"}, nil
}
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}
func (r *recorder) reply(_ context.Context, in whatsapp.ReplyInput) (whatsapp.SendResult, error) {
	return r.record(in)
}

func replyPath(chat, id string) string { return "/api/v1/messages/" + writerRef(chat, id) + "/reply" }

func TestV1ReplyAllowedAuditedWithoutBodies(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Reply: rec.reply})
	agent := f.grantWriter(t, "replier", []string{agentauth.ScopeSend}, 0, "one@g.us")
	status, body := f.send(t, "POST", replyPath("one@g.us", "a"), agent, `{"text":"`+secretBody+` @12345","mentions":["12345@s.whatsapp.net"]}`)
	var got struct {
		RequestID string              `json:"request_id"`
		Data      whatsapp.SendResult `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &got) != nil || got.Data.Status != "server_acknowledged" || got.Data.MessageID != "NEW1" || !requestIDPattern.MatchString(got.RequestID) {
		t.Fatalf("reply: %d %s", status, body)
	}
	if rec.count() != 1 {
		t.Fatalf("calls %d", rec.count())
	}
	in := rec.calls[0].(whatsapp.ReplyInput)
	if in.To != "one@g.us" || in.Target.MessageID != "a" || in.Target.SenderID != "bob@lid" || in.Text != secretBody+" @12345" || len(in.Mentions) != 1 {
		t.Fatalf("input %+v", in)
	}
	audit := f.auditLog(t)
	if !strings.Contains(audit, `"scope":"send"`) || !strings.Contains(audit, `"outcome":"allowed"`) || !strings.Contains(audit, `"chat":"one@g.us"`) || !strings.Contains(audit, writerRef("one@g.us", "a")) || !strings.Contains(audit, got.RequestID) {
		t.Fatalf("mutation not audited: %s", audit)
	}
	if strings.Contains(audit, secretBody) || strings.Contains(string(body), "hello archive") {
		t.Fatalf("body leaked: %s %s", audit, body)
	}
}

func TestV1ReplyDeniedWithoutScopeOrAllowlist(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Reply: rec.reply})
	reader := f.agent // default read-only scopes
	reactOnly := f.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, 0, "one@g.us")
	narrow := f.grantWriter(t, "narrow", []string{agentauth.ScopeSend}, 0, "one@g.us")
	req := `{"text":"` + secretBody + `"}`
	for _, tc := range []struct {
		name, token, path string
		status            int
		code              string
	}{
		{"no credential", "", replyPath("one@g.us", "a"), 401, query.CodeUnauthorized},
		{"legacy api.token", f.token, replyPath("one@g.us", "a"), 401, query.CodeUnauthorized},
		{"read-only agent", reader, replyPath("one@g.us", "a"), 403, query.CodeForbidden},
		{"other write scope", reactOnly, replyPath("one@g.us", "a"), 403, query.CodeForbidden},
		{"chat outside allowlist", narrow, replyPath("two@g.us", "c"), 403, query.CodeForbidden},
	} {
		status, body := f.send(t, "POST", tc.path, tc.token, req)
		wantV1Error(t, status, body, tc.status, tc.code)
		if strings.Contains(string(body), secretBody) {
			t.Fatalf("%s echoed the body: %s", tc.name, body)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("denied request reached WhatsApp: %d", rec.count())
	}
	audit := f.auditLog(t)
	if strings.Count(audit, `"outcome":"denied"`) < 4 || strings.Contains(audit, secretBody) {
		t.Fatalf("denials not audited cleanly: %s", audit)
	}
}

func TestV1ReplyRateLimited(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Reply: rec.reply})
	agent := f.grantWriter(t, "slow", []string{agentauth.ScopeSend}, 1, "one@g.us")
	if status, body := f.send(t, "POST", replyPath("one@g.us", "a"), agent, `{"text":"one"}`); status != 200 {
		t.Fatalf("first: %d %s", status, body)
	}
	req, err := http.NewRequest("POST", f.server.URL+replyPath("one@g.us", "b"), strings.NewReader(`{"text":"two"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+agent)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if seconds, convErr := strconv.Atoi(res.Header.Get("Retry-After")); res.StatusCode != 429 || convErr != nil || seconds < 1 {
		t.Fatalf("second: %d retry-after %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if rec.count() != 1 {
		t.Fatalf("rate-limited request reached WhatsApp: %d", rec.count())
	}
}

func TestV1ReplyRejectsBadRefsAndTargets(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Reply: rec.reply})
	agent := f.grantWriter(t, "replier", []string{agentauth.ScopeSend}, 0, "one@g.us")
	body := `{"text":"hello"}`
	// A ref that does not decode names no chat, so an allowlisted scope refuses it.
	for _, ref := range []string{"garbage", "m1_", "m1_garbage", writerRef("one@g.us", "a")[:12]} {
		status, data := f.send(t, "POST", "/api/v1/messages/"+ref+"/reply", agent, body)
		wantV1Error(t, status, data, 403, query.CodeForbidden)
	}
	// Well-formed refs that resolve to nothing in this account's archive.
	for _, ref := range []string{msgref.Encode("other@lid", "one@g.us", "a"), writerRef("one@g.us", "missing")} {
		status, data := f.send(t, "POST", "/api/v1/messages/"+ref+"/reply", agent, body)
		wantV1Error(t, status, data, 404, query.CodeNotFound)
	}
	// Deleted and reaction rows are not conversation messages.
	ctx := context.Background()
	stamp := time.Date(2026, 10, 1, 12, 5, 0, 0, time.UTC)
	if _, err := f.store.Apply(ctx, f.account, []storage.Message{
		{GroupID: "one@g.us", MessageID: "r1", SenderID: "alice@lid", Timestamp: stamp, Type: "reaction", Text: "👍", ReplyTo: "a", Source: "live"},
		{GroupID: "one@g.us", MessageID: "gone", SenderID: "alice@lid", Timestamp: stamp, Type: "text", Text: "bye", Source: "live"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Apply(ctx, f.account, []storage.Message{{GroupID: "one@g.us", MessageID: "gone", Kind: "delete", Type: "deleted", MutationAt: stamp, Timestamp: stamp, Source: "live"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gone", "r1"} {
		status, data := f.send(t, "POST", replyPath("one@g.us", id), agent, body)
		wantV1Error(t, status, data, 409, query.CodeTargetUnavailable)
	}
	if rec.count() != 0 {
		t.Fatalf("invalid target reached WhatsApp: %d", rec.count())
	}
}

func TestV1ReplyValidatesBody(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Reply: rec.reply})
	agent := f.grantWriter(t, "replier", []string{agentauth.ScopeSend}, agentauth.MaxRatePerMinute, "one@g.us")
	for _, body := range []string{
		``, `{`, `[]`, `{"text":"ok"} {"text":"two"}`,
		`{"text":"ok","unknown":1}`,
		`{"text":"   "}`, `{}`,
		`{"text":"` + strings.Repeat("x", whatsapp.MaxMessageText+1) + `"}`,
		`{"text":"no tag here","mentions":["12345@s.whatsapp.net"]}`,
		`{"text":"hi @12345","mentions":["12345@g.us"]}`,
		`{"text":"hi @12345","mentions":["not a jid"]}`,
		`{"text":"hi","mentions":[` + strings.Repeat(`"1@s.whatsapp.net",`, whatsapp.MaxMentions) + `"2@s.whatsapp.net"]}`,
	} {
		status, data := f.send(t, "POST", replyPath("one@g.us", "a"), agent, body)
		wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	}
	if rec.count() != 0 {
		t.Fatalf("invalid body reached WhatsApp: %d", rec.count())
	}
}

func TestV1ReplyOfflineAndFailure(t *testing.T) {
	offline := setup(t)
	agent := offline.grantWriter(t, "replier", []string{agentauth.ScopeSend}, 0, "one@g.us")
	status, data := offline.send(t, "POST", replyPath("one@g.us", "a"), agent, `{"text":"hi"}`)
	wantV1Error(t, status, data, 503, query.CodeOwnerOffline)

	rec := &recorder{err: errors.New("send outcome unknown; check WhatsApp before retrying; do not retry automatically")}
	f := setupOps(t, whatsapp.Operations{Reply: rec.reply})
	agent = f.grantWriter(t, "replier", []string{agentauth.ScopeSend}, 0, "one@g.us")
	status, data = f.send(t, "POST", replyPath("one@g.us", "a"), agent, `{"text":"`+secretBody+`"}`)
	e := wantV1Error(t, status, data, 502, query.CodeSendFailed)
	if !strings.Contains(e.Help, "do not retry automatically") || strings.Contains(string(data), secretBody) {
		t.Fatalf("failure: %s", data)
	}
	if rec.count() != 1 {
		t.Fatalf("failed send was retried: %d", rec.count())
	}
}

func TestV1ReplyOnlyAcceptsPost(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Reply: rec.reply})
	agent := f.grantWriter(t, "replier", []string{agentauth.ScopeSend}, 0, "one@g.us")
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		status, data := f.send(t, method, replyPath("one@g.us", "a"), agent, "")
		wantV1Error(t, status, data, 405, query.CodeInvalidArgument)
	}
	if rec.count() != 0 {
		t.Fatal("non-POST request reached WhatsApp")
	}
}

// signedLegacy posts a signed owner command with the send.token, as the CLI does.
func (f fixture) signedLegacy(t *testing.T, path, body string) (int, []byte) {
	t.Helper()
	token, err := loadNamedToken(f.dir, "send.token")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", f.server.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonceBytes := make([]byte, 16)
	if _, err = rand.Read(nonceBytes); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	req.Header.Set("X-Wasync-Time", stamp)
	req.Header.Set("X-Wasync-Nonce", nonce)
	req.Header.Set("X-Wasync-Signature", SignCommand(token, stamp, nonce, req.Method, path, []byte(body)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	return res.StatusCode, buf.Bytes()
}

func TestLegacyReplyIsSignedAndUsesArchivedTarget(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Reply: rec.reply})
	// Not signed: neither the read token nor an agent credential may reply.
	for _, token := range []string{f.token, f.agent} {
		status, _ := f.send(t, "POST", "/api/reply", token, `{"chat":"one@g.us","message_id":"a","text":"hi"}`)
		if status != 401 {
			t.Fatalf("unsigned reply accepted: %d", status)
		}
	}
	status, data := f.signedLegacy(t, "/api/reply", `{"chat":"one@g.us","message_id":"a","text":"hi"}`)
	if status != 200 || rec.count() != 1 || !strings.Contains(string(data), "server_acknowledged") {
		t.Fatalf("signed reply: %d %s", status, data)
	}
	in := rec.calls[0].(whatsapp.ReplyInput)
	if in.To != "one@g.us" || in.Target.MessageID != "a" || in.Target.SenderID != "bob@lid" {
		t.Fatalf("input %+v", in)
	}
	for _, body := range []string{`{"chat":"one@g.us","message_id":"missing","text":"hi"}`, `{"chat":"two@g.us","message_id":"a","text":"hi"}`} {
		if status, data = f.signedLegacy(t, "/api/reply", body); status != 404 {
			t.Fatalf("unarchived target: %d %s", status, data)
		}
	}
	for _, body := range []string{`{"chat":"one@g.us","message_id":"a"}`, `{"chat":"one@g.us","message_id":"a","text":"hi","extra":1}`, `{"chat":"nope","message_id":"a","text":"hi"}`} {
		if status, data = f.signedLegacy(t, "/api/reply", body); status != 400 {
			t.Fatalf("bad body %s: %d %s", body, status, data)
		}
	}
	if rec.count() != 1 {
		t.Fatalf("invalid requests reached WhatsApp: %d", rec.count())
	}
	offline := setup(t)
	if status, _ = offline.signedLegacy(t, "/api/reply", `{"chat":"one@g.us","message_id":"a","text":"hi"}`); status != 503 {
		t.Fatalf("offline reply: %d", status)
	}
}
