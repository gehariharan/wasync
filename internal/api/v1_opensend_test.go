package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/opensend"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/sentlog"
)

func TestSendToANumberIsRefusedUntilOpenSendingIsOn(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"+1 555 123 0000","text":"hello"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if !strings.Contains(string(body), "Open sending") || strings.Contains(string(body), "hello") {
		t.Fatalf("the refusal should say how to get access, and not echo the text: %s", body)
	}
	// A chat the archive has but the agent was not given is refused the same way.
	status, body = f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"two@g.us","text":"hello"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if rec.count() != 0 {
		t.Fatalf("a refused send reached WhatsApp: %+v", rec.calls)
	}
}

func TestOpenSendingLetsAnAgentWithTheSendScopeMessageAnyoneAndMarksIt(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	readOnly := f.agent
	if _, err := opensend.Start(f.dir, time.Now()); err != nil {
		t.Fatal(err)
	}

	// A new number, written the way a person writes it.
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"+1 (555) 123-0000","text":"hello"}`)
	if status != 200 {
		t.Fatalf("send to a number: %d %s", status, body)
	}
	// A chat the archive knows but the agent was not given.
	if status, body = f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"two@g.us","text":"hello again"}`); status != 200 {
		t.Fatalf("send to a known chat: %d %s", status, body)
	}
	// A chat the agent was given goes the ordinary way and is not counted.
	if status, body = f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"one@g.us","text":"and again"}`); status != 200 {
		t.Fatalf("send to an allowed chat: %d %s", status, body)
	}
	if rec.count() != 3 || rec.calls[0].To != "15551230000@s.whatsapp.net" {
		t.Fatalf("what reached WhatsApp: %+v", rec.calls)
	}
	byChat := map[string]sentlog.Entry{}
	for _, e := range f.sentEntries(t) {
		byChat[e.Chat] = e
	}
	if e := byChat["15551230000@s.whatsapp.net"]; !e.Open || !e.NewNumber || e.Actor != "poster" {
		t.Fatalf("a new number should be marked open and new: %+v", e)
	}
	if e := byChat["two@g.us"]; !e.Open || e.NewNumber {
		t.Fatalf("a known chat should be marked open but not new: %+v", e)
	}
	if e := byChat["one@g.us"]; e.Open || e.NewNumber {
		t.Fatalf("an allowed chat is not an open send: %+v", e)
	}
	if audit := f.auditLog(t); !strings.Contains(audit, `"event":"send_open"`) || !strings.Contains(audit, "15551230000@s.whatsapp.net") {
		t.Fatalf("open sending was not audited: %s", audit)
	}
	// Open sending widens where an agent may send, never who may send.
	status, body = f.send(t, "POST", "/api/v1/messages", readOnly, `{"to":"15551230001","text":"hello"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if rec.count() != 3 {
		t.Fatalf("an agent without the send scope sent: %d", rec.count())
	}
}

func TestOpenSendingStopsWhenSwitchedOffOrTimedOut(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	if _, err := opensend.Start(f.dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"x"}`); status != 200 {
		t.Fatalf("while on: %d %s", status, body)
	}
	if err := opensend.Stop(f.dir); err != nil {
		t.Fatal(err)
	}
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230002","text":"x"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if err := opensend.Save(f.dir, opensend.State{StartedAt: time.Now().Add(-2 * time.Hour), Until: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	status, body = f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230002","text":"x"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if rec.count() != 1 {
		t.Fatalf("sends after it was off or expired: %d", rec.count())
	}
}

func TestOpenSendingCapsMessagesAndNewNumbersPerHour(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	if _, err := opensend.Start(f.dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	number := func(i int) string { return fmt.Sprintf(`{"to":"1555123%04d","text":"x"}`, i) }
	for i := 0; i < opensend.MaxNewNumbers; i++ {
		if status, body := f.send(t, "POST", "/api/v1/messages", agent, number(i)); status != 200 {
			t.Fatalf("new number %d: %d %s", i, status, body)
		}
	}
	status, body := f.send(t, "POST", "/api/v1/messages", agent, number(opensend.MaxNewNumbers))
	wantV1Error(t, status, body, 429, query.CodeRateLimited)
	if !strings.Contains(string(body), "new numbers") {
		t.Fatalf("the refusal should say which cap: %s", body)
	}
	// A number already messaged this hour, and a chat the archive knows, still go.
	for i := opensend.MaxNewNumbers; i < opensend.MaxMessages; i++ {
		target := number(0)
		if i%2 == 1 {
			target = `{"chat":"two@g.us","text":"x"}`
		}
		if status, body = f.send(t, "POST", "/api/v1/messages", agent, target); status != 200 {
			t.Fatalf("message %d: %d %s", i, status, body)
		}
	}
	status, body = f.send(t, "POST", "/api/v1/messages", agent, number(0))
	wantV1Error(t, status, body, 429, query.CodeRateLimited)
	if !strings.Contains(string(body), "messages an hour") {
		t.Fatalf("the refusal should say which cap: %s", body)
	}
	if rec.count() != opensend.MaxMessages {
		t.Fatalf("messages sent: %d, want %d", rec.count(), opensend.MaxMessages)
	}
}

func TestOpenSendingCapsCountOnlyTheLastHour(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	if _, err := opensend.Start(f.dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	log := sentlog.New(f.dir)
	for i := 0; i < opensend.MaxMessages; i++ {
		e := sentlog.Entry{Time: time.Now().Add(-2 * time.Hour), Actor: "poster", Kind: "send", Chat: fmt.Sprintf("1555000%04d@s.whatsapp.net", i), Outcome: sentlog.OutcomeSent, Open: true, NewNumber: true}
		if err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551239999","text":"x"}`); status != 200 {
		t.Fatalf("older sends should not count: %d %s", status, body)
	}
}

func TestSendToRejectsBadNumbersAndBothTargets(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	if _, err := opensend.Start(f.dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"to":"12345","text":"x"}`,
		`{"to":"0015551230000","text":"x"}`,
		`{"to":"call me maybe","text":"x"}`,
		`{"to":"15551230000@s.whatsapp.net","text":"x"}`,
		`{"to":"15551230000","chat":"one@g.us","text":"x"}`,
		`{"to":"15551230000"}`,
	} {
		if status, out := f.send(t, "POST", "/api/v1/messages", agent, body); status != 400 && status != 403 {
			t.Fatalf("%s: %d %s", body, status, out)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("a bad request reached WhatsApp: %+v", rec.calls)
	}
}

func TestNumberJID(t *testing.T) {
	for in, want := range map[string]string{
		"+15551230000": "15551230000@s.whatsapp.net", "1 555 123 0000": "15551230000@s.whatsapp.net", "(555) 123-4567": "5551234567@s.whatsapp.net",
		"": "", "0551234567": "", "123456": "", "1234567890123456": "", "+1555abc0000": "", "15551230000@s.whatsapp.net": "",
	} {
		if got, ok := numberJID(in); got != want || ok != (want != "") {
			t.Errorf("numberJID(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestSendToIsListedAsAFeature(t *testing.T) {
	f := setupSend(t, &sendRecorder{})
	status, body := f.send(t, "GET", "/api/v1/version", f.agent, "")
	if status != 200 || !strings.Contains(string(body), `"write.send.to"`) {
		t.Fatalf("version: %d %s", status, body)
	}
}
