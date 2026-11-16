package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"go.mau.fi/whatsmeow/store/sqlstore"
)

// savePairedFixture writes a synthetic linked device into session.db and
// returns its noise key. It never contacts WhatsApp.
func savePairedFixture(t *testing.T, path string) []byte {
	t.Helper()
	ctx := context.Background()
	c, err := Open(ctx, path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
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
	return noise
}

func savedDeviceCount(t *testing.T, path string) int {
	t.Helper()
	container, err := sqlstore.New(context.Background(), "sqlite3", storage.DSN(path), waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	devices, err := container.GetAllDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(devices)
}

// A phone that unlinked wasync leaves a saved login that WhatsApp rejects on
// every connect. Login must clear it and show a new QR code in the same run,
// without touching the message archive.
func TestLoginAfterThePhoneUnlinkedClearsTheSavedLoginAndShowsANewQR(t *testing.T) {
	ctx := context.Background()
	dir := privateTestDir(t)
	session := filepath.Join(dir, "session.db")
	archive := filepath.Join(dir, "archive.db")
	oldNoise := savePairedFixture(t, session)

	store, err := storage.Open(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.UseAccount(ctx, "15550000000@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveGroups(ctx, account, []storage.Group{{ID: "kept@g.us", Name: "Kept group"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	c, err := Open(ctx, session, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Paired() {
		t.Fatal("fixture should start with a saved login")
	}
	// WhatsApp rejects the saved login as soon as the client connects.
	c.dial = func(context.Context) error {
		c.handleEvent(&events.LoggedOut{OnConnect: true, Reason: events.ConnectFailureLoggedOut})
		return nil
	}
	pairer := &openPairer{attempts: [][]whatsmeow.QRChannelItem{{{Event: whatsmeow.QRChannelEventCode, Code: "2@FRESH"}}}}
	c.pairing = pairer

	qrCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var shown, said []string
	err = c.login(qrCtx, func(code string) { shown = append(shown, code); cancel() }, func(m string) { said = append(said, m) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("login stopped with %v, want it waiting for the QR scan (cancelled by the test)", err)
	}
	if len(shown) != 1 || shown[0] != "2@FRESH" {
		t.Fatalf("QR codes shown %q, want one new code", shown)
	}
	if text := strings.Join(said, "\n"); !strings.Contains(text, "unlinked") || !strings.Contains(text, "new pairing") || !strings.Contains(text, "messages are kept") {
		t.Fatalf("the person was not told what happened: %q", text)
	}
	if c.Paired() {
		t.Fatal("the rejected login is still in use")
	}
	if bytes.Equal(oldNoise, c.client.Store.NoiseKey.Priv[:]) {
		t.Fatal("the new pairing reused the old device keys")
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if n := savedDeviceCount(t, session); n != 0 {
		t.Fatalf("session.db still holds %d saved device(s)", n)
	}

	store, err = storage.Open(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	groups, err := store.Groups(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Name != "Kept group" {
		t.Fatalf("archive changed: %+v", groups)
	}
}

// When the phone unlinks wasync while it is running, the library has already
// dropped the device and the logout error is still queued. Connecting again
// must not replay that error.
func TestPairingAgainAfterAnUnlinkDuringARunIgnoresTheOldLogoutError(t *testing.T) {
	ctx := context.Background()
	c, err := Open(ctx, filepath.Join(privateTestDir(t), "session.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.handleEvent(&events.LoggedOut{Reason: events.ConnectFailureLoggedOut})
	c.pairing = &openPairer{attempts: [][]whatsmeow.QRChannelItem{{{Event: whatsmeow.QRChannelEventCode, Code: "2@AGAIN"}}}}

	qrCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var shown []string
	var connection string
	err = c.LoginWithQR(qrCtx, func(code string) { shown = append(shown, code); connection = c.Status().Connection; cancel() })
	if errors.Is(err, ErrUnlinked) || !errors.Is(err, context.Canceled) {
		t.Fatalf("login replayed the logout error or stopped early: %v", err)
	}
	if len(shown) != 1 || shown[0] != "2@AGAIN" {
		t.Fatalf("QR codes shown %q", shown)
	}
	if connection != "pairing" {
		t.Fatalf("connection %q while the code was shown, want pairing", connection)
	}
}

// Only a logout is repaired by pairing again. The other terminal errors keep
// the saved login and report their own problem.
func TestOtherTerminalErrorsKeepTheSavedLogin(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event events.PermanentDisconnect
		text  string
	}{
		{"replaced", &events.StreamReplaced{}, "session replaced"},
		{"outdated", &events.ClientOutdated{}, "client version"},
		{"banned", &events.TemporaryBan{}, "temporarily restricted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			session := filepath.Join(privateTestDir(t), "session.db")
			savePairedFixture(t, session)
			c, err := Open(ctx, session, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			c.dial = func(context.Context) error { c.handleEvent(tc.event); return nil }
			c.pairing = &openPairer{} // asking it for a QR code would panic: no attempts are scripted
			err = c.login(ctx, func(string) { t.Fatal("a QR code was requested") }, func(string) {})
			if err == nil || !strings.Contains(err.Error(), tc.text) || errors.Is(err, ErrUnlinked) {
				t.Fatalf("got %v, want the %s error", err, tc.name)
			}
			if !c.Paired() {
				t.Fatal("the saved login was dropped")
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			if n := savedDeviceCount(t, session); n != 1 {
				t.Fatalf("session.db holds %d saved device(s), want the original one", n)
			}
		})
	}
}

func TestTheUnlinkedMessageDoesNotSendThePersonBackToAFailingCommand(t *testing.T) {
	msg := ErrUnlinked.Error()
	for _, want := range []string{"unlinked", "wasync login", "QR", "kept"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "login again") {
		t.Fatalf("message %q repeats the old looping advice", msg)
	}
}
