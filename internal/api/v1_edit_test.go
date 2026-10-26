package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (r *recorder) edit(_ context.Context, in whatsapp.EditInput) (whatsapp.SendResult, error) {
	return r.record(in)
}

func editPath(chat, id string) string { return "/api/v1/messages/" + writerRef(chat, id) + "/edit" }

// ownMessages adds the account's own messages to one@g.us: a fresh text, a
// text sent well outside the edit window, and a fresh image.
func (f fixture) ownMessages(t *testing.T) {
	t.Helper()
	now := time.Now()
	own := func(id, kind string, age time.Duration) storage.Message {
		return storage.Message{GroupID: "one@g.us", MessageID: id, SenderID: "test@lid", FromMe: true, Timestamp: now.Add(-age), Type: kind, Text: "mine " + id, Source: "live"}
	}
	if _, err := f.store.Apply(context.Background(), f.account, []storage.Message{own("fresh", "text", time.Minute), own("stale", "text", time.Hour), own("pic", "image", time.Minute)}); err != nil {
		t.Fatal(err)
	}
}

func TestV1EditAllowedAuditedWithoutBodies(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Edit: rec.edit})
	f.ownMessages(t)
	agent := f.grantWriter(t, "editor", []string{agentauth.ScopeEditOwn}, 0, "one@g.us")
	status, body := f.send(t, "POST", editPath("one@g.us", "fresh"), agent, `{"text":"`+secretBody+`"}`)
	if status != 200 || !strings.Contains(string(body), `"status":"server_acknowledged"`) {
		t.Fatalf("edit: %d %s", status, body)
	}
	if rec.count() != 1 {
		t.Fatalf("calls %d", rec.count())
	}
	in := rec.calls[0].(whatsapp.EditInput)
	if in.To != "one@g.us" || in.Target.MessageID != "fresh" || !in.Target.FromMe || in.Text != secretBody {
		t.Fatalf("input %+v", in)
	}
	audit := f.auditLog(t)
	if !strings.Contains(audit, `"scope":"edit.own"`) || !strings.Contains(audit, `"outcome":"allowed"`) || !strings.Contains(audit, writerRef("one@g.us", "fresh")) || strings.Contains(audit, secretBody) || strings.Contains(string(body), "mine fresh") {
		t.Fatalf("audit or response: %s %s", audit, body)
	}
}

func TestV1EditNeedsItsOwnScopeAndAllowlist(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Edit: rec.edit})
	f.ownMessages(t)
	sendReact := f.grantWriter(t, "sender", []string{agentauth.ScopeSend, agentauth.ScopeReact}, 0, "one@g.us")
	narrow := f.grantWriter(t, "narrow", []string{agentauth.ScopeEditOwn}, 0, "two@g.us")
	for _, tc := range []struct {
		name, token string
		status      int
		code        string
	}{
		{"no credential", "", 401, query.CodeUnauthorized},
		{"legacy api.token", f.token, 401, query.CodeUnauthorized},
		{"read-only agent", f.agent, 403, query.CodeForbidden},
		{"send and react scopes do not edit", sendReact, 403, query.CodeForbidden},
		{"chat outside allowlist", narrow, 403, query.CodeForbidden},
	} {
		status, body := f.send(t, "POST", editPath("one@g.us", "fresh"), tc.token, `{"text":"`+secretBody+`"}`)
		wantV1Error(t, status, body, tc.status, tc.code)
		if strings.Contains(string(body), secretBody) {
			t.Fatalf("%s echoed the body: %s", tc.name, body)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("denied request reached WhatsApp: %d", rec.count())
	}
	if strings.Count(f.auditLog(t), `"outcome":"denied"`) < 4 || strings.Contains(f.auditLog(t), secretBody) {
		t.Fatalf("denials not audited cleanly: %s", f.auditLog(t))
	}
	// An edit credential cannot send or react either.
	editor := f.grantWriter(t, "editor", []string{agentauth.ScopeEditOwn}, 0, "one@g.us")
	status, body := f.send(t, "POST", replyPath("one@g.us", "a"), editor, `{"text":"hi"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	status, body = f.send(t, "PUT", reactionPath("one@g.us", "a"), editor, `{"emoji":"👍"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
}

func TestV1EditRefusesWhatWhatsAppWouldRefuse(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Edit: rec.edit})
	f.ownMessages(t)
	agent := f.grantWriter(t, "editor", []string{agentauth.ScopeEditOwn}, agentauth.MaxRatePerMinute, "one@g.us")
	body := `{"text":"changed"}`
	// Someone else's message and a non-text message are not editable.
	for _, id := range []string{"a", "b", "pic"} {
		status, data := f.send(t, "POST", editPath("one@g.us", id), agent, body)
		e := wantV1Error(t, status, data, 409, query.CodeNotEditable)
		if id == "a" && !strings.Contains(e.Error, "your own") {
			t.Fatalf("not-own message: %+v", e)
		}
	}
	// A message outside the edit window reports a clear, distinct error.
	status, data := f.send(t, "POST", editPath("one@g.us", "stale"), agent, body)
	e := wantV1Error(t, status, data, 409, query.CodeEditWindowExpired)
	if !strings.Contains(e.Error, "allows editing a message for 20m0s") || !strings.Contains(e.Help, "send a new message") {
		t.Fatalf("expiry message: %+v", e)
	}
	// Unknown, foreign-account and malformed refs.
	status, data = f.send(t, "POST", editPath("one@g.us", "missing"), agent, body)
	wantV1Error(t, status, data, 404, query.CodeNotFound)
	for _, ref := range []string{"garbage", "m1_", "m1_garbage"} {
		status, data = f.send(t, "POST", "/api/v1/messages/"+ref+"/edit", agent, body)
		wantV1Error(t, status, data, 403, query.CodeForbidden)
	}
	if rec.count() != 0 {
		t.Fatalf("refused edit reached WhatsApp: %d", rec.count())
	}
}

func TestV1EditRateLimitedValidatesBodyAndOutcome(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Edit: rec.edit})
	f.ownMessages(t)
	slow := f.grantWriter(t, "slow", []string{agentauth.ScopeEditOwn}, 1, "one@g.us")
	if status, data := f.send(t, "POST", editPath("one@g.us", "fresh"), slow, `{"text":"one"}`); status != 200 {
		t.Fatalf("first: %d %s", status, data)
	}
	status, data := f.send(t, "POST", editPath("one@g.us", "fresh"), slow, `{"text":"two"}`)
	wantV1Error(t, status, data, 429, query.CodeRateLimited)

	agent := f.grantWriter(t, "editor", []string{agentauth.ScopeEditOwn}, agentauth.MaxRatePerMinute, "one@g.us")
	for _, body := range []string{``, `{`, `{}`, `{"text":" "}`, `{"text":"ok","extra":1}`, `{"text":"ok"} {"text":"ok"}`,
		`{"text":"` + strings.Repeat("x", whatsapp.MaxMessageText+1) + `"}`} {
		status, data = f.send(t, "POST", editPath("one@g.us", "fresh"), agent, body)
		wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		status, data = f.send(t, method, editPath("one@g.us", "fresh"), agent, "")
		wantV1Error(t, status, data, 405, query.CodeInvalidArgument)
	}
	if rec.count() != 1 {
		t.Fatalf("calls %d", rec.count())
	}

	offline := setup(t)
	offline.ownMessages(t)
	token := offline.grantWriter(t, "editor", []string{agentauth.ScopeEditOwn}, 0, "one@g.us")
	status, data = offline.send(t, "POST", editPath("one@g.us", "fresh"), token, `{"text":"hi"}`)
	wantV1Error(t, status, data, 503, query.CodeOwnerOffline)

	failing := &recorder{err: errors.New("send outcome unknown; check WhatsApp before retrying; do not retry automatically")}
	g := setupOps(t, whatsapp.Operations{Edit: failing.edit})
	g.ownMessages(t)
	token = g.grantWriter(t, "editor", []string{agentauth.ScopeEditOwn}, 0, "one@g.us")
	status, data = g.send(t, "POST", editPath("one@g.us", "fresh"), token, `{"text":"hi"}`)
	wantV1Error(t, status, data, 502, query.CodeSendFailed)
	if failing.count() != 1 {
		t.Fatalf("failed edit was retried: %d", failing.count())
	}
}

func TestLegacyEditIsSignedAndChecksTheTarget(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{Edit: rec.edit})
	f.ownMessages(t)
	for _, token := range []string{f.token, f.agent} {
		if status, _ := f.send(t, "POST", "/api/edit", token, `{"chat":"one@g.us","message_id":"fresh","text":"x"}`); status != 401 {
			t.Fatalf("unsigned edit accepted: %d", status)
		}
	}
	if status, data := f.signedLegacy(t, "/api/edit", `{"chat":"one@g.us","message_id":"fresh","text":"x"}`); status != 200 || !strings.Contains(string(data), "server_acknowledged") {
		t.Fatalf("signed edit: %d %s", status, data)
	}
	for body, want := range map[string]int{
		`{"chat":"one@g.us","message_id":"stale","text":"x"}`:           409,
		`{"chat":"one@g.us","message_id":"a","text":"x"}`:               409,
		`{"chat":"one@g.us","message_id":"pic","text":"x"}`:             409,
		`{"chat":"one@g.us","message_id":"missing","text":"x"}`:         404,
		`{"chat":"one@g.us","message_id":"fresh"}`:                      400,
		`{"chat":"one@g.us","message_id":"fresh","text":"x","extra":1}`: 400,
	} {
		if status, data := f.signedLegacy(t, "/api/edit", body); status != want {
			t.Fatalf("%s: %d %s", body, status, data)
		}
	}
	if rec.count() != 1 {
		t.Fatalf("refused edits reached WhatsApp: %d", rec.count())
	}
	offline := setup(t)
	offline.ownMessages(t)
	if status, _ := offline.signedLegacy(t, "/api/edit", `{"chat":"one@g.us","message_id":"fresh","text":"x"}`); status != 503 {
		t.Fatalf("offline: %d", status)
	}
}
