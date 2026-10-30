// Package humancheck asks the person at this computer to confirm an action
// with something an agent cannot supply: Touch ID or the account password on
// macOS, Windows Hello on Windows. The operating system draws the prompt and
// the sentence on it is written by wasync, never by the caller of an API.
package humancheck

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	// ErrDeclined means the person cancelled, failed the check or never answered.
	ErrDeclined = errors.New("not confirmed on this computer")
	// ErrBusy means another confirmation prompt is already showing.
	ErrBusy = errors.New("a confirmation prompt is already open on this computer")
	// ErrLocked means too many recent prompts went unconfirmed.
	ErrLocked = errors.New("too many unconfirmed prompts; try again in a few minutes")
	// ErrUnavailable means this computer has no way to run the check right now.
	ErrUnavailable = errors.New("no system confirmation is available on this computer")
)

// Checker runs one operating-system confirmation.
type Checker interface {
	// Name says what the person will be asked for, in plain words.
	Name() string
	// Available reports whether Verify can show a prompt right now.
	Available() bool
	// Verify returns nil only when a person confirmed. reason is shown verbatim.
	Verify(ctx context.Context, reason string) error
}

// Clean makes untrusted text (a chat name, an agent name) safe to put in a
// prompt: control characters and line breaks become spaces, runs of spaces
// collapse, and long text is cut with an ellipsis.
func Clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); max > 0 && len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

const (
	promptTimeout = 60 * time.Second
	failLimit     = 3
	failWindow    = 10 * time.Minute
	lockFor       = 5 * time.Minute
)

// Gate serialises confirmations and slows down an agent that tries to wear the
// person out with prompts: one prompt at a time, and a short lock-out after
// several that nobody confirmed.
type Gate struct {
	checker Checker
	now     func() time.Time

	mu     sync.Mutex
	busy   bool
	fails  []time.Time
	locked time.Time
}

// NewGate wraps c. A nil c gives a gate that is never enabled.
func NewGate(c Checker) *Gate { return &Gate{checker: c, now: time.Now} }

// Enabled reports whether approvals must be confirmed with the system prompt.
func (g *Gate) Enabled() bool { return g != nil && g.checker != nil && g.checker.Available() }

// Name is the plain-words name of the check, or "" when there is none.
func (g *Gate) Name() string {
	if g == nil || g.checker == nil {
		return ""
	}
	return g.checker.Name()
}

// Confirm shows the prompt and returns nil only when the person confirmed.
func (g *Gate) Confirm(ctx context.Context, reason string) error {
	if !g.Enabled() {
		return ErrUnavailable
	}
	g.mu.Lock()
	now := g.now()
	if now.Before(g.locked) {
		g.mu.Unlock()
		return ErrLocked
	}
	if g.busy {
		g.mu.Unlock()
		return ErrBusy
	}
	g.busy = true
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, promptTimeout)
	defer cancel()
	err := g.checker.Verify(ctx, Clean(reason, 240))

	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy = false
	now = g.now()
	if err == nil {
		g.fails, g.locked = nil, time.Time{}
		return nil
	}
	kept := g.fails[:0]
	for _, t := range g.fails {
		if now.Sub(t) < failWindow {
			kept = append(kept, t)
		}
	}
	g.fails = append(kept, now)
	if len(g.fails) >= failLimit {
		g.locked, g.fails = now.Add(lockFor), nil
	}
	if errors.Is(err, ErrUnavailable) {
		return err
	}
	return ErrDeclined
}
