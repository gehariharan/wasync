package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gehariharan/wasync/internal/avatars"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/prefs"
	"github.com/gehariharan/wasync/internal/storage"
)

const (
	bookClub   = "120363000000000011@g.us"
	officeTeam = "120363000000000012@g.us"
	ammaChat   = "15550001111@s.whatsapp.net"
	unknownLID = "123456789012345@lid"
	shownLID   = "223456789012345@lid"
)

// pickerFixture is the access fixture with more chats, their last activity,
// and a signed-in browser. tweak adjusts what the owner offers.
func pickerFixture(t *testing.T, tweak func(*Owner)) (accessFixture, *pageClient, string) {
	t.Helper()
	f := setupAccessOwner(t, nil, tweak)
	ctx := context.Background()
	if err := f.store.SaveGroups(ctx, f.account, []storage.Group{{ID: bookClub, Name: "Book club"}, {ID: officeTeam, Name: "Office team"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []storage.Group{{ID: ammaChat, Name: "Amma"}, {ID: unknownLID, Name: unknownLID}, {ID: shownLID, Name: shownLID}} {
		if err := f.store.SaveDiscoveredChat(ctx, f.account, c); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := f.store.SaveChatActivity(ctx, f.account, map[string]time.Time{
		officeTeam: now.Add(-time.Hour),
		ammaChat:   now.Add(-2 * time.Hour),
		bookClub:   now.Add(-48 * time.Hour),
		unknownLID: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	c := newPageClient(t, f)
	c.get(linkPath(t, f))
	_, page, _ := c.get("/ui/chats/pick")
	return f, c, csrfOf(t, page)
}

func pickJSON(t *testing.T, c *pageClient, query string) pickList {
	t.Helper()
	code, body, hd := c.get("/ui/api/chats" + query)
	if code != 200 || !strings.HasPrefix(hd.Get("Content-Type"), "application/json") {
		t.Fatalf("GET /ui/api/chats%s: %d %s", query, code, body)
	}
	var out pickList
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func names(chats []pickChat) string {
	var out []string
	for _, c := range chats {
		out = append(out, c.Name)
	}
	return strings.Join(out, ", ")
}

func TestPickerListsRecentFirstWithTabCountsAndFoldsUnnamedChats(t *testing.T) {
	_, c, _ := pickerFixture(t, nil)
	all := pickJSON(t, c, "")
	if !all.Paired || all.Total != 8 || all.Counts != (pickCounts{All: 8, Groups: 3, People: 5, Unnamed: 4, Selected: 0}) {
		t.Fatalf("counts: %+v total %d", all.Counts, all.Total)
	}
	// Most recently active first; chats with no known activity A to Z after
	// them; the chats without a name folded into one entry at the end.
	if got := names(all.Chats); got != "Office team, Amma, Book club, Family Group" {
		t.Fatalf("order: %s", got)
	}
	if all.Unnamed == nil || all.Unnamed.Count != 4 || all.NextOffset != nil {
		t.Fatalf("folded entry: %+v next %v", all.Unnamed, all.NextOffset)
	}
	if all.Chats[0].Kind != "group" || all.Chats[1].Kind != "person" || all.Chats[1].Initials != "A" || all.Chats[0].LastActive == nil || all.Chats[3].LastActive == nil {
		t.Fatalf("rows: %+v", all.Chats)
	}
	if all.Chats[1].Picture != "/ui/avatar?c="+url.QueryEscape(ammaChat) {
		t.Fatalf("picture address: %q", all.Chats[1].Picture)
	}
	if got := names(pickJSON(t, c, "?tab=groups").Chats); got != "Office team, Book club, Family Group" {
		t.Fatalf("groups tab: %s", got)
	}
	people := pickJSON(t, c, "?tab=people")
	if names(people.Chats) != "Amma" || people.Unnamed == nil || people.Unnamed.Count != 4 {
		t.Fatalf("people tab: %s %+v", names(people.Chats), people.Unnamed)
	}
	// Opening the folded entry lists them by number, or as unknown people,
	// never by their raw address; the most recently active first.
	unnamed := pickJSON(t, c, "?tab=unnamed")
	if got := names(unnamed.Chats); got != "Unknown person, +15551234567, +15557654321, Unknown person" {
		t.Fatalf("unnamed tab: %s", got)
	}
	for _, ch := range unnamed.Chats {
		if !ch.Unnamed || ch.Initials != "#" || strings.Contains(ch.Name, "@") {
			t.Fatalf("unnamed row: %+v", ch)
		}
	}
	if _, page, _ := c.get("/ui/chats/pick"); strings.Contains(page, ">"+unknownLID+"<") || strings.Contains(page, "Numbers") {
		t.Fatalf("the page showed a raw address or the old Numbers tab:\n%s", page)
	}
}

func TestPickerShowsTheNumberBehindAHiddenAddressWhenKnown(t *testing.T) {
	_, c, _ := pickerFixture(t, func(o *Owner) {
		o.PhoneOf = func(_ context.Context, chat string) string {
			if chat == shownLID {
				return "+15559998888"
			}
			return ""
		}
	})
	if got := names(pickJSON(t, c, "?tab=unnamed").Chats); got != "Unknown person, +15551234567, +15557654321, +15559998888" {
		t.Fatalf("unnamed tab: %s", got)
	}
}

func TestPickerSearchesNamesAndNumbers(t *testing.T) {
	_, c, _ := pickerFixture(t, nil)
	got := pickJSON(t, c, "?q=team")
	if names(got.Chats) != "Office team" || got.Counts.All != 1 || got.Counts.Groups != 1 || got.Counts.People != 0 {
		t.Fatalf("name search: %s %+v", names(got.Chats), got.Counts)
	}
	// A number finds named and unnamed chats alike, and while searching the
	// unnamed ones are listed instead of folded.
	got = pickJSON(t, c, "?q="+url.QueryEscape("+1 555 123"))
	if names(got.Chats) != "+15551234567" || got.Unnamed != nil || got.Counts.Unnamed != 1 {
		t.Fatalf("number search: %s %+v", names(got.Chats), got)
	}
	if got = pickJSON(t, c, "?q=5550001"); names(got.Chats) != "Amma" {
		t.Fatalf("number of a named chat: %s", names(got.Chats))
	}
	// A hidden address is never searchable by its raw digits.
	if got = pickJSON(t, c, "?q=123456789012"); len(got.Chats) != 0 {
		t.Fatalf("hidden address matched: %s", names(got.Chats))
	}
}

func TestPickerPagesFiftyAtATimeWithNoCap(t *testing.T) {
	f, c, _ := pickerFixture(t, nil)
	var many []storage.Group
	for i := range 340 {
		many = append(many, storage.Group{ID: fmt.Sprintf("1203630000001%05d@g.us", i), Name: fmt.Sprintf("Group %03d", i)})
	}
	if err := f.store.SaveGroups(context.Background(), f.account, many); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	offset, pages := 0, 0
	for {
		page := pickJSON(t, c, fmt.Sprintf("?tab=groups&offset=%d", offset))
		pages++
		if len(page.Chats) > pickPage {
			t.Fatalf("page of %d", len(page.Chats))
		}
		for _, ch := range page.Chats {
			if seen[ch.ID] {
				t.Fatalf("%s listed twice", ch.ID)
			}
			seen[ch.ID] = true
		}
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if len(seen) != 343 || pages != 7 {
		t.Fatalf("listed %d chats in %d pages", len(seen), pages)
	}
	if got := pickJSON(t, c, "?tab=groups&limit=500"); len(got.Chats) != pickMaxPage {
		t.Fatalf("a page is capped at %d, got %d", pickMaxPage, len(got.Chats))
	}
	// The page without script shows 50 and offers 50 more, up to every chat.
	_, page, _ := c.get("/ui/chats/pick?tab=groups")
	if strings.Count(page, `name="sync"`) != pickPage || !strings.Contains(page, `href="/ui/chats/pick?show=100&amp;tab=groups">Show 50 more`) {
		t.Fatalf("first page without script:\n%s", page)
	}
	if _, page, _ = c.get("/ui/chats/pick?tab=groups&show=400"); strings.Count(page, `name="sync"`) != 343 || strings.Contains(page, "Show 50 more") {
		t.Fatalf("all chats were not reachable without script")
	}
}

func syncForm(csrf string, sync string, chats ...string) url.Values {
	return url.Values{"csrf": {csrf}, "sync": {sync}, "chat": chats}
}

func TestPickerSavesEachTickAtOnce(t *testing.T) {
	f, c, csrf := pickerFixture(t, nil)
	if code, body, _ := c.post("/ui/api/chats/sync", syncForm(csrf, "on", bookClub)); code != 200 || !strings.Contains(body, `"syncing":1`) {
		t.Fatalf("tick: %d %s", code, body)
	}
	// A tick made after searching for another chat keeps the first one.
	pickJSON(t, c, "?q=amma")
	if code, body, _ := c.post("/ui/api/chats/sync", syncForm(csrf, "on", ammaChat)); code != 200 || !strings.Contains(body, `"syncing":2`) {
		t.Fatalf("second tick: %d %s", code, body)
	}
	if !f.group(t, bookClub).Enabled || !f.group(t, ammaChat).Enabled {
		t.Fatal("a tick was lost")
	}
	selected := pickJSON(t, c, "?tab=selected")
	if names(selected.Chats) != "Amma, Book club" || selected.Syncing != 2 {
		t.Fatalf("selected: %s", names(selected.Chats))
	}
	if code, _, _ := c.post("/ui/api/chats/sync", syncForm(csrf, "off", bookClub)); code != 200 || f.group(t, bookClub).Enabled || !f.group(t, ammaChat).Enabled {
		t.Fatalf("untick: %d", code)
	}
	for _, bad := range []url.Values{
		syncForm(csrf, "on", "made-up@g.us"),
		syncForm(csrf, "maybe", bookClub),
		syncForm(csrf, "on"),
		{"csrf": {csrf}, "sync": {"on"}, "all": {"1"}},
	} {
		if code, _, _ := c.post("/ui/api/chats/sync", bad); code != 400 {
			t.Fatalf("%v: %d", bad, code)
		}
	}
	if code, _, _ := c.post("/ui/api/chats/sync", syncForm("wrong", "on", bookClub)); code != 403 || f.group(t, bookClub).Enabled {
		t.Fatalf("forged tick: %d", code)
	}
	if code, body, _ := c.post("/ui/api/chats/sync", url.Values{"csrf": {csrf}, "sync": {"off"}, "all": {"1"}}); code != 200 || !strings.Contains(body, `"syncing":0`) || f.group(t, ammaChat).Enabled {
		t.Fatalf("clear all: %d %s", code, body)
	}
	signedOut := newPageClient(t, f)
	for _, path := range []string{"/ui/api/chats", "/ui/api/settings", "/ui/avatar?c=" + url.QueryEscape(bookClub)} {
		if code, _, _ := signedOut.get(path); code != 401 {
			t.Fatalf("%s without a session: %d", path, code)
		}
	}
	if code, _, _ := signedOut.post("/ui/api/chats/sync", syncForm(csrf, "on", bookClub)); code != 401 {
		t.Fatalf("tick without a session: %d", code)
	}
}

func TestPickerAsksForOlderMessagesExceptInAgentOnlyMode(t *testing.T) {
	f, c, csrf := pickerFixture(t, nil)
	queued := func(chat string) bool { return f.group(t, chat).HistoryRequest == "queued" }
	c.post("/ui/api/chats/sync", syncForm(csrf, "on", bookClub))
	if !queued(bookClub) {
		t.Fatal("a newly synced chat did not ask for older messages by default")
	}
	if code, body, _ := c.post("/ui/api/settings", url.Values{"csrf": {csrf}, "use": {"agents"}}); code != 200 || !strings.Contains(body, `"agent_only":true`) || !strings.Contains(body, `"history_on_sync":false`) {
		t.Fatalf("agent-only: %d %s", code, body)
	}
	c.post("/ui/api/chats/sync", syncForm(csrf, "on", officeTeam))
	if queued(officeTeam) || !f.group(t, officeTeam).Enabled {
		t.Fatal("agent-only mode asked for older messages by itself")
	}
	// Still allowed when asked for.
	form := syncForm(csrf, "on", ammaChat)
	form.Set("history", "yes")
	c.post("/ui/api/chats/sync", form)
	if !queued(ammaChat) {
		t.Fatal("an explicit request for older messages was ignored")
	}
	// The page's box says the same, and an unticked box is honoured.
	_, page, _ := c.get("/ui/chats/pick")
	if strings.Contains(page, `name="history" value="yes" checked`) {
		t.Fatalf("agent-only page ticks older messages:\n%s", page)
	}
	c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "historybox": {"1"}, "sync": {phoneChat}})
	if queued(phoneChat) || !f.group(t, phoneChat).Enabled {
		t.Fatal("the unticked box asked for older messages")
	}
}

func TestSettingsDefaultToChatsWithMediaOnAndKeepOtherKeys(t *testing.T) {
	f, c, csrf := pickerFixture(t, nil)
	code, body, _ := c.get("/ui/api/settings")
	var s settingsJSON
	if code != 200 || json.Unmarshal([]byte(body), &s) != nil || s != (settingsJSON{Use: "chats", SaveMedia: true, HistoryOnSync: true}) {
		t.Fatalf("defaults: %d %s", code, body)
	}
	// Another wasync's key survives this one saving.
	if err := os.WriteFile(filepath.Join(f.dir, prefs.FileName), []byte(`{"first_open_seen":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, body, _ = c.post("/ui/api/settings", url.Values{"csrf": {csrf}, "use": {"agents"}, "save_media": {"off"}})
	if code != 200 || json.Unmarshal([]byte(body), &s) != nil || s != (settingsJSON{Use: "agents", UseChosen: true, AgentOnly: true}) {
		t.Fatalf("saved: %d %s", code, body)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, prefs.FileName))
	if err != nil || !strings.Contains(string(data), `"first_open_seen": true`) || localfs.CheckPrivateFile(filepath.Join(f.dir, prefs.FileName)) != nil {
		t.Fatalf("settings file: %s %v", data, err)
	}
	for _, bad := range []url.Values{{"csrf": {csrf}, "use": {"robots"}}, {"csrf": {csrf}, "save_media": {"yes"}}, {"csrf": {csrf}}} {
		if code, _, _ := c.post("/ui/api/settings", bad); code != 400 {
			t.Fatalf("%v: %d", bad, code)
		}
	}
	if code, _, _ := c.post("/ui/api/settings", url.Values{"csrf": {"wrong"}, "use": {"chats"}}); code != 403 {
		t.Fatalf("forged settings: %d", code)
	}
	// The page's media box saves the same setting.
	c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"media"}, "save_media": {"on"}})
	if got, _ := prefs.Open(f.dir).Get(); !got.MediaOn() || !got.AgentOnly() {
		t.Fatalf("media box: %+v", got)
	}
	c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"media"}})
	if got, _ := prefs.Open(f.dir).Get(); got.MediaOn() {
		t.Fatal("unticking the media box did not turn it off")
	}
}

var testJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 'j', 'p', 'g'}

func TestAvatarsAreFetchedOnRequestCachedAndFallBackToInitials(t *testing.T) {
	var fetches atomic.Int32
	_, c, _ := pickerFixture(t, func(o *Owner) {
		o.Pictures = func(_ context.Context, chat, known string) (avatars.Picture, error) {
			fetches.Add(1)
			if chat == bookClub {
				return avatars.Picture{ID: "p1", Data: testJPEG}, nil
			}
			return avatars.Picture{}, nil
		}
	})
	for range 2 {
		code, body, hd := c.get("/ui/avatar?c=" + url.QueryEscape(bookClub))
		if code != 200 || hd.Get("Content-Type") != "image/jpeg" || body != string(testJPEG) {
			t.Fatalf("picture: %d %q %q", code, hd.Get("Content-Type"), body)
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("fetched %d times; the cache was not used", fetches.Load())
	}
	code, body, hd := c.get("/ui/avatar?c=" + url.QueryEscape(ammaChat))
	if code != 200 || hd.Get("Content-Type") != "image/svg+xml" || !strings.Contains(body, ">A</text>") || hd.Get("Cache-Control") != "no-store" {
		t.Fatalf("initials: %d %q %s", code, hd.Get("Content-Type"), body)
	}
	if _, body, _ = c.get("/ui/avatar?c=" + url.QueryEscape(unknownLID)); !strings.Contains(body, ">#</text>") {
		t.Fatalf("unnamed chat initials: %s", body)
	}
	if code, _, _ = c.get("/ui/avatar?c=" + url.QueryEscape("999@g.us")); code != 404 {
		t.Fatalf("unknown chat: %d", code)
	}
	if fetches.Load() != 3 {
		t.Fatalf("fetches: %d", fetches.Load())
	}
}

func TestAvatarsWithoutAConnectionShowInitials(t *testing.T) {
	_, c, _ := pickerFixture(t, nil)
	code, body, hd := c.get("/ui/avatar?c=" + url.QueryEscape(officeTeam))
	if code != 200 || hd.Get("Content-Type") != "image/svg+xml" || !strings.Contains(body, ">OT</text>") {
		t.Fatalf("offline: %d %s", code, body)
	}
}

func TestPickerPageWorksWithoutScript(t *testing.T) {
	f, c, csrf := pickerFixture(t, nil)
	_, page, _ := c.get("/ui/chats/pick")
	for _, want := range []string{"All 8", "Groups 3", "People 5", "4 chats without a name", `href="/ui/chats/pick?tab=unnamed"`, `name="sync" value="` + officeTeam + `"`, "0 of 8 syncing", "Nothing is syncing yet.", `name="save_media" value="on" checked`, `name="history" value="yes" checked`} {
		if !strings.Contains(page, want) {
			t.Fatalf("picker lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "style=") {
		t.Fatalf("the picker grew inline styles:\n%s", page)
	}
	code, _, hd := c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "tab": {"groups"}, "q": {"o"}, "sync": {officeTeam}})
	if code != 303 || hd.Get("Location") != "/ui/chats/pick?q=o&tab=groups&n=saved" || !f.group(t, officeTeam).Enabled {
		t.Fatalf("sync: %d %v", code, hd.Get("Location"))
	}
	_, page, _ = c.get("/ui/chats/pick")
	if !strings.Contains(page, "Selected · 1") || !strings.Contains(page, `name="stop" value="`+officeTeam+`" aria-label="Remove Office team"`) {
		t.Fatalf("selected panel:\n%s", page)
	}
	if code, _, _ = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "stop": {officeTeam}}); code != 303 || f.group(t, officeTeam).Enabled {
		t.Fatalf("stop: %d", code)
	}
	if code, _, hd = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "sync": {"made-up@g.us"}}); code != 303 || !strings.Contains(hd.Get("Location"), "n=invalid") {
		t.Fatalf("unknown chat: %d %v", code, hd.Get("Location"))
	}
	if code, _, _ = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"nonsense"}}); code != 400 {
		t.Fatalf("unknown action: %d", code)
	}
	if code, _, _ = c.post("/ui/chats/pick", url.Values{"csrf": {"wrong"}, "action": {"clearall"}}); code != 403 {
		t.Fatalf("forged form: %d", code)
	}
	c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "sync": {bookClub}})
	if code, _, _ = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"clearall"}}); code != 303 || f.group(t, bookClub).Enabled {
		t.Fatalf("clear all: %d", code)
	}
}
