package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

const accessID = "00000000000000000000000000abc123"

type accessBackend struct {
	fakeBackend
	decided [3]string
}

func (a *accessBackend) DecideAccess(_ int64, id, chat, history string) error {
	a.decided = [3]string{id, chat, history}
	return nil
}

func accessModel() (Model, *accessBackend) {
	b := &accessBackend{}
	b.snapshot = Snapshot{Account: 1}
	m := NewModel(b)
	m.snapshot = b.snapshot
	m.qrSink = func(string) {}
	m.confirm, m.confirmID, m.confirmHistory = "agent-action", accessID, approval.HistoryNow
	m.confirmRequest = &approval.Request{ID: accessID, Account: 1, Input: approval.Input{Kind: approval.KindChatAccess}, Access: &approval.Access{
		Agent: "jit", AgentID: "0123abcd", Send: true, Query: "alice", Scopes: []string{"read.chats", "read.messages", "send"},
		Candidates: []approval.Candidate{{Chat: "1@s.whatsapp.net", Name: "Alice A"}, {Chat: "2@s.whatsapp.net", Name: "Alice B"}},
	}}
	return m, b
}

func TestAccessDialogShowsWhoGetsWhatBeforeConfirming(t *testing.T) {
	m, _ := accessModel()
	text := m.confirmDescription()
	for _, want := range []string{"jit", "READ and SEND", "alice", "Alice A", "2@s.whatsapp.net", "FROM NOW ON ONLY", "only this chat", "ALLOW abc123"} {
		if !strings.Contains(text, want) {
			t.Fatalf("dialog lacks %q:\n%s", want, text)
		}
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	text = m.confirmDescription()
	if !strings.Contains(text, "FULL HISTORY SYNC") || !strings.Contains(text, "> Alice B") || !strings.Contains(text, "turn on capture for 2@s.whatsapp.net") {
		t.Fatalf("choices not reflected:\n%s", text)
	}
	if !strings.Contains(m.View().Content, "AGENT ASKS FOR CHAT ACCESS") {
		t.Fatal("dialog is titled as a destructive action")
	}
}

func TestAccessApprovalAppliesExactlyTheDisplayedChoice(t *testing.T) {
	m, b := accessModel()
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyTab})
	for _, r := range "DELETE abc123" { // the destructive phrase must not approve an access request
		m, _ = update(m, key(string(r)))
	}
	if _, cmd := update(m, key("enter")); cmd != nil {
		t.Fatal("wrong phrase approved")
	}
	m.confirmDraft = ""
	for _, r := range "ALLOW abc123" {
		m, _ = update(m, key(string(r)))
	}
	// Changing a choice after typing clears the phrase, so it is re-confirmed.
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.confirmDraft != "" {
		t.Fatal("phrase survived a changed choice")
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyTab})
	for _, r := range "ALLOW abc123" {
		m, _ = update(m, key(string(r)))
	}
	_, cmd := update(m, key("enter"))
	if cmd == nil {
		t.Fatal("typed phrase did not schedule approval")
	}
	cmd()
	if b.decided != [3]string{accessID, "2@s.whatsapp.net", approval.HistoryFull} || b.requested != accessID {
		t.Fatalf("decided %v approved %q", b.decided, b.requested)
	}
}

func TestAccessRejectionGrantsAndEnablesNothing(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "private")
	if err := localfs.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	account, _ := store.UseAccount(ctx, "synthetic@lid")
	_ = store.SaveGroups(ctx, account, []storage.Group{{ID: "one@g.us", Name: "One"}})
	_, client, err := agentauth.Grant(dir, agentauth.GrantRequest{Name: "jit", OnDemand: true}, timeNow())
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(ctx, store, nil)
	session.Approvals, session.DataDir = &approval.Queue{}, dir
	r, err := session.Approvals.AddAccess(account, approval.Access{Agent: "jit", AgentID: client.ID, Scopes: []string{"read.chats"}, Query: "one", Candidates: []approval.Candidate{{Chat: "one@g.us", Name: "One"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = session.ApproveAction(account, r.ID); err == nil {
		t.Fatal("approved without choosing")
	}
	if err = session.RejectAction(account, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = session.ApproveAction(account, r.ID); err == nil {
		t.Fatal("approved after rejection")
	}
	groups, _ := store.GroupSummaries(ctx, account)
	if groups[0].Enabled {
		t.Fatal("rejection enabled capture")
	}
	if c, _ := agentauth.ActiveByName(dir, "jit"); len(c.Grants) != 0 {
		t.Fatalf("rejection granted %+v", c.Grants)
	}
	// An agent revoked while the dialog was open gets nothing, and capture is left untouched.
	r, _ = session.Approvals.AddAccess(account, approval.Access{Agent: "jit", AgentID: client.ID, Scopes: []string{"read.chats"}, Query: "one", Candidates: []approval.Candidate{{Chat: "one@g.us", Name: "One"}}})
	if err = session.DecideAccess(account, r.ID, "one@g.us", approval.HistoryNow); err != nil {
		t.Fatal(err)
	}
	if _, err = agentauth.Revoke(dir, "jit", timeNow()); err != nil {
		t.Fatal(err)
	}
	if err = session.ApproveAction(account, r.ID); err == nil {
		t.Fatal("granted to a revoked agent")
	}
	if got := session.Approvals.Get(account, r.ID); got.Status != "failed_or_unknown" {
		t.Fatalf("status %q", got.Status)
	}
	if groups, _ = store.GroupSummaries(ctx, account); groups[0].Enabled {
		t.Fatal("capture enabled for a revoked agent")
	}
}

func timeNow() time.Time { return time.Now() }
