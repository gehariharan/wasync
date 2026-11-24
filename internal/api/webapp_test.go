package api

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/storage"
)

// chatsFixture is the access fixture with the family chat syncing and a few
// messages in it, behind a signed-in browser.
func chatsFixture(t *testing.T) (accessFixture, *pageClient, string) {
	t.Helper()
	f := setupAccess(t)
	ctx := context.Background()
	if err := f.store.SetEnabled(ctx, f.account, []string{familyChat}, true); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 12, 5, 0, 0, time.UTC)
	if _, err := f.store.Apply(ctx, f.account, []storage.Message{
		{GroupID: familyChat, MessageID: "m2", SenderID: "alice@lid", Timestamp: at, Type: "text", Text: "<script>alert(1)</script> hello", Source: "live"},
		{GroupID: familyChat, MessageID: "m3", SenderID: "me@lid", Timestamp: at.Add(time.Minute), Type: "text", Text: "my own reply", FromMe: true, Source: "live"},
		{GroupID: familyChat, MessageID: "m4", SenderID: "15551230000@s.whatsapp.net", Timestamp: at.Add(2 * time.Minute), Type: "image", Source: "live"},
	}); err != nil {
		t.Fatal(err)
	}
	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	_, page, _ := c.get("/ui/chats/pick")
	return f, c, csrfOf(t, page)
}

func TestChatsPageNeedsASession(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	for _, path := range []string{"/ui/chats", "/ui/chats/pick"} {
		if code, page, _ := c.get(path); code != 401 || strings.Contains(page, "synthetic family text") {
			t.Fatalf("%s without a session: %d", path, code)
		}
	}
	if code, _, _ := c.post("/ui/chats/pick", url.Values{"action": {"clearall"}}); code != 401 {
		t.Fatalf("saving without a session: %d", code)
	}
}

func TestChatsPageShowsSyncedChatsAndTheirMessagesSafely(t *testing.T) {
	_, c, _ := chatsFixture(t)
	code, page, hd := c.get("/ui/chats")
	if code != 200 {
		t.Fatalf("chats: %d", code)
	}
	for _, want := range []string{"Family Group", "my own reply", "[image]", "&#43;15551230000", "Member", "1 of 3 syncing"} {
		if !strings.Contains(page, want) {
			t.Fatalf("chats page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "<script>alert(1)") || !strings.Contains(page, "&lt;script&gt;alert(1)") {
		t.Fatalf("message text was not escaped:\n%s", page)
	}
	if strings.Contains(page, "c=15551234567%40") {
		t.Fatalf("a chat that is not syncing was listed by default:\n%s", page)
	}
	if csp := hd.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Fatalf("the page policy changed: %q", csp)
	}
	_, all, _ := c.get("/ui/chats?all=1&c=" + url.QueryEscape(phoneChat))
	if !strings.Contains(all, "c=15551234567%40") || !strings.Contains(all, "not syncing") || !strings.Contains(all, "No messages kept for this chat yet.") {
		t.Fatalf("show-all view:\n%s", all)
	}
}

func TestChatsPageMarksChatsAnAgentCanRead(t *testing.T) {
	f, c, _ := chatsFixture(t)
	// The on-demand test agents have no chats yet, so no chat carries the icon.
	if _, page, _ := c.get("/ui/chats"); strings.Contains(page, `class="bot"`) {
		t.Fatalf("an agent with no chats was shown as reading one:\n%s", page)
	}
	if _, _, err := agentauth.Grant(f.dir, agentauth.GrantRequest{Name: "reader", Scopes: []string{agentauth.ScopeReadChats, agentauth.ScopeReadMessages}, Chats: []string{familyChat}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, page, _ := c.get("/ui/chats"); !strings.Contains(page, `class="bot"`) {
		t.Fatalf("a chat an agent may read had no icon:\n%s", page)
	}
	// A revoked agent no longer counts.
	if _, err := agentauth.Revoke(f.dir, "reader", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, page, _ := c.get("/ui/chats"); strings.Contains(page, `class="bot"`) {
		t.Fatalf("a revoked agent still showed on a chat:\n%s", page)
	}
}

func TestChatPickerSearchesFiltersAndOnlyChangesWhatItShows(t *testing.T) {
	f, c, csrf := chatsFixture(t)
	_, page, _ := c.get("/ui/chats/pick")
	for _, want := range []string{"All 3", "Groups 1", "People 0", "Numbers 2", "1 of 3 syncing", `name="history" value="yes" checked`} {
		if !strings.Contains(page, want) {
			t.Fatalf("picker lacks %q:\n%s", want, page)
		}
	}
	_, page, _ = c.get("/ui/chats/pick?tab=groups")
	if !strings.Contains(page, "Family Group") || strings.Contains(page, phoneChat) {
		t.Fatalf("groups tab:\n%s", page)
	}
	_, page, _ = c.get("/ui/chats/pick?q=family")
	if !strings.Contains(page, "Family Group") || strings.Contains(page, `value="`+otherChat+`"`) {
		t.Fatalf("search:\n%s", page)
	}

	if code, _, _ := c.post("/ui/chats/pick", url.Values{"csrf": {"wrong"}, "action": {"clearall"}}); code != 403 {
		t.Fatalf("forged form: %d", code)
	}
	// Saving from a search for the family chat unticks only it and leaves the others alone.
	if err := f.store.SetEnabled(context.Background(), f.account, []string{phoneChat}, true); err != nil {
		t.Fatal(err)
	}
	code, _, hd := c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"save"}, "q": {"family"}, "shown": {familyChat}})
	if code != 303 || !strings.Contains(hd.Get("Location"), "n=saved") || !strings.Contains(hd.Get("Location"), "q=family") {
		t.Fatalf("save: %d %v", code, hd)
	}
	if f.group(t, familyChat).Enabled || !f.group(t, phoneChat).Enabled {
		t.Fatalf("a chat that was not shown changed: family=%v phone=%v", f.group(t, familyChat).Enabled, f.group(t, phoneChat).Enabled)
	}
	// Tick all shown turns on exactly the listed chats; a made-up id is ignored.
	code, _, _ = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"tickshown"}, "shown": {familyChat, "made-up@g.us"}})
	if code != 303 || !f.group(t, familyChat).Enabled || f.group(t, otherChat).Enabled {
		t.Fatalf("tick shown: %d", code)
	}
	code, _, _ = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"clearshown"}, "shown": {familyChat}})
	if code != 303 || f.group(t, familyChat).Enabled || !f.group(t, phoneChat).Enabled {
		t.Fatalf("clear shown: %d", code)
	}
	if code, _, _ = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"nonsense"}}); code != 400 {
		t.Fatalf("unknown action: %d", code)
	}
	if code, _, _ = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"clearall"}}); code != 303 || f.group(t, phoneChat).Enabled {
		t.Fatalf("stop syncing everything: %d", code)
	}
}

func TestChatPickerAsksForOlderMessagesOnlyWhenTheBoxIsTicked(t *testing.T) {
	f, c, csrf := chatsFixture(t)
	for _, tc := range []struct {
		chat, history string
		queued        bool
	}{{phoneChat, "yes", true}, {otherChat, "", false}} {
		form := url.Values{"csrf": {csrf}, "action": {"save"}, "shown": {tc.chat}, "chat": {tc.chat}}
		if tc.history != "" {
			form.Set("history", tc.history)
		}
		if code, _, _ := c.post("/ui/chats/pick", form); code != 303 {
			t.Fatalf("save %s: %d", tc.chat, code)
		}
		g := f.group(t, tc.chat)
		if !g.Enabled || (g.HistoryRequest == "queued") != tc.queued {
			t.Fatalf("%s: enabled=%v history=%q, want queued=%v", tc.chat, g.Enabled, g.HistoryRequest, tc.queued)
		}
	}
}

func TestSentPageShowsWhoSentWhatToWhomAndHowItWent(t *testing.T) {
	f, c, _ := chatsFixture(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	if _, err := f.store.Apply(ctx, f.account, []storage.Message{{GroupID: familyChat, MessageID: "OUT1", SenderID: "me@lid", Timestamp: at, Type: "text", Text: "rent is paid <b>today</b>", FromMe: true, Source: "live"}}); err != nil {
		t.Fatal(err)
	}
	log := sentlog.New(f.dir)
	for _, e := range []sentlog.Entry{
		{Time: at, Actor: "whatsapp-axi", Kind: "send", Chat: familyChat, MessageID: "OUT1", Outcome: sentlog.OutcomeSent},
		{Time: at.Add(time.Minute), Actor: sentlog.Owner, Kind: "reply", Chat: phoneChat, Outcome: sentlog.OutcomeFailed},
		{Time: at.Add(2 * time.Minute), Actor: "<i>helper</i>", Kind: "send", Chat: familyChat, MessageID: "GONE1", Outcome: sentlog.OutcomeUnknown},
	} {
		if err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	code, page, _ := c.get("/ui/sent")
	if code != 200 {
		t.Fatalf("sent page: %d", code)
	}
	for _, want := range []string{"Family Group", "whatsapp-axi", "Sent: WhatsApp accepted it", "Not sent", "Not confirmed: check WhatsApp", "text not kept", "(reply)", "You", "&lt;b&gt;today&lt;/b&gt;"} {
		if !strings.Contains(page, want) {
			t.Fatalf("sent page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "<b>today") || strings.Contains(page, "<i>helper") {
		t.Fatalf("untrusted text was not escaped:\n%s", page)
	}
	if strings.Index(page, "&lt;i&gt;helper") > strings.Index(page, "rent is paid") {
		t.Fatalf("not newest first:\n%s", page)
	}
	if code, page, _ = newPageClient(t, f).get("/ui/sent"); code != 401 || strings.Contains(page, "rent is paid") {
		t.Fatalf("the sent page was served without a session: %d", code)
	}
}

func TestSentPageIsEmptyBeforeAnythingIsSent(t *testing.T) {
	_, c, _ := chatsFixture(t)
	if _, page, _ := c.get("/ui/sent"); !strings.Contains(page, "Nothing has been sent from this computer yet.") {
		t.Fatalf("empty state:\n%s", page)
	}
}
