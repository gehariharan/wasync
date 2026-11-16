package whatsapp

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestHealthFactsFromEvents(t *testing.T) {
	c := &Client{}
	t0 := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	if s := c.Status(); !s.LastEventAt.IsZero() || !s.LastConnectedAt.IsZero() || !s.LastKeepAliveOK.IsZero() || s.KeepAliveFailing || s.Paired {
		t.Fatalf("fresh client has health facts: %+v", s)
	}
	c.health.record(&events.Connected{}, t0)
	s := c.Status()
	if !s.LastEventAt.Equal(t0) || !s.LastConnectedAt.Equal(t0) || !s.LastKeepAliveOK.Equal(t0) || s.KeepAliveFailing || !s.Paired {
		t.Fatalf("after connect %+v", s)
	}
	c.health.record(&events.Message{}, t0.Add(time.Minute))
	c.health.record(&events.KeepAliveTimeout{ErrorCount: 2, LastSuccess: t0.Add(90 * time.Second)}, t0.Add(3*time.Minute))
	s = c.Status()
	if !s.KeepAliveFailing || !s.LastEventAt.Equal(t0.Add(time.Minute)) || !s.LastKeepAliveOK.Equal(t0.Add(90*time.Second)) {
		t.Fatalf("keepalive timeout must not count as contact: %+v", s)
	}
	// An older LastSuccess never moves the timestamp backwards.
	c.health.record(&events.KeepAliveTimeout{ErrorCount: 3, LastSuccess: t0}, t0.Add(4*time.Minute))
	c.health.record(&events.Disconnected{}, t0.Add(5*time.Minute))
	if s = c.Status(); !s.LastKeepAliveOK.Equal(t0.Add(90*time.Second)) || !s.LastEventAt.Equal(t0.Add(time.Minute)) || !s.KeepAliveFailing {
		t.Fatalf("after stale timeout %+v", s)
	}
	c.health.record(&events.KeepAliveRestored{}, t0.Add(6*time.Minute))
	if s = c.Status(); s.KeepAliveFailing || !s.LastKeepAliveOK.Equal(t0.Add(6*time.Minute)) || !s.LastEventAt.Equal(t0.Add(6*time.Minute)) {
		t.Fatalf("after restore %+v", s)
	}
	c.health.record(&events.KeepAliveTimeout{}, t0.Add(7*time.Minute))
	c.health.record(&events.Connected{}, t0.Add(8*time.Minute))
	if s = c.Status(); s.KeepAliveFailing || !s.LastConnectedAt.Equal(t0.Add(8*time.Minute)) {
		t.Fatalf("reconnect must clear keepalive failure %+v", s)
	}
	c.health.record(&events.LoggedOut{}, t0.Add(9*time.Minute))
	if s = c.Status(); s.Paired {
		t.Fatal("logged out session still paired")
	}
}

func TestStatusConcurrentWithEvents(t *testing.T) {
	c := &Client{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			c.health.record(&events.KeepAliveTimeout{LastSuccess: time.Unix(int64(i), 0)}, time.Now())
			c.health.record(&events.KeepAliveRestored{}, time.Now())
		}
	}()
	for range 200 {
		_ = c.Status()
	}
	<-done
}

func TestProbeSessionReadsPairingWithoutCreatingFiles(t *testing.T) {
	ctx := context.Background()
	dir := privateTestDir(t)
	path := filepath.Join(dir, "session.db")
	if exists, paired, err := storage.ProbeSession(ctx, path); err != nil || exists || paired {
		t.Fatalf("missing store: %t %t %v", exists, paired, err)
	}
	if names := entryNames(t, dir); len(names) != 0 {
		t.Fatalf("probe created %v", names)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := Open(ctx, path, log)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status().Paired {
		t.Fatal("fresh store reported paired")
	}
	// While the store is open (WAL sidecars present) the probe shares them.
	if exists, paired, err := storage.ProbeSession(ctx, path); err != nil || !exists || paired {
		t.Fatalf("open unpaired store: %t %t %v", exists, paired, err)
	}
	jid, err := types.ParseJID("15550000000:1@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic local fixture only: this does not pair with or contact WhatsApp.
	device := c.wa().Store
	device.ID = &jid
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte("fixture"), AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err = device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if exists, paired, err := storage.ProbeSession(ctx, path); err != nil || !exists || !paired {
		t.Fatalf("open paired store: %t %t %v", exists, paired, err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	before := entryNames(t, dir)
	if exists, paired, err := storage.ProbeSession(ctx, path); err != nil || !exists || !paired {
		t.Fatalf("closed paired store: %t %t %v", exists, paired, err)
	}
	if after := entryNames(t, dir); !slices.Equal(before, after) {
		t.Fatalf("probe changed files: %v -> %v", before, after)
	}
	c, err = Open(ctx, path, log)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Status().Paired {
		t.Fatal("reopened paired store not reported paired")
	}
}

func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}
