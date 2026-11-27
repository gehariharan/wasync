package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

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

func TestTUIConfirmationIsSkippedWithoutAPrompt(t *testing.T) {
	stub := &promptStub{}
	s, account, id := humanSession(t, stub)
	s.Human = nil
	if err := s.ConfirmApproval(account, id); err != nil {
		t.Fatalf("no prompt on this computer: %v", err)
	}
}

// The TUI prompt for an agent's send names the agent, the chat and the text.
func TestTUISendPromptNamesTheMessage(t *testing.T) {
	stub := &promptStub{}
	s, account, _ := humanSession(t, stub)
	r, _, err := s.Approvals.AddSend(account, "one@g.us", approval.AgentSend{Agent: "codex", AgentID: "0123abcd", Text: "Meeting moved to 7"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmApproval(account, r.ID); err == nil {
		t.Fatal("declined prompt went ahead")
	}
	if len(stub.asked) != 1 || !strings.Contains(stub.asked[0], `agent "codex" send this message to "Book Club", a chat that was not synced when it asked: "Meeting moved to 7"`) {
		t.Fatalf("prompt: %q", stub.asked)
	}
}
