package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/opensend"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/sendlist"
	"github.com/gehariharan/wasync/internal/storage"
)

// approveList saves a list for the named agent as the Agents page would.
func (f fixture) approveList(t *testing.T, name string, until *time.Time, numbers ...string) {
	t.Helper()
	c, err := agentauth.ActiveByName(f.dir, name)
	if err != nil || c == nil {
		t.Fatalf("agent %s: %v", name, err)
	}
	l := sendlist.List{AgentID: c.ID, Agent: c.Name, GrantedAt: time.Now(), Until: until}
	for _, n := range numbers {
		chat, ok := sendlist.NumberChat(n)
		if !ok {
			t.Fatalf("bad number %s", n)
		}
		l.Numbers = append(l.Numbers, sendlist.Number{Chat: chat})
	}
	if err = sendlist.Put(f.dir, l); err != nil {
		t.Fatal(err)
	}
}

// incoming stores a message from the person at chat, as capture would.
func (f fixture) incoming(t *testing.T, chat, id, text string) {
	t.Helper()
	m := storage.Message{GroupID: chat, MessageID: id, SenderID: chat, Timestamp: time.Now().Add(time.Second), Type: "text", Text: text}
	if res, err := f.store.Apply(context.Background(), f.account, []storage.Message{m}); err != nil || res.Stored != 1 {
		t.Fatalf("store reply: %+v %v", res, err)
	}
}

// dueNow lifts the random spacing so the next first message may go at once.
func (f fixture) dueNow(t *testing.T, name string) {
	t.Helper()
	c, _ := agentauth.ActiveByName(f.dir, name)
	if err := sendlist.Update(f.dir, c.ID, time.Now(), func(l *sendlist.List) error { l.NextAt = nil; return nil }); err != nil {
		t.Fatal(err)
	}
}

func listOf(t *testing.T, f fixture, name string) sendlist.List {
	t.Helper()
	c, _ := agentauth.ActiveByName(f.dir, name)
	l, ok := sendlist.For(f.dir, c.ID, time.Now())
	if !ok {
		t.Fatalf("no list for %s", name)
	}
	return l
}

const listed = "15551230000@s.whatsapp.net"

func TestAnApprovedListLetsItsAgentMessageThoseNumbersOnly(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	other := f.grantWriter(t, "other", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "+1 555 123 0000", "15551230001")

	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"+1 (555) 123-0000","text":"hello"}`); status != 200 {
		t.Fatalf("send to a listed number: %d %s", status, body)
	}
	// A number not on the list, or another agent using this list, is refused.
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551239999","text":"hello"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if !strings.Contains(string(body), "approve it in a list") {
		t.Fatalf("the refusal should say how to get access: %s", body)
	}
	status, body = f.send(t, "POST", "/api/v1/messages", other, `{"to":"15551230001","text":"hello"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if rec.count() != 1 || rec.calls[0].To != listed {
		t.Fatalf("what reached WhatsApp: %+v", rec.calls)
	}
	entries := f.sentEntries(t)
	if len(entries) != 1 || !entries[0].List || !entries[0].NewNumber || entries[0].Open || entries[0].Actor != "poster" {
		t.Fatalf("a list send should be marked in the sent log: %+v", entries)
	}
	if audit := f.auditLog(t); !strings.Contains(audit, `"event":"send_list"`) || !strings.Contains(audit, listed) {
		t.Fatalf("the list send was not audited: %s", audit)
	}
	l := listOf(t, f, "poster")
	if n := l.Find(listed); n == nil || n.FirstSentAt == nil || n.LastSentAt == nil {
		t.Fatalf("the number was not marked as messaged: %+v", n)
	}
	// Its chat is archived from now on, so a reply can be seen.
	if on, err := f.store.IsEnabled(context.Background(), f.account, listed); err != nil || !on {
		t.Fatalf("the messaged chat is not captured: %v %v", on, err)
	}
}

func TestAListAllowsOneMessagePerNumberUntilItReplies(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000")
	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("first: %d %s", status, body)
	}
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi again"}`)
	wantV1Error(t, status, body, 429, query.CodeRateLimited)
	if !strings.Contains(string(body), "has not replied") {
		t.Fatalf("the refusal should say why: %s", body)
	}
	f.incoming(t, listed, "R1", "Yes, tell me more")
	if status, body = f.send(t, "POST", "/api/v1/messages", agent, `{"chat":"`+listed+`","text":"here it is"}`); status != 200 {
		t.Fatalf("after a reply: %d %s", status, body)
	}
	entries := f.sentEntries(t)
	if rec.count() != 2 || len(entries) != 2 || entries[0].NewNumber || !entries[1].NewNumber {
		t.Fatalf("only the first message is a new number: %+v", entries)
	}
}

func TestAListAllowsFiveNewNumbersAnHourPerAgent(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	second := f.grantWriter(t, "second", []string{agentauth.ScopeSend}, 0, "one@g.us")
	var numbers []string
	for i := 0; i <= sendlist.DefaultPerHour; i++ {
		numbers = append(numbers, fmt.Sprintf("1555123%04d", i))
	}
	f.approveList(t, "poster", nil, numbers...)
	f.approveList(t, "second", nil, "15559990000")
	for i := 0; i < sendlist.DefaultPerHour; i++ {
		f.dueNow(t, "poster")
		if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"`+numbers[i]+`","text":"x"}`); status != 200 {
			t.Fatalf("number %d: %d %s", i, status, body)
		}
	}
	f.dueNow(t, "poster")
	req, _ := http.NewRequest("POST", f.server.URL+"/api/v1/messages", strings.NewReader(`{"to":"`+numbers[sendlist.DefaultPerHour]+`","text":"x"}`))
	req.Header.Set("Authorization", "Bearer "+agent)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatalf("the sixth new number: %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	// The cap is per agent.
	if status, body := f.send(t, "POST", "/api/v1/messages", second, `{"to":"15559990000","text":"x"}`); status != 200 {
		t.Fatalf("another agent's list: %d %s", status, body)
	}
	if rec.count() != sendlist.DefaultPerHour+1 {
		t.Fatalf("messages sent: %d", rec.count())
	}
}

func TestANumberThatRepliesStopIsDroppedFromTheList(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000", "15551230001")
	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("first: %d %s", status, body)
	}
	f.incoming(t, listed, "R1", "  Stop ")
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	l := listOf(t, f, "poster")
	if l.Find(listed) != nil || l.Stopped != 1 || len(l.Numbers) != 1 {
		t.Fatalf("the number that said STOP is still listed: %+v", l)
	}
	// Approving the same list again does not bring it back while the STOP is archived.
	f.approveList(t, "poster", nil, "15551230000", "15551230001")
	status, body = f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if rec.count() != 1 {
		t.Fatalf("a number that said STOP was messaged again: %d", rec.count())
	}
}

// Older chat with a listed person never drops them, only a reply after the
// list's first message to them does.
func TestOnlyAStopAfterTheFirstListMessageDropsTheNumber(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	old := storage.Message{GroupID: listed, MessageID: "OLD", SenderID: listed, Timestamp: time.Now().Add(-48 * time.Hour), Type: "text", Text: "see you at the bus stop"}
	if _, err := f.store.Apply(context.Background(), f.account, []storage.Message{old}); err != nil {
		t.Fatal(err)
	}
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000")
	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("first: %d %s", status, body)
	}
	f.incoming(t, listed, "R1", "sure")
	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("an old message dropped the number: %d %s", status, body)
	}
	f.incoming(t, listed, "R2", "stop")
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if l := listOf(t, f, "poster"); l.Find(listed) != nil || l.Stopped != 1 || rec.count() != 2 {
		t.Fatalf("a STOP after the first message must drop it: %+v sends=%d", l, rec.count())
	}
}

// A STOP belongs to the number: no other agent may message it after, and
// approving the same numbers again neither brings it back nor counts it twice.
func TestAStopToOneAgentKeepsTheNumberOffEveryList(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	first := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	second := f.grantWriter(t, "other", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000", "15551230001")
	if status, body := f.send(t, "POST", "/api/v1/messages", first, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("first: %d %s", status, body)
	}
	f.incoming(t, listed, "R1", "stop")
	status, body := f.send(t, "POST", "/api/v1/messages", first, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	f.approveList(t, "other", nil, "15551230000", "15551230001")
	status, body = f.send(t, "POST", "/api/v1/messages", second, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if l := listOf(t, f, "other"); l.Find(listed) != nil || l.Stopped != 1 || len(l.Numbers) != 1 {
		t.Fatalf("another agent's list holds the number that said STOP: %+v", l)
	}
	for range 2 {
		f.approveList(t, "poster", nil, "15551230000", "15551230001")
		if l := listOf(t, f, "poster"); l.Find(listed) != nil || l.Stopped != 1 {
			t.Fatalf("approving again changed the STOP count or listed the number: %+v", l)
		}
	}
	if rec.count() != 1 {
		t.Fatalf("a number that said STOP was messaged again: %d", rec.count())
	}
}

// A STOP that arrives after the list is gone, and after its send history is
// forgotten, still keeps the number from any agent's first message.
func TestAStopAfterTheListIsRemovedOutlivesTheHistory(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	first := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	second := f.grantWriter(t, "other", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000")
	if status, body := f.send(t, "POST", "/api/v1/messages", first, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("first: %d %s", status, body)
	}
	c, _ := agentauth.ActiveByName(f.dir, "poster")
	if err := sendlist.Remove(f.dir, c.ID); err != nil {
		t.Fatal(err)
	}
	f.incoming(t, listed, "R1", "stop")
	// Day 35: the removed list's send history has been forgotten.
	if err := os.WriteFile(filepath.Join(f.dir, sendlist.FileName), []byte(`{"lists":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.approveList(t, "other", nil, "15551230000")
	status, body := f.send(t, "POST", "/api/v1/messages", second, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if rec.count() != 1 {
		t.Fatalf("a number that said STOP after its list was removed got a first message: %d", rec.count())
	}
}

func TestAListEndsWhenItExpiresIsRemovedOrTheAgentCannotSend(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	past := time.Now().Add(-time.Minute)
	f.approveList(t, "poster", &past, "15551230000")
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"x"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	f.approveList(t, "poster", nil, "15551230000")
	c, _ := agentauth.ActiveByName(f.dir, "poster")
	if err := sendlist.Remove(f.dir, c.ID); err != nil {
		t.Fatal(err)
	}
	status, body = f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"x"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	// A list never gives the send scope.
	reader := f.grantWriter(t, "reader", []string{agentauth.ScopeReact}, 0, "one@g.us")
	f.approveList(t, "reader", nil, "15551230000")
	status, body = f.send(t, "POST", "/api/v1/messages", reader, `{"to":"15551230000","text":"x"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if rec.count() != 0 {
		t.Fatalf("a send went out: %+v", rec.calls)
	}
}

func TestAListIsUsedBeforeOpenSending(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000")
	if _, err := opensend.Start(f.dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"x"}`)
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"x"}`)
	wantV1Error(t, status, body, 429, query.CodeRateLimited)
	if e := f.sentEntries(t); len(e) != 1 || !e[0].List || e[0].Open {
		t.Fatalf("a listed number goes by the list's rules: %+v", e)
	}
}

func TestSendListStatusReportsTheAllowanceLeft(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	read := func(token string) sendListView {
		t.Helper()
		status, body := f.send(t, "GET", "/api/v1/send-list", token, "")
		var got struct {
			Data sendListView `json:"data"`
		}
		if status != 200 || json.Unmarshal(body, &got) != nil {
			t.Fatalf("send-list: %d %s", status, body)
		}
		if strings.Contains(string(body), "1555") {
			t.Fatalf("the status must not list the numbers: %s", body)
		}
		return got.Data
	}
	if v := read(agent); v.Active || !v.CanSend {
		t.Fatalf("no list yet: %+v", v)
	}
	until := time.Now().Add(24 * time.Hour)
	f.approveList(t, "poster", &until, "15551230000", "15551230001", "15551230002")
	f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"x"}`)
	f.incoming(t, listed, "R1", "STOP")
	v := read(agent)
	if !v.Active || v.Numbers != 2 || v.NotMessaged != 2 || v.Stopped != 1 || v.NewNumbersLeft != sendlist.DefaultPerHour-1 || v.MessagesLeft != sendlist.MessagesPerNew*sendlist.DefaultPerHour-1 || v.ExpiresAt == nil || v.NextNewAt == nil || v.NewPerHour != sendlist.DefaultPerHour {
		t.Fatalf("status: %+v", v)
	}
	if v := read(f.agent); v.Active || v.CanSend {
		t.Fatalf("another agent sees no list: %+v", v)
	}
	status, body := f.send(t, "GET", "/api/v1/send-list", "", "")
	wantV1Error(t, status, body, 401, query.CodeUnauthorized)
	status, body = f.send(t, "GET", "/api/v1/version", agent, "")
	if status != 200 || !strings.Contains(string(body), `"write.send.list"`) {
		t.Fatalf("version: %d %s", status, body)
	}
}

func TestSendListNextNewAtIsTheLaterOfTheHourlyResetAndTheSpacing(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	read := func() sendListView {
		t.Helper()
		status, body := f.send(t, "GET", "/api/v1/send-list", agent, "")
		var got struct {
			Data sendListView `json:"data"`
		}
		if status != 200 || json.Unmarshal(body, &got) != nil {
			t.Fatalf("send-list: %d %s", status, body)
		}
		return got.Data
	}
	var numbers []string
	for i := 0; i <= sendlist.DefaultPerHour; i++ {
		numbers = append(numbers, fmt.Sprintf("1555123%04d", i))
	}
	f.approveList(t, "poster", nil, numbers...)
	start := time.Now()
	for i := 0; i < sendlist.DefaultPerHour; i++ {
		f.dueNow(t, "poster")
		if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"`+numbers[i]+`","text":"x"}`); status != 200 {
			t.Fatalf("number %d: %d %s", i, status, body)
		}
	}
	// The hour's new numbers are used up: the first one counts until an hour after it went.
	if v := read(); v.NewNumbersLeft != 0 || v.NextNewAt == nil || v.NextNewAt.Before(start.Add(sendlist.Window-time.Minute)) || v.NextNewAt.After(time.Now().Add(sendlist.Window)) {
		t.Fatalf("next new number after the hourly reset: %+v", v)
	}
	// A spacing gap that ends later than the reset is the real time.
	c, _ := agentauth.ActiveByName(f.dir, "poster")
	later := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if err := sendlist.Update(f.dir, c.ID, time.Now(), func(l *sendlist.List) error { l.NextAt = &later; return nil }); err != nil {
		t.Fatal(err)
	}
	if v := read(); v.NextNewAt == nil || !v.NextNewAt.Equal(later) {
		t.Fatalf("next new number after the spacing gap %v: %+v", later, v)
	}
	// With every number messaged there is no next new number.
	f.approveList(t, "poster", nil, numbers[0])
	if v := read(); v.NotMessaged != 0 || v.NextNewAt != nil {
		t.Fatalf("nothing left to start: %+v", v)
	}
}

func TestSendListNextNewAtWaitsForTheHourlyMessageCap(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000", "15551230001", "15551230002")
	start := time.Now()
	send := func(to string) {
		t.Helper()
		if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"`+to+`","text":"x"}`); status != 200 {
			t.Fatalf("send to %s: %d %s", to, status, body)
		}
	}
	// Two new numbers, then replies back and forth until the hour's messages are used up.
	send("15551230000")
	f.dueNow(t, "poster")
	send("15551230001")
	for i := 2; i < sendlist.MessagesPerNew*sendlist.DefaultPerHour; i++ {
		f.incoming(t, listed, fmt.Sprintf("R%d", i), "ok")
		send("15551230000")
	}
	f.dueNow(t, "poster")
	status, body := f.send(t, "GET", "/api/v1/send-list", agent, "")
	var got struct {
		Data sendListView `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &got) != nil {
		t.Fatalf("send-list: %d %s", status, body)
	}
	v := got.Data
	if v.MessagesLeft != 0 || v.NewNumbersLeft == 0 || v.NotMessaged != 1 || v.NextNewAt == nil || v.NextNewAt.Before(start.Add(sendlist.Window-time.Minute)) {
		t.Fatalf("a new number must wait for the first message to stop counting: %+v", v)
	}
	status, body = f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230002","text":"x"}`)
	wantV1Error(t, status, body, 429, query.CodeRateLimited)
}

// postList sends the Agents page's list form as a browser would.
func (c *pageClient) postList(t *testing.T, fields map[string]string, file string) (int, string, http.Header) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if file != "" {
		fw, _ := mw.CreateFormFile("file", "numbers.csv")
		fw.Write([]byte(file))
	}
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+"/ui/agents", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", c.origin())
	return c.do(req)
}

func TestApprovingAListNeedsTheSystemConfirmationAndRemovingItDoesNot(t *testing.T) {
	stub := &promptStub{}
	f, c, csrf := agentsFixture(t, stub)
	if _, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "helper", Scopes: []string{agentauth.ScopeReadChats, agentauth.ScopeSend}, OnDemand: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "quiet", Scopes: []string{agentauth.ScopeReadChats}, OnDemand: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, page, _ := c.get("/ui/agents")
	if !strings.Contains(page, "Approved numbers") || !strings.Contains(page, "Approve list") || !strings.Contains(page, `<option>helper</option>`) || strings.Contains(page, `<option>quiet</option>`) {
		t.Fatalf("the list form should offer agents that can send:\n%s", page)
	}
	form := func(numbers, file, agent string) string {
		_, _, hd := c.postList(t, map[string]string{"csrf": csrf, "action": "liston", "agent": agent, "numbers": numbers, "ends": "7d", "rate": "10"}, file)
		return notice(hd)
	}
	if got := form("+15551230000\n15551230001", "", "helper"); got != "declined" {
		t.Fatalf("a declined prompt gave %q", got)
	}
	if lists, _ := sendlist.Load(f.dir); len(lists) != 0 {
		t.Fatalf("a list was saved without a confirmation: %+v", lists)
	}
	stub.set(true)
	for _, tc := range []struct{ numbers, file, agent, want string }{
		{"+15551230000\nJane", "", "helper", "badlist"},
		{"", "", "helper", "nolist"},
		{"+15551230000", "", "quiet", "needsend"},
		{"+15551230000", "", "nobody", "failed"},
	} {
		if got := form(tc.numbers, tc.file, tc.agent); got != tc.want {
			t.Fatalf("%+v gave %q", tc, got)
		}
	}
	before := len(stub.seen())
	if _, _, hd := c.postList(t, map[string]string{"csrf": csrf, "action": "liston", "agent": "helper", "numbers": "+15551230000", "ends": "7d", "rate": "25"}, ""); notice(hd) != "invalid" {
		t.Fatalf("a pace above the maximum: %q", notice(hd))
	}
	if got := form("+1 555 123 0000\n+15551230001", "15551230002\r\n15551230003,15551230004\n15551230001\n", "helper"); got != "liston" {
		t.Fatalf("a confirmed list gave %q", got)
	}
	prompts := stub.seen()
	if len(prompts) != before+1 || !strings.Contains(prompts[len(prompts)-1], `"helper"`) || !strings.Contains(prompts[len(prompts)-1], "these 5 number(s)") || !strings.Contains(prompts[len(prompts)-1], "+15551230000, +15551230001, +15551230002 and 2 more") || !strings.Contains(prompts[len(prompts)-1], "10 new numbers an hour, spaced at random") {
		t.Fatalf("the prompt must name the agent, the count and some numbers: %q", prompts)
	}
	l := listOf(t, fixture{dir: f.dir}, "helper")
	if len(l.Numbers) != 5 || l.PerHour != 10 || l.Until == nil || l.Until.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("saved list: %+v", l)
	}
	_, page, _ = c.get("/ui/agents")
	if !strings.Contains(page, "5 <span class=\"mut\">(5 not messaged yet)") || !strings.Contains(page, "Remove list") {
		t.Fatalf("the list is not shown:\n%s", page)
	}
	stub.set(false)
	before = len(stub.seen())
	_, _, hd := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"listoff"}, "agent": {"helper"}})
	if notice(hd) != "listoff" || len(stub.seen()) != before {
		t.Fatalf("removing a list must work without a prompt: %q", notice(hd))
	}
	if lists, _ := sendlist.Load(f.dir); len(lists) != 0 {
		t.Fatalf("the list was not removed: %+v", lists)
	}
	if audit := auditText(t, f.dir); !strings.Contains(audit, `"event":"send_list_on"`) || !strings.Contains(audit, `"event":"send_list_off"`) {
		t.Fatalf("the list changes were not audited: %s", audit)
	}
	if code, _, _ := c.postList(t, map[string]string{"csrf": "forged", "action": "liston", "agent": "helper", "numbers": "+15551230000", "ends": "7d"}, ""); code != 403 {
		t.Fatalf("forged list form: %d", code)
	}
}

func TestAListCannotBeApprovedWithoutASystemConfirmation(t *testing.T) {
	f, c, csrf := agentsFixture(t, nil)
	if _, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "helper", Scopes: []string{agentauth.ScopeReadChats, agentauth.ScopeSend}, OnDemand: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, page, _ := c.get("/ui/agents")
	if strings.Contains(page, "Approve list") {
		t.Fatalf("a computer with no confirmation was offered the list form:\n%s", page)
	}
	if _, _, hd := c.postList(t, map[string]string{"csrf": csrf, "action": "liston", "agent": "helper", "numbers": "+15551230000", "ends": "7d"}, ""); notice(hd) != "nogate" {
		t.Fatalf("list without a confirmation: %q", notice(hd))
	}
	if lists, _ := sendlist.Load(f.dir); len(lists) != 0 {
		t.Fatal("a list was saved with no system confirmation")
	}
}

func TestAListSpacesFirstMessagesAtRandomIntervals(t *testing.T) {
	for _, perHour := range []int{sendlist.MinPerHour, sendlist.MaxPerHour} {
		rec := &sendRecorder{}
		f := setupSend(t, rec)
		agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
		f.approveList(t, "poster", nil, "15551230000", "15551230001")
		c, _ := agentauth.ActiveByName(f.dir, "poster")
		if err := sendlist.Update(f.dir, c.ID, time.Now(), func(l *sendlist.List) error { l.PerHour = perHour; return nil }); err != nil {
			t.Fatal(err)
		}
		if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"x"}`); status != 200 {
			t.Fatalf("first: %d %s", status, body)
		}
		req, _ := http.NewRequest("POST", f.server.URL+"/api/v1/messages", strings.NewReader(`{"to":"15551230001","text":"x"}`))
		req.Header.Set("Authorization", "Bearer "+agent)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		wait, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		avg := int(time.Hour.Seconds()) / perHour
		if res.StatusCode != 429 || wait < avg/2-2 || wait > avg*3/2+2 {
			t.Fatalf("%d an hour: the next new number should wait 0.5 to 1.5 of %ds: %d, Retry-After %d", perHour, avg, res.StatusCode, wait)
		}
		// A reply in a conversation is not held back by the spacing.
		f.incoming(t, listed, "R1", "ok")
		if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"x"}`); status != 200 {
			t.Fatalf("answer to a reply: %d %s", status, body)
		}
		if rec.count() != 2 {
			t.Fatalf("sent: %d", rec.count())
		}
	}
}

func TestGapStaysWithinHalfToOneAndAHalfOfTheAverage(t *testing.T) {
	for _, n := range []int{5, 20} {
		avg := time.Hour / time.Duration(n)
		if lo, hi := sendlist.Gap(n, 0), sendlist.Gap(n, 0.9999); lo != avg/2 || hi > avg*3/2 || hi < avg*3/2-time.Second {
			t.Fatalf("%d: %v %v", n, lo, hi)
		}
	}
}

func TestAPausedListSendsNothingUntilItIsResumed(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000", "15551230001")
	c, _ := agentauth.ActiveByName(f.dir, "poster")
	if err := sendlist.Pause(f.dir, c.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	if !strings.Contains(string(body), "paused") {
		t.Fatalf("the refusal should say the list is paused: %s", body)
	}
	status, body = f.send(t, "GET", "/api/v1/send-list", agent, "")
	var got struct {
		Data sendListView `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &got) != nil || !got.Data.Active || !got.Data.Paused || got.Data.NewNumbersLeft != 0 || got.Data.MessagesLeft != 0 || got.Data.NextNewAt != nil || got.Data.Numbers != 2 {
		t.Fatalf("send-list while paused: %d %s", status, body)
	}
	if err := sendlist.Pause(f.dir, c.ID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if status, body = f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("after resuming: %d %s", status, body)
	}
	if rec.count() != 1 {
		t.Fatalf("sends: %d", rec.count())
	}
}

func TestRemovingAndApprovingAListAgainNeverSendsASecondFirstMessage(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	agent := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000", "15551230001")
	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("first: %d %s", status, body)
	}
	c, _ := agentauth.ActiveByName(f.dir, "poster")
	if err := sendlist.Remove(f.dir, c.ID); err != nil {
		t.Fatal(err)
	}
	f.approveList(t, "poster", nil, "15551230000", "15551230001")
	status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hi again"}`)
	wantV1Error(t, status, body, 429, query.CodeRateLimited)
	if !strings.Contains(string(body), "has not replied") || rec.count() != 1 {
		t.Fatalf("a removed and re-approved number got a second first message: %d %s", rec.count(), body)
	}
	if l := listOf(t, f, "poster"); l.NotMessaged() != 1 {
		t.Fatalf("the re-approved list forgot who was messaged: %+v", l)
	}
}

// replyBetween stores a reply from the person at chat timed now, between the
// list message before it and the one after it.
func (f fixture) replyBetween(t *testing.T, chat, id, text string) {
	t.Helper()
	time.Sleep(10 * time.Millisecond)
	m := storage.Message{GroupID: chat, MessageID: id, SenderID: chat, Timestamp: time.Now(), Type: "text", Text: text}
	if res, err := f.store.Apply(context.Background(), f.account, []storage.Message{m}); err != nil || res.Stored != 1 {
		t.Fatalf("store reply: %+v %v", res, err)
	}
	time.Sleep(10 * time.Millisecond)
}

// One message per number until it replies holds across every agent: while
// two lists hold the same number, neither sends while the latest list message
// to it, by either agent, is unanswered.
func TestNoAgentMessagesANumberWhileAnotherAgentsMessageIsUnanswered(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	first := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	second := f.grantWriter(t, "other", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "other", nil, "15551230000", "15551230001")
	f.approveList(t, "poster", nil, "15551230000", "15551230001")
	if status, body := f.send(t, "POST", "/api/v1/messages", first, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("first message: %d %s", status, body)
	}
	status, body := f.send(t, "POST", "/api/v1/messages", second, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 429, query.CodeRateLimited)
	if !strings.Contains(string(body), "has not replied") {
		t.Fatalf("another agent sent a second first message: %s", body)
	}
	f.replyBetween(t, listed, "R1", "Yes, tell me more")
	if status, body = f.send(t, "POST", "/api/v1/messages", first, `{"to":"15551230000","text":"here it is"}`); status != 200 {
		t.Fatalf("after a reply: %d %s", status, body)
	}
	status, body = f.send(t, "POST", "/api/v1/messages", second, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 429, query.CodeRateLimited)
	if rec.count() != 2 {
		t.Fatalf("a reply to the first message let another agent answer past the latest one: %d", rec.count())
	}
	f.incoming(t, listed, "R2", "Thanks")
	if status, body = f.send(t, "POST", "/api/v1/messages", second, `{"to":"15551230000","text":"anything else?"}`); status != 200 {
		t.Fatalf("after a reply to the latest message: %d %s", status, body)
	}
	if entries := f.sentEntries(t); len(entries) != 3 || entries[0].NewNumber {
		t.Fatalf("a number already messaged was counted as new: %+v", entries)
	}
}

// The latest list message to a number is remembered for good, after a removed
// list's send history is forgotten.
func TestAnUnansweredListMessageOutlivesTheSendHistory(t *testing.T) {
	rec := &sendRecorder{}
	f := setupSend(t, rec)
	first := f.grantWriter(t, "poster", []string{agentauth.ScopeSend}, 0, "one@g.us")
	second := f.grantWriter(t, "other", []string{agentauth.ScopeSend}, 0, "one@g.us")
	f.approveList(t, "poster", nil, "15551230000", "15551230001")
	if status, body := f.send(t, "POST", "/api/v1/messages", first, `{"to":"15551230000","text":"hi"}`); status != 200 {
		t.Fatalf("first message: %d %s", status, body)
	}
	f.replyBetween(t, listed, "R1", "Yes, tell me more")
	if status, body := f.send(t, "POST", "/api/v1/messages", first, `{"to":"15551230000","text":"here it is"}`); status != 200 {
		t.Fatalf("after a reply: %d %s", status, body)
	}
	c, _ := agentauth.ActiveByName(f.dir, "poster")
	if err := sendlist.Remove(f.dir, c.ID); err != nil {
		t.Fatal(err)
	}
	// Day 35: the removed list's send history has been forgotten.
	if err := os.WriteFile(filepath.Join(f.dir, sendlist.FileName), []byte(`{"lists":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.approveList(t, "other", nil, "15551230000", "15551230001")
	status, body := f.send(t, "POST", "/api/v1/messages", second, `{"to":"15551230000","text":"hi"}`)
	wantV1Error(t, status, body, 429, query.CodeRateLimited)
	if l := listOf(t, f, "other"); l.NotMessaged() != 1 || rec.count() != 2 {
		t.Fatalf("another agent's list messaged a number with an unanswered message: %d %+v", rec.count(), l)
	}
	f.incoming(t, listed, "R2", "Thanks")
	if status, body = f.send(t, "POST", "/api/v1/messages", second, `{"to":"15551230000","text":"anything else?"}`); status != 200 {
		t.Fatalf("after a reply to the latest message: %d %s", status, body)
	}
}

func TestPausingAListNeedsNoConfirmationAndResumingDoes(t *testing.T) {
	stub := &promptStub{}
	f, c, csrf := agentsFixture(t, stub)
	if _, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "helper", Scopes: []string{agentauth.ScopeReadChats, agentauth.ScopeSend}, OnDemand: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	fixture{dir: f.dir}.approveList(t, "helper", nil, "15551230000", "15551230001")
	_, page, _ := c.get("/ui/agents")
	if !strings.Contains(page, `value="listpause">Pause</button>`) || strings.Contains(page, "Resume") {
		t.Fatalf("a running list should offer Pause:\n%s", page)
	}
	post := func(action string) string {
		_, _, hd := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {action}, "agent": {"helper"}})
		return notice(hd)
	}
	if got := post("listpause"); got != "listpause" || len(stub.seen()) != 0 || !listOf(t, fixture{dir: f.dir}, "helper").Paused() {
		t.Fatalf("pausing must work without a prompt: %q %d", got, len(stub.seen()))
	}
	_, page, _ = c.get("/ui/agents")
	if !strings.Contains(page, "Paused</strong>") || !strings.Contains(page, `value="listresume">Resume…</button>`) || strings.Contains(page, `value="listpause"`) {
		t.Fatalf("a paused list should say so and offer Resume:\n%s", page)
	}
	if got := post("listresume"); got != "declined" || !listOf(t, fixture{dir: f.dir}, "helper").Paused() {
		t.Fatalf("a declined prompt resumed the list: %q", got)
	}
	stub.set(true)
	if got := post("listresume"); got != "listresume" || listOf(t, fixture{dir: f.dir}, "helper").Paused() {
		t.Fatalf("a confirmed prompt did not resume the list: %q", got)
	}
	if p := stub.seen(); len(p) != 2 || !strings.Contains(p[1], `"helper"`) || !strings.Contains(p[1], "2 number(s)") {
		t.Fatalf("the prompt must name the agent and the count: %q", p)
	}
	if got := post("listresume"); got != "failed" {
		t.Fatalf("resuming a running list: %q", got)
	}
	if audit := auditText(t, f.dir); !strings.Contains(audit, `"event":"send_list_pause"`) || !strings.Contains(audit, `"event":"send_list_resume"`) {
		t.Fatalf("pause and resume were not audited: %s", audit)
	}
}

func TestAPausedListCannotBeResumedWithoutASystemConfirmation(t *testing.T) {
	f, c, csrf := agentsFixture(t, nil)
	if _, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "helper", Scopes: []string{agentauth.ScopeReadChats, agentauth.ScopeSend}, OnDemand: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	fixture{dir: f.dir}.approveList(t, "helper", nil, "15551230000")
	if _, _, hd := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"listpause"}, "agent": {"helper"}}); notice(hd) != "listpause" {
		t.Fatalf("pausing on a computer with no confirmation: %q", notice(hd))
	}
	_, page, _ := c.get("/ui/agents")
	if strings.Contains(page, "Resume") {
		t.Fatalf("a computer with no confirmation was offered Resume:\n%s", page)
	}
	if _, _, hd := c.post("/ui/agents", url.Values{"csrf": {csrf}, "action": {"listresume"}, "agent": {"helper"}}); notice(hd) != "nogate" || !listOf(t, fixture{dir: f.dir}, "helper").Paused() {
		t.Fatalf("resumed with no confirmation: %q", notice(hd))
	}
}
