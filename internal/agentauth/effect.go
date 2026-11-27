package agentauth

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/humancheck"
)

// QuotedName is a client name made safe to show inside a prompt.
func QuotedName(name string) string { return `"` + humancheck.Clean(name, 40) + `"` }

// GrantEffect is the prompt sentence for creating an agent credential, in
// plain words, built from the values the caller validated. synced marks a
// credential that uses every synced chat; chats are then the ones synced now.
func GrantEffect(name string, scopes, chats []string, synced bool, expires time.Duration) string {
	where := "ALL chats"
	switch {
	case synced:
		where = fmt.Sprintf("every chat you sync in wasync (%d now) and any you sync later", len(chats))
	case len(chats) > 0:
		where = fmt.Sprintf("%d named chat(s)", len(chats))
	}
	text := "Approve: create the agent credential " + QuotedName(name) + " with scopes " + humancheck.Clean(strings.Join(scopes, ","), 120) + " for " + where
	if synced && slices.Equal(scopes, ConnectScopes) {
		text = "Approve: let the agent " + QuotedName(name) + " read, reply and react in " + where + " (it asks you before sending anywhere else)"
	}
	if expires > 0 {
		text += ", expiring in " + expires.String()
	}
	return text
}

// RevokeEffect is the prompt sentence for revoking an agent credential.
func RevokeEffect(name string) string {
	return "Approve: revoke agent " + QuotedName(name) + "; its credential stops working at once"
}
