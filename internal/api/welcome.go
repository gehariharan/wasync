package api

import (
	"html/template"
	"net/http"
	"time"

	"github.com/gehariharan/wasync/internal/firstopen"
)

// The First open screen, shown once for a data directory before the setup
// steps: the logo builds itself, then a welcome, the @gehariharan byline and
// the note that wasync is not WhatsApp, with "I understand". The same note
// stays in Settings, About. It is for the person at this computer, so a
// device signed in through the tailnet goes straight to the steps.

const welcomePath = "/ui/welcome"

// notWhatsApp is the note every first-run screen shows (round 41 of the
// setup board), drawn from the README's unofficial-client note.
const notWhatsApp = "wasync is an independent, open-source project. It is not made by, affiliated with or endorsed by WhatsApp or Meta. It links to your account as a linked device through an unofficial method (whatsmeow), so WhatsApp could limit or ban an account that uses it. Use it at your own risk, and only keep or share chats you have permission to. WhatsApp is a trademark of Meta Platforms, Inc."

type welcomeData struct {
	uiData
	Note string
}

func (h *handler) welcomePage(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if remoteLogin(r) != "" {
		http.Redirect(w, r, "/ui/setup/link", http.StatusSeeOther)
		return
	}
	d := &welcomeData{uiData: uiData{CSRF: sess.csrf, Title: "Welcome to wasync", Welcome: true}, Note: notWhatsApp}
	h.noticeFrom(r, &d.uiData)
	h.webRender(w, r, welcomeTemplate, d)
}

// welcomeDone records that the screen was read and goes on to step 1.
func (h *handler) welcomeDone(w http.ResponseWriter, r *http.Request, sess uiSession) {
	if !h.uiForm(w, r, sess) {
		return
	}
	if err := firstopen.Done(h.dataDir, time.Now()); err != nil {
		http.Redirect(w, r, welcomePath+"?n=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/ui/setup/link", http.StatusSeeOther)
}

// uiLogoAnim is logo D drawn in parts, so components.css can build it once:
// the tile pops in, the bubble rises, its lines draw, the sync badge pops in
// and its arrows turn into place. Reduced motion shows it still.
const uiLogoAnim = `{{define "logoanim"}}<svg class="logo-anim" viewBox="0 0 512 512" role="img" aria-label="wasync">
<rect class="l-tile" width="512" height="512" rx="116" fill="#128254"/>
<path class="l-bubble" d="M152 112h208a52 52 0 0 1 52 52v116a52 52 0 0 1-52 52H226l-74 58 8-58h-8a52 52 0 0 1-52-52V164a52 52 0 0 1 52-52z" fill="#fff"/>
<rect class="l-line one" x="156" y="170" width="200" height="26" rx="13" fill="#128254"/>
<rect class="l-line two" x="156" y="226" width="132" height="26" rx="13" fill="#128254"/>
<g class="l-badge"><circle cx="372" cy="366" r="104" fill="#0d6a44" stroke="#128254" stroke-width="22"/>
<g class="l-arrows"><g fill="none" stroke="#fff" stroke-width="22" stroke-linecap="round"><path d="M320 350a54 54 0 0 1 96-30"/><path d="M424 382a54 54 0 0 1-96 30"/></g>
<path d="M424 296l4 46-44-10z" fill="#fff"/><path d="M320 436l-4-46 44 10z" fill="#fff"/></g></g>
</svg>{{end}}`

var welcomeTemplate = template.Must(template.New("welcome").Funcs(uiFuncs).Parse(uiHead + uiLogoAnim + `{{template "head" .}}
<div class="welcome">
{{template "notice" .}}
{{template "logoanim"}}
<h1 class="fade">Welcome to wasync</h1>
<p class="tagline fade">Your WhatsApp, on your computer. Agents see only what you allow.</p>
<p class="byline fade">by <a href="https://gehariharan.com" target="_blank" rel="noopener">@gehariharan</a></p>
<section class="card fade-late"><h2>Before you start</h2><p class="disclaimer">{{.Note}}</p></section>
<form method="post" action="/ui/welcome" class="fade-late"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="go">I understand</button></form>
</div>
</main></body></html>
`))
