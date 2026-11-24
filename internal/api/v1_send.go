package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/opensend"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// A plain send: a new message to a chat or a phone number, not a quoted reply.
// Normally it needs the send scope and a chat on the agent's allowlist, exactly
// like reply, and goes through the same scope check, rate limit and audit record
// before this handler runs. A chat or number the agent has not been given is
// refused; it is never turned into a grant by asking.
//
// The one exception is open sending, a time-boxed switch the person turns on
// from the web app after the system confirmation: while it is on, an agent that
// holds the send scope may reach any chat or number, up to a small cap per hour,
// and every such message is marked in the sent log.
func init() {
	registerV1(v1Route{Pattern: "POST /api/v1/messages", Scope: agentauth.ScopeSend, Mutation: true, Authorize: (*handler).authorizeSend, Handle: (*handler).v1Send})
	registerV1(v1Route{Pattern: "/api/v1/messages", Handle: v1MethodNotAllowed("POST", `use POST /api/v1/messages with {"chat":"CHAT_JID","text":"..."} or {"to":"PHONE_NUMBER","text":"..."}`)})
	query.RegisterFeature("write.send")
	query.RegisterFeature("write.send.to")
}

type v1OpenKey struct{}

// sendTarget peeks at the body for where the message is going, and puts the body
// back for the handler, which decodes it strictly. A body that cannot be read
// names no target, so it fails closed.
func sendTarget(r *http.Request) (chat, to string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 128<<10+1))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return "", ""
	}
	var in struct {
		Chat string `json:"chat"`
		To   string `json:"to"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return "", ""
	}
	return strings.TrimSpace(in.Chat), strings.TrimSpace(in.To)
}

// numberJID turns a phone number in international form ("+1 555 123 0000",
// "15551230000") into the WhatsApp address for that person.
func numberJID(number string) (string, bool) {
	digits := strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' {
			return -1
		}
		return r
	}, strings.TrimPrefix(strings.TrimSpace(number), "+"))
	if len(digits) < 7 || len(digits) > 15 || digits[0] == '0' || strings.Trim(digits, "0123456789") != "" {
		return "", false
	}
	return digits + "@s.whatsapp.net", true
}

// authorizeSend decides who may send where. A chat on the agent's allowlist
// goes the ordinary audited way. Anything else is refused, unless open sending
// is on and the agent holds the send scope.
func (h *handler) authorizeSend(w http.ResponseWriter, r *http.Request, id string) bool {
	chat, to := sendTarget(r)
	target, numberGiven := chat, false
	if target == "" && to != "" {
		numberGiven = true
		target, _ = numberJID(to)
	}
	ok := h.v1Decide(w, r, id, agentauth.ScopeSend, target, func(c *agentauth.Client) error {
		if target == "" || c.AllowsChatScope(agentauth.ScopeSend, target) {
			return h.agents.AuthorizeAndAudit(c, agentauth.ScopeSend, target, "", id, true)
		}
		if _, on := opensend.ActiveNow(h.dataDir, time.Now()); on {
			return h.agents.AuthorizeOpenSend(c, target, id)
		}
		err := h.agents.AuthorizeAndAudit(c, agentauth.ScopeSend, target, "", id, true)
		var denied *agentauth.Error
		if numberGiven && errors.As(err, &denied) && denied.Code == agentauth.CodeForbidden {
			denied.Help = "Sending to a number you were not given needs the person to switch on Open sending on the Agents page of the wasync web app (wasync web), or to approve a chat for you. Do not try another route."
		}
		return err
	})
	if ok && target != "" && !v1Client(r).AllowsChatScope(agentauth.ScopeSend, target) {
		*r = *r.WithContext(context.WithValue(r.Context(), v1OpenKey{}, true))
	}
	return ok
}

func (h *handler) v1Send(w http.ResponseWriter, r *http.Request, id string) {
	if h.sender == nil {
		v1WriteOffline(w, id, "sending")
		return
	}
	var body struct {
		Chat string `json:"chat"`
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if !v1WriteBody(w, r, id, &body) {
		return
	}
	target := strings.TrimSpace(body.Chat)
	switch {
	case target != "" && strings.TrimSpace(body.To) != "":
		v1Fail(w, id, 400, query.CodeInvalidArgument, "give either chat or to, not both", `send {"chat":"CHAT_JID","text":"..."} or {"to":"PHONE_NUMBER","text":"..."}`)
		return
	case target == "":
		var ok bool
		if target, ok = numberJID(body.To); !ok {
			v1Fail(w, id, 400, query.CodeInvalidArgument, "to must be a phone number with its country code, such as +15551230000", "")
			return
		}
	}
	in := whatsapp.SendInput{To: target, Text: body.Text}
	if err := in.Validate(); err != nil {
		v1WriteInvalid(w, id, err)
		return
	}
	entry := sentlog.Entry{Actor: agentName(r), Kind: "send", Chat: in.To}
	if open, _ := r.Context().Value(v1OpenKey{}).(bool); open {
		// Open sends run one at a time so the hourly caps cannot be raced.
		h.openMu.Lock()
		defer h.openMu.Unlock()
		if !h.openSendRoom(w, r, id, in.To, &entry) {
			return
		}
	}
	result, err := h.sender(r.Context(), in)
	h.recordSentEntry(entry, result, err)
	v1WriteResult(w, id, result, err)
}

// openSendRoom enforces open sending's hourly caps and marks the sent-log entry.
// It answers 429 itself when there is no room.
func (h *handler) openSendRoom(w http.ResponseWriter, r *http.Request, id, chat string, entry *sentlog.Entry) bool {
	if _, on := opensend.ActiveNow(h.dataDir, time.Now()); !on {
		v1Fail(w, id, 403, query.CodeForbidden, "open sending has just been switched off", "ask the person to approve this chat or switch open sending on again")
		return false
	}
	if h.sent == nil {
		v1Fail(w, id, 503, query.CodeInternal, "the sent log is unavailable, so open sending is refused", "restart wasync")
		return false
	}
	recent, err := h.sent.Recent(1000)
	if err != nil {
		v1Fail(w, id, 503, query.CodeInternal, "the sent log cannot be read, so open sending is refused", "check the permissions of the wasync data directory")
		return false
	}
	since := time.Now().Add(-opensend.Window)
	messages, fresh := 0, map[string]bool{}
	for _, e := range recent {
		if !e.Open || e.Time.Before(since) {
			continue
		}
		messages++
		if e.NewNumber {
			fresh[e.Chat] = true
		}
	}
	if messages >= opensend.MaxMessages {
		v1Fail(w, id, 429, query.CodeRateLimited, fmt.Sprintf("open sending allows %d messages an hour and they are used up", opensend.MaxMessages), "wait for the hour to pass, or ask the person to approve this chat")
		return false
	}
	known, err := h.knownChat(r.Context(), chat)
	if err != nil {
		v1Fail(w, id, 503, query.CodeInternal, "the archive is unavailable, so open sending is refused", "")
		return false
	}
	if !known && !fresh[chat] && len(fresh) >= opensend.MaxNewNumbers {
		v1Fail(w, id, 429, query.CodeRateLimited, fmt.Sprintf("open sending allows %d new numbers an hour and they are used up", opensend.MaxNewNumbers), "message a number from an existing chat, wait for the hour to pass, or ask the person to approve this one")
		return false
	}
	entry.Open, entry.NewNumber = true, !known
	return true
}

// knownChat reports whether the archive already has this chat.
func (h *handler) knownChat(ctx context.Context, chat string) (bool, error) {
	account, err := h.store.Account(ctx)
	if err != nil {
		return false, err
	}
	if account == nil {
		return false, nil
	}
	groups, err := h.store.GroupSummaries(ctx, account.ID)
	if err != nil {
		return false, err
	}
	for _, g := range groups {
		if g.ID == chat {
			return true, nil
		}
	}
	return false, nil
}

// agentName is the authenticated agent's name for the sent log.
func agentName(r *http.Request) string {
	if c := v1Client(r); c != nil && c.Name != "" {
		return c.Name
	}
	return "agent"
}

// recordSent notes a send attempt in the sent log. It is best effort: a log that
// cannot be written never changes what the send reported, because the message may
// already be on its way.
func (h *handler) recordSent(actor, kind, chat string, result whatsapp.SendResult, err error) {
	h.recordSentEntry(sentlog.Entry{Actor: actor, Kind: kind, Chat: chat}, result, err)
}

func (h *handler) recordSentEntry(e sentlog.Entry, result whatsapp.SendResult, err error) {
	if h.sent == nil {
		return
	}
	e.MessageID, e.Outcome = result.MessageID, sentlog.OutcomeSent
	if err != nil {
		e.Outcome, e.Code = sentlog.OutcomeFailed, query.CodeSendFailed
		if result.MessageID != "" {
			e.Outcome = sentlog.OutcomeUnknown // it left, but nothing came back
		}
	}
	_ = h.sent.Append(e)
}
