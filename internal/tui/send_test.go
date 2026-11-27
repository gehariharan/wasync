package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

const sendID = "00000000000000000000000000abc123"

func sendModel() (Model, *fakeBackend) {
	b := &fakeBackend{}
	b.snapshot = Snapshot{Account: 1, Groups: []storage.Group{{ID: "ravi@s.whatsapp.net", Name: "Ravi"}}}
	m := NewModel(b)
	m.snapshot = b.snapshot
	m.qrSink = func(string) {}
	m.confirm, m.confirmID = "agent-action", sendID
	m.confirmRequest = &approval.Request{ID: sendID, Account: 1, Input: approval.Input{Kind: approval.KindAgentSend, Chat: "ravi@s.whatsapp.net"}, Send: &approval.AgentSend{Agent: "codex", AgentID: "0123abcd", Text: "See you at 6\nBring the keys"}}
	return m, b
}

func TestSendDialogShowsTheAgentTheChatAndTheMessage(t *testing.T) {
	m, _ := sendModel()
	text := m.confirmDescription()
	for _, want := range []string{"codex", "Ravi", "a chat that was not synced when it asked", "Message:\n │ See you at 6\n │ Bring the keys\n", "exactly this message once", "SEND abc123"} {
		if !strings.Contains(text, want) {
			t.Fatalf("dialog lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(m.View().Content, "AGENT ASKS TO SEND A MESSAGE") {
		t.Fatal("dialog is titled as a destructive action")
	}
	m.confirmRequest.Send.NewNumber, m.confirmRequest.Chat = true, "15551230000@s.whatsapp.net"
	if text = m.confirmDescription(); !strings.Contains(text, "the new number +15551230000") {
		t.Fatalf("new number:\n%s", text)
	}
}

func TestSendApprovalNeedsItsOwnPhrase(t *testing.T) {
	m, b := sendModel()
	for _, r := range "DELETE abc123" { // the destructive phrase must not approve a send
		m, _ = update(m, key(string(r)))
	}
	if _, cmd := update(m, key("enter")); cmd != nil {
		t.Fatal("wrong phrase approved")
	}
	m.confirmDraft = ""
	for _, r := range "SEND abc123" {
		m, _ = update(m, key(string(r)))
	}
	_, cmd := update(m, key("enter"))
	if cmd == nil {
		t.Fatal("typed phrase did not schedule approval")
	}
	cmd()
	if b.requested != sendID {
		t.Fatalf("approved %q", b.requested)
	}
}

type sendFixture struct {
	session *Session
	account int64
	dir     string
	sent    []whatsapp.SendInput
	agentID string
}

func newSendFixture(t *testing.T) *sendFixture {
	t.Helper()
	ctx := context.Background()
	f := &sendFixture{dir: filepath.Join(t.TempDir(), "private")}
	if err := localfs.EnsureDir(f.dir); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, filepath.Join(f.dir, "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if f.account, err = store.UseAccount(ctx, "synthetic@lid"); err != nil {
		t.Fatal(err)
	}
	_, client, err := agentauth.Grant(f.dir, agentauth.ConnectRequest("codex", nil), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.agentID = client.ID
	f.session = NewSession(ctx, store, &syncClient{status: whatsapp.CaptureStatus{Connection: "online", AccountID: f.account}})
	f.session.Approvals, f.session.DataDir = &approval.Queue{}, f.dir
	f.session.Send = func(_ context.Context, in whatsapp.SendInput) (whatsapp.SendResult, error) {
		f.sent = append(f.sent, in)
		return whatsapp.SendResult{MessageID: "3EB0AA"}, nil
	}
	return f
}

func (f *sendFixture) ask(t *testing.T, text string) approval.Request {
	t.Helper()
	r, _, err := f.session.Approvals.AddSend(f.account, "15551230000@s.whatsapp.net", approval.AgentSend{Agent: "codex", AgentID: f.agentID, Text: text, NewNumber: true})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func auditEvents(t *testing.T, dir string) []string {
	t.Helper()
	file, err := os.Open(filepath.Join(dir, agentauth.AuditFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var e agentauth.AuditEntry
		if err = json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e.Event+"/"+e.Outcome)
	}
	return events
}

func TestApprovedSendGoesOutOnceWithTheAgentsText(t *testing.T) {
	f := newSendFixture(t)
	r := f.ask(t, "Hello from codex")
	if err := f.session.ApproveAction(f.account, r.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || f.sent[0].To != "15551230000@s.whatsapp.net" || f.sent[0].Text != "Hello from codex" || f.sent[0].SentBy != "codex" {
		t.Fatalf("sent %+v", f.sent)
	}
	if got := f.session.Approvals.Get(f.account, r.ID); got.Status != "completed" || got.Send.MessageID != "3EB0AA" {
		t.Fatalf("request %+v", got)
	}
	if err := f.session.ApproveAction(f.account, r.ID); err == nil || len(f.sent) != 1 {
		t.Fatal("an approved send went out twice")
	}
	entries, err := sentlog.New(f.dir).Recent(10)
	if err != nil || len(entries) != 1 || !entries[0].Approved || !entries[0].NewNumber || entries[0].Actor != "codex" || entries[0].Outcome != sentlog.OutcomeSent {
		t.Fatalf("sent log %+v %v", entries, err)
	}
	if events := auditEvents(t, f.dir); strings.Join(events, ",") != "send_approved/allowed" {
		t.Fatalf("audit %v", events)
	}
}

func TestSendRequestsWaitForWhatsAppAndNeverReachAGoneAgent(t *testing.T) {
	f := newSendFixture(t)
	r := f.ask(t, "first")
	send := f.session.Send
	f.session.Send = nil
	if err := f.session.ApproveAction(f.account, r.ID); err == nil || f.session.Approvals.Get(f.account, r.ID).Status != approval.StatusPending {
		t.Fatalf("approved without WhatsApp: %v", err)
	}
	f.session.Send = send
	if _, err := agentauth.Revoke(f.dir, "codex", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.session.ApproveAction(f.account, r.ID); err == nil || len(f.sent) != 0 {
		t.Fatalf("sent for a revoked agent: %v %+v", err, f.sent)
	}
	if got := f.session.Approvals.Get(f.account, r.ID); got.Status != "failed_or_unknown" {
		t.Fatalf("status %q", got.Status)
	}
}

func TestDecliningASendSendsNothingAndIsAudited(t *testing.T) {
	f := newSendFixture(t)
	r := f.ask(t, "no thanks")
	if err := f.session.RejectAction(f.account, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.session.ApproveAction(f.account, r.ID); err == nil || len(f.sent) != 0 {
		t.Fatal("a declined send went out")
	}
	if events := auditEvents(t, f.dir); strings.Join(events, ",") != "send_declined/denied" {
		t.Fatalf("audit %v", events)
	}
}
