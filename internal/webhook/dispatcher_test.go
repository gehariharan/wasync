package webhook

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/storage"
)

const bodyText = "launch codes are 0000"

type harness struct {
	t       *testing.T
	dir     string
	store   *storage.Store
	account int64
	rcv     *receiver
	sender  *Sender
	ep      Endpoint
	clock   *fakeClock
	n       int
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// newHarness builds an archive with two enabled chats, a registered endpoint
// and a receiver that verifies signatures. The endpoint has not been seen by
// a dispatcher yet.
func newHarness(t *testing.T, req AddRequest) *harness {
	t.Helper()
	ctx := context.Background()
	dir := privateDir(t)
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	account, err := store.UseAccount(ctx, "me@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveGroups(ctx, account, []storage.Group{{ID: "one@g.us", Name: "One"}, {ID: "two@g.us", Name: "Two"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.SetEnabled(ctx, account, []string{"one@g.us", "two@g.us"}, true); err != nil {
		t.Fatal(err)
	}
	req.Name, req.URL = "crm", "https://example.com/hook"
	secret, ep, err := Add(dir, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rcv := &receiver{t: t, secret: secret}
	return &harness{t: t, dir: dir, store: store, account: account, rcv: rcv, sender: senderFor(t, rcv), ep: ep, clock: &fakeClock{now: time.Now()}}
}

func (h *harness) dispatcher() *Dispatcher {
	return &Dispatcher{Dir: h.dir, Store: h.store, Sender: h.sender, Audit: agentauth.NewAuditLog(h.dir), Backoff: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}, Now: h.clock.Now}
}

// message stores one captured message and returns its ID.
func (h *harness) message(chat string) string {
	h.t.Helper()
	h.n++
	id := "m" + string(rune('a'+h.n))
	_, err := h.store.Apply(context.Background(), h.account, []storage.Message{{GroupID: chat, MessageID: id, SenderID: "alice@lid", Text: bodyText, Type: "text", Timestamp: time.Unix(1700000000+int64(h.n), 0), Source: "live"}})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) messageIDs() []string {
	var out []string
	for _, e := range h.rcv.received() {
		out = append(out, e.MessageID)
	}
	return out
}

func equal(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestDispatcherDeliversInOrderOnceWithoutBodies(t *testing.T) {
	h := newHarness(t, AddRequest{})
	d := h.dispatcher()
	h.message("one@g.us") // before the endpoint was seen: never delivered
	d.Tick(t.Context())
	if got := h.rcv.received(); len(got) != 0 {
		t.Fatalf("history delivered to a new endpoint: %+v", got)
	}
	a, b, c := h.message("one@g.us"), h.message("two@g.us"), h.message("one@g.us")
	d.Tick(t.Context())
	if got := h.messageIDs(); !equal(got, []string{a, b, c}) {
		t.Fatalf("delivered %v, want %v", got, []string{a, b, c})
	}
	events := h.rcv.received()
	seen := map[string]bool{}
	for i, e := range events {
		if e.Type != "change" || e.Op != storage.OpMessageCreated || e.Ref == "" || e.Endpoint != h.ep.ID || seen[e.ID] || (i > 0 && e.Sequence <= events[i-1].Sequence) {
			t.Fatalf("event %d: %+v", i, e)
		}
		seen[e.ID] = true
	}
	for _, raw := range h.rcv.raw {
		if strings.Contains(string(raw), bodyText) || strings.Contains(string(raw), "alice") {
			t.Fatalf("webhook body leaks message content: %s", raw)
		}
		var fields map[string]any
		if json.Unmarshal(raw, &fields) != nil {
			t.Fatal("body is not JSON")
		}
		for k := range fields {
			if k == "text" || k == "body" || k == "content" || k == "sender" {
				t.Fatalf("content field %q in %s", k, raw)
			}
		}
	}
	d.Tick(t.Context())
	d.Tick(t.Context())
	if got := h.messageIDs(); len(got) != 3 {
		t.Fatalf("re-delivered after acknowledgement: %v", got)
	}
}

func TestDispatcherAppliesChatAndOperationFilters(t *testing.T) {
	h := newHarness(t, AddRequest{Chats: []string{"one@g.us"}, Ops: []string{storage.OpMessageCreated}})
	d := h.dispatcher()
	d.Tick(t.Context())
	h.message("two@g.us") // other chat
	a := h.message("one@g.us")
	if err := h.store.SetEnabled(t.Context(), h.account, []string{"one@g.us"}, false); err != nil { // capture.disabled: filtered by op
		t.Fatal(err)
	}
	d.Tick(t.Context())
	if got := h.messageIDs(); !equal(got, []string{a}) {
		t.Fatalf("delivered %v, want only %s", got, a)
	}
	// Filtered entries still advance the checkpoint: nothing is re-read.
	d.Tick(t.Context())
	if got := h.messageIDs(); len(got) != 1 {
		t.Fatalf("delivered again: %v", got)
	}
}

func TestDispatcherRetriesThenDeadLettersAndContinues(t *testing.T) {
	h := newHarness(t, AddRequest{})
	h.rcv.status = func(e Event) int {
		if e.MessageID == "mb" {
			return 503
		}
		return 200
	}
	d := h.dispatcher()
	d.Tick(t.Context())
	first, second := h.message("one@g.us"), h.message("one@g.us")
	if first != "mb" {
		t.Fatalf("test assumes the first message is mb, got %s", first)
	}
	d.Tick(t.Context())
	if got := h.messageIDs(); !equal(got, []string{"mb"}) {
		t.Fatalf("after first failure: %v", got)
	}
	// Inside the backoff window nothing is sent.
	d.Tick(t.Context())
	if got := h.messageIDs(); len(got) != 1 {
		t.Fatalf("retried before backoff elapsed: %v", got)
	}
	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		h.clock.advance(wait)
		d.Tick(t.Context())
	}
	if got := h.messageIDs(); len(got) != 4 {
		t.Fatalf("attempts so far: %v", got)
	}
	if dead, _ := ReadDeadLetters(h.dir, ""); len(dead) != 0 {
		t.Fatalf("dead-lettered early: %+v", dead)
	}
	h.clock.advance(8 * time.Second)
	d.Tick(t.Context())

	ids := h.rcv.ids()
	if len(ids) != 6 {
		t.Fatalf("want 5 attempts for the failing event plus 1 for the next, got %v", ids)
	}
	for _, id := range ids[:5] {
		if id != ids[0] {
			t.Fatalf("event ID changed across retries: %v", ids)
		}
	}
	if h.rcv.received()[5].MessageID != second {
		t.Fatalf("delivery did not continue past the dead letter: %v", h.messageIDs())
	}
	dead, err := ReadDeadLetters(h.dir, h.ep.ID)
	if err != nil || len(dead) != 1 {
		t.Fatalf("dead letters %+v %v", dead, err)
	}
	if got := dead[0]; got.EventID != ids[0] || got.Attempts != 5 || got.Code != CodeServerError || got.Status != 503 || got.Op != storage.OpMessageCreated || got.EndpointName != "crm" {
		t.Fatalf("dead letter %+v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(h.dir, DeadLetterFileName))
	if strings.Contains(string(raw), bodyText) {
		t.Fatal("dead-letter file contains message content")
	}
}

func TestDispatcherBacksOffWhenDeadLetterCannotBeWritten(t *testing.T) {
	h := newHarness(t, AddRequest{})
	h.rcv.status = func(Event) int { return 400 }
	if err := os.Mkdir(filepath.Join(h.dir, DeadLetterFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	d := h.dispatcher()
	d.Tick(t.Context())
	h.message("one@g.us")
	d.Tick(t.Context())
	d.Tick(t.Context())
	if got := h.rcv.ids(); len(got) != 1 {
		t.Fatalf("event re-sent inside the backoff window: %v", got)
	}
	h.clock.advance(8 * time.Second)
	d.Tick(t.Context())
	if got := h.rcv.ids(); len(got) != 2 {
		t.Fatalf("event not retried after backoff: %v", got)
	}
}

func TestDispatcherDeadLettersPermanentRejectionImmediately(t *testing.T) {
	h := newHarness(t, AddRequest{})
	h.rcv.status = func(Event) int { return 400 }
	d := h.dispatcher()
	d.Tick(t.Context())
	h.message("one@g.us")
	d.Tick(t.Context())
	if got := h.rcv.ids(); len(got) != 1 {
		t.Fatalf("a 400 was retried: %v", got)
	}
	dead, _ := ReadDeadLetters(h.dir, "")
	if len(dead) != 1 || dead[0].Attempts != 1 || dead[0].Code != CodeRejected || dead[0].Status != 400 {
		t.Fatalf("dead letters %+v", dead)
	}
}

func TestDispatcherNeverSendsToABlockedAddress(t *testing.T) {
	h := newHarness(t, AddRequest{})
	h.sender = NewSender(NewClient(ClientOptions{Resolver: &fakeResolver{answers: [][]netip.Addr{addrs("10.9.9.9")}}, Dial: func(context.Context, string, string) (net.Conn, error) {
		t.Error("dialed a blocked address")
		return nil, context.Canceled
	}}))
	d := h.dispatcher()
	d.Tick(t.Context())
	h.message("one@g.us")
	d.Tick(t.Context())
	dead, _ := ReadDeadLetters(h.dir, "")
	if len(dead) != 1 || dead[0].Code != CodeBlocked || dead[0].Attempts != 1 {
		t.Fatalf("dead letters %+v", dead)
	}
}

// A crash after the receiver acknowledged but before the checkpoint was
// written must re-send that event (same ID) and nothing earlier.
func TestDispatcherResumesAfterCrashWithoutSkippingOrRepeating(t *testing.T) {
	h := newHarness(t, AddRequest{})
	ctx, crash := context.WithCancel(t.Context())
	defer crash()
	d := h.dispatcher()
	d.Tick(ctx)
	m1, m2, m3 := h.message("one@g.us"), h.message("one@g.us"), h.message("one@g.us")
	h.rcv.hook = func(e Event) {
		if e.MessageID == m2 {
			crash() // the process dies while the second request is in flight
		}
	}
	d.Tick(ctx)
	if got := h.messageIDs(); !equal(got, []string{m1, m2}) {
		t.Fatalf("before the crash: %v", got)
	}
	firstIDs := h.rcv.ids()

	h.rcv.hook = nil
	restarted := h.dispatcher() // fresh process: only the state file survives
	restarted.Tick(t.Context())
	if got := h.messageIDs(); !equal(got, []string{m1, m2, m2, m3}) {
		t.Fatalf("after restart: %v, want m2 re-sent once and m3 delivered", got)
	}
	if again := h.rcv.ids(); again[2] != firstIDs[1] {
		t.Fatalf("re-sent event has a new ID: %v vs %v", again, firstIDs)
	}
	restarted.Tick(t.Context())
	if got := h.messageIDs(); len(got) != 4 {
		t.Fatalf("delivered again after recovery: %v", got)
	}
}

func TestDispatcherResumesRetryBookkeepingAfterRestart(t *testing.T) {
	h := newHarness(t, AddRequest{})
	h.rcv.status = func(Event) int { return 500 }
	d := h.dispatcher()
	d.Tick(t.Context())
	h.message("one@g.us")
	d.Tick(t.Context())
	h.rcv.status = func(Event) int { return 200 }
	restarted := h.dispatcher()
	restarted.Tick(t.Context())
	if got := h.rcv.ids(); len(got) != 1 {
		t.Fatalf("restart ignored the persisted backoff: %v", got)
	}
	h.clock.advance(time.Second)
	restarted.Tick(t.Context())
	if got := h.rcv.ids(); len(got) != 2 || got[0] != got[1] {
		t.Fatalf("retry after backoff: %v", got)
	}
}

func TestDispatcherResetsToHeadWhenPositionIsUnservable(t *testing.T) {
	h := newHarness(t, AddRequest{})
	d := h.dispatcher()
	d.Tick(t.Context())
	h.message("one@g.us")

	// Simulate an archive restored from a backup: the saved epoch is foreign.
	path := filepath.Join(h.dir, StateFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st stateFile
	if err = json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	cur := st.Endpoints[h.ep.ID]
	cur.Epoch = strings.Repeat("0", 32)
	st.Endpoints[h.ep.ID] = cur
	data, _ := json.Marshal(st)
	if err = writeAtomic(h.dir, StateFileName, data); err != nil {
		t.Fatal(err)
	}

	d = h.dispatcher()
	d.Tick(t.Context())
	if got := h.rcv.ids(); len(got) != 0 {
		t.Fatalf("delivered across an epoch change: %v", got)
	}
	after := h.message("one@g.us")
	d.Tick(t.Context())
	if got := h.messageIDs(); !equal(got, []string{after}) {
		t.Fatalf("after reset delivered %v, want only %s", got, after)
	}
	log, _ := os.ReadFile(filepath.Join(h.dir, agentauth.AuditFileName))
	if !strings.Contains(string(log), "webhook.cursor_reset") {
		t.Fatalf("reset not audited:\n%s", log)
	}
}

func TestDispatcherStopsForRemovedEndpointsAndForgetsTheirPosition(t *testing.T) {
	h := newHarness(t, AddRequest{})
	d := h.dispatcher()
	d.Tick(t.Context())
	h.message("one@g.us")
	d.Tick(t.Context())
	if _, err := Remove(h.dir, "crm", time.Now()); err != nil {
		t.Fatal(err)
	}
	h.message("one@g.us")
	d.Tick(t.Context())
	if got := h.rcv.ids(); len(got) != 1 {
		t.Fatalf("delivered to a removed endpoint: %v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(h.dir, StateFileName))
	if strings.Contains(string(raw), h.ep.ID) {
		t.Fatalf("state still tracks the removed endpoint: %s", raw)
	}
}

func TestDispatcherEndpointsAreIndependent(t *testing.T) {
	h := newHarness(t, AddRequest{})
	other := &receiver{t: t}
	secret, ep2, err := Add(h.dir, AddRequest{Name: "slow", URL: "https://example.com/slow"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other.secret = secret
	other.status = func(Event) int { return 500 }
	// One server and client serve both receivers, routed by path.
	h.sender = senderForMany(t, map[string]*receiver{"/hook": h.rcv, "/slow": other})
	d := h.dispatcher()
	d.Tick(t.Context())
	h.message("one@g.us")
	d.Tick(t.Context())
	if len(h.rcv.received()) != 1 || len(other.received()) != 1 {
		t.Fatalf("healthy %d, failing %d", len(h.rcv.received()), len(other.received()))
	}
	h.message("one@g.us")
	d.Tick(t.Context())
	if len(h.rcv.received()) != 2 {
		t.Fatalf("a failing endpoint blocked a healthy one: %d", len(h.rcv.received()))
	}
	if other.received()[0].Endpoint != ep2.ID {
		t.Fatalf("endpoint IDs mixed up: %+v", other.received()[0])
	}
}

func TestDispatcherAuditHasOutcomesButNoBodiesOrSecrets(t *testing.T) {
	h := newHarness(t, AddRequest{})
	d := h.dispatcher()
	d.Tick(t.Context())
	h.message("one@g.us")
	d.Tick(t.Context())
	raw, err := os.ReadFile(filepath.Join(h.dir, agentauth.AuditFileName))
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	for _, want := range []string{"webhook.start", "webhook.delivered", h.ep.ID, `"client_name":"crm"`} {
		if !strings.Contains(log, want) {
			t.Errorf("audit log lacks %q:\n%s", want, log)
		}
	}
	secret, _ := Active(h.dir)
	for _, banned := range []string{bodyText, secret[0].Secret, "alice"} {
		if strings.Contains(log, banned) {
			t.Errorf("audit log contains %q", banned)
		}
	}
}

func TestDispatcherIdlesWithoutEndpointsOrAccount(t *testing.T) {
	dir := privateDir(t)
	store, err := storage.Open(t.Context(), filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d := &Dispatcher{Dir: dir, Store: store, Sender: NewSender(nil)}
	d.Tick(t.Context()) // no registry
	if _, _, err = Add(dir, AddRequest{Name: "crm", URL: "https://example.com/h"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	d.Tick(t.Context()) // endpoint but no paired account
	if _, err = os.Stat(filepath.Join(dir, StateFileName)); !os.IsNotExist(err) {
		t.Fatal("state written without an account")
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	h := newHarness(t, AddRequest{})
	d := h.dispatcher()
	d.Poll = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	h.message("one@g.us")
	deadline := time.Now().Add(5 * time.Second)
	for len(h.rcv.received()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if len(h.rcv.received()) == 0 {
		t.Fatal("Run delivered nothing")
	}
}
