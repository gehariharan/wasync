package api

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/gehariharan/wasync/internal/sentlog"
)

// composeFixture is the chats fixture with the owner's WhatsApp send replaced by
// rec (nil: not connected).
func composeFixture(t *testing.T, rec *sendRecorder) (accessFixture, *pageClient, string) {
	t.Helper()
	f := setupAccessOwner(t, nil, func(o *Owner) {
		if rec != nil {
			o.Sender = rec.send
		}
	})
	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	_, page, _ := c.get("/ui/chats/pick")
	return f, c, csrfOf(t, page)
}

func TestComposerNeedsASession(t *testing.T) {
	f := setupAccess(t)
	c := newPageClient(t, f)
	if code, _, _ := c.get("/ui/chats/new"); code != 401 {
		t.Fatalf("new message page without a session: %d", code)
	}
	if code, _, _ := c.post("/ui/chats/send", url.Values{"chat": {familyChat}, "text": {"hi"}}); code != 401 {
		t.Fatalf("sending without a session: %d", code)
	}
}

func TestComposerShowsOnlyWhereSendingIsPossible(t *testing.T) {
	_, c, _ := composeFixture(t, &sendRecorder{})
	if _, page, _ := c.get("/ui/chats?c=" + url.QueryEscape(familyChat)); !strings.Contains(page, `action="/ui/chats/send"`) || !strings.Contains(page, `name="chat" value="`+familyChat+`"`) || !strings.Contains(page, "New message") {
		t.Fatalf("no composer on a chat when connected:\n%s", page)
	}
	_, c2, _ := composeFixture(t, nil)
	if _, page, _ := c2.get("/ui/chats?c=" + url.QueryEscape(familyChat)); strings.Contains(page, `action="/ui/chats/send"`) {
		t.Fatalf("a composer was offered while not connected:\n%s", page)
	}
	if _, page, _ := c2.get("/ui/chats/new"); !strings.Contains(page, "not connected to WhatsApp") || strings.Contains(page, `name="to"`) {
		t.Fatalf("the new message page while not connected:\n%s", page)
	}
}

func TestNewMessageOpensInTheChatsPane(t *testing.T) {
	_, c, _ := composeFixture(t, &sendRecorder{})
	_, page, _ := c.get("/ui/chats/new")
	for _, want := range []string{`<div class="app picked">`, `class="list"`, `<span class="nm">New message</span>`, `name="to"`, `action="/ui/chats/send"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("new message pane lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, `name="chat"`) {
		t.Fatalf("a chat was opened beside the new message:\n%s", page)
	}
}

func TestComposerSendsToAChatOnceAndRecordsIt(t *testing.T) {
	rec := &sendRecorder{}
	f, c, csrf := composeFixture(t, rec)
	text := "Line one\n<b>line two</b> & more"
	code, _, hd := c.post("/ui/chats/send", url.Values{"csrf": {"forged"}, "chat": {familyChat}, "text": {text}})
	if code != 403 || rec.count() != 0 {
		t.Fatalf("forged form: %d, sent %d", code, rec.count())
	}
	code, _, hd = c.post("/ui/chats/send", url.Values{"csrf": {csrf}, "chat": {familyChat}, "text": {"   \n "}})
	if code != 303 || notice(hd) != "notext" || rec.count() != 0 {
		t.Fatalf("blank text: %d %q sent %d", code, notice(hd), rec.count())
	}
	code, _, hd = c.post("/ui/chats/send", url.Values{"csrf": {csrf}, "chat": {familyChat}, "text": {text}})
	loc := hd.Get("Location")
	if code != 303 || notice(hd) != "sentok" || !strings.HasPrefix(loc, "/ui/chats?") || !strings.Contains(loc, "c="+url.QueryEscape(familyChat)) {
		t.Fatalf("send: %d %q", code, loc)
	}
	if rec.count() != 1 || rec.calls[0].To != familyChat || rec.calls[0].Text != text {
		t.Fatalf("what reached WhatsApp: %+v", rec.calls)
	}
	entries := sentEntries(t, f.dir)
	if len(entries) != 1 || entries[0].Actor != sentlog.Owner || entries[0].Chat != familyChat || entries[0].Outcome != sentlog.OutcomeSent || entries[0].Open {
		t.Fatalf("sent log: %+v", entries)
	}
	if _, page, _ := c.get(loc); !strings.Contains(page, "Sent: WhatsApp accepted it") {
		t.Fatalf("the chat page did not say it was sent:\n%s", page)
	}
	if rec.count() != 1 {
		t.Fatalf("loading the page sent again: %d", rec.count())
	}
}

func TestComposerSendsToANumberAndReportsAFailureHonestly(t *testing.T) {
	rec := &sendRecorder{}
	f, c, csrf := composeFixture(t, rec)
	code, _, hd := c.post("/ui/chats/send", url.Values{"csrf": {csrf}, "to": {"+1 (555) 123-0000"}, "text": {"Hello there"}})
	// A sent message opens its new chat, where it is archived.
	if loc := hd.Get("Location"); code != 303 || !strings.HasPrefix(loc, "/ui/chats?") || !strings.Contains(loc, "c="+url.QueryEscape("15551230000@s.whatsapp.net")) || notice(hd) != "sentok" {
		t.Fatalf("send to a number: %d %q", code, loc)
	}
	if rec.count() != 1 || rec.calls[0].To != "15551230000@s.whatsapp.net" {
		t.Fatalf("what reached WhatsApp: %+v", rec.calls)
	}
	rec.err = errors.New("connection lost")
	code, _, hd = c.post("/ui/chats/send", url.Values{"csrf": {csrf}, "to": {"+1 555 123 0000"}, "text": {"again"}})
	if code != 303 || !strings.HasPrefix(hd.Get("Location"), "/ui/sent?") || notice(hd) != "sendfail" {
		t.Fatalf("failed send to a number: %d %q", code, hd.Get("Location"))
	}
	rec.err = nil
	for _, bad := range []string{"12345", "not a number", ""} {
		code, _, hd = c.post("/ui/chats/send", url.Values{"csrf": {csrf}, "to": {bad}, "text": {"x"}})
		if code != 303 || notice(hd) != "badto" || !strings.HasPrefix(hd.Get("Location"), "/ui/chats/new?") {
			t.Fatalf("bad number %q: %d %q", bad, code, hd.Get("Location"))
		}
	}
	rec.err, rec.id = errors.New("connection lost"), "MAYBE1"
	code, _, hd = c.post("/ui/chats/send", url.Values{"csrf": {csrf}, "chat": {familyChat}, "text": {"will it?"}})
	if code != 303 || notice(hd) != "sendfail" || rec.count() != 3 {
		t.Fatalf("failed send: %d %q sent %d", code, notice(hd), rec.count())
	}
	entries := sentEntries(t, f.dir)
	if len(entries) != 3 || entries[0].Outcome != sentlog.OutcomeUnknown || entries[0].MessageID != "MAYBE1" {
		t.Fatalf("a send that may have left must be logged as not confirmed: %+v", entries)
	}
	if _, page, _ := c.get("/ui/chats?c=" + url.QueryEscape(familyChat) + "&n=sendfail"); !strings.Contains(page, "Check WhatsApp before sending it again") {
		t.Fatalf("failure notice missing:\n%s", page)
	}
}

func TestComposerSaysSoWhenNotConnected(t *testing.T) {
	_, c, csrf := composeFixture(t, nil)
	code, _, hd := c.post("/ui/chats/send", url.Values{"csrf": {csrf}, "chat": {familyChat}, "text": {"hi"}})
	if code != 303 || notice(hd) != "offline" {
		t.Fatalf("not connected: %d %q", code, notice(hd))
	}
}

func sentEntries(t *testing.T, dir string) []sentlog.Entry {
	t.Helper()
	entries, err := sentlog.New(dir).Recent(50)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
