package whatsapp

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	signallog "go.mau.fi/libsignal/logger"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// captureTerminal replaces standard output and error with pipes for the
// duration of fn and returns whatever was written to them.
func captureTerminal(t *testing.T, fn func()) string {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		io.Copy(&b, r)
		done <- b.String()
	}()
	func() {
		defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
		fn()
	}()
	w.Close()
	return <-done
}

func emitLibraryNoise() {
	signallog.Error("Unable to get or create message keys: received message with old counter (index: 3, count: 2)")
	signallog.Error("Error deserializing signal message: bad proto")
	signallog.Warning("session is older than expected")
	signallog.Info("identity key material")
	signallog.Debug("ratchet state")
	log.Printf("package using the standard logger")
}

func TestLibraryLoggingNeverReachesTheTerminal(t *testing.T) {
	var logged bytes.Buffer
	ctx := context.Background()
	c, err := Open(ctx, filepath.Join(privateTestDir(t), "session.db"), slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	if out := captureTerminal(t, emitLibraryNoise); out != "" {
		t.Fatalf("library logging reached the terminal: %q", out)
	}
	text := logged.String()
	if !strings.Contains(text, "Error deserializing signal message") || !strings.Contains(text, `"level":"ERROR"`) {
		t.Fatalf("a real decryption problem was not written to the wasync log: %q", text)
	}
	if !strings.Contains(text, "session is older than expected") || !strings.Contains(text, "package using the standard logger") {
		t.Fatalf("warnings were not written to the wasync log: %q", text)
	}
	if strings.Contains(text, "old counter") {
		t.Fatalf("a benign retry was logged at an operator-visible level: %q", text)
	}
	if strings.Contains(text, "identity key material") || strings.Contains(text, "ratchet state") {
		t.Fatalf("low-level protocol detail was logged: %q", text)
	}
}

func TestBenignRetryIsKeptAsDebugOnly(t *testing.T) {
	var logged bytes.Buffer
	routeLibraryLogs(slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { routeLibraryLogs(nil) })
	signallog.Error("received message with old counter (index: 3, count: 2)")
	if !strings.Contains(logged.String(), `"level":"DEBUG"`) || strings.Contains(logged.String(), `"level":"ERROR"`) {
		t.Fatalf("old-counter retry was not demoted to debug: %q", logged.String())
	}
}

func TestLibraryLoggingIsDiscardedWithoutADestination(t *testing.T) {
	routeLibraryLogs(nil)
	if out := captureTerminal(t, emitLibraryNoise); out != "" {
		t.Fatalf("library logging reached the terminal with no logger configured: %q", out)
	}
}

func TestUndecryptableMessageInEnabledChatIsCounted(t *testing.T) {
	c, _, _ := historyClient(t)
	enabled := types.NewJID("123", types.GroupServer)
	other := types.NewJID("999", types.GroupServer)
	for _, chat := range []types.JID{enabled, other} {
		if err := c.captureEvent(&events.UndecryptableMessage{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat}}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.Status().Undecryptable; got != 1 {
		t.Fatalf("undecryptable count = %d, want 1 (only the enabled chat counts)", got)
	}
}

func TestGroupSubjectLookupFillsBlankName(t *testing.T) {
	c := &Client{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	jid := types.NewJID("120363266948712648", types.GroupServer)
	c.groupInfo = func(_ context.Context, got types.JID) (*types.GroupInfo, error) {
		if got != jid {
			t.Fatalf("looked up %s", got)
		}
		return &types.GroupInfo{GroupName: types.GroupName{Name: "  Family  "}}, nil
	}
	if got := c.groupSubject(context.Background(), jid); got != "Family" {
		t.Fatalf("subject = %q", got)
	}
	c.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) { return nil, io.ErrUnexpectedEOF }
	if got := c.groupSubject(context.Background(), jid); got != "" {
		t.Fatalf("failed lookup returned %q", got)
	}
}
