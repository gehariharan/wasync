package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/opensend"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/sendlist"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/storage"
)

// connect grants name the credential a person gives an agent at Connect.
func (f accessFixture) connect(t *testing.T, name string) string {
	t.Helper()
	credential, _, err := agentauth.Grant(f.dir, agentauth.ConnectRequest(name, nil), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func (f accessFixture) sync(t *testing.T, chat string, on bool) {
	t.Helper()
	if err := f.store.SetEnabled(context.Background(), f.account, []string{chat}, on); err != nil {
		t.Fatal(err)
	}
}

type sendRequestData struct {
	Data  sendRequestView `json:"data"`
	Error string          `json:"error"`
	Code  string          `json:"error_code"`
	Help  string          `json:"help"`
}

func (f accessFixture) askToSend(t *testing.T, token string, body any, want int) sendRequestData {
	t.Helper()
	code, out := f.do(t, "POST", "/api/v1/send-requests", token, body)
	if code != want {
		t.Fatalf("send request: got %d want %d: %s", code, want, out)
	}
	var got sendRequestData
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// A connected agent reads, replies and reacts in exactly the synced chats:
// a chat synced or unsynced anywhere (here straight in the archive, as the CLI
// or the TUI would) applies at its next request, with no prompt.
func TestAConnectedAgentFollowsTheSyncedChats(t *testing.T) {
	stub := &promptStub{}
	f := setupAccessWith(t, humancheck.NewGate(stub))
	codex := f.connect(t, "codex")
	messages := "/api/v1/chats/" + familyChat + "/messages"
	if code, out := f.do(t, "GET", messages, codex, nil); code != 403 || !strings.Contains(string(out), "not synced") {
		t.Fatalf("an unsynced chat: %d %s", code, out)
	}
	f.sync(t, familyChat, true)
	if code, out := f.do(t, "GET", messages, codex, nil); code != 200 || !strings.Contains(string(out), "synthetic family text") {
		t.Fatalf("a chat synced later: %d %s", code, out)
	}
	if got := mustClient(t, f.dir, "codex"); strings.Join(got.Chats, ",") != familyChat {
		t.Fatalf("stored chats %v", got.Chats)
	}
	// Replying passes authorization in a synced chat (the owner has no
	// WhatsApp connection here, so it stops there), and asks nobody.
	if code, out := f.do(t, "POST", "/api/v1/messages", codex, map[string]string{"chat": familyChat, "text": "hi"}); code == 403 || code == 401 {
		t.Fatalf("send in a synced chat refused: %d %s", code, out)
	}
	f.sync(t, familyChat, false)
	if code, _ := f.do(t, "GET", messages, codex, nil); code != 403 {
		t.Fatalf("an unsynced chat is still readable: %d", code)
	}
	if len(stub.seen()) != 0 || len(f.queue.PendingList(f.account)) != 0 {
		t.Fatalf("a synced chat asked the person: %q", stub.seen())
	}
	log, _ := os.ReadFile(filepath.Join(f.dir, agentauth.AuditFileName))
	if strings.Count(string(log), `"event":"synced_chats"`) != 2 {
		t.Fatalf("audit log:\n%s", log)
	}
	// An agent connected the old way keeps only what it had: no synced chat.
	f.sync(t, familyChat, true)
	if code, _ := f.do(t, "GET", messages, f.jit, nil); code != 403 {
		t.Fatalf("an on-demand agent gained a synced chat: %d", code)
	}
}

func TestAnAgentAsksToSendOnceAndThePersonApprovesThatMessage(t *testing.T) {
	stub := &promptStub{confirm: true}
	f := setupAccessWith(t, humancheck.NewGate(stub))
	codex := f.connect(t, "codex")
	f.sync(t, familyChat, true)

	// A synced chat needs no request.
	if got := f.askToSend(t, codex, map[string]string{"chat": familyChat, "text": "hi"}, 200); got.Data.Status != "already_allowed" {
		t.Fatalf("synced chat: %+v", got)
	}
	// A chat that is not synced, and a number the archive has never seen.
	asked := f.askToSend(t, codex, map[string]string{"chat": otherChat, "text": "Can we meet?"}, 202)
	if asked.Data.Status != "pending" || asked.Data.Chat != otherChat || asked.Data.NewNumber || asked.Data.ExpiresAt == nil || asked.Data.ApproveWith != "wasync actions approve "+asked.Data.RequestID {
		t.Fatalf("pending: %+v", asked)
	}
	fresh := f.askToSend(t, codex, map[string]string{"to": "+1 555 000 1111", "text": "Hello"}, 202)
	if !fresh.Data.NewNumber || fresh.Data.Chat != "15550001111@s.whatsapp.net" {
		t.Fatalf("new number: %+v", fresh)
	}
	if again := f.askToSend(t, codex, map[string]string{"chat": otherChat, "text": "Can we meet?"}, 202); again.Data.RequestID != asked.Data.RequestID {
		t.Fatal("asking again prompted the person twice")
	}
	// Only the agent that asked can see it.
	other := f.connect(t, "other-app")
	if code, _ := f.do(t, "GET", "/api/v1/send-requests/"+asked.Data.RequestID, other, nil); code != 404 {
		t.Fatalf("another agent read the request: %d", code)
	}
	if len(stub.seen()) != 0 || len(*f.sent) != 0 {
		t.Fatal("asking sent something or prompted")
	}

	// The person approves one, from the desktop app's route.
	code, out := f.signed(t, "POST", agentRequestsPath+"/"+asked.Data.RequestID+"/approve", f.approver, nil)
	if code != 200 || len(*f.sent) != 1 || (*f.sent)[0].Text != "Can we meet?" || (*f.sent)[0].To != otherChat || (*f.sent)[0].SentBy != "codex" {
		t.Fatalf("approve: %d %s %+v", code, out, *f.sent)
	}
	if got := stub.seen(); len(got) != 1 || !strings.Contains(got[0], `agent "codex" send this message to "+1∙∙∙∙21", a chat that was not synced when it asked: "Can we meet?"`) {
		t.Fatalf("prompt %q", got)
	}
	code, out = f.do(t, "GET", "/api/v1/send-requests/"+asked.Data.RequestID, codex, nil)
	var status sendRequestData
	_ = json.Unmarshal(out, &status)
	if code != 200 || status.Data.Status != "sent" || status.Data.MessageID == "" || status.Data.ExpiresAt != nil {
		t.Fatalf("status: %d %s", code, out)
	}
	// Approving it twice sends nothing more.
	if code, _ = f.signed(t, "POST", agentRequestsPath+"/"+asked.Data.RequestID+"/approve", f.approver, nil); code != 404 || len(*f.sent) != 1 {
		t.Fatalf("second approval: %d", code)
	}
}

func TestSendRequestsAreRefusedWithoutTheSendScopeOrWithBadInput(t *testing.T) {
	f := setupAccess(t)
	codex := f.connect(t, "codex")
	if got := f.askToSend(t, f.other, map[string]string{"chat": otherChat, "text": "hi"}, 403); got.Code != query.CodeForbidden {
		t.Fatalf("no send scope: %+v", got)
	}
	for name, body := range map[string]any{
		"both":      map[string]string{"chat": otherChat, "to": "+15550001111", "text": "hi"},
		"neither":   map[string]string{"text": "hi"},
		"no text":   map[string]string{"chat": otherChat, "text": "  "},
		"long text": map[string]string{"chat": otherChat, "text": strings.Repeat("x", approval.MaxSendText+1)},
		"bad chat":  map[string]string{"chat": "not a chat", "text": "hi"},
		"bad phone": map[string]string{"to": "call me", "text": "hi"},
		"a video":   map[string]any{"chat": otherChat, "text": "hi", "video": "AAAA"},
	} {
		if got := f.askToSend(t, codex, body, 400); got.Code != query.CodeInvalidArgument {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	for i := 0; i < approval.MaxPendingSendsPerAgent; i++ {
		f.askToSend(t, codex, map[string]string{"chat": otherChat, "text": strings.Repeat("y", i+1)}, 202)
	}
	if got := f.askToSend(t, codex, map[string]string{"chat": otherChat, "text": "one too many"}, 429); got.Code != query.CodeRateLimited {
		t.Fatalf("cap: %+v", got)
	}
	// A refused send points at a send request, never at the removed access requests.
	code, out := f.do(t, "POST", "/api/v1/messages", codex, map[string]string{"chat": otherChat, "text": "hi"})
	if code != 403 || !strings.Contains(string(out), "send-request --chat "+otherChat) || strings.Contains(string(out), "access request") {
		t.Fatalf("refused send: %d %s", code, out)
	}
}

func TestTheOwnerListsDeclinesAndApprovesAgentRequestsApartFromDeletes(t *testing.T) {
	f := setupAccessWith(t, humancheck.NewGate(&promptStub{confirm: true}))
	sendToken, err := loadNamedToken(f.dir, "send.token")
	if err != nil {
		t.Fatal(err)
	}
	purge, err := f.queue.Add(f.account, approval.Input{Kind: "purge-chat", Chat: familyChat})
	if err != nil {
		t.Fatal(err)
	}
	first := f.askSend(t, otherChat, "first")
	second := f.askSend(t, phoneChat, "second")
	third := f.askSend(t, familyChat, "third")

	code, out := f.signed(t, "GET", agentRequestsPath, sendToken, nil)
	var list struct {
		Items []agentRequestView `json:"items"`
	}
	if code != 200 || json.Unmarshal(out, &list) != nil || len(list.Items) != 3 || list.Items[0].ID != first.ID || list.Items[2].ID != third.ID {
		t.Fatalf("list: %d %s", code, out)
	}
	if item := list.Items[2]; item.Agent != "jit" || item.ChatName != "Family Group" || item.Text != "third" || item.ExpiresAt.Sub(item.AskedAt) != approval.RequestTTL || !strings.Contains(item.Effect, "send this message") {
		t.Fatalf("item %+v", item)
	}
	// An agent credential or an unsigned request cannot reach these routes.
	if code, _ = f.do(t, "GET", agentRequestsPath, f.jit, nil); code != 401 {
		t.Fatalf("agent credential: %d", code)
	}
	// Approving needs the approve credential, not the send one.
	if code, _ = f.signed(t, "POST", agentRequestsPath+"/"+first.ID+"/approve", sendToken, nil); code != 401 || len(*f.sent) != 0 {
		t.Fatalf("approve with the send credential: %d", code)
	}
	// The owner's own delete is not an agent request.
	if code, _ = f.signed(t, "POST", agentRequestsPath+"/"+purge.ID+"/decline", sendToken, nil); code != 404 {
		t.Fatalf("declined a delete here: %d", code)
	}
	if code, _ = f.signed(t, "POST", agentRequestsPath+"/"+first.ID+"/decline", sendToken, nil); code != 200 || f.queue.Get(f.account, first.ID).Status != approval.StatusRejected {
		t.Fatalf("decline one: %d", code)
	}
	code, out = f.signed(t, "POST", agentRequestsPath+"/decline-all", sendToken, nil)
	if code != 200 || !strings.Contains(string(out), `"declined":2`) || f.queue.Get(f.account, second.ID).Status != approval.StatusRejected {
		t.Fatalf("decline all: %d %s", code, out)
	}
	if f.queue.Get(f.account, purge.ID).Status != approval.StatusPending || len(*f.sent) != 0 {
		t.Fatal("declining agent requests touched the owner's delete or sent something")
	}
	log, _ := os.ReadFile(filepath.Join(f.dir, agentauth.AuditFileName))
	if strings.Count(string(log), `"event":"send_declined"`) != 3 {
		t.Fatalf("audit:\n%s", log)
	}
}

func TestTheApprovalsPageShowsAgentRequestsApartFromDeletesAndDeclinesThemAll(t *testing.T) {
	f := setupAccessWith(t, humancheck.NewGate(&promptStub{confirm: true}))
	purge, _ := f.queue.Add(f.account, approval.Input{Kind: "purge-chat", Chat: familyChat})
	f.askSend(t, otherChat, "first message")
	id, _, _ := agentauth.ParseCredential(f.jit)
	if _, _, err := f.queue.AddSend(f.account, "15550001111@s.whatsapp.net", approval.AgentSend{Agent: "jit", AgentID: id, Text: "hello stranger", NewNumber: true}); err != nil {
		t.Fatal(err)
	}
	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	_, page, _ := c.get("/ui/approvals")
	agents, deletes := strings.Index(page, "Agents asking to send"), strings.Index(page, "Your delete confirmations")
	if agents < 0 || deletes < agents || !strings.Contains(page[agents:deletes], "first message") || !strings.Contains(page[agents:deletes], "the new number &#43;15550001111") || !strings.Contains(page[deletes:], "permanently erase") || !strings.Contains(page, "Not now to all") {
		t.Fatalf("page:\n%s", page)
	}
	if strings.Contains(page, "How agents get chats") {
		t.Fatal("the removed access mode is still offered")
	}
	code, _, hd := c.post("/ui/decide", url.Values{"csrf": {csrfOf(t, page)}, "action": {"decline-all"}})
	if code != 303 || !strings.Contains(hd.Get("Location"), "n=rejected") || len(f.queue.AgentRequests(f.account)) != 0 || f.queue.Get(f.account, purge.ID).Status != approval.StatusPending {
		t.Fatalf("decline all: %d %v", code, hd)
	}
}

func TestAccessRequestsAreGoneAndSaySoAndTheModeIsGone(t *testing.T) {
	f := setupAccess(t)
	for _, call := range [][2]string{{"POST", "/api/v1/access-requests"}, {"GET", "/api/v1/access-requests/0123456789abcdef0123456789abcdef"}} {
		code, out := f.do(t, call[0], call[1], f.jit, map[string]string{"match": "family"})
		if code != 410 || !strings.Contains(string(out), "send-requests") {
			t.Fatalf("%s %s: %d %s", call[0], call[1], code, out)
		}
	}
	_, out := f.do(t, "GET", "/api/v1/version", f.jit, nil)
	if !strings.Contains(string(out), `"write.send.request"`) || strings.Contains(string(out), `"access.request"`) {
		t.Fatalf("features: %s", out)
	}
	apiToken, _ := loadNamedToken(f.dir, "api.token")
	if code, _ := f.do(t, "GET", "/api/access/mode", apiToken, nil); code != 404 {
		t.Fatalf("access mode route: %d", code)
	}
}

// A chat wasync switched on only so one agent's approved list sees replies is
// not handed to the other connected agents. They get it once the person syncs
// it in Choose chats, where it is shown as not chosen until then.
func TestAChatSyncedOnlyForAnAgentsListIsNotSharedUntilThePersonSyncsIt(t *testing.T) {
	f, c, csrf := chatsFixture(t)
	codex := f.connect(t, "codex")
	// Another agent's approved list messaged this number, so its capture is on.
	if err := f.store.EnableListCapture(context.Background(), f.account, phoneChat); err != nil {
		t.Fatal(err)
	}
	if g := f.group(t, phoneChat); !g.Enabled || !g.ListCapture {
		t.Fatalf("list capture: %+v", g)
	}
	messages := "/api/v1/chats/" + phoneChat + "/messages"
	if code, out := f.do(t, "GET", messages, codex, nil); code != 403 {
		t.Fatalf("a connected agent read a chat synced for another agent's list: %d %s", code, out)
	}
	if code, out := f.do(t, "POST", "/api/v1/messages", codex, map[string]string{"chat": phoneChat, "text": "hi"}); code != 403 {
		t.Fatalf("a connected agent wrote in a chat synced for another agent's list: %d %s", code, out)
	}
	if got := mustClient(t, f.dir, "codex"); strings.Join(got.Chats, ",") != familyChat {
		t.Fatalf("stored chats %v", got.Chats)
	}
	asked := f.askToSend(t, codex, map[string]string{"chat": phoneChat, "text": "hi"}, 202)
	if asked.Data.Status != "pending" {
		t.Fatalf("pending: %+v", asked)
	}
	sendToken, err := loadNamedToken(f.dir, "send.token")
	if err != nil {
		t.Fatal(err)
	}
	_, out := f.signed(t, "GET", agentRequestsPath, sendToken, nil)
	if !strings.Contains(string(out), "a chat that was not synced when it asked") || strings.Contains(string(out), "a synced chat this agent may not use") {
		t.Fatalf("a chat synced only for a list was described as synced: %s", out)
	}
	if _, page, _ := c.get("/ui/chats/pick"); !strings.Contains(page, "1 of 3 syncing") {
		t.Fatalf("Choose chats counted a chat the person did not choose:\n%s", page)
	}
	// The person ticks it in Choose chats; from then on it is shared.
	if code, _, _ := c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "sync": {phoneChat}}); code != 303 {
		t.Fatalf("save: %d", code)
	}
	if g := f.group(t, phoneChat); !g.Enabled || g.ListCapture {
		t.Fatalf("after the person synced it: %+v", g)
	}
	if code, out := f.do(t, "GET", messages, codex, nil); code != 200 {
		t.Fatalf("a chat the person synced is not readable: %d %s", code, out)
	}
}

// An agent whose own fixed list leaves out a synced chat asks to send there;
// the person is told the chat is synced, not that it was not.
func TestASendRequestForASyncedChatTheAgentMayNotUseSaysSo(t *testing.T) {
	stub := &promptStub{confirm: true}
	f := setupAccessWith(t, humancheck.NewGate(stub))
	f.sync(t, familyChat, true)
	asked := f.askToSend(t, f.jit, map[string]string{"chat": familyChat, "text": "See you at 6"}, 202)
	if asked.Data.Status != "pending" || asked.Data.NewNumber {
		t.Fatalf("pending: %+v", asked)
	}
	if code, _ := f.signed(t, "POST", agentRequestsPath+"/"+asked.Data.RequestID+"/approve", f.approver, nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if got := stub.seen(); len(got) != 1 || !strings.Contains(got[0], `agent "jit" send this message to "Family Group", a synced chat this agent may not use: "See you at 6"`) {
		t.Fatalf("prompt %q", got)
	}
}

// An archive from a release before list_capture: an approved list's send
// switched a number's chat on like any other. After the upgrade a newly
// connected agent must not get it; the person ticking it shares it, and the
// one-time marking does not undo that.
func TestAChatAnEarlierReleaseSyncedForAListIsNotSharedAfterTheUpgrade(t *testing.T) {
	f, c, csrf := chatsFixture(t)
	ctx := context.Background()
	if err := f.store.SetEnabled(ctx, f.account, []string{phoneChat}, true); err != nil {
		t.Fatal(err)
	}
	poster, err := agentauth.ActiveByName(f.dir, "jit")
	if err != nil || poster == nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = sendlist.Put(f.dir, sendlist.List{AgentID: poster.ID, Agent: poster.Name, GrantedAt: now, Numbers: []sendlist.Number{{Chat: phoneChat, FirstSentAt: &now, LastSentAt: &now}}}); err != nil {
		t.Fatal(err)
	}
	// The fixture's archive is new; record what migration 009 records when it
	// upgrades an archive that already synced chats.
	raw, err := sql.Open("sqlite3", storage.DSN(filepath.Join(f.dir, "archive.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("INSERT INTO list_capture_backfill(id) VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	codex := f.connect(t, "codex")
	messages := "/api/v1/chats/" + phoneChat + "/messages"
	if code, out := f.do(t, "GET", messages, codex, nil); code != 403 {
		t.Fatalf("a newly connected agent read a chat an earlier release synced for a list: %d %s", code, out)
	}
	if g := f.group(t, phoneChat); !g.Enabled || !g.ListCapture {
		t.Fatalf("the earlier list chat was not marked: %+v", g)
	}
	if code, _ := f.do(t, "GET", "/api/v1/chats/"+familyChat+"/messages", codex, nil); code != 200 {
		t.Fatalf("a chat the person synced, not on any list, was taken away: %d", code)
	}
	if code, _, _ := c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "sync": {phoneChat}}); code != 303 {
		t.Fatalf("save: %d", code)
	}
	if code, out := f.do(t, "GET", messages, codex, nil); code != 200 {
		t.Fatalf("the person synced it, yet the agent cannot read it: %d %s", code, out)
	}
}

// On a new archive nothing is backfilled: a chat the person synced on this
// release stays shared when its number later goes on an approved list.
func TestANewArchiveKeepsAPersonSyncedChatSharedWhenItGoesOnAList(t *testing.T) {
	f, c, csrf := chatsFixture(t)
	if code, _, _ := c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "sync": {phoneChat}}); code != 303 {
		t.Fatalf("save: %d", code)
	}
	poster, err := agentauth.ActiveByName(f.dir, "jit")
	if err != nil || poster == nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = sendlist.Put(f.dir, sendlist.List{AgentID: poster.ID, Agent: poster.Name, GrantedAt: now, Numbers: []sendlist.Number{{Chat: phoneChat, FirstSentAt: &now, LastSentAt: &now}}}); err != nil {
		t.Fatal(err)
	}
	codex := f.connect(t, "codex")
	if code, out := f.do(t, "GET", "/api/v1/chats/"+phoneChat+"/messages", codex, nil); code != 200 {
		t.Fatalf("a chat the person synced was withdrawn from a connected agent: %d %s", code, out)
	}
	if g := f.group(t, phoneChat); !g.PersonSynced() {
		t.Fatalf("the person's chat was marked as list capture: %+v", g)
	}
}

// While open sending is on, an agent may ask the person to send once
// open sending's hourly limits are used up, as the send refusal tells it to.
func TestAnAgentMayAskToSendOnceOpenSendingsHourlyLimitsAreUsedUp(t *testing.T) {
	f := setupAccessWith(t, humancheck.NewGate(&promptStub{}))
	codex := f.connect(t, "codex")
	if _, err := opensend.Start(f.dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	log := sentlog.New(f.dir)
	openSend := func(chat string, newNumber bool) {
		t.Helper()
		e := sentlog.Entry{Time: time.Now(), Actor: "codex", Kind: "send", Chat: chat, Outcome: sentlog.OutcomeSent, Open: true, NewNumber: newNumber}
		if err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	freshNumber := map[string]string{"to": "+1 555 000 2222", "text": "Hello"}
	if got := f.askToSend(t, codex, freshNumber, 200); got.Data.Status != "already_allowed" {
		t.Fatalf("with room left: %+v", got)
	}

	for i := 0; i < opensend.MaxNewNumbers; i++ {
		openSend(fmt.Sprintf("1555000%04d@s.whatsapp.net", i), true)
	}
	if got := f.askToSend(t, codex, freshNumber, 202); got.Data.Status != "pending" || !got.Data.NewNumber {
		t.Fatalf("new numbers used up, a new number: %+v", got)
	}
	if got := f.askToSend(t, codex, map[string]string{"chat": otherChat, "text": "Hi"}, 200); got.Data.Status != "already_allowed" {
		t.Fatalf("new numbers used up, a chat the archive has: %+v", got)
	}

	for i := opensend.MaxNewNumbers; i < opensend.MaxMessages; i++ {
		openSend(otherChat, false)
	}
	if got := f.askToSend(t, codex, map[string]string{"chat": otherChat, "text": "Hi"}, 202); got.Data.Status != "pending" || got.Data.NewNumber {
		t.Fatalf("messages used up, a chat the archive has: %+v", got)
	}
	if n := len(f.queue.PendingList(f.account)); n != 2 {
		t.Fatalf("requests waiting for the person: %d, want 2", n)
	}
}

// The approvals page and the desktop app's request list show an agent's
// message without invisible direction overrides, so the person reads it in
// the order it will be sent; the message sent is still the agent's own.
func TestAgentRequestsShowTheMessageWithoutDirectionOverrides(t *testing.T) {
	f := setupAccessWith(t, humancheck.NewGate(&promptStub{confirm: true}))
	sent := "Pay ‮redro on\nthen ⁦call⁩ me"
	shown := "Pay redro on\nthen call me"
	asked := f.askSend(t, otherChat, sent)

	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	_, page, _ := c.get("/ui/approvals")
	if strings.ContainsAny(page, "‮⁦⁩") || !strings.Contains(page, shown) {
		t.Fatalf("page:\n%s", page)
	}
	sendToken, err := loadNamedToken(f.dir, "send.token")
	if err != nil {
		t.Fatal(err)
	}
	code, out := f.signed(t, "GET", agentRequestsPath, sendToken, nil)
	var list struct {
		Items []agentRequestView `json:"items"`
	}
	if code != 200 || json.Unmarshal(out, &list) != nil || len(list.Items) != 1 || list.Items[0].Text != shown {
		t.Fatalf("list: %d %s", code, out)
	}
	if code, out = f.signed(t, "POST", agentRequestsPath+"/"+asked.ID+"/approve", f.approver, nil); code != 200 || len(*f.sent) != 1 || (*f.sent)[0].Text != sent {
		t.Fatalf("approve: %d %s %+v", code, out, *f.sent)
	}
}
