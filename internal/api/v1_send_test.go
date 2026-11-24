package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// sendRecorder stands in for the owner's WhatsApp connection.
type sendRecorder struct {
	mu    sync.Mutex
	calls []whatsapp.SendInput
	err   error
	id    string
}

func (s *sendRecorder) send(_ context.Context, in whatsapp.SendInput) (whatsapp.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, in)
	if s.err != nil {
		return whatsapp.SendResult{ChatID: in.To, MessageID: s.id, Status: "unknown"}, s.err
	}
	return whatsapp.SendResult{ChatID: in.To, MessageID: "SENT1", Status: "server_acknowledged"}, nil
}

func (s *sendRecorder) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// setupSend starts an owner whose WhatsApp send is replaced by rec (nil: offline).
func setupSend(t *testing.T, rec *sendRecorder) fixture {
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
	owner := Owner{Addr: "127.0.0.1:0", Dir: dir, Store: store}
	if rec != nil {
		owner.Sender = rec.send
	}
	srv, err := StartOwner(owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
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

func (f fixture) sentEntries(t *testing.T) []sentlog.Entry {
	t.Helper()
	entries, err := sentlog.New(f.dir).Recent(50)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestV1SendDeliversToAnAllowedChatAndRecordsWhoSentIt(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"one@g.us","text":"`+secretBody+`"}`)
	var got struct {
		RequestID string              `json:"request_id"`
		Data      whatsapp.SendResult `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &got) != nil || got.Data.Status != "server_acknowledged" || got.Data.MessageID != "SENT1" || !requestIDPattern.MatchString(got.RequestID) {
		t.Fatalf("send: %d %s", status, body)
	}
	if rec.count() != 1 || rec.calls[0].To != "one@g.us" || rec.calls[0].Text != secretBody || rec.calls[0].File != "" {
		t.Fatalf("what reached WhatsApp: %+v", rec.calls)
	}
	entries := f.sentEntries(t)
	if len(entries) != 1 || entries[0].Actor != "poster" || entries[0].Kind != "send" || entries[0].Chat != "one@g.us" || entries[0].MessageID != "SENT1" || entries[0].Outcome != sentlog.OutcomeSent {
		t.Fatalf("sent log: %+v", entries)
	}
	audit := f.auditLog(t)
	if !strings.Contains(audit, `"scope":"send"`) || !strings.Contains(audit, `"outcome":"allowed"`) || !strings.Contains(audit, `"chat":"one@g.us"`) {
		t.Fatalf("send not audited: %s", audit)
	}
	if strings.Contains(audit, secretBody) {
		t.Fatalf("the message text reached the audit log: %s", audit)
	}
	raw, err := os.ReadFile(filepath.Join(f.dir, sentlog.FileName))
	if err != nil || strings.Contains(string(raw), secretBody) {
		t.Fatalf("the message text reached the sent log: %s", raw)
	}
}

func TestV1SendIsRefusedWithoutTheScopeTheChatOrACredential(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	narrow := f.grantWriter(t, "narrow", []string{agentauth.ScopeSend}, 0, "one@g.us")
	reactOnly := f.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, 0, "one@g.us")
	for _, tc := range []struct {
		name, token, body string
		status            int
		code              string
	}{
		{"no credential", "", `{"chat":"one@g.us","text":"x"}`, 401, query.CodeUnauthorized},
		{"legacy api token", f.token, `{"chat":"one@g.us","text":"x"}`, 401, query.CodeUnauthorized},
		{"read-only agent", f.agent, `{"chat":"one@g.us","text":"x"}`, 403, query.CodeForbidden},
		{"other write scope", reactOnly, `{"chat":"one@g.us","text":"x"}`, 403, query.CodeForbidden},
		{"chat outside the allowlist", narrow, `{"chat":"two@g.us","text":"x"}`, 403, query.CodeForbidden},
		{"a number the agent was never given", narrow, `{"chat":"15551230000@s.whatsapp.net","text":"x"}`, 403, query.CodeForbidden},
		{"no chat named", narrow, `{"text":"x"}`, 403, query.CodeForbidden},
	} {
		status, body := f.send(t, "POST", "/api/v1/messages", tc.token, tc.body)
		wantV1Error(t, status, body, tc.status, tc.code)
		if strings.Contains(string(body), `"text"`) {
			t.Fatalf("%s: the error echoed the request: %s", tc.name, body)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("a refused send reached WhatsApp: %d", rec.count())
	}
	if len(f.sentEntries(t)) != 0 {
		t.Fatal("a refused send was logged as sent")
	}
}

func TestV1SendRejectsABadBodyAndTheWrongMethod(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	for _, body := range []string{`{"chat":"one@g.us"}`, `{"chat":"one@g.us","text":"   "}`, `{"chat":"one@g.us","text":"x","file":"/etc/passwd"}`} {
		status, data := f.send(t, "POST", "/api/v1/messages", agent, body)
		wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	}
	// A body whose chat cannot be read fails closed, before any strict decoding.
	for _, body := range []string{`{"chat":"one@g.us","text":"x"} {}`, `not json`} {
		status, data := f.send(t, "POST", "/api/v1/messages", agent, body)
		wantV1Error(t, status, data, 403, query.CodeForbidden)
	}
	if status, data := f.send(t, "PUT", "/api/v1/messages", agent, `{"chat":"one@g.us","text":"x"}`); status != 405 {
		t.Fatalf("PUT: %d %s", status, data)
	}
	if rec.count() != 0 {
		t.Fatalf("a bad request reached WhatsApp: %d", rec.count())
	}
}

func TestV1SendReportsAFailedSendAndNeverRetriesIt(t *testing.T) {
	rec := &sendRecorder{err: errors.New("send outcome unknown; check WhatsApp before retrying; do not retry automatically"), id: "LOST1"}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"one@g.us","text":"hi"}`)
	wantV1Error(t, status, body, 502, query.CodeSendFailed)
	if rec.count() != 1 {
		t.Fatalf("a failed send was retried: %d", rec.count())
	}
	entries := f.sentEntries(t)
	if len(entries) != 1 || entries[0].Outcome != sentlog.OutcomeUnknown || entries[0].MessageID != "LOST1" || entries[0].Code != query.CodeSendFailed {
		t.Fatalf("an unknown outcome was not recorded as unknown: %+v", entries)
	}
}

func TestV1SendNeedsARunningOwner(t *testing.T) {
	f := setupSend(t, nil)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"one@g.us","text":"hi"}`)
	wantV1Error(t, status, body, 503, query.CodeOwnerOffline)
}

func TestV1SendIsListedAsAFeature(t *testing.T) {
	f := setupSend(t, &sendRecorder{})
	var got struct {
		Data struct {
			Features []string `json:"features"`
		} `json:"data"`
	}
	status, body := f.send(t, "GET", "/api/v1/version", f.agent, "")
	if status != 200 || json.Unmarshal(body, &got) != nil {
		t.Fatalf("version: %d %s", status, body)
	}
	for _, name := range got.Data.Features {
		if name == "write.send" {
			return
		}
	}
	t.Fatalf("write.send is not advertised: %v", got.Data.Features)
}

func TestRecordSentClassifiesTheOutcome(t *testing.T) {
	dir := t.TempDir()
	h := &handler{sent: sentlog.New(dir)}
	h.recordSent(sentlog.Owner, "send", "a@g.us", whatsapp.SendResult{MessageID: "m1"}, nil)
	h.recordSent(sentlog.Owner, "send", "a@g.us", whatsapp.SendResult{MessageID: "m2"}, errors.New("no ack"))
	h.recordSent(sentlog.Owner, "send", "a@g.us", whatsapp.SendResult{}, errors.New("not connected"))
	(&handler{}).recordSent("x", "send", "a@g.us", whatsapp.SendResult{}, nil) // no log: must not panic
	got, err := sentlog.New(dir).Recent(10)
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if got[2].Outcome != sentlog.OutcomeSent || got[1].Outcome != sentlog.OutcomeUnknown || got[0].Outcome != sentlog.OutcomeFailed {
		t.Fatalf("outcomes: %+v", got)
	}
}
