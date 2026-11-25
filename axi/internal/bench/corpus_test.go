package bench

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gehariharan/wasync/axi/internal/testutil"
	"github.com/gehariharan/wasync/axi/internal/transport"
)

// The synthetic corpus: invented chat names, people and sentences from a
// fixed seed, shaped like wasync's v1 data (including the fields
// wasync-axi does not print, because raw readers receive them too).

const (
	account     = "4915100000000@lid"
	searchTerm  = "invoice"
	targetChat  = "Project Falcon"
	pageLimit   = 20  // wasync-axi's default page; the raw calls use the same
	changeLimit = 100 // observe's default page
	contextSize = 2
)

var (
	groupNames = []string{targetChat, "Family", "Climbing Club", "Book Circle", "Flat 4B", "Design Review", "Parents Class 3", "Five-a-side",
		"Wedding Planning", "Garden Allotment", "Release Train", "Sunday Brunch", "Choir Altos", "Bike Repairs", "Trip to Lisbon"}
	firstNames = []string{"Alice", "Bruno", "Chiara", "Deepak", "Elena", "Farid", "Grace", "Hiro", "Ines", "Jonas", "Kemi", "Lars",
		"Maya", "Nikolai", "Oona", "Pablo", "Quinn", "Rosa", "Sanjay", "Tove", "Umar", "Vera", "Wen", "Ximena", "Yusuf", "Zoe"}
	lastNames = []string{"Moreau", "Silva", "Rossi", "Iyer", "Novak", "Haddad", "Okafor", "Tanaka", "Lopez", "Berg", "Adeyemi", "Nilsson"}
	words     = strings.Fields(`the a we I you it is are was be will can should could please thanks ok sure maybe tomorrow today tonight
		meeting call lunch dinner train bus ticket price budget plan draft review deadline friday monday weekend morning evening
		send share check update confirm book pay invoice receipt photo link file doc slides notes list agenda venue room table
		late early soon done ready busy free great good nice sorry again also still just already really quick small big new old`)
)

type chat struct {
	ID, Name, Kind string
	Messages       []message // oldest first
}

type message struct {
	ID, Sender, Text string
	FromMe           bool
	At               time.Time
}

type corpus struct {
	Chats   []chat
	Changes []change
}

type change struct {
	Op, ChatID, MessageID string
}

func ref(chatID, msgID string) string {
	return "m1_" + base64.RawURLEncoding.EncodeToString([]byte(account+"\x00"+chatID+"\x00"+msgID))
}

// newCorpus builds chats × perChat messages and a change journal of the
// newest message per change.
func newCorpus(chats, perChat int) corpus {
	r := rand.New(rand.NewPCG(2026, 10))
	var c corpus
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	for i := 0; i < chats; i++ {
		ch := chat{Kind: "direct"}
		if i%2 == 0 && i/2 < len(groupNames) { // groupNames[0] is the target chat
			ch.Kind = "group"
			ch.Name = groupNames[i/2]
			ch.ID = fmt.Sprintf("1203630%011d@g.us", 4200+i)
		} else {
			ch.Name = firstNames[i%len(firstNames)] + " " + lastNames[(i/3)%len(lastNames)]
			ch.ID = fmt.Sprintf("49151%08d@s.whatsapp.net", 1000+i)
		}
		at := base.Add(time.Duration(r.IntN(3600)) * time.Second)
		for m := 0; m < perChat; m++ {
			at = at.Add(time.Duration(5+r.IntN(240)) * time.Minute)
			msg := message{ID: fmt.Sprintf("3EB0%08X%04d", r.Uint32(), m), At: at, FromMe: r.IntN(4) == 0}
			if msg.FromMe {
				msg.Sender = account
			} else if ch.Kind == "group" {
				msg.Sender = fmt.Sprintf("1555%07d@lid", r.IntN(40))
			} else {
				msg.Sender = strings.TrimSuffix(ch.ID, "@s.whatsapp.net") + "@lid"
			}
			n := 3 + r.IntN(18)
			if r.IntN(12) == 0 {
				n += 40 + r.IntN(60) // a few long messages
			}
			ws := make([]string, n)
			for k := range ws {
				ws[k] = words[r.IntN(len(words))]
			}
			if r.IntN(40) == 0 {
				ws[r.IntN(n)] = searchTerm
			}
			text := strings.Join(ws, " ")
			msg.Text = strings.ToUpper(text[:1]) + text[1:] + "."
			ch.Messages = append(ch.Messages, msg)
		}
		c.Chats = append(c.Chats, ch)
	}
	// The journal, oldest first: one change for each of the newest
	// 2*changeLimit messages, mostly creates with some edits and deletes.
	type pos struct{ c, m int }
	var all []pos
	for ci, ch := range c.Chats {
		for mi := range ch.Messages {
			all = append(all, pos{ci, mi})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return c.Chats[all[i].c].Messages[all[i].m].At.Before(c.Chats[all[j].c].Messages[all[j].m].At)
	})
	for _, p := range all[max(0, len(all)-2*changeLimit):] {
		op := "message.created"
		switch r.IntN(10) {
		case 0:
			op = "message.edited"
		case 1:
			op = "message.deleted"
		}
		c.Changes = append(c.Changes, change{Op: op, ChatID: c.Chats[p.c].ID, MessageID: c.Chats[p.c].Messages[p.m].ID})
	}
	return c
}

func (c corpus) chatJSON(ch chat) map[string]any {
	return map[string]any{
		"chat_kind": ch.Kind, "id": ch.ID, "name": ch.Name, "sync_enabled": true, "message_count": len(ch.Messages),
		"last_message_at": ch.Messages[len(ch.Messages)-1].At.Format(time.RFC3339), "media_policy": "inherit",
		"history_state": "complete", "history_request_state": "none", "history_batch_messages": 0, "potential_account_gaps": 0,
	}
}

func messageJSON(ch chat, idx int) map[string]any {
	m := ch.Messages[idx]
	return map[string]any{
		"account_id": 1, "id": 100000 + idx, "chat_id": ch.ID, "group_id": ch.ID, "chat_kind": ch.Kind, "message_id": m.ID,
		"ref": ref(ch.ID, m.ID), "sender_id": m.Sender, "is_from_me": m.FromMe, "message_type": "text", "text_content": m.Text,
		"timestamp": m.At.Format(time.RFC3339), "received_at": m.At.Add(2 * time.Second).Format(time.RFC3339), "source": "live",
	}
}

// fixture builds one canned read: the API path and the `wasync query`
// arguments the fake matches, exactly as transport.QueryArgs makes them.
func fixture(path string, q url.Values, body any) testutil.Fixture {
	args, err := transport.QueryArgs("", path, q)
	if err != nil {
		panic(err)
	}
	var query []string
	for _, a := range args[1:] {
		if a != "--v1" && a != "--json" {
			query = append(query, a)
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	api := path
	if len(q) > 0 {
		api += "?" + q.Encode()
	}
	return testutil.Fixture{API: api, Query: query, Body: raw}
}

func envelope(data any, page map[string]any) map[string]any {
	out := map[string]any{"request_id": "q-bench", "data": data}
	if page != nil {
		out["page"] = page
	}
	return out
}

func (c corpus) find(name string) chat {
	for _, ch := range c.Chats {
		if ch.Name == name || ch.ID == name {
			return ch
		}
	}
	panic("no chat " + name)
}

// busiestChangedChat is the chat with the most changes on the first page.
func (c corpus) busiestChangedChat() chat {
	count := map[string]int{}
	best := ""
	for _, ch := range c.Changes[changeLimit:min(len(c.Changes), 2*changeLimit)] {
		count[ch.ChatID]++
		if best == "" || count[ch.ChatID] > count[best] {
			best = ch.ChatID
		}
	}
	return c.find(best)
}

func (c corpus) cursor(seq int) string {
	return "c1_" + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s\x00epoch1\x00%d", account, seq)))
}

// fixtures returns every read the benchmark tasks make.
func (c corpus) fixtures() []testutil.Fixture {
	var out []testutil.Fixture
	all := make([]map[string]any, 0, len(c.Chats))
	for _, ch := range c.Chats {
		all = append(all, c.chatJSON(ch))
	}
	out = append(out, fixture("/chats", nil, envelope(map[string]any{"account_id": 1, "items": all}, nil)))

	target := c.find(targetChat)
	changed := c.busiestChangedChat()
	for _, match := range []string{targetChat, changed.ID} {
		ch := c.find(match)
		out = append(out, fixture("/chats", url.Values{"match": {match}}, envelope(map[string]any{"account_id": 1, "items": []any{c.chatJSON(ch)}}, nil)))
	}
	for _, ch := range []chat{target, changed} {
		items := []any{}
		for i := len(ch.Messages) - 1; i >= 0 && len(items) < pageLimit; i-- {
			items = append(items, messageJSON(ch, i))
		}
		page := map[string]any{"next_cursor": "p_" + ch.ID + "_2", "has_more": len(ch.Messages) > pageLimit}
		q := url.Values{"limit": {fmt.Sprint(pageLimit)}}
		out = append(out, fixture("/chats/"+ch.ID+"/messages", q, envelope(map[string]any{"account_id": 1, "items": items, "total": len(ch.Messages)}, page)))
	}

	type hit struct {
		ch chat
		i  int
	}
	var hits []hit
	for _, ch := range c.Chats {
		for i, m := range ch.Messages {
			if strings.Contains(m.Text, searchTerm) {
				hits = append(hits, hit{ch, i})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].ch.Messages[hits[i].i].At.After(hits[j].ch.Messages[hits[j].i].At) })
	items := []any{}
	for _, h := range hits[:min(len(hits), pageLimit)] {
		item := messageJSON(h.ch, h.i)
		before, after := []any{}, []any{}
		for k := max(0, h.i-contextSize); k < h.i; k++ {
			before = append(before, messageJSON(h.ch, k))
		}
		for k := h.i + 1; k <= min(len(h.ch.Messages)-1, h.i+contextSize); k++ {
			after = append(after, messageJSON(h.ch, k))
		}
		item["before"], item["after"] = before, after
		items = append(items, item)
	}
	q := url.Values{"q": {searchTerm}, "limit": {fmt.Sprint(pageLimit)}, "context": {fmt.Sprint(contextSize)}}
	out = append(out, fixture("/messages/search", q, envelope(map[string]any{"account_id": 1, "items": items, "total": len(hits)},
		map[string]any{"next_cursor": "s_2", "has_more": len(hits) > pageLimit})))

	// Changes after the checkpoint at sequence changeLimit: one full page.
	changes := []any{}
	start := changeLimit
	for seq := start + 1; seq <= start+changeLimit && seq <= len(c.Changes); seq++ {
		ch := c.Changes[seq-1]
		changes = append(changes, map[string]any{
			"sequence": seq, "cursor": c.cursor(seq), "op": ch.Op, "op_version": 1, "chat_id": ch.ChatID, "message_id": ch.MessageID,
			"ref": ref(ch.ChatID, ch.MessageID), "committed_at": time.Date(2026, 10, 19, 9, 0, seq, 0, time.UTC).Format(time.RFC3339),
		})
	}
	last := min(start+changeLimit, len(c.Changes))
	q = url.Values{"after": {c.cursor(start)}, "limit": {fmt.Sprint(changeLimit)}}
	out = append(out, fixture("/changes", q, envelope(map[string]any{"changes": changes},
		map[string]any{"next_cursor": c.cursor(last), "has_more": last < len(c.Changes)})))
	return out
}
