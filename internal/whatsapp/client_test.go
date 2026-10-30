package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"github.com/gehariharan/wasync/internal/localfs"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestUnpairedSessionReopensWithoutNetwork(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "session.db")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	for range 2 {
		c, err := Open(ctx, path, log)
		if err != nil {
			t.Fatal(err)
		}
		if c.Paired() {
			t.Fatal("fresh store appears paired")
		}
		if err = c.Connect(ctx); !errors.Is(err, ErrNotPaired) {
			t.Fatalf("unpaired connect: %v", err)
		}
		if err = c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestLifecycleAndObservationDiscardPayloads(t *testing.T) {
	c := &Client{connected: make(chan struct{}, 1), fatal: make(chan error, 1), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.onEvent(&events.Connected{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.waitAuthenticated(ctx); err != nil {
		t.Fatal(err)
	}
	c.onEvent(&events.Message{})
	c.onEvent(&events.HistorySync{})
	if o := c.Observation(); o.LiveEvents != 1 || o.HistoryBatches != 1 {
		t.Fatalf("bad observation %+v", o)
	}
	c.onEvent(&events.LoggedOut{})
	if err := c.Observe(ctx, time.Second); err == nil {
		t.Fatal("terminal disconnect ignored")
	}
}

func TestSavedDeviceCredentialsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTestDir(t), "session.db")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := Open(ctx, path, log)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic local fixture only: this does not pair with or contact WhatsApp.
	device := c.client.Store
	jid, err := types.ParseJID("15550000000:1@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	device.ID = &jid
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte("fixture"), AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err = device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	noise := append([]byte(nil), device.NoiseKey.Priv[:]...)
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = Open(ctx, path, log)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Paired() || c.AccountJID() != "15550000000@s.whatsapp.net" {
		t.Fatal("saved device identity did not survive restart")
	}
	if !bytes.Equal(noise, c.client.Store.NoiseKey.Priv[:]) {
		t.Fatal("device keys were regenerated")
	}
}

func privateTestDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// openPairer behaves like whatsmeow while pairing: a QR channel can only be
// requested while disconnected, and a stopped attempt leaves the socket open.
type openPairer struct {
	mu        sync.Mutex
	connected bool
	attempts  [][]whatsmeow.QRChannelItem
	ch        chan whatsmeow.QRChannelItem
}

func (p *openPairer) GetQRChannel(context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.connected {
		return nil, whatsmeow.ErrQRAlreadyConnected
	}
	p.ch = make(chan whatsmeow.QRChannelItem, 8)
	return p.ch, nil
}
func (p *openPairer) ConnectContext(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connected = true
	for _, item := range p.attempts[0] {
		p.ch <- item
	}
	p.attempts = p.attempts[1:]
	return nil
}
func (p *openPairer) Disconnect() { p.mu.Lock(); p.connected = false; p.mu.Unlock() }

func TestAStoppedQRLoginClosesTheConnectionSoARetryGetsAFreshCode(t *testing.T) {
	c, err := Open(context.Background(), filepath.Join(privateTestDir(t), "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	p := &openPairer{attempts: [][]whatsmeow.QRChannelItem{
		{{Event: whatsmeow.QRChannelEventCode, Code: "2@FIRST"}, {Event: whatsmeow.QRChannelEventPasskeyRequest}},
		{{Event: whatsmeow.QRChannelEventCode, Code: "2@SECOND"}},
	}}
	c.pairing = p
	var shown []string
	if err := c.LoginWithQR(context.Background(), func(code string) { shown = append(shown, code) }); err == nil {
		t.Fatal("a passkey request should stop QR linking")
	}
	if p.connected {
		t.Fatal("the stopped attempt left the connection open")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.LoginWithQR(ctx, func(code string) { shown = append(shown, code); cancel() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("retry: %v", err)
	}
	if len(shown) != 2 || shown[1] != "2@SECOND" {
		t.Fatalf("codes shown %q, want a fresh code on retry", shown)
	}
}
