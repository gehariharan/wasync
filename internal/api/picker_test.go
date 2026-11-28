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
	// The number shown is also the number search finds.
	if got := names(pickJSON(t, c, "?q=5559998888").Chats); got != "+15559998888" {
		t.Fatalf("number search of a hidden address: %q", got)
	}
	if got := pickJSON(t, c, "?q=223456789012"); len(got.Chats) != 0 {
		t.Fatalf("hidden address matched by its raw digits: %s", names(got.Chats))
	}
}

func TestPickerFindsANamedHiddenAddressByItsNumber(t *testing.T) {
	f, c, _ := pickerFixture(t, func(o *Owner) {
		o.PhoneOf = func(_ context.Context, chat string) string {
			if chat == shownLID {
				return "+15559998888"
			}
			return ""
		}
	})
	if err := f.store.SaveDiscoveredChat(context.Background(), f.account, storage.Group{ID: shownLID, Name: "Ravi"}); err != nil {
		t.Fatal(err)
	}
	if got := names(pickJSON(t, c, "?q=555999").Chats); got != "Ravi" {
		t.Fatalf("number search of a named hidden address: %q", got)
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

func TestPickerPageWithoutScriptAsksToNarrowPastItsLimit(t *testing.T) {
	f, c, _ := pickerFixture(t, nil)
	var many []storage.Group
	for i := range pickMaxShow + 10 {
		many = append(many, storage.Group{ID: fmt.Sprintf("1203630000002%05d@g.us", i), Name: fmt.Sprintf("Group %04d", i)})
	}
	if err := f.store.SaveGroups(context.Background(), f.account, many); err != nil {
		t.Fatal(err)
	}
	_, page, _ := c.get(fmt.Sprintf("/ui/chats/pick?tab=groups&show=%d", pickMaxShow-pickPage))
	if !strings.Contains(page, fmt.Sprintf("show=%d&amp;tab=groups\">Show 50 more", pickMaxShow)) {
		t.Fatalf("no link to the last page within the limit")
	}
	_, page, _ = c.get(fmt.Sprintf("/ui/chats/pick?tab=groups&show=%d", pickMaxShow))
	if strings.Count(page, `name="sync"`) != pickMaxShow || strings.Contains(page, "Show 50 more") || !strings.Contains(page, "Search to narrow the list.") {
		t.Fatalf("at the limit the page offers a link that cannot show more")
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
	for _, want := range []string{`All<span class="n">8<`, `Groups<span class="n">3<`, `People<span class="n">5<`, "4 chats without a name", `href="/ui/chats/pick?tab=unnamed"`, `name="sync" value="` + officeTeam + `"`, "Selected · <span data-count>0<", "Nothing is syncing yet.", `name="save_media" value="on" checked`, `name="history" value="yes" checked`} {
		if !strings.Contains(page, want) {
			t.Fatalf("picker lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "style=") {
		t.Fatalf("the picker grew inline styles:\n%s", page)
	}
	code, _, hd := c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "tab": {"groups"}, "q": {"o"}, "sync": {officeTeam}})
	if code != 303 || hd.Get("Location") != "/ui/chats/pick?q=o&tab=groups#c-"+officeTeam || !f.group(t, officeTeam).Enabled {
		t.Fatalf("sync: %d %v", code, hd.Get("Location"))
	}
	_, page, _ = c.get("/ui/chats/pick")
	if !strings.Contains(page, "Selected · <span data-count>1<") || !strings.Contains(page, `name="stop" value="`+officeTeam+`" aria-label="Remove Office team"`) {
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

func TestPickerSelectAllRespectsTabAndSearch(t *testing.T) {
	f, c, csrf := pickerFixture(t, nil)
	ctx := context.Background()
	const (
		jewelGroup  = "120363000000000091@g.us"
		jewelPerson = "15550009991@s.whatsapp.net"
		jewelStudio = "15550009992@s.whatsapp.net"
	)
	if err := f.store.SaveGroups(ctx, f.account, []storage.Group{{ID: jewelGroup, Name: "Jewellery Designs"}}); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []storage.Group{{ID: jewelPerson, Name: "Asha Jewellery"}, {ID: jewelStudio, Name: "Kiran Jewellery"}} {
		if err := f.store.SaveDiscoveredChat(ctx, f.account, ch); err != nil {
			t.Fatal(err)
		}
	}

	// On All, folded unnamed chats (4) are excluded from the shown list:
	// 4 groups + 3 named people = 7.
	all := pickJSON(t, c, "")
	if all.SelectAll != 7 {
		t.Fatalf("all select_all = %d, want 7", all.SelectAll)
	}
	// On both /ui/chats/pick and /ui/setup/chats, the button shows the count it will add.
	for _, path := range []string{"/ui/chats/pick", "/ui/setup/chats"} {
		_, page, _ := c.get(path)
		if !strings.Contains(page, `name="action" value="selectall" class="pick-all">Select all 7</button>`) {
			t.Fatalf("%s lacks Select all 7:\n%s", path, page)
		}
	}

	// Pre-select one non-matching chat; filtered Select all must preserve it.
	c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "sync": {officeTeam}})

	// Search "jewellery" on Groups tab: matches only jewelGroup (1 chat).
	_, page, _ := c.get("/ui/setup/chats?tab=groups&q=jewellery")
	if !strings.Contains(page, ">Select all 1</button>") {
		t.Fatalf("groups+search page lacks Select all 1:\n%s", page)
	}
	code, _, hd := c.post("/ui/setup/chats", url.Values{"csrf": {csrf}, "tab": {"groups"}, "q": {"jewellery"}, "action": {"selectall"}})
	if code != 303 || hd.Get("Location") != "/ui/setup/chats?q=jewellery&tab=groups" {
		t.Fatalf("setup select all groups+search: %d %v", code, hd.Get("Location"))
	}
	if !f.group(t, jewelGroup).Enabled || !f.group(t, officeTeam).Enabled {
		t.Fatal("matching group or previously selected chat is not enabled")
	}
	if f.group(t, jewelPerson).Enabled || f.group(t, jewelStudio).Enabled || f.group(t, bookClub).Enabled {
		t.Fatal("select all on Groups+search enabled chats outside the filter")
	}
	// Once every matching chat is selected, Select all disappears.
	_, page, _ = c.get("/ui/setup/chats?tab=groups&q=jewellery")
	if strings.Contains(page, `value="selectall"`) {
		t.Fatalf("Select all still shown when 0 unselected remain:\n%s", page)
	}

	// Search "jewellery" on All tab: jewelGroup is already selected, so only the
	// 2 unselected jewellery people remain to add.
	_, page, _ = c.get("/ui/chats/pick?q=jewellery")
	if !strings.Contains(page, ">Select all 2</button>") {
		t.Fatalf("all+search page lacks Select all 2:\n%s", page)
	}
	code, _, hd = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "tab": {"all"}, "q": {"jewellery"}, "action": {"selectall"}})
	if code != 303 || hd.Get("Location") != "/ui/chats/pick?q=jewellery" {
		t.Fatalf("pick select all search: %d %v", code, hd.Get("Location"))
	}
	if !f.group(t, jewelPerson).Enabled || !f.group(t, jewelStudio).Enabled {
		t.Fatal("search select all missed matching chats")
	}
	if f.group(t, bookClub).Enabled || f.group(t, ammaChat).Enabled || f.group(t, phoneChat).Enabled {
		t.Fatal("search select all enabled non-matching chats")
	}

	// People tab: 3 named people, 2 already selected -> Select all 1 (Amma).
	// Folded unnamed chats are not included until the unnamed tab is opened.
	_, page, _ = c.get("/ui/chats/pick?tab=people")
	if !strings.Contains(page, ">Select all 1</button>") {
		t.Fatalf("people tab lacks Select all 1:\n%s", page)
	}
	c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "tab": {"people"}, "action": {"selectall"}})
	if !f.group(t, ammaChat).Enabled {
		t.Fatal("people select all did not enable Amma")
	}
	if f.group(t, phoneChat).Enabled || f.group(t, unknownLID).Enabled {
		t.Fatal("people select all silently enabled folded unnamed chats")
	}

	// Unnamed tab: all 4 unnamed chats can be selected together there.
	_, page, _ = c.get("/ui/chats/pick?tab=unnamed")
	if !strings.Contains(page, ">Select all 4</button>") {
		t.Fatalf("unnamed tab lacks Select all 4:\n%s", page)
	}
	c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "tab": {"unnamed"}, "action": {"selectall"}})
	if !f.group(t, phoneChat).Enabled || !f.group(t, unknownLID).Enabled || !f.group(t, shownLID).Enabled {
		t.Fatal("unnamed tab select all did not enable unnamed chats")
	}
	if f.group(t, bookClub).Enabled {
		t.Fatal("unnamed tab select all touched a group chat")
	}

	// Clear still clears everything.
	if code, _, _ = c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "action": {"clearall"}}); code != 303 || f.group(t, jewelPerson).Enabled || f.group(t, officeTeam).Enabled {
		t.Fatalf("clear all after select all: %d", code)
	}
}

func TestPickerSelectAllAcrossPagesAuthHistoryAndSyncCount(t *testing.T) {
	f, c, csrf := pickerFixture(t, nil)
	ctx := context.Background()
	var many []storage.Group
	for i := range 220 {
		many = append(many, storage.Group{ID: fmt.Sprintf("1203630000003%05d@g.us", i), Name: fmt.Sprintf("Club %03d", i)})
	}
	if err := f.store.SaveGroups(ctx, f.account, many); err != nil {
		t.Fatal(err)
	}
	// Pre-select 59 of the 223 groups (with history off so we can verify they are untouched).
	pre := make([]string, 0, 59)
	for i := range 59 {
		pre = append(pre, many[i].ID)
	}
	if err := f.store.EnableCaptureMany(ctx, f.account, pre, false); err != nil {
		t.Fatal(err)
	}

	// Signed-out and forged requests are rejected just like single-chat ticks.
	signedOut := newPageClient(t, f)
	if code, _, _ := signedOut.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "tab": {"groups"}, "action": {"selectall"}}); code != 401 {
		t.Fatalf("signed-out select all: %d", code)
	}
	if code, _, _ := c.post("/ui/chats/pick", url.Values{"csrf": {"wrong"}, "tab": {"groups"}, "action": {"selectall"}}); code != 403 {
		t.Fatalf("forged select all: %d", code)
	}
	if f.group(t, many[100].ID).Enabled {
		t.Fatal("rejected request enabled a chat")
	}

	// 223 total groups - 59 selected = 164 remaining, even though page 1 shows only 50.
	_, page, _ := c.get("/ui/chats/pick?tab=groups")
	if !strings.Contains(page, ">Select all 164</button>") {
		t.Fatalf("paged groups tab lacks Select all 164:\n%s", page)
	}

	// Single-chat JSON sync with tab+q returns the live select_all count.
	form := syncForm(csrf, "on", many[59].ID)
	form.Set("tab", "groups")
	form.Set("q", "")
	code, body, _ := c.post("/ui/api/chats/sync", form)
	if code != 200 || !strings.Contains(body, `"syncing":60`) || !strings.Contains(body, `"select_all":163`) {
		t.Fatalf("sync with tab: %d %s", code, body)
	}
	// Ticking a chat outside the active tab leaves select_all for groups unchanged.
	form = syncForm(csrf, "on", ammaChat)
	form.Set("tab", "groups")
	form.Set("q", "")
	code, body, _ = c.post("/ui/api/chats/sync", form)
	if code != 200 || !strings.Contains(body, `"syncing":61`) || !strings.Contains(body, `"select_all":163`) {
		t.Fatalf("sync outside tab: %d %s", code, body)
	}

	// Select all with the history box unticked enables all remaining 163 groups
	// across pages without queueing history requests.
	code, _, hd := c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "tab": {"groups"}, "historybox": {"1"}, "action": {"selectall"}})
	if code != 303 || hd.Get("Location") != "/ui/chats/pick?tab=groups" {
		t.Fatalf("select all across pages: %d %v", code, hd.Get("Location"))
	}
	for _, g := range many {
		got := f.group(t, g.ID)
		if !got.Enabled {
			t.Fatalf("%s was not enabled", g.ID)
		}
		if g.ID != many[59].ID && got.HistoryRequest == "queued" {
			t.Fatalf("%s queued history despite unticked box", g.ID)
		}
	}
	if !f.group(t, bookClub).Enabled || !f.group(t, officeTeam).Enabled {
		t.Fatal("existing groups were not enabled")
	}

	// Turning history back on and selecting all on unnamed queues history for them.
	c.post("/ui/chats/pick", url.Values{"csrf": {csrf}, "tab": {"unnamed"}, "historybox": {"1"}, "history": {"yes"}, "action": {"selectall"}})
	if f.group(t, phoneChat).HistoryRequest != "queued" {
		t.Fatal("select all with history=yes did not queue history")
	}
}
