package api

import (
	"html/template"
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

type composeData struct {
	uiData
	CanSend bool
}

func (h *handler) composeNewPage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	d := &composeData{uiData: uiData{CSRF: sess.csrf, Title: "wasync new message", Wide: true}, CanSend: h.sender != nil}
	if n, ok := notices[r.URL.Query().Get("n")]; ok {
		d.Notice, d.NoticeOK = n.text, n.ok
	}
	h.webRender(w, composeTemplate, d)
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
	if viaNumber {
		back("/ui/sent", url.Values{}, notice)
		return
	}
	back("/ui/chats", url.Values{"c": {chat}}, notice)
}

var composeTemplate = template.Must(template.New("compose").Parse(uiHead + `{{template "head" .}}
<h1>New message</h1>
{{if .Notice}}<p role="status" class="{{if .NoticeOK}}ok{{else}}bad{{end}}"><strong>{{.Notice}}</strong></p>{{end}}
{{if .CanSend}}
<form method="post" action="/ui/chats/send">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<p><label>Phone number, with its country code<br><input name="to" placeholder="+15551230000" autocomplete="off" required></label></p>
<p><label>Message<br><textarea name="text" rows="5" maxlength="16000" required style="width:100%"></textarea></label></p>
<p><button>Send</button> <span class="mut">It is sent once, as you. It shows in <a href="/ui/sent">Sent</a>. Nothing is kept if you leave without sending.</span></p>
</form>
{{else}}<p class="bad">wasync is not connected to WhatsApp, so nothing can be sent. Start it with <code>wasync serve</code> or <code>wasync tui</code> on a linked account.</p>{{end}}
</main></body></html>
`))
