package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/accessmode"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

type promptStub struct {
	confirm bool
	asked   []string
}

func (p *promptStub) Name() string    { return "the test prompt" }
func (p *promptStub) Available() bool { return true }
func (p *promptStub) Verify(_ context.Context, reason string) error {
	p.asked = append(p.asked, reason)
	if p.confirm {
		return nil
	}
	return errors.New("no")
}

func humanSession(t *testing.T, stub *promptStub) (*Session, int64, string) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	account, err := store.UseAccount(ctx, "synthetic@lid")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveGroups(ctx, account, []storage.Group{{ID: "one@g.us", Name: "Book Club"}}); err != nil {
		t.Fatal(err)
	}
	s := NewSession(ctx, store, nil)
	s.Approvals, s.DataDir = &approval.Queue{}, dir
	s.Human = humancheck.NewGate(stub)
	r, err := s.Approvals.Add(account, approval.Input{Kind: "purge-chat", Chat: "one@g.us"})
	if err != nil {
		t.Fatal(err)
	}
	return s, account, r.ID
}

// Pressing keys in the TUI is not proof of a person either (a script can drive
// a terminal), so the TUI asks the same system prompt before it approves.
func TestTUIApprovalWaitsForThePerson(t *testing.T) {
	stub := &promptStub{}
	s, account, id := humanSession(t, stub)
	err := s.ConfirmApproval(account, id)
	if err == nil || !strings.Contains(err.Error(), "nothing changed") {
		t.Fatalf("declined prompt: %v", err)
	}
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], `"Book Club"`) || !strings.Contains(stub.asked[0], "permanently erase") {
		t.Fatalf("prompt: %q", stub.asked)
	}
	stub.confirm = true
	if err = s.ConfirmApproval(account, id); err != nil {
		t.Fatal(err)
	}
}

func TestTUIConfirmationIsSkippedWithoutAPromptOrForAsk(t *testing.T) {
	stub := &promptStub{}
	s, account, id := humanSession(t, stub)
	s.Human = nil
	if err := s.ConfirmApproval(account, id); err != nil {
		t.Fatalf("no prompt on this computer: %v", err)
	}
	s.Human = humancheck.NewGate(stub)
	if err := s.ConfirmAccessMode(accessmode.Mode{Mode: accessmode.Ask}); err != nil || len(stub.asked) != 0 {
		t.Fatalf("ask each time prompted: %v %q", err, stub.asked)
	}
	if err := s.ConfirmAccessMode(accessmode.Mode{Mode: accessmode.All, History: accessmode.HistoryNow}); err == nil {
		t.Fatal("sync all went ahead when the person declined")
	}
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], "ALL chats") {
		t.Fatalf("prompt: %q", stub.asked)
	}
}
