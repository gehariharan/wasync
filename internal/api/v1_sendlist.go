package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/sendlist"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/storage"
)

// Approved recipient lists: the person gives one agent a list of phone numbers
// on the Agents page, and that agent may message them with
// POST /api/v1/messages, paced here. GET /api/v1/send-list tells the agent
// whether it has a list, how much of this hour's allowance is left and which
// of its numbers were dropped after they replied STOP, so it can record the
// opt-out. It never lists the other numbers, which the agent's own records
// already hold.
func init() {
	registerV1(v1Route{Pattern: "GET /api/v1/send-list", Handle: (*handler).v1SendList})
	registerV1(v1Route{Pattern: "/api/v1/send-list", Handle: v1MethodNotAllowed("GET, HEAD", "use GET /api/v1/send-list")})
	query.RegisterFeature("write.send.list")
}

// sendListView is the agent-facing state of its list.
type sendListView struct {
	Active         bool       `json:"active"`
	Paused         bool       `json:"paused"`                    // the person paused it: no sends until they resume it
	CanSend        bool       `json:"can_send"`                  // holds the send scope
	Numbers        int        `json:"numbers"`                   // on the list now
	NotMessaged    int        `json:"not_messaged"`              // never messaged yet
	Stopped        int        `json:"stopped"`                   // removed after they replied STOP
	StoppedNumbers []string   `json:"stopped_numbers,omitempty"` // those of them it can name, as phone-number addresses
	NewNumbersLeft int        `json:"new_numbers_left"`          // first messages left this hour
	MessagesLeft   int        `json:"messages_left"`             // list messages left this hour
	NextNewAt      *time.Time `json:"next_new_at,omitempty"`     // no new number before this
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	NewPerHour     int        `json:"new_numbers_per_hour"`
	MessagesHour   int        `json:"messages_per_hour"`
}

func (h *handler) v1SendList(w http.ResponseWriter, r *http.Request, id string) {
	c := v1Client(r)
	if err := h.agents.AllowRequest(c); err != nil {
		var denied *agentauth.Error
		if errors.As(err, &denied) {
			if denied.RetryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(denied.RetryAfter.Seconds()))))
			}
			v1Fail(w, id, denied.Status, denied.Code, denied.Message, denied.Help)
		} else {
			v1Fail(w, id, 500, query.CodeInternal, "agent registry unavailable", "check permissions of agents.json in the wasync data directory")
		}
		return
	}
	now := time.Now()
	_ = h.dropStopped(r.Context(), now, c.ID)
	view := sendListView{CanSend: c.HasScope(agentauth.ScopeSend)}
	if list, ok := sendlist.For(h.dataDir, c.ID, now); ok {
		view.Active, view.Numbers, view.NotMessaged, view.Stopped, view.ExpiresAt = true, len(list.Numbers), list.NotMessaged(), list.Stopped, list.Until
		view.StoppedNumbers = list.StoppedChats
		view.NewPerHour, view.MessagesHour = list.NewPerHour(), list.MessagesPerHour()
		if list.Paused() {
			view.Paused = true
			v1OK(w, id, view, nil)
			return
		}
		pace, err := h.listPace(c.Name, now)
		if err != nil {
			v1Fail(w, id, 503, query.CodeInternal, "the sent log cannot be read", "check the permissions of the wasync data directory")
			return
		}
		view.MessagesLeft = max(0, list.MessagesPerHour()-pace.messages)
		view.NewNumbersLeft = max(0, list.NewPerHour()-pace.fresh)
		if view.NotMessaged > 0 {
			next := now
			if view.NewNumbersLeft == 0 && pace.nextFresh.After(next) {
				next = pace.nextFresh
			}
			if view.MessagesLeft == 0 && pace.nextMessage.After(next) {
				next = pace.nextMessage
			}
			if list.NextAt != nil && list.NextAt.After(next) {
				next = *list.NextAt
			}
			if next.After(now) {
				view.NextNewAt = &next
			}
		}
	}
	v1OK(w, id, view, nil)
}

// listPace is an agent's list sends in the last Window, from the sent log, so
// a number dropped or approved again still counts: all messages, and first
// messages to a number, each with when the oldest stops counting.
type listPace struct {
	messages, fresh        int
	nextMessage, nextFresh time.Time
}

func (h *handler) listPace(agent string, now time.Time) (listPace, error) {
	var p listPace
	if h.sent == nil {
		return p, errors.New("no sent log")
	}
	recent, err := h.sent.Recent(1000)
	if err != nil {
		return p, err
	}
	since := now.Add(-sendlist.Window)
	for _, e := range recent { // newest first, so the last one seen is the oldest
		if !e.List || e.Actor != agent || !e.Time.After(since) {
			continue
		}
		p.messages++
		p.nextMessage = e.Time.Add(sendlist.Window)
		if e.NewNumber {
			p.fresh++
			p.nextFresh = e.Time.Add(sendlist.Window)
		}
	}
	return p, nil
}

// dropStopped finds the numbers that replied STOP after any agent's first
// list message to them and keeps them off every list for good. It looks up
// the numbers on the active list of agentID, or, when agentID is empty, every
// number a list ever messaged, so a STOP after a list is gone still counts.
func (h *handler) dropStopped(ctx context.Context, now time.Time, agentID string) error {
	first, err := sendlist.FirstSent(h.dataDir)
	if err != nil {
		return err
	}
	since := first
	if agentID != "" {
		list, _ := sendlist.For(h.dataDir, agentID, now)
		since = map[string]time.Time{}
		for _, x := range list.Numbers {
			if at, ok := first[x.Chat]; ok {
				since[x.Chat] = at
			}
		}
	}
	if len(since) == 0 {
		return nil
	}
	account, err := h.store.Account(ctx)
	if err != nil || account == nil {
		return err
	}
	stops, err := h.store.StopSenders(ctx, account.ID, since)
	if err != nil || len(stops) == 0 {
		return err
	}
	return sendlist.OptOut(h.dataDir, stops)
}

// listReads returns the chats agentID may read because of its approved
// recipient list in force: the numbers on it that a list messaged, and the
// hidden-address chats the archive links to those numbers. A number dropped
// after it replied STOP is no longer on the list and is not readable. A paused list still counts, so replies can be read;
// a removed or expired one does not. A problem reading the list gives no
// chats, and one reading the archive leaves out the hidden-address chats, so
// an error never widens access.
func (h *handler) listReads(ctx context.Context, agentID string) []string {
	list, ok := sendlist.For(h.dataDir, agentID, time.Now())
	if !ok {
		return nil
	}
	chats := list.Messaged()
	if len(chats) == 0 || h.store == nil {
		return chats
	}
	account, err := h.store.Account(ctx)
	if err != nil || account == nil {
		return chats
	}
	if linked, err := h.store.PersonChats(ctx, account.ID, chats); err == nil {
		return linked
	}
	return chats
}

// retryLater answers 429 with a Retry-After when the time is known.
func retryLater(w http.ResponseWriter, id string, at time.Time, message, help string) {
	if wait := time.Until(at); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		help = fmt.Sprintf("%s Retry after %s.", help, at.UTC().Format(time.RFC3339))
	}
	v1Fail(w, id, 429, query.CodeRateLimited, message, help)
}

// listSendRoom enforces the list's pacing and marks the sent-log entry. It
// answers the refusal itself.
func (h *handler) listSendRoom(w http.ResponseWriter, r *http.Request, id, chat string, entry *sentlog.Entry) bool {
	newNumber, no := h.listSendCheck(r.Context(), v1Client(r), chat)
	if no != nil {
		no.write(w, id)
		return false
	}
	entry.List, entry.NewNumber = true, newNumber
	return true
}

// listSendCheck reports whether c's approved list takes a message to chat
// right now, and whether it would be the number's first.
func (h *handler) listSendCheck(ctx context.Context, c *agentauth.Client, chat string) (newNumber bool, no *sendRefusal) {
	now := time.Now()
	if err := h.dropStopped(ctx, now, c.ID); err != nil {
		return false, &sendRefusal{status: 503, code: query.CodeInternal, message: "replies cannot be checked, so the send is refused", help: "check the wasync archive and data directory"}
	}
	list, ok := sendlist.For(h.dataDir, c.ID, now)
	number := list.Find(chat)
	if !ok || number == nil {
		return false, &sendRefusal{status: 403, code: query.CodeForbidden, message: "this number is not on your approved list (it was removed, it replied STOP, or the list ended)", help: "do not message it; ask the person if you need it approved again"}
	}
	if list.Paused() {
		return false, &sendRefusal{status: 403, code: query.CodeForbidden, message: "the person paused your approved list", help: "do not send until they resume it; GET /api/v1/send-list shows paused: false then"}
	}
	if number.LastSentAt != nil {
		account, err := h.store.Account(ctx)
		var last time.Time
		if err == nil && account != nil {
			last, err = h.store.LastIncoming(ctx, account.ID, chat)
		}
		if err != nil {
			return false, &sendRefusal{status: 503, code: query.CodeInternal, message: "the archive is unavailable, so the send is refused"}
		}
		if !last.After(*number.LastSentAt) {
			return false, &sendRefusal{status: 429, code: query.CodeRateLimited, message: "you already messaged this number and it has not replied; a list allows one message per number until it replies", help: "wait for a reply before writing again"}
		}
	}
	pace, err := h.listPace(c.Name, now)
	if err != nil {
		return false, &sendRefusal{status: 503, code: query.CodeInternal, message: "the sent log cannot be read, so the send is refused", help: "check the permissions of the wasync data directory"}
	}
	if pace.messages >= list.MessagesPerHour() {
		return false, &sendRefusal{status: 429, code: query.CodeRateLimited, message: fmt.Sprintf("your approved list allows %d messages an hour and they are used up", list.MessagesPerHour()), help: "Wait for the hour to pass.", retryAt: pace.nextMessage}
	}
	if number.FirstSentAt == nil && pace.fresh >= list.NewPerHour() {
		return false, &sendRefusal{status: 429, code: query.CodeRateLimited, message: fmt.Sprintf("your approved list allows %d new numbers an hour and they are used up", list.NewPerHour()), help: "Wait for the hour to pass.", retryAt: pace.nextFresh}
	}
	if number.FirstSentAt == nil && list.NextAt != nil && now.Before(*list.NextAt) {
		return false, &sendRefusal{status: 429, code: query.CodeRateLimited, message: "your approved list spaces first messages at random intervals and the next one is not due yet", help: "Send it then.", retryAt: *list.NextAt}
	}
	return number.FirstSentAt == nil, nil
}

// captureListed makes sure a listed number's chat is archived before the
// message goes, so the message itself is kept in that chat and a reply, or a
// STOP, can be seen. It is best effort, and it holds even for a chat the owner
// turned off: an approved list needs its replies seen.
func (h *handler) captureListed(ctx context.Context, chat string) {
	account, err := h.store.Account(ctx)
	if err != nil || account == nil {
		return
	}
	if on, err := h.store.IsEnabled(ctx, account.ID, chat); err != nil || on {
		return
	}
	if h.store.SaveDiscoveredChat(ctx, account.ID, storage.Group{ID: chat, Name: chat}) == nil {
		_ = h.store.EnableListCapture(ctx, account.ID, chat)
	}
}

// listSent notes that the number was messaged and picks when the next first
// message may go. It is best effort: the message is already on its way.
func (h *handler) listSent(agentID, chat string) {
	now := time.Now().UTC()
	_ = sendlist.Update(h.dataDir, agentID, now, func(l *sendlist.List) error {
		if n := l.Find(chat); n != nil {
			if n.FirstSentAt == nil {
				n.FirstSentAt = &now
				next := now.Add(sendlist.Gap(l.NewPerHour(), rand.Float64()))
				l.NextAt = &next
			}
			n.LastSentAt = &now
		}
		return nil
	})
}
