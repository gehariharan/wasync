package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// The owner's composer: a plain form to write to a chat that is open on the
// chats page, or to start a new message to a phone number. It is the person at
// the browser sending as themselves, the same as `wasync send`, so it needs the
// signed-in page session and its form token but no extra confirmation. The
// message is sent once and the page then redirects, so reloading cannot send it
// again. A message that was not sent is not kept: the form just comes back empty.

const composeFormLimit = 32 << 10

// composeNewPage is the chats page with a new message to a phone number open
// in the right pane.
func (h *handler) composeNewPage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	h.chatsView(w, r, sess, true)
}

func (h *handler) composeSend(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiFormLimit(w, r, sess, composeFormLimit) {
		return
	}
	chat, to, text := strings.TrimSpace(r.PostForm.Get("chat")), strings.TrimSpace(r.PostForm.Get("to")), r.PostForm.Get("text")
	back := func(path string, q url.Values, notice string) {
		q.Set("n", notice)
		http.Redirect(w, r, path+"?"+q.Encode(), http.StatusSeeOther)
	}
	newPage := func(notice string) { back("/ui/chats/new", url.Values{}, notice) }
	target, viaNumber := chat, false
	if target == "" {
		var ok bool
		if target, ok = numberJID(to); !ok {
			newPage("badto")
			return
		}
		viaNumber = true
	}
	if strings.TrimSpace(text) == "" {
		if viaNumber {
			newPage("notext")
		} else {
			back("/ui/chats", url.Values{"c": {chat}}, "notext")
		}
		return
	}
	in := whatsapp.SendInput{To: target, Text: text}
	if err := in.Validate(); err != nil {
		if viaNumber {
			newPage("sendfail")
		} else {
			back("/ui/chats", url.Values{"c": {chat}}, "sendfail")
		}
		return
	}
	if h.sender == nil {
		if viaNumber {
			newPage("offline")
		} else {
			back("/ui/chats", url.Values{"c": {chat}}, "offline")
		}
		return
	}
	result, err := h.sender(r.Context(), in)
	h.recordSent(sentlog.Owner, "send", in.To, result, err)
	notice := "sentok"
	if err != nil {
		notice = "sendfail"
	}
	switch {
	case err == nil:
		// The message is in its chat, which is now at the top of Chats.
		back("/ui/chats", url.Values{"c": {target}}, notice)
	case viaNumber:
		back("/ui/sent", url.Values{}, notice)
	default:
		back("/ui/chats", url.Values{"c": {chat}}, notice)
	}
}
