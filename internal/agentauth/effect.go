package agentauth

import (
	"fmt"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/humancheck"
)

// QuotedName is a client name made safe to show inside a prompt.
func QuotedName(name string) string { return `"` + humancheck.Clean(name, 40) + `"` }

// GrantEffect is the prompt sentence for creating an agent credential, in
// plain words, built from the values the caller validated.
func GrantEffect(name string, scopes, chats []string, onDemand bool, expires time.Duration) string {
	where := "ALL chats"
	switch {
	case onDemand && len(chats) > 0:
		where = "the chats you name now and any you approve later"
	case onDemand:
		where = "only the chats you approve when it asks"
	case len(chats) > 0:
		where = fmt.Sprintf("%d named chat(s)", len(chats))
	}
	text := "Approve: create the agent credential " + QuotedName(name) + " with scopes " + humancheck.Clean(strings.Join(scopes, ","), 120) + " for " + where
	if expires > 0 {
		text += ", expiring in " + expires.String()
	}
	return text
}
