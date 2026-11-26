package api

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/sendlist"
	"github.com/gehariharan/wasync/internal/storage"
)

// grantOnDemand registers an agent that starts with no chats at all, like
// one set up on the Agents page.
func (f fixture) grantOnDemand(t *testing.T, name string, scopes ...string) string {
	t.Helper()
	credential, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: name, Scopes: scopes, OnDemand: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

// archive stores a message from the person in chat, archiving the chat first
// when it is not yet. alt is the phone-number address of a hidden-address
// sender, or "".
func (f fixture) archive(t *testing.T, chat, id, text, alt string) {
	t.Helper()
	ctx := context.Background()
	if on, err := f.store.IsEnabled(ctx, f.account, chat); err != nil {
		t.Fatal(err)
	} else if !on {
		if err = f.store.SaveDiscoveredChat(ctx, f.account, storage.Group{ID: chat, Name: chat}); err != nil {
			t.Fatal(err)
		}
		if err = f.store.EnableCapture(ctx, f.account, chat, false); err != nil {
			t.Fatal(err)
		}
	}
	m := storage.Message{GroupID: chat, MessageID: id, SenderID: chat, SenderAlt: alt, Timestamp: time.Now().Add(time.Second), Type: "text", Text: text}
	if res, err := f.store.Apply(ctx, f.account, []storage.Message{m}); err != nil || res.Stored != 1 {
		t.Fatalf("archive %s: %+v %v", chat, res, err)
	}
}

// readChat reads a chat's messages as the outreach reply check does and
// returns the texts, or fails unless the status is want.
func (f fixture) readChat(t *testing.T, token, chat string, want int) []string {
	t.Helper()
	status, body := f.send(t, "GET", "/api/v1/chats/"+url.PathEscape(chat)+"/messages?from="+url.QueryEscape(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)), token, "")
	if status != want {
		t.Fatalf("read %s: want %d, got %d %s", chat, want, status, body)
	}
	if want != 200 {
		wantV1Error(t, status, body, want, query.CodeForbidden)
		return nil
	}
	var got struct {
		Data struct {
			Items []struct {
				Ref  string `json:"ref"`
				Text string `json:"text_content"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, m := range got.Data.Items {
		texts = append(texts, m.Text)
	}
	return texts
}

const (
	unmessaged = "15551230001@s.whatsapp.net"
	hiddenChat = "999000111@lid"
	othersOwn  = "15559990000@s.whatsapp.net"
)

// The outreach agent: it may send to its list and read, but holds no chat.
func listReaderSetup(t *testing.T) (fixture, string, string) {
	t.Helper()
	f := setupSend(t, &sendRecorder{})
	agent := f.grantOnDemand(t, "outreach", agentauth.ScopeReadMessages, agentauth.ScopeSend)
	other := f.grantOnDemand(t, "other", agentauth.ScopeReadMessages, agentauth.ScopeSend)
	f.approveList(t, "outreach", nil, "15551230000", "15551230001")
	f.approveList(t, "other", nil, "15559990000")
	if status, body := f.send(t, "POST", "/api/v1/messages", agent, `{"to":"15551230000","text":"hello"}`); status != 200 {
		t.Fatalf("list send: %d %s", status, body)
	}
	if status, body := f.send(t, "POST", "/api/v1/messages", other, `{"to":"15559990000","text":"hello"}`); status != 200 {
		t.Fatalf("other list send: %d %s", status, body)
	}
	f.archive(t, listed, "R1", "yes please", "")
	f.archive(t, hiddenChat, "R2", "tell me more", "15551230000:4@s.whatsapp.net")
	f.archive(t, unmessaged, "U1", "an older chat", "")
	f.archive(t, othersOwn, "O1", "a reply to the other agent", "")
	f.archive(t, "999000222@lid", "H2", "someone else", "15557770000@s.whatsapp.net")
	if _, err := f.store.Apply(context.Background(), f.account, []storage.Message{{GroupID: "one@g.us", MessageID: "G1", SenderID: "x@s.whatsapp.net", Timestamp: time.Now(), Type: "text", Text: "group talk"}}); err != nil {
		t.Fatal(err)
	}
	return f, agent, other
}

func TestAListAgentReadsTheChatsOfTheNumbersItMessagedAndNothingElse(t *testing.T) {
	f, agent, _ := listReaderSetup(t)

	if got := f.readChat(t, agent, listed, 200); len(got) != 1 || got[0] != "yes please" {
		t.Fatalf("the messaged number's chat: %q", got)
	}
	if got := f.readChat(t, agent, hiddenChat, 200); len(got) != 1 || got[0] != "tell me more" {
		t.Fatalf("the hidden-address chat of the messaged number: %q", got)
	}
	// Not a listed number it never messaged, another agent's number, another
	// person's hidden-address chat or any other chat.
	for _, chat := range []string{unmessaged, othersOwn, "999000222@lid", "one@g.us"} {
		f.readChat(t, agent, chat, 403)
	}
	// Single messages and threads go by the same rule.
	status, body := f.send(t, "GET", "/api/v1/messages/search?q=please&chat="+url.QueryEscape(listed), agent, "")
	if status != 200 || !strings.Contains(string(body), "yes please") {
		t.Fatalf("search the messaged number's chat: %d %s", status, body)
	}
	status, body = f.send(t, "GET", "/api/v1/messages/search?q=talk", agent, "")
	if status != 200 || strings.Contains(string(body), "group talk") || strings.Contains(string(body), "older chat") {
		t.Fatalf("a search over every chat shows only list chats: %d %s", status, body)
	}
	// A list adds no scope: the agent cannot list chats without read.chats.
	status, body = f.send(t, "GET", "/api/v1/chats", agent, "")
	wantV1Error(t, status, body, 403, query.CodeForbidden)

	audit := f.auditLog(t)
	if !strings.Contains(audit, `"event":"read_list"`) || !strings.Contains(audit, hiddenChat) {
		t.Fatalf("list reads are not audited: %s", audit)
	}
	for _, line := range strings.Split(audit, "\n") {
		if strings.Contains(line, unmessaged) && !strings.Contains(line, `"outcome":"denied"`) {
			t.Fatalf("a read of an unmessaged number was allowed: %s", line)
		}
	}
}

func TestAMessageRefInAListChatReadsOnlyWhileTheListLasts(t *testing.T) {
	f, agent, other := listReaderSetup(t)
	status, body := f.send(t, "GET", "/api/v1/chats/"+url.PathEscape(listed)+"/messages", agent, "")
	var page struct {
		Data struct {
			Items []struct {
				Ref string `json:"ref"`
			} `json:"items"`
		} `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &page) != nil || len(page.Data.Items) == 0 {
		t.Fatalf("read: %d %s", status, body)
	}
	ref := page.Data.Items[0].Ref
	if status, body = f.send(t, "GET", "/api/v1/messages/"+ref, agent, ""); status != 200 {
		t.Fatalf("the message by ref: %d %s", status, body)
	}
	if status, body = f.send(t, "GET", "/api/v1/messages/"+ref+"/thread", agent, ""); status != 200 {
		t.Fatalf("the thread by ref: %d %s", status, body)
	}
	// The other agent's list does not reach it.
	status, body = f.send(t, "GET", "/api/v1/messages/"+ref, other, "")
	wantV1Error(t, status, body, 403, query.CodeForbidden)

	c, _ := agentauth.ActiveByName(f.dir, "outreach")
	if err := sendlist.Remove(f.dir, c.ID); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []string{listed, hiddenChat} {
		f.readChat(t, agent, chat, 403)
	}
	status, body = f.send(t, "GET", "/api/v1/messages/"+ref, agent, "")
	wantV1Error(t, status, body, 403, query.CodeForbidden)
	// The other agent keeps its own.
	f.readChat(t, other, othersOwn, 200)
}

func TestAListChatStaysReadableWhilePausedAndAfterStopButNotAfterItExpires(t *testing.T) {
	f, agent, _ := listReaderSetup(t)
	c, _ := agentauth.ActiveByName(f.dir, "outreach")
	if err := sendlist.Pause(f.dir, c.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.readChat(t, agent, listed, 200)
	if err := sendlist.Pause(f.dir, c.ID, false, time.Now()); err != nil {
		t.Fatal(err)
	}

	// The STOP drops the number from the list, but its chat stays readable so
	// the agent sees why.
	f.archive(t, hiddenChat, "R3", "STOP", "15551230000@s.whatsapp.net")
	status, body := f.send(t, "GET", "/api/v1/send-list", agent, "")
	if status != 200 || !strings.Contains(string(body), `"stopped":1`) {
		t.Fatalf("the STOP did not drop the number: %d %s", status, body)
	}
	if got := f.readChat(t, agent, hiddenChat, 200); len(got) != 2 || !slices.Contains(got, "STOP") {
		t.Fatalf("the STOP is not readable: %q", got)
	}
	f.readChat(t, agent, listed, 200)
	// Approving the list again keeps it readable; leaving the number out does not.
	f.approveList(t, "outreach", nil, "15551230000", "15551230001")
	f.readChat(t, agent, hiddenChat, 200)
	f.approveList(t, "outreach", nil, "15551230001")
	f.readChat(t, agent, listed, 403)

	past := time.Now().Add(-time.Minute)
	f.approveList(t, "outreach", &past, "15551230000", "15551230001")
	f.readChat(t, agent, listed, 403)
	f.readChat(t, agent, hiddenChat, 403)
}

func TestAListAgentWithReadChatsSeesOnlyItsListChatsInTheChatList(t *testing.T) {
	f, _, _ := listReaderSetup(t)
	lister := f.grantOnDemand(t, "lister", agentauth.ScopeReadChats, agentauth.ScopeReadMessages, agentauth.ScopeSend)
	f.approveList(t, "lister", nil, "15551230000", "15551230001", "15559990000")
	status, body := f.send(t, "GET", "/api/v1/chats", lister, "")
	var got struct {
		Data struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &got) != nil {
		t.Fatalf("chat list: %d %s", status, body)
	}
	var ids []string
	for _, c := range got.Data.Items {
		ids = append(ids, c.ID)
	}
	// Both numbers a list messaged, and the hidden-address chat of one; not
	// the number no list messaged yet, nor anything else.
	want := []string{listed, othersOwn, hiddenChat}
	slices.Sort(ids)
	if !slices.Equal(ids, want) {
		t.Fatalf("chats listed: %v, want %v", ids, want)
	}
	if status, body = f.send(t, "GET", "/api/v1/chats/"+url.PathEscape(hiddenChat), lister, ""); status != 200 {
		t.Fatalf("one list chat: %d %s", status, body)
	}
	status, body = f.send(t, "GET", "/api/v1/chats/"+url.PathEscape(unmessaged), lister, "")
	wantV1Error(t, status, body, 403, query.CodeForbidden)
}

// An agent without a list, or one that reads every chat, is unchanged.
func TestAgentsWithoutAListReadAsBefore(t *testing.T) {
	f, _, _ := listReaderSetup(t)
	plain := f.grantOnDemand(t, "plain", agentauth.ScopeReadMessages)
	for _, chat := range []string{listed, hiddenChat, othersOwn} {
		f.readChat(t, plain, chat, 403)
	}
	if got := f.readChat(t, f.agent, unmessaged, 200); len(got) != 1 {
		t.Fatalf("an agent that reads every chat: %q", got)
	}
}
