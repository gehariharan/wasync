package avatars

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

var jpeg = []byte{0xFF, 0xD8, 0xFF, 0xE0, 1}

type fakeWhatsApp struct {
	mu    sync.Mutex
	calls []string // chat/knownID per request
	at    []time.Time
	reply func(chat, known string) (Picture, error)
}

func (f *fakeWhatsApp) fetch(_ context.Context, chat, known string) (Picture, error) {
	f.mu.Lock()
	f.calls = append(f.calls, chat+"/"+known)
	f.at = append(f.at, time.Now())
	f.mu.Unlock()
	return f.reply(chat, known)
}

func (f *fakeWhatsApp) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newCache(t *testing.T, f *fakeWhatsApp) *Cache {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	c := New(dir, f.fetch)
	c.Interval = 20 * time.Millisecond
	return c
}

func TestPicturesAreCachedAndRefreshedWithTheirID(t *testing.T) {
	ctx := context.Background()
	f := &fakeWhatsApp{reply: func(chat, known string) (Picture, error) {
		if known == "p1" {
			return Picture{ID: "p1", Unchanged: true}, nil
		}
		return Picture{ID: "p1", Data: jpeg}, nil
	}}
	c := newCache(t, f)
	var now atomic.Pointer[time.Time]
	start := time.Now()
	now.Store(&start)
	c.now = func() time.Time { return *now.Load() }
	if got := c.Picture(ctx, 1, "a@g.us", time.Second); string(got) != string(jpeg) {
		t.Fatalf("first: %v", got)
	}
	if got := c.Picture(ctx, 1, "a@g.us", time.Second); string(got) != string(jpeg) || f.count() != 1 {
		t.Fatalf("cached: %v after %d requests", got, f.count())
	}
	// Past the TTL the cached picture is shown at once and checked by its ID.
	later := start.Add(DefaultTTL + time.Minute)
	now.Store(&later)
	if got := c.Picture(ctx, 1, "a@g.us", 0); string(got) != string(jpeg) {
		t.Fatalf("stale: %v", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	calls := append([]string(nil), f.calls...)
	f.mu.Unlock()
	if len(calls) != 2 || calls[1] != "a@g.us/p1" {
		t.Fatalf("refresh: %v", calls)
	}
	// Another account's chat with the same address has its own picture.
	if c.Picture(ctx, 2, "a@g.us", time.Second); f.count() != 3 {
		t.Fatalf("another account's cached picture was used: %d requests", f.count())
	}
}

func TestNoPictureIsRememberedAndFailuresAreNotRetriedAtOnce(t *testing.T) {
	ctx := context.Background()
	fail := errors.New("offline")
	f := &fakeWhatsApp{reply: func(chat, known string) (Picture, error) {
		if chat == "down@g.us" {
			return Picture{}, fail
		}
		return Picture{}, nil
	}}
	c := newCache(t, f)
	for range 3 {
		if c.Picture(ctx, 1, "none@g.us", time.Second) != nil {
			t.Fatal("a chat without a picture got one")
		}
	}
	for range 3 {
		if c.Picture(ctx, 1, "down@g.us", time.Second) != nil {
			t.Fatal("a failed request gave a picture")
		}
	}
	if f.count() != 2 {
		t.Fatalf("%d requests; no-picture answers and failures must not repeat at once", f.count())
	}
}

func TestRequestsArePacedOneAtATime(t *testing.T) {
	ctx := context.Background()
	var inFlight, most atomic.Int32
	f := &fakeWhatsApp{reply: func(chat, known string) (Picture, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		if n > most.Load() {
			most.Store(n)
		}
		time.Sleep(time.Millisecond)
		return Picture{ID: chat, Data: jpeg}, nil
	}}
	c := newCache(t, f)
	c.Interval = 30 * time.Millisecond
	var wg sync.WaitGroup
	for _, chat := range []string{"a@g.us", "b@g.us", "c@g.us", "a@g.us", "b@g.us"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Picture(ctx, 1, chat, 2*time.Second)
		}()
	}
	wg.Wait()
	if f.count() != 3 || most.Load() != 1 {
		t.Fatalf("%d requests, %d at once", f.count(), most.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 1; i < len(f.at); i++ {
		if gap := f.at[i].Sub(f.at[i-1]); gap < c.Interval-5*time.Millisecond {
			t.Fatalf("requests %d and %d were %v apart", i-1, i, gap)
		}
	}
}

func TestWithoutAFetcherOnlyCachedPicturesShow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if got := New(dir, nil).Picture(context.Background(), 1, "a@g.us", time.Second); got != nil {
		t.Fatalf("offline: %v", got)
	}
	online := New(dir, func(context.Context, string, string) (Picture, error) { return Picture{ID: "p", Data: jpeg}, nil })
	online.Picture(context.Background(), 1, "a@g.us", time.Second)
	if got := New(dir, nil).Picture(context.Background(), 1, "a@g.us", time.Second); string(got) != string(jpeg) {
		t.Fatalf("cached while offline: %v", got)
	}
}
