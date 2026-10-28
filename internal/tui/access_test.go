package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/gehariharan/wasync/internal/accessmode"
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

type modeBackend struct {
	fakeBackend
	set []accessmode.Mode
}

func (b *modeBackend) SetAccessMode(_ int64, m accessmode.Mode) error {
	b.set = append(b.set, m)
	return nil
}

func modeModel(chosen bool) (Model, *modeBackend) {
	b := &modeBackend{}
	b.snapshot = Snapshot{Account: 1, ModeChosen: chosen, Mode: accessmode.Mode{Mode: accessmode.Ask}}
	m := NewModel(b)
	m.snapshot = b.snapshot
	m.qrSink = func(string) {}
	return m, b
}

func TestFirstRunScreenAsksOnceAndEscChangesNothing(t *testing.T) {
	m, b := modeModel(false)
	m, _ = update(m, snapshotMsg{snapshot: b.snapshot})
	if m.confirm != "access-mode" {
		t.Fatal("first run did not ask how agents should get chats")
	}
	text := m.confirmDescription()
	for _, want := range []string{"Ask me each time", "Sync all chats", "agents can send, reply and react in any chat", "WITHOUT asking you", "stay manual", "access already given stays"} {
		if !strings.Contains(text, want) {
			t.Fatalf("screen lacks %q:\n%s", want, text)
		}
	}
	m, _ = update(m, key("esc"))
	m, _ = update(m, snapshotMsg{snapshot: b.snapshot})
	if m.confirm != "" || len(b.set) != 0 {
		t.Fatalf("screen came back or saved after Esc: %q %v", m.confirm, b.set)
	}
	m, _ = update(m, key("M"))
	if m.confirm != "access-mode" {
		t.Fatal("M does not reopen the screen")
	}
}

func TestSyncAllNeedsDepthAndTypedPhrase(t *testing.T) {
	m, b := modeModel(true)
	m, _ = update(m, key("M"))
	m, _ = update(m, key("2"))
	if !strings.Contains(m.confirmDescription(), "How much history") {
		t.Fatalf("no history question:\n%s", m.confirmDescription())
	}
	m, _ = update(m, key("2"))
	text := m.confirmDescription()
	if !strings.Contains(text, "full history sync") || !strings.Contains(text, "READ, SEND, REPLY and REACT in any chat") || !strings.Contains(text, "SYNC ALL") {
		t.Fatalf("summary:\n%s", text)
	}
	for _, r := range "sync all" { // wrong case must not confirm
		m, _ = update(m, key(string(r)))
	}
	if _, cmd := update(m, key("enter")); cmd != nil || len(b.set) != 0 {
		t.Fatal("a wrong phrase confirmed")
	}
	m.confirmDraft = ""
	for _, r := range "SYNC ALL" {
		m, _ = update(m, key(string(r)))
	}
	_, cmd := update(m, key("enter"))
	if cmd == nil {
		t.Fatal("typed phrase did not apply")
	}
	cmd()
	if len(b.set) != 1 || b.set[0].Mode != accessmode.All || b.set[0].History != accessmode.HistoryFull {
		t.Fatalf("applied %+v", b.set)
	}
}

func TestChoosingAskSavesAtOnce(t *testing.T) {
	m, b := modeModel(false)
	m, _ = update(m, snapshotMsg{snapshot: b.snapshot})
	_, cmd := update(m, key("enter"))
	if cmd == nil {
		t.Fatal("Enter did not choose the default")
	}
	cmd()
	if len(b.set) != 1 || b.set[0].Mode != accessmode.Ask || b.set[0].History != "" {
		t.Fatalf("%+v", b.set)
	}
}

func TestRequestThatMatchedNothingCanOnlyBeRejected(t *testing.T) {
	m, b := accessModel()
	m.confirmRequest.Access.Candidates = nil
	text := m.confirmDescription()
	if !strings.Contains(text, "No chat matched") || !strings.Contains(text, "alice") || !strings.Contains(m.View().Content, "AGENT ASKS FOR CHAT ACCESS") {
		t.Fatalf("dialog:\n%s", text)
	}
	for _, r := range "ALLOW abc123" {
		m, _ = update(m, key(string(r)))
	}
	if _, cmd := update(m, key("enter")); cmd != nil || b.requested != "" {
		t.Fatal("a request that matched nothing could be approved")
	}
	if _, cmd := update(m, key("y")); cmd != nil {
		t.Fatal("y approved a request that matched nothing")
	}
	_, cmd := update(m, key("n"))
	if cmd == nil {
		t.Fatal("n did not reject")
	}
}
