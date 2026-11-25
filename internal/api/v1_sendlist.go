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
// whether it has a list and how much of this hour's allowance is left. It
// never lists the numbers, which the agent's own records already hold.
func init() {
	registerV1(v1Route{Pattern: "GET /api/v1/send-list", Handle: (*handler).v1SendList})
	registerV1(v1Route{Pattern: "/api/v1/send-list", Handle: v1MethodNotAllowed("GET, HEAD", "use GET /api/v1/send-list")})
	query.RegisterFeature("write.send.list")
}

// sendListView is the agent-facing state of its list.
type sendListView struct {
	Active         bool       `json:"active"`
	Paused         bool       `json:"paused"`                // the person paused it: no sends until they resume it
	CanSend        bool       `json:"can_send"`              // holds the send scope
	Numbers        int        `json:"numbers"`               // on the list now
	NotMessaged    int        `json:"not_messaged"`          // never messaged yet
	Stopped        int        `json:"stopped"`               // removed after they replied STOP
	NewNumbersLeft int        `json:"new_numbers_left"`      // first messages left this hour
	MessagesLeft   int        `json:"messages_left"`         // list messages left this hour
	NextNewAt      *time.Time `json:"next_new_at,omitempty"` // no new number before this
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	NewPerHour     int        `json:"new_numbers_per_hour"`
	MessagesHour   int        `json:"messages_per_hour"`
}

func (h *handler) v1SendList(w http.ResponseWriter, r *http.Request, id string) {
	c := v1Client(r)
	if err := h.agents.AllowAccessRequest(c); err != nil {
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

// dropStopped removes the numbers on the active list of agentID, or on every
// active list when agentID is empty, that replied STOP after any agent's first
// list message to them, from every list for good. Only those numbers are
// looked up.
func (h *handler) dropStopped(ctx context.Context, now time.Time, agentID string) error {
	first, err := sendlist.FirstSent(h.dataDir)
	if err != nil {
		return err
	}
	lists, err := sendlist.Load(h.dataDir)
	if err != nil {
		return err
	}
	since := map[string]time.Time{}
	for _, l := range lists {
		if l.Active(now) && (agentID == "" || l.AgentID == agentID) {
			for _, x := range l.Numbers {
				if at, ok := first[x.Chat]; ok {
					since[x.Chat] = at
				}
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
	c, now := v1Client(r), time.Now()
	if err := h.dropStopped(r.Context(), now, c.ID); err != nil {
		v1Fail(w, id, 503, query.CodeInternal, "replies cannot be checked, so the send is refused", "check the wasync archive and data directory")
		return false
	}
	list, ok := sendlist.For(h.dataDir, c.ID, now)
	number := list.Find(chat)
	if !ok || number == nil {
		v1Fail(w, id, 403, query.CodeForbidden, "this number is not on your approved list (it was removed, it replied STOP, or the list ended)", "do not message it; ask the person if you need it approved again")
		return false
	}
	if list.Paused() {
		v1Fail(w, id, 403, query.CodeForbidden, "the person paused your approved list", "do not send until they resume it; GET /api/v1/send-list shows paused: false then")
		return false
	}
	if number.LastSentAt != nil {
		account, err := h.store.Account(r.Context())
		var last time.Time
		if err == nil && account != nil {
			last, err = h.store.LastIncoming(r.Context(), account.ID, chat)
		}
		if err != nil {
			v1Fail(w, id, 503, query.CodeInternal, "the archive is unavailable, so the send is refused", "")
			return false
		}
		if !last.After(*number.LastSentAt) {
			v1Fail(w, id, 429, query.CodeRateLimited, "you already messaged this number and it has not replied; a list allows one message per number until it replies", "wait for a reply before writing again")
			return false
		}
	}
	pace, err := h.listPace(c.Name, now)
	if err != nil {
		v1Fail(w, id, 503, query.CodeInternal, "the sent log cannot be read, so the send is refused", "check the permissions of the wasync data directory")
		return false
	}
	if pace.messages >= list.MessagesPerHour() {
		retryLater(w, id, pace.nextMessage, fmt.Sprintf("your approved list allows %d messages an hour and they are used up", list.MessagesPerHour()), "Wait for the hour to pass.")
		return false
	}
	if number.FirstSentAt == nil && pace.fresh >= list.NewPerHour() {
		retryLater(w, id, pace.nextFresh, fmt.Sprintf("your approved list allows %d new numbers an hour and they are used up", list.NewPerHour()), "Wait for the hour to pass.")
		return false
	}
	if number.FirstSentAt == nil && list.NextAt != nil && now.Before(*list.NextAt) {
		retryLater(w, id, *list.NextAt, "your approved list spaces first messages at random intervals and the next one is not due yet", "Send it then.")
		return false
	}
	entry.List, entry.NewNumber = true, number.FirstSentAt == nil
	return true
}

// listSent notes that the number was messaged, picks when the next first
// message may go, and makes sure its chat is
// archived from now on so a reply, or a STOP, can be seen. Both are best
// effort: the message is already on its way.
func (h *handler) listSent(ctx context.Context, agentID, chat string) {
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
	account, err := h.store.Account(ctx)
	if err != nil || account == nil {
		return
	}
	if on, err := h.store.IsEnabled(ctx, account.ID, chat); err != nil || on {
		return
	}
	if h.store.SaveDiscoveredChat(ctx, account.ID, storage.Group{ID: chat, Name: chat}) == nil {
		_ = h.store.EnableCapture(ctx, account.ID, chat, false)
	}
}
