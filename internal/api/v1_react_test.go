package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

func (r *recorder) react(_ context.Context, in whatsapp.ReactInput) (whatsapp.SendResult, error) {
	return r.record(in)
}

func reactionPath(chat, id string) string {
	return "/api/v1/messages/" + writerRef(chat, id) + "/reaction"
}

func TestV1ReactSetsAndClearsAndIsAudited(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{React: rec.react})
	agent := f.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, 0, "one@g.us")
	status, body := f.send(t, "PUT", reactionPath("one@g.us", "a"), agent, `{"emoji":"👍"}`)
	if status != 200 || !strings.Contains(string(body), `"status":"server_acknowledged"`) {
		t.Fatalf("set: %d %s", status, body)
	}
	status, body = f.send(t, "DELETE", reactionPath("one@g.us", "a"), agent, "")
	if status != 200 {
		t.Fatalf("clear: %d %s", status, body)
	}
	if rec.count() != 2 {
		t.Fatalf("calls %d", rec.count())
	}
	set, clear := rec.calls[0].(whatsapp.ReactInput), rec.calls[1].(whatsapp.ReactInput)
	if set.Emoji != "👍" || set.To != "one@g.us" || set.Target.MessageID != "a" || clear.Emoji != "" || clear.Target.MessageID != "a" {
		t.Fatalf("inputs %+v %+v", set, clear)
	}
	audit := f.auditLog(t)
	if strings.Count(audit, `"scope":"react"`) != 2 || strings.Count(audit, `"outcome":"allowed"`) != 2 || !strings.Contains(audit, writerRef("one@g.us", "a")) || strings.Contains(audit, "👍") {
		t.Fatalf("audit: %s", audit)
	}
}

func TestV1ReactNeedsItsOwnScopeAndAllowlist(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{React: rec.react})
	replyOnly := f.grantWriter(t, "replier", []string{agentauth.ScopeSend, agentauth.ScopeEditOwn}, 0, "one@g.us")
	narrow := f.grantWriter(t, "narrow", []string{agentauth.ScopeReact}, 0, "one@g.us")
	for _, tc := range []struct {
		name, token, path string
		status            int
		code              string
	}{
		{"no credential", "", reactionPath("one@g.us", "a"), 401, query.CodeUnauthorized},
		{"legacy api.token", f.token, reactionPath("one@g.us", "a"), 401, query.CodeUnauthorized},
		{"read-only agent", f.agent, reactionPath("one@g.us", "a"), 403, query.CodeForbidden},
		{"send and edit scopes do not react", replyOnly, reactionPath("one@g.us", "a"), 403, query.CodeForbidden},
		{"chat outside allowlist", narrow, reactionPath("two@g.us", "c"), 403, query.CodeForbidden},
	} {
		for _, call := range []struct{ method, body string }{{"PUT", `{"emoji":"👍"}`}, {"DELETE", ""}} {
			status, body := f.send(t, call.method, tc.path, tc.token, call.body)
			wantV1Error(t, status, body, tc.status, tc.code)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("denied request reached WhatsApp: %d", rec.count())
	}
	if strings.Count(f.auditLog(t), `"outcome":"denied"`) < 8 {
		t.Fatalf("denials not audited: %s", f.auditLog(t))
	}
	// A reaction credential cannot send or edit either.
	status, body := f.send(t, "POST", replyPath("one@g.us", "a"), narrow, `{"text":"hi"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
}

func TestV1ReactRateLimitedAndBadTargets(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{React: rec.react})
	slow := f.grantWriter(t, "slow", []string{agentauth.ScopeReact}, 1, "one@g.us")
	if status, body := f.send(t, "PUT", reactionPath("one@g.us", "a"), slow, `{"emoji":"👍"}`); status != 200 {
		t.Fatalf("first: %d %s", status, body)
	}
	status, body := f.send(t, "PUT", reactionPath("one@g.us", "b"), slow, `{"emoji":"👍"}`)
	wantV1Error(t, status, body, 429, query.CodeRateLimited)

	agent := f.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, agentauth.MaxRatePerMinute, "one@g.us")
	for _, ref := range []string{"garbage", "m1_", "m1_garbage"} {
		status, body = f.send(t, "PUT", "/api/v1/messages/"+ref+"/reaction", agent, `{"emoji":"👍"}`)
		wantV1Error(t, status, body, 403, query.CodeForbidden)
	}
	status, body = f.send(t, "PUT", "/api/v1/messages/"+writerRef("one@g.us", "missing")+"/reaction", agent, `{"emoji":"👍"}`)
	wantV1Error(t, status, body, 404, query.CodeNotFound)
	status, body = f.send(t, "DELETE", "/api/v1/messages/"+writerRef("one@g.us", "missing")+"/reaction", agent, "")
	wantV1Error(t, status, body, 404, query.CodeNotFound)
	if rec.count() != 1 {
		t.Fatalf("calls %d", rec.count())
	}
}

func TestV1ReactValidatesBodyMethodsAndOutcome(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{React: rec.react})
	agent := f.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, agentauth.MaxRatePerMinute, "one@g.us")
	for _, body := range []string{``, `{`, `{}`, `{"emoji":""}`, `{"emoji":"hi"}`, `{"emoji":"1"}`, `{"emoji":":)"}`, `{"emoji":"👍 "}`,
		`{"emoji":"` + strings.Repeat("👍", 9) + `"}`, `{"emoji":"👍","extra":1}`, `{"emoji":"👍"} {"emoji":"👍"}`} {
		status, data := f.send(t, "PUT", reactionPath("one@g.us", "a"), agent, body)
		wantV1Error(t, status, data, 400, query.CodeInvalidArgument)
	}
	for _, method := range []string{"GET", "POST"} {
		status, data := f.send(t, method, reactionPath("one@g.us", "a"), agent, "")
		wantV1Error(t, status, data, 405, query.CodeInvalidArgument)
	}
	if rec.count() != 0 {
		t.Fatalf("invalid request reached WhatsApp: %d", rec.count())
	}
	for _, emoji := range []string{"❤️", "1️⃣", "👍🏽", "🙏"} {
		if status, data := f.send(t, "PUT", reactionPath("one@g.us", "a"), agent, `{"emoji":"`+emoji+`"}`); status != 200 {
			t.Fatalf("%s rejected: %d %s", emoji, status, data)
		}
	}

	offline := setup(t)
	token := offline.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, 0, "one@g.us")
	status, data := offline.send(t, "PUT", reactionPath("one@g.us", "a"), token, `{"emoji":"👍"}`)
	wantV1Error(t, status, data, 503, query.CodeOwnerOffline)

	failing := &recorder{err: errors.New("send outcome unknown; check WhatsApp before retrying; do not retry automatically")}
	g := setupOps(t, whatsapp.Operations{React: failing.react})
	token = g.grantWriter(t, "reactor", []string{agentauth.ScopeReact}, 0, "one@g.us")
	status, data = g.send(t, "PUT", reactionPath("one@g.us", "a"), token, `{"emoji":"👍"}`)
	wantV1Error(t, status, data, 502, query.CodeSendFailed)
	if failing.count() != 1 {
		t.Fatalf("failed reaction was retried: %d", failing.count())
	}
}

func TestLegacyReactIsSignedAndUsesArchivedTarget(t *testing.T) {
	rec := &recorder{}
	f := setupOps(t, whatsapp.Operations{React: rec.react})
	for _, token := range []string{f.token, f.agent} {
		if status, _ := f.send(t, "POST", "/api/react", token, `{"chat":"one@g.us","message_id":"a","emoji":"👍"}`); status != 401 {
			t.Fatalf("unsigned reaction accepted: %d", status)
		}
	}
	if status, data := f.signedLegacy(t, "/api/react", `{"chat":"one@g.us","message_id":"a","emoji":"👍"}`); status != 200 || !strings.Contains(string(data), "server_acknowledged") {
		t.Fatalf("set: %d %s", status, data)
	}
	if status, data := f.signedLegacy(t, "/api/react", `{"chat":"one@g.us","message_id":"a","remove":true}`); status != 200 {
		t.Fatalf("remove: %d %s", status, data)
	}
	if rec.count() != 2 || rec.calls[0].(whatsapp.ReactInput).Emoji != "👍" || rec.calls[1].(whatsapp.ReactInput).Emoji != "" {
		t.Fatalf("calls %+v", rec.calls)
	}
	for body, want := range map[string]int{
		`{"chat":"one@g.us","message_id":"missing","emoji":"👍"}`:         404,
		`{"chat":"one@g.us","message_id":"a"}`:                           400,
		`{"chat":"one@g.us","message_id":"a","emoji":"👍","remove":true}`: 400,
		`{"chat":"one@g.us","message_id":"a","emoji":"words"}`:           400,
		`{"chat":"one@g.us","message_id":"a","emoji":"👍","extra":1}`:     400,
	} {
		if status, data := f.signedLegacy(t, "/api/react", body); status != want {
			t.Fatalf("%s: %d %s", body, status, data)
		}
	}
	if rec.count() != 2 {
		t.Fatalf("invalid requests reached WhatsApp: %d", rec.count())
	}
	if status, _ := setup(t).signedLegacy(t, "/api/react", `{"chat":"one@g.us","message_id":"a","emoji":"👍"}`); status != 503 {
		t.Fatalf("offline: %d", status)
	}
}
