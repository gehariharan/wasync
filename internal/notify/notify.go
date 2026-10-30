// Package notify tells the person at this computer that something is waiting
// for them. It is only a nudge: a notification carries no code and no button,
// and nothing is approved by seeing or dismissing it.
package notify

import "github.com/gehariharan/wasync/internal/humancheck"

// Send shows a desktop notification, best effort. Text is cleaned first. A
// platform without a notifier, or any failure, is silently ignored because the
// request stays pending either way.
func Send(title, text string) {
	send(humancheck.Clean(title, 60), humancheck.Clean(text, 160))
}
