package humancheck

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fake struct {
	available bool
	result    error
	calls     int
	reasons   []string
	block     chan struct{}
}

func (f *fake) Name() string    { return "test prompt" }
func (f *fake) Available() bool { return f.available }
func (f *fake) Verify(ctx context.Context, reason string) error {
	f.calls++
	f.reasons = append(f.reasons, reason)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.result
}

func TestCleanStripsControlTextAndTruncates(t *testing.T) {
	got := Clean("Fam\x00ily\r\n  chat‮ evil", 100)
	if got != "Fam ily chat evil" {
		t.Fatalf("got %q", got)
	}
	long := Clean(strings.Repeat("a", 500), 20)
	if r := []rune(long); len(r) != 20 || r[19] != '…' {
		t.Fatalf("not truncated: %q", long)
	}
}

func TestGateWithoutCheckerIsNeverEnabled(t *testing.T) {
	for _, g := range []*Gate{nil, NewGate(nil), NewGate(&fake{available: false})} {
		if g.Enabled() {
			t.Fatal("gate enabled without an available prompt")
		}
		if err := g.Confirm(context.Background(), "x"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("got %v, want ErrUnavailable", err)
		}
	}
}

func TestConfirmPassesOnlyWhenThePersonConfirms(t *testing.T) {
	f := &fake{available: true}
	g := NewGate(f)
	if err := g.Confirm(context.Background(), "Approve\nthing"); err != nil {
		t.Fatal(err)
	}
	if f.reasons[0] != "Approve thing" {
		t.Fatalf("reason not cleaned: %q", f.reasons[0])
	}
	f.result = errors.New("cancelled by the person")
	if err := g.Confirm(context.Background(), "x"); !errors.Is(err, ErrDeclined) {
		t.Fatalf("got %v, want ErrDeclined", err)
	}
}

func TestOnePromptAtATime(t *testing.T) {
	f := &fake{available: true, block: make(chan struct{})}
	g := NewGate(f)
	first := make(chan error, 1)
	go func() { first <- g.Confirm(context.Background(), "first") }()
	for i := 0; i < 200 && func() bool { g.mu.Lock(); defer g.mu.Unlock(); return !g.busy }(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if err := g.Confirm(context.Background(), "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
	close(f.block)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Fatalf("the system prompt ran %d times", f.calls)
	}
}

func TestRepeatedUnconfirmedPromptsLockOutThenRecover(t *testing.T) {
	f := &fake{available: true, result: errors.New("no")}
	g := NewGate(f)
	clock := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return clock }
	for i := 0; i < failLimit; i++ {
		if err := g.Confirm(context.Background(), "x"); !errors.Is(err, ErrDeclined) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	before := f.calls
	if err := g.Confirm(context.Background(), "x"); !errors.Is(err, ErrLocked) || f.calls != before {
		t.Fatalf("expected a lock-out without a prompt, got %v (calls %d->%d)", err, before, f.calls)
	}
	clock = clock.Add(lockFor + time.Second)
	f.result = nil
	if err := g.Confirm(context.Background(), "x"); err != nil {
		t.Fatalf("lock-out should end: %v", err)
	}
}

func TestSuccessClearsEarlierFailures(t *testing.T) {
	f := &fake{available: true, result: errors.New("no")}
	g := NewGate(f)
	for i := 0; i < failLimit-1; i++ {
		_ = g.Confirm(context.Background(), "x")
	}
	f.result = nil
	if err := g.Confirm(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	f.result = errors.New("no")
	for i := 0; i < failLimit-1; i++ {
		if err := g.Confirm(context.Background(), "x"); !errors.Is(err, ErrDeclined) {
			t.Fatalf("a success should reset the count: %v", err)
		}
	}
}

// The real prompt is never shown here; this only checks the platform code is
// wired and does not claim a prompt exists where there is none.
func TestSystemCheckerReportsAvailability(t *testing.T) {
	c := System()
	t.Logf("%q available=%v", c.Name(), c.Available())
}
