package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

func TestSignMatchesIndependentHMAC(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	mac := hmac.New(sha256.New, []byte("whsec_key"))
	mac.Write([]byte("1700000000.{\"id\":\"evt_1\"}"))
	want := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if got := Sign("whsec_key", 1700000000, body); got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
}

func TestVerifyRejectsTamperReplayAndWrongKey(t *testing.T) {
	now := time.Unix(1700000000, 0)
	body := []byte(`{"id":"evt_1"}`)
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := Sign("whsec_key", now.Unix(), body)
	if err := Verify("whsec_key", ts, sig, body, now); err != nil {
		t.Fatal(err)
	}
	if err := Verify("whsec_key", ts, sig, body, now.Add(SignatureTolerance-time.Second)); err != nil {
		t.Fatalf("inside tolerance: %v", err)
	}
	cases := map[string]error{
		"tampered body": Verify("whsec_key", ts, sig, []byte(`{"id":"evt_2"}`), now),
		"wrong key":     Verify("whsec_other", ts, sig, body, now),
		"old (replay)":  Verify("whsec_key", ts, sig, body, now.Add(SignatureTolerance+time.Second)),
		"future":        Verify("whsec_key", ts, sig, body, now.Add(-SignatureTolerance-time.Second)),
		"bad timestamp": Verify("whsec_key", "soon", sig, body, now),
		"retimed":       Verify("whsec_key", strconv.FormatInt(now.Unix()+1, 10), sig, body, now),
		"no signature":  Verify("whsec_key", ts, "", body, now),
	}
	for name, err := range cases {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// receiver is a fake webhook endpoint that verifies every request the way a
// real receiver should and records what arrived.
type receiver struct {
	t      *testing.T
	secret string
	mu     sync.Mutex
	events []Event
	raw    [][]byte
	status func(Event) int
	hook   func(Event)
	// quiet suppresses test failures for requests that are meant to fail
	// verification.
	quiet bool
}

func (r *receiver) handler(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	quiet := r.quiet
	r.mu.Unlock()
	if err := Verify(r.secret, req.Header.Get(HeaderTimestamp), req.Header.Get(HeaderSignature), body, time.Now()); err != nil {
		if !quiet {
			r.t.Errorf("receiver rejected a request: %v", err)
		}
		w.WriteHeader(401)
		return
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil || req.Header.Get(HeaderID) != ev.ID || req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
		r.t.Errorf("malformed request: %v %s %v", err, body, req.Header)
		w.WriteHeader(400)
		return
	}
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.raw = append(r.raw, body)
	status, hook := r.status, r.hook
	r.mu.Unlock()
	if hook != nil {
		hook(ev)
	}
	code := 200
	if status != nil {
		code = status(ev)
	}
	w.WriteHeader(code)
}

func (r *receiver) setQuiet(v bool) {
	r.mu.Lock()
	r.quiet = v
	r.mu.Unlock()
}

func (r *receiver) received() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func (r *receiver) ids() []string {
	var out []string
	for _, e := range r.received() {
		out = append(out, e.ID)
	}
	return out
}

func senderFor(t *testing.T, r *receiver) *Sender {
	t.Helper()
	srv := tlsServer(t, r.handler)
	client, _ := guardedClient(t, srv, &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34")}})
	return NewSender(client)
}

// senderForMany returns a sender whose single server hands each request to the
// receiver registered for its path.
func senderForMany(t *testing.T, routes map[string]*receiver) *Sender {
	t.Helper()
	srv := tlsServer(t, func(w http.ResponseWriter, req *http.Request) {
		r := routes[req.URL.Path]
		if r == nil {
			w.WriteHeader(404)
			return
		}
		r.handler(w, req)
	})
	client, _ := guardedClient(t, srv, &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34")}})
	return NewSender(client)
}

func TestSendSignsFreshlyAndKeepsEventID(t *testing.T) {
	r := &receiver{t: t, secret: "whsec_" + strings.Repeat("a", 43)}
	s := senderFor(t, r)
	ep := Endpoint{ID: "0a1b2c3d", Name: "n", URL: "https://example.com/hook", Secret: r.secret}
	ev := Event{ID: "evt_0a1b2c3d_e_1", Type: "change", Endpoint: ep.ID, Sequence: 1, Op: "message.created", ChatID: "one@g.us", MessageID: "m1"}
	first := time.Unix(1700000000, 0)
	s.Now = func() time.Time { return first }
	r.setQuiet(true)
	// A request signed with an old clock is outside the receiver's tolerance,
	// which proves the timestamp is the send time and not the event time.
	if res := s.Send(t.Context(), ep, ev); res.OK() || res.Code != CodeRejected {
		t.Fatalf("stale timestamp: %+v", res)
	}
	s.Now = time.Now
	r.setQuiet(false)
	if res := s.Send(t.Context(), ep, ev); !res.OK() || res.Status != 200 {
		t.Fatalf("send: %+v", res)
	}
	if res := s.Send(t.Context(), ep, ev); !res.OK() {
		t.Fatalf("resend: %+v", res)
	}
	if got := r.ids(); len(got) != 2 || got[0] != ev.ID || got[1] != ev.ID {
		t.Fatalf("event IDs %v", got)
	}
}

func TestSendRejectsUnsafeTargetsWithoutConnecting(t *testing.T) {
	r := &receiver{t: t, secret: "whsec_x"}
	s := senderFor(t, r)
	for _, target := range []string{"http://example.com/h", "https://127.0.0.1/h", "https://localhost/h", "https://user@example.com/h"} {
		if res := s.Send(t.Context(), Endpoint{ID: "0a1b2c3d", URL: target, Secret: "whsec_x"}, Event{ID: "e"}); res.OK() || res.Retry {
			t.Errorf("%s: %+v", target, res)
		}
	}
	if len(r.received()) != 0 {
		t.Fatal("an unsafe target received a request")
	}
}

func TestSendClassifiesAnswers(t *testing.T) {
	cases := []struct {
		status int
		code   string
		retry  bool
	}{
		{200, CodeDelivered, false}, {202, CodeDelivered, false}, {204, CodeDelivered, false},
		{301, CodeRedirect, false}, {302, CodeRedirect, false}, {307, CodeRedirect, false},
		{400, CodeRejected, false}, {401, CodeRejected, false}, {404, CodeRejected, false}, {410, CodeRejected, false},
		{408, CodeTimeout, true}, {429, CodeRateLimited, true},
		{500, CodeServerError, true}, {502, CodeServerError, true}, {503, CodeServerError, true},
	}
	for _, c := range cases {
		r := &receiver{t: t, secret: "whsec_x", status: func(Event) int { return c.status }}
		s := senderFor(t, r)
		res := s.Send(t.Context(), Endpoint{ID: "0a1b2c3d", URL: "https://example.com/h", Secret: "whsec_x"}, Event{ID: "e"})
		if res.Code != c.code || res.Retry != c.retry || res.Status != c.status {
			t.Errorf("%d: %+v, want %s retry=%v", c.status, res, c.code, c.retry)
		}
	}
}

func TestSendReportsBlockedAndUnreachableTargets(t *testing.T) {
	srv := tlsServer(t, func(http.ResponseWriter, *http.Request) {})
	blocked, _ := guardedClient(t, srv, &fakeResolver{answers: [][]netip.Addr{addrs("10.1.1.1")}})
	res := NewSender(blocked).Send(t.Context(), Endpoint{ID: "0a1b2c3d", URL: "https://example.com/h", Secret: "whsec_x"}, Event{ID: "e"})
	if res.Code != CodeBlocked || res.Retry {
		t.Fatalf("blocked: %+v", res)
	}
	srv.Close()
	down, _ := guardedClient(t, srv, &fakeResolver{answers: [][]netip.Addr{addrs("93.184.216.34")}})
	res = NewSender(down).Send(t.Context(), Endpoint{ID: "0a1b2c3d", URL: "https://example.com/h", Secret: "whsec_x"}, Event{ID: "e"})
	if res.Code != CodeNetworkError || !res.Retry {
		t.Fatalf("unreachable: %+v", res)
	}
}

// The wire format must stay identifier-only: a new field in Event that could
// carry content fails this test and needs a conscious decision.
func TestEventCarriesIdentifiersOnly(t *testing.T) {
	ep := Endpoint{ID: "0a1b2c3d"}
	ev := ChangeEvent(ep, "me@s.whatsapp.net", "0123456789abcdef0123456789abcdef", storage.Change{Sequence: 7, Op: storage.OpMessageCreated, OpVersion: 1, ChatID: "one@g.us", MessageID: "m1", CommittedAt: time.Unix(1700000000, 0)})
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "type": true, "endpoint": true, "epoch": true, "sequence": true, "cursor": true, "op": true, "op_version": true, "chat_id": true, "message_id": true, "ref": true, "committed_at": true, "sent_at": true}
	for k := range fields {
		if !allowed[k] {
			t.Errorf("unexpected event field %q", k)
		}
	}
	if fields["id"] != "evt_0a1b2c3d_0123456789abcdef0123456789abcdef_7" || fields["ref"] == "" || fields["cursor"] == "" {
		t.Fatalf("event %s", data)
	}
	capture := ChangeEvent(ep, "me@s.whatsapp.net", "e", storage.Change{Sequence: 8, Op: storage.OpCaptureEnabled, OpVersion: 1, ChatID: "one@g.us"})
	if capture.Ref != "" {
		t.Fatalf("capture event has a message ref: %+v", capture)
	}
	ping, err := PingEvent(ep, time.Now())
	if err != nil || ping.Type != "ping" || !strings.HasPrefix(ping.ID, "evt_0a1b2c3d_ping_") || ping.ChatID != "" {
		t.Fatalf("ping %+v %v", ping, err)
	}
}

func TestWebhooksAreNotAnAgentScope(t *testing.T) {
	for _, scope := range []string{"webhook", "webhooks", "webhook.manage", "admin"} {
		if agentauth.ValidScope(scope) {
			t.Errorf("%q is grantable to agents", scope)
		}
		if _, err := agentauth.ParseScopes(scope); err == nil {
			t.Errorf("%q parsed as a scope", scope)
		}
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRegistryAddListRemove(t *testing.T) {
	dir := privateDir(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	secret, ep, err := Add(dir, AddRequest{Name: "crm", URL: "https://example.com/hook", Chats: []string{"b@g.us", "a@g.us", "a@g.us"}, Ops: []string{"message.updated,message.created"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, SecretPrefix) || len(secret) != len(SecretPrefix)+43 || len(ep.ID) != 8 {
		t.Fatalf("secret %q id %q", secret, ep.ID)
	}
	if want := []string{"message.created", "message.updated"}; strings.Join(ep.Ops, ",") != strings.Join(want, ",") || len(ep.Chats) != 2 {
		t.Fatalf("normalized %+v", ep)
	}
	if err = localfs.CheckPrivateFile(filepath.Join(dir, FileName)); err != nil {
		t.Fatalf("registry not private: %v", err)
	}

	listed, err := List(dir)
	if err != nil || len(listed) != 1 || listed[0].Secret != "" {
		t.Fatalf("List %+v %v", listed, err)
	}
	data, _ := json.Marshal(listed)
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "secret") {
		t.Fatalf("listing exposes the secret: %s", data)
	}
	active, err := Active(dir)
	if err != nil || len(active) != 1 || active[0].Secret != secret {
		t.Fatalf("Active %+v %v", active, err)
	}

	if _, _, err = Add(dir, AddRequest{Name: "crm", URL: "https://example.com/other"}, now); err == nil {
		t.Fatal("duplicate active name accepted")
	}
	if _, err = Remove(dir, "crm", now); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(raw), secret) {
		t.Fatal("removed endpoint's secret is still on disk")
	}
	if active, _ = Active(dir); len(active) != 0 {
		t.Fatalf("removed endpoint still active: %+v", active)
	}
	if listed, _ = List(dir); len(listed) != 1 || listed[0].Active() {
		t.Fatalf("removed endpoint not kept for audit: %+v", listed)
	}
	if _, err = Remove(dir, "crm", now); err == nil {
		t.Fatal("removed twice")
	}
	if _, _, err = Add(dir, AddRequest{Name: "crm", URL: "https://example.com/hook"}, now); err != nil {
		t.Fatalf("name not reusable after removal: %v", err)
	}
}

func TestRegistryValidationAndLimits(t *testing.T) {
	dir := privateDir(t)
	now := time.Now()
	bad := []AddRequest{
		{Name: "Bad_Name", URL: "https://example.com/h"},
		{Name: "", URL: "https://example.com/h"},
		{Name: "ok", URL: "http://example.com/h"},
		{Name: "ok", URL: "https://169.254.169.254/h"},
		{Name: "ok", URL: "https://example.com/h", Chats: []string{"nonsense"}},
		{Name: "ok", URL: "https://example.com/h", Ops: []string{"message.deleted"}},
	}
	for _, r := range bad {
		if _, _, err := Add(dir, r, now); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatal("registry written for rejected requests")
	}
	for i := 0; i < MaxEndpoints; i++ {
		if _, _, err := Add(dir, AddRequest{Name: "e" + strconv.Itoa(i), URL: "https://example.com/h"}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := Add(dir, AddRequest{Name: "extra", URL: "https://example.com/h"}, now); err == nil {
		t.Fatal("exceeded the endpoint limit")
	}
}

func TestRegistryFailsClosed(t *testing.T) {
	dir := privateDir(t)
	if _, _, err := Add(dir, AddRequest{Name: "crm", URL: "https://example.com/h"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	good, _ := os.ReadFile(path)
	for name, mutate := range map[string]func(string) string{
		"http url":       func(s string) string { return strings.Replace(s, "https://example.com/h", "http://example.com/h", 1) },
		"private url":    func(s string) string { return strings.Replace(s, "https://example.com/h", "https://10.0.0.1/h", 1) },
		"unknown field":  func(s string) string { return strings.Replace(s, `"version": 1`, `"version": 1, "extra": true`, 1) },
		"future version": func(s string) string { return strings.Replace(s, `"version": 1`, `"version": 2`, 1) },
		"short secret":   func(s string) string { return strings.Replace(s, SecretPrefix, SecretPrefix+"x", 1) },
		"not json":       func(string) string { return "{" },
	} {
		if err := os.WriteFile(path, []byte(mutate(string(good))), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Active(dir); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestDeadLetterListFilterClearAndTornLine(t *testing.T) {
	dir := privateDir(t)
	at := time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)
	for i, id := range []string{"aaaaaaaa", "bbbbbbbb", "aaaaaaaa"} {
		d := DeadLetter{Time: at, EndpointID: id, EndpointName: "n" + id[:1], EventID: "evt_" + strconv.Itoa(i), Sequence: int64(i + 1), Op: "message.created", ChatID: "one@g.us", Attempts: 5, Code: CodeServerError, Status: 503}
		if err := AppendDeadLetter(dir, d); err != nil {
			t.Fatal(err)
		}
	}
	all, err := ReadDeadLetters(dir, "")
	if err != nil || len(all) != 3 || all[0].EventID != "evt_0" {
		t.Fatalf("all %+v %v", all, err)
	}
	if only, _ := ReadDeadLetters(dir, "aaaaaaaa"); len(only) != 2 {
		t.Fatalf("filtered %+v", only)
	}
	path := filepath.Join(dir, DeadLetterFileName)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"time":"2026-`) // torn write after a crash
	f.Close()
	if all, err = ReadDeadLetters(dir, ""); err != nil || len(all) != 3 {
		t.Fatalf("torn line hid entries: %+v %v", all, err)
	}
	if n, err := ClearDeadLetters(dir); err != nil || n != 3 {
		t.Fatalf("clear %d %v", n, err)
	}
	if all, _ = ReadDeadLetters(dir, ""); len(all) != 0 {
		t.Fatalf("not cleared: %+v", all)
	}
}
