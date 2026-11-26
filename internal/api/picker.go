package api

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/prefs"
	"github.com/gehariharan/wasync/internal/storage"
)

// Choose chats to sync, as data for the web app's pages and their script. The
// list is searched, split into tabs with counts, ordered most recent first and
// paged; each change to which chats sync is saved at once, so a tick is never
// lost by searching or changing tabs. Direct chats WhatsApp gives no name for
// are folded under People as one entry instead of showing raw addresses.
//
// Like the other /ui pages these need a signed-in browser session; changes also
// need the session's form token.

const (
	pickTabAll      = "all"
	pickTabGroups   = "groups"
	pickTabPeople   = "people"
	pickTabUnnamed  = "unnamed"
	pickTabSelected = "selected"

	pickPage     = 50
	pickMaxPage  = 200
	pickMaxShow  = 2000
	pickMaxTicks = 200
	// unknownPerson names a direct chat with neither a name nor a known number.
	unknownPerson = "Unknown person"
	// avatarWait is how long a picture request waits for a first fetch.
	avatarWait = 3 * time.Second
)

func pickTabKey(s string) string {
	switch s {
	case pickTabGroups, pickTabPeople, pickTabUnnamed, pickTabSelected:
		return s
	}
	return pickTabAll
}

// numberName reports whether a direct chat is known only by its number or
// address, not by a name.
func numberName(id, name string) bool {
	return name == "" || name == id || strings.HasPrefix(name, "+") || strings.Contains(name, "@")
}

// phoneOfID is the "+digits" phone number in a phone-number chat address.
func phoneOfID(id string) string {
	user, server, ok := strings.Cut(id, "@")
	if !ok || server != "s.whatsapp.net" || user == "" || strings.Trim(user, "0123456789") != "" {
		return ""
	}
	return "+" + user
}

// digits keeps only the digits of s.
func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

type pickQuery struct {
	Tab, Query    string
	Offset, Limit int
}

type pickChat struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"` // "group" or "person"
	Unnamed    bool       `json:"unnamed,omitempty"`
	Syncing    bool       `json:"syncing"`
	LastActive *time.Time `json:"last_active,omitempty"`
	Initials   string     `json:"initials"`
	Picture    string     `json:"picture"`
}

type pickCounts struct {
	All      int `json:"all"`
	Groups   int `json:"groups"`
	People   int `json:"people"`
	Unnamed  int `json:"unnamed"`
	Selected int `json:"selected"`
}

// pickFold is the one entry that stands for the chats without a name.
type pickFold struct {
	Count   int `json:"count"`
	Syncing int `json:"syncing"`
}

type pickList struct {
	Paired  bool       `json:"paired"`
	Tab     string     `json:"tab"`
	Query   string     `json:"query"`
	Counts  pickCounts `json:"counts"`
	Syncing int        `json:"syncing"`
	Total   int        `json:"total"`
	Chats   []pickChat `json:"chats"`
	// Unnamed is the folded entry, shown after the chats of the last page of
	// the All and People tabs while nothing is searched.
	Unnamed *pickFold `json:"unnamed,omitempty"`
	// NextOffset is where the next page starts; nil on the last page.
	NextOffset *int `json:"next_offset"`
}

func readPickQuery(r *http.Request) pickQuery {
	q := r.URL.Query()
	pq := pickQuery{Tab: pickTabKey(q.Get("tab")), Query: humancheck.Clean(q.Get("q"), 60), Limit: pickPage}
	if n, err := strconv.Atoi(q.Get("offset")); err == nil && n > 0 {
		pq.Offset = n
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		pq.Limit = min(n, pickMaxPage)
	}
	return pq
}

// pickName is how the picker names a chat, and whether it has no name: a
// direct chat WhatsApp gives no name for shows its phone number when known,
// otherwise "Unknown person". Raw addresses are never shown. Only with resolve
// is the number behind a hidden (@lid) address looked up, which costs a lookup
// per chat, so lists do it only for the rows they return.
func (h *handler) pickName(ctx context.Context, c storage.PickerChat, resolve bool) (string, bool) {
	name := humancheck.Clean(c.Name, 80)
	if storage.ChatKind(c.ID) == "group" {
		if strings.TrimSpace(name) == "" || name == c.ID {
			return "Unnamed group", false
		}
		return name, false
	}
	if !numberName(c.ID, name) {
		return name, false
	}
	if phone := phoneOfID(c.ID); phone != "" {
		return phone, true
	}
	if resolve && h.phoneOf != nil {
		if phone := h.phoneOf(ctx, c.ID); phone != "" && strings.Trim(phone[1:], "0123456789") == "" {
			return phone, true
		}
	}
	return unknownPerson, true
}

type pickEntry struct {
	chat    storage.PickerChat
	name    string
	unnamed bool
	group   bool
}

// matches reports whether a chat matches the search: by name, or by phone
// number for a direct chat whose number is known.
func (e pickEntry) matches(needle, needleDigits string) bool {
	if needle == "" {
		return true
	}
	if !e.unnamed && strings.Contains(strings.ToLower(e.name), needle) {
		return true
	}
	if e.group || len(needleDigits) < 3 {
		return false
	}
	phone := phoneOfID(e.chat.ID)
	if phone == "" && e.unnamed && strings.HasPrefix(e.name, "+") {
		phone = e.name
	}
	return phone != "" && strings.Contains(digits(phone), needleDigits)
}

// before orders the picker: named chats first, the most recently active
// first, chats with no known activity A to Z after them.
func (e pickEntry) before(o pickEntry) bool {
	if e.unnamed != o.unnamed {
		return !e.unnamed
	}
	a, b := e.chat.LastActive, o.chat.LastActive
	if (a == nil) != (b == nil) {
		return a != nil
	}
	if a != nil && !a.Equal(*b) {
		return a.After(*b)
	}
	if na, nb := strings.ToLower(e.name), strings.ToLower(o.name); na != nb {
		return na < nb
	}
	return e.chat.ID < o.chat.ID
}

func (h *handler) pickChatJSON(e pickEntry) pickChat {
	kind, initialsOf := "person", initials(e.name)
	if e.group {
		kind = "group"
	}
	if e.unnamed {
		initialsOf = "#"
	}
	return pickChat{ID: e.chat.ID, Name: e.name, Kind: kind, Unnamed: e.unnamed, Syncing: e.chat.Enabled, LastActive: e.chat.LastActive, Initials: initialsOf, Picture: "/ui/avatar?c=" + url.QueryEscape(e.chat.ID)}
}

// listPick builds one page of the picker.
func (h *handler) listPick(ctx context.Context, q pickQuery) (pickList, error) {
	out := pickList{Tab: q.Tab, Query: q.Query, Chats: []pickChat{}}
	account, err := h.store.Account(ctx)
	if err != nil || account == nil {
		return out, err
	}
	out.Paired = true
	chats, err := h.store.PickerChats(ctx, account.ID)
	if err != nil {
		return out, err
	}
	needle := strings.ToLower(strings.TrimSpace(q.Query))
	needleDigits := digits(needle)
	if strings.Trim(needle, "+0123456789 -().") != "" {
		needleDigits = "" // words are searched as names only
	}
	fold := needle == "" && (q.Tab == pickTabAll || q.Tab == pickTabPeople)
	var shown []pickEntry
	var folded pickFold
	out.Total = len(chats)
	for _, c := range chats {
		if c.Enabled {
			out.Syncing++
		}
		e := pickEntry{chat: c, group: storage.ChatKind(c.ID) == "group"}
		e.name, e.unnamed = h.pickName(ctx, c, false)
		if !e.matches(needle, needleDigits) {
			continue
		}
		out.Counts.All++
		switch {
		case e.group:
			out.Counts.Groups++
		default:
			out.Counts.People++
		}
		if e.unnamed {
			out.Counts.Unnamed++
		}
		if c.Enabled {
			out.Counts.Selected++
		}
		in := false
		switch q.Tab {
		case pickTabAll:
			in = true
		case pickTabGroups:
			in = e.group
		case pickTabPeople:
			in = !e.group
		case pickTabUnnamed:
			in = e.unnamed
		case pickTabSelected:
			in = c.Enabled
		}
		if !in {
			continue
		}
		if fold && e.unnamed {
			folded.Count++
			if c.Enabled {
				folded.Syncing++
			}
			continue
		}
		shown = append(shown, e)
	}
	sort.SliceStable(shown, func(i, j int) bool { return shown[i].before(shown[j]) })
	start := min(q.Offset, len(shown))
	end := min(start+q.Limit, len(shown))
	for _, e := range shown[start:end] {
		if e.unnamed && e.name == unknownPerson {
			e.name, _ = h.pickName(ctx, e.chat, true)
		}
		out.Chats = append(out.Chats, h.pickChatJSON(e))
	}
	if end < len(shown) {
		next := end
		out.NextOffset = &next
	} else if folded.Count > 0 {
		out.Unnamed = &folded
	}
	return out, nil
}

// pickChatsJSON serves GET /ui/api/chats.
func (h *handler) pickChatsJSON(w http.ResponseWriter, r *http.Request, _ uiSession) {
	list, err := h.listPick(r.Context(), readPickQuery(r))
	if err != nil {
		fail(w, 500, "archive unavailable")
		return
	}
	writeJSON(w, 200, list)
}

// pickChange is one change to which chats sync.
type pickChange struct {
	Chats []string
	Sync  bool
	// All with Sync false stops syncing every chat.
	All bool
	// History also asks the phone for older messages of a chat that starts
	// syncing.
	History bool
}

var errUnknownChat = errors.New("unknown chat")

// applyPick saves one change at once and returns how many chats now sync.
func (h *handler) applyPick(ctx context.Context, account int64, ch pickChange) (int, error) {
	chats, err := h.store.PickerChats(ctx, account)
	if err != nil {
		return 0, err
	}
	enabled := make(map[string]bool, len(chats))
	for _, c := range chats {
		enabled[c.ID] = c.Enabled
	}
	var off []string
	if ch.All && !ch.Sync {
		for id, on := range enabled {
			if on {
				off = append(off, id)
			}
		}
		sort.Strings(off)
	} else {
		for _, id := range ch.Chats {
			if _, known := enabled[id]; !known {
				return 0, errUnknownChat
			}
		}
		for _, id := range ch.Chats {
			switch {
			case ch.Sync && !enabled[id]:
				if err = h.store.EnableCapture(ctx, account, id, ch.History); err != nil {
					return 0, err
				}
				enabled[id] = true
			case !ch.Sync && enabled[id]:
				off = append(off, id)
				enabled[id] = false
			}
		}
	}
	if len(off) > 0 {
		if err = h.store.SetEnabled(ctx, account, off, false); err != nil {
			return 0, err
		}
		for _, id := range off {
			enabled[id] = false
		}
	}
	n := 0
	for _, on := range enabled {
		if on {
			n++
		}
	}
	return n, nil
}

// historyDefault is whether a chat that starts syncing also asks the phone for
// older messages when the person did not say: yes, except in agent-only mode,
// which downloads no older history by itself.
func (h *handler) historyDefault() bool {
	s, _ := h.prefs.Get()
	return !s.AgentOnly()
}

// pickHistory reads the history choice of a form: "yes" or "no", or when
// absent the default.
func (h *handler) pickHistory(r *http.Request) bool {
	switch r.PostForm.Get("history") {
	case "yes":
		return true
	case "no":
		return false
	}
	return h.historyDefault()
}

// pickSyncJSON serves POST /ui/api/chats/sync: chat (one or more), sync=on or
// off, optionally history=yes|no, or all=1 with sync=off to stop syncing every
// chat.
func (h *handler) pickSyncJSON(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	account, err := h.store.Account(r.Context())
	if err != nil {
		fail(w, 500, "archive unavailable")
		return
	}
	if account == nil {
		fail(w, 409, "no paired account in archive")
		return
	}
	ch := pickChange{Chats: r.PostForm["chat"], All: r.PostForm.Get("all") == "1", History: h.pickHistory(r)}
	switch r.PostForm.Get("sync") {
	case "on":
		ch.Sync = true
	case "off":
	default:
		fail(w, 400, `sync must be "on" or "off"`)
		return
	}
	switch {
	case ch.All && (ch.Sync || len(ch.Chats) > 0):
		fail(w, 400, "all stops syncing every chat; send it with sync=off and no chat")
		return
	case !ch.All && len(ch.Chats) == 0:
		fail(w, 400, "name at least one chat")
		return
	case len(ch.Chats) > pickMaxTicks:
		fail(w, 400, "too many chats in one change")
		return
	}
	n, err := h.applyPick(r.Context(), account.ID, ch)
	switch {
	case errors.Is(err, errUnknownChat):
		fail(w, 400, "unknown chat")
		return
	case err != nil:
		fail(w, 500, "cannot update capture preference")
		return
	}
	writeJSON(w, 200, map[string]int{"syncing": n})
}

type settingsJSON struct {
	// Use is "agents" (For my AI agents only) or "chats" (Also read my chats here).
	Use string `json:"use"`
	// UseChosen is false until the owner has chosen.
	UseChosen bool `json:"use_chosen"`
	AgentOnly bool `json:"agent_only"`
	// SaveMedia is the "Also save photos, videos and files" box.
	SaveMedia bool `json:"save_media"`
	// HistoryOnSync is whether a chat that starts syncing also asks for older
	// messages unless told otherwise.
	HistoryOnSync bool `json:"history_on_sync"`
}

func settingsOf(s prefs.Settings) settingsJSON {
	out := settingsJSON{Use: s.Use, UseChosen: s.Use != "", AgentOnly: s.AgentOnly(), SaveMedia: s.MediaOn(), HistoryOnSync: !s.AgentOnly()}
	if out.Use == "" {
		out.Use = prefs.UseChats
	}
	return out
}

// settingsGetJSON serves GET /ui/api/settings.
func (h *handler) settingsGetJSON(w http.ResponseWriter, _ *http.Request, _ uiSession) {
	s, err := h.prefs.Get()
	if err != nil {
		fail(w, 500, "settings unavailable")
		return
	}
	writeJSON(w, 200, settingsOf(s))
}

// settingsPostJSON serves POST /ui/api/settings: use=agents|chats and
// save_media=on|off, either or both.
func (h *handler) settingsPostJSON(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	change, err := readSettingsForm(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s, err := h.prefs.Update(change)
	if err != nil {
		fail(w, 500, "settings were not saved")
		return
	}
	writeJSON(w, 200, settingsOf(s))
}

func readSettingsForm(r *http.Request) (func(*prefs.Settings), error) {
	use, media := r.PostForm.Get("use"), r.PostForm.Get("save_media")
	if use != "" && use != prefs.UseAgents && use != prefs.UseChats {
		return nil, errors.New(`use must be "agents" or "chats"`)
	}
	if media != "" && media != "on" && media != "off" {
		return nil, errors.New(`save_media must be "on" or "off"`)
	}
	if use == "" && media == "" {
		return nil, errors.New("nothing to change")
	}
	return func(s *prefs.Settings) {
		if use != "" {
			s.Use = use
		}
		if media != "" {
			on := media == "on"
			s.SaveMedia = &on
		}
	}, nil
}

// avatar serves GET /ui/avatar?c=CHAT: the chat's small profile picture, or
// its initials when it has none or none is known yet. Pictures are fetched only
// when a page asks for them, which a page does only for the rows it shows.
func (h *handler) avatar(w http.ResponseWriter, r *http.Request, _ uiSession) {
	account, err := h.store.Account(r.Context())
	if err != nil || account == nil {
		http.NotFound(w, r)
		return
	}
	chat, ok, err := h.store.PickerChat(r.Context(), account.ID, r.URL.Query().Get("c"))
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	if data := h.avatars.Picture(r.Context(), account.ID, chat.ID, avatarWait); data != nil {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=600")
		_, _ = w.Write(data)
		return
	}
	name, unnamed := h.pickName(r.Context(), chat, false)
	letters := initials(name)
	if unnamed {
		letters = "#"
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_ = initialsSVG.Execute(w, letters)
}

// initialsSVG is the picture of a chat with no profile picture: its initials
// on a plain circle. It is never cached, so a picture fetched later shows.
var initialsSVG = template.Must(template.New("initials").Parse(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40" role="img" aria-label="{{.}}"><circle cx="20" cy="20" r="20" fill="#d9ddd8"/><text x="20" y="25" text-anchor="middle" font-family="system-ui,sans-serif" font-size="14" font-weight="700" fill="#1c1f1d">{{.}}</text></svg>`))
