package api

import (
	"context"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/gehariharan/wasync/internal/tailnet"
)

// Opening the web app from another computer on the owner's tailnet.
//
// wasync still listens only on loopback. `tailscale serve` on this computer
// takes HTTPS on the machine's tailnet name and forwards to the loopback
// listener, so every such request arrives from 127.0.0.1 with the tailnet name
// in its Host header. Arriving from loopback therefore proves nothing about who
// is at the other end. What counts as the owner is the Tailscale login that
// `tailscale serve` puts in Tailscale-User-Login (it removes any copy the
// browser sent), checked against the one login the owner allowed with
// `wasync tailnet allow`. With no such setting, any Host other than the
// loopback address is refused exactly as before.
//
// A request that passes gets the web app only: never the archive API, /api/v1
// or anything an agent or the CLI uses. Its session is bound to that login.
// Reading and sending as the owner work as they do locally. The operating
// system's confirmation prompt appears on the computer running wasync, which a
// person at another computer cannot answer, so anything that needs it
// (approving a request, giving an agent more power, start at login) needs a
// passkey on the remote device instead (passkeys.go), and is refused there
// until the owner sets one up.

type remoteKey struct{}

// remoteLogin is the Tailscale login a request came from, or "" for a request
// to the loopback address.
func remoteLogin(r *http.Request) string { return remoteFrom(r.Context()) }

func remoteFrom(ctx context.Context) string {
	login, _ := ctx.Value(remoteKey{}).(string)
	return login
}

// tailnetLogin checks a request whose Host is not the loopback address. known
// says the host is the one the owner allowed; login is the allowed Tailscale
// login it carries, or "" when it carries none or another one.
func (h *handler) tailnetLogin(r *http.Request) (login string, known bool) {
	s, on, err := tailnet.Load(h.dataDir)
	if err != nil || !on || !s.Serves(r.Host) {
		return "", false
	}
	// Funnel requests come from the public internet; they are never the owner.
	if len(r.Header.Values(tailnet.FunnelHeader)) > 0 {
		return "", true
	}
	logins := r.Header.Values(tailnet.LoginHeader)
	if len(logins) != 1 {
		return "", true
	}
	// Tailscale encodes a non-ASCII login as an RFC 2047 word; ASCII passes as is.
	login, err = new(mime.WordDecoder).DecodeHeader(logins[0])
	if err != nil || !s.Allows(login) {
		return "", true
	}
	return login, true
}

// sameHTTPSOrigin accepts a request with no Origin or one from the tailnet
// name it was sent to, over HTTPS as `tailscale serve` provides.
func sameHTTPSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Path != "" || u.User != nil {
		return false
	}
	return strings.ToLower(u.Host) == strings.TrimSuffix(strings.ToLower(r.Host), ":443")
}

// noGate is the notice for an action that needs a confirmation when there is
// none for this request.
func noGate(r *http.Request) string {
	if remoteLogin(r) != "" {
		return "remote"
	}
	return "nogate"
}
