// Package sendlist holds the owner's approved recipient lists. The owner gives
// one agent a fixed list of phone numbers, once, after the system confirmation;
// that agent may then message those numbers, and only those, without the owner
// approving each chat or switching on open sending. wasync still paces it: the
// owner picks how many new numbers an hour (5 to 20), first messages are
// spaced at random intervals, and each number gets one message until it
// replies. A number that answers STOP is dropped from the list.
//
// The lists are a private file next to agents.json. A missing file means no
// lists; a damaged one means no lists too, so sends are refused rather than
// guessed. Pausing or removing a list needs no confirmation, because stopping
// is safe. A removed list's send history is kept for HistoryKept, so approving
// the same numbers again never sends anyone a second first message.
package sendlist

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/opensend"
)

const (
	// FileName is the private file in the data directory.
	FileName = "send-lists.json"
	// MaxNumbers is the most numbers one list may hold.
	MaxNumbers = 1000
	// Window is the period the hourly caps are counted over.
	Window = opensend.Window
	// MinPerHour and MaxPerHour bound how many numbers from its list the
	// owner may let one agent message for the first time per Window.
	// DefaultPerHour, the same cap as open sending, applies to a list saved
	// without a choice.
	MinPerHour     = opensend.MaxNewNumbers
	MaxPerHour     = 20
	DefaultPerHour = MinPerHour
	// MessagesPerNew is how many list messages, replies to replies included,
	// one agent may send per Window for each new number it may start.
	MessagesPerNew = 3
	// HistoryKept is the longest a removed list's send history is remembered.
	HistoryKept = 30 * 24 * time.Hour
	// maxHistory bounds the remembered numbers, and so does maxFileBytes, so
	// the file stays readable; the oldest go first.
	maxHistory = 3000

	maxFileBytes = 1 << 20
)

// Number is one approved recipient and what was sent to it.
type Number struct {
	Chat        string     `json:"chat"` // digits@s.whatsapp.net
	FirstSentAt *time.Time `json:"first_sent_at,omitempty"`
	LastSentAt  *time.Time `json:"last_sent_at,omitempty"`
}

// List is one agent's approved recipients.
type List struct {
	AgentID   string     `json:"agent_id"`
	Agent     string     `json:"agent"`
	GrantedAt time.Time  `json:"granted_at"`
	Until     *time.Time `json:"until,omitempty"`    // nil: until removed
	PerHour   int        `json:"per_hour,omitempty"` // new numbers an hour; 0 means DefaultPerHour
	NextAt    *time.Time `json:"next_at,omitempty"`  // no first message before this
	Numbers   []Number   `json:"numbers"`
	Stopped   int        `json:"stopped,omitempty"`   // numbers dropped after they replied STOP
	PausedAt  *time.Time `json:"paused_at,omitempty"` // no sends while set
}

// Active reports whether the list is in force at now. A paused list is in
// force (its numbers stay listed and replies are still read) but allows no
// sends.
func (l List) Active(now time.Time) bool { return l.Until == nil || now.Before(*l.Until) }

// Paused reports whether the owner paused the list.
func (l List) Paused() bool { return l.PausedAt != nil }

// sent is one messaged number of a removed list, remembered for HistoryKept.
type sent struct {
	AgentID string `json:"agent_id"`
	Number
	RemovedAt time.Time `json:"removed_at"`
}

// NewPerHour is how many numbers the agent may message for the first time per Window.
func (l List) NewPerHour() int {
	if l.PerHour == 0 {
		return DefaultPerHour
	}
	return l.PerHour
}

// MessagesPerHour is how many list messages the agent may send per Window.
func (l List) MessagesPerHour() int { return MessagesPerNew * l.NewPerHour() }

// Gap is the wait after a first message before the next one may go: on
// average an hour divided by perHour, drawn between half and one and a half
// times that so the messages do not go out like clockwork. r is in [0, 1).
func Gap(perHour int, r float64) time.Duration {
	avg := Window / time.Duration(perHour)
	return avg/2 + time.Duration(r*float64(avg))
}

// ValidPerHour reports whether n is a pace the owner may choose.
func ValidPerHour(n int) bool { return n >= MinPerHour && n <= MaxPerHour }

// Find returns the entry for chat, or nil.
func (l *List) Find(chat string) *Number {
	for i := range l.Numbers {
		if l.Numbers[i].Chat == chat {
			return &l.Numbers[i]
		}
	}
	return nil
}

// NotMessaged counts the numbers that have had no message yet.
func (l List) NotMessaged() int {
	n := 0
	for _, x := range l.Numbers {
		if x.FirstSentAt == nil {
			n++
		}
	}
	return n
}

// Drop removes the numbers in stop from the list, counting them as stopped,
// and reports how many went.
func (l *List) Drop(stop map[string]bool) int {
	kept := l.Numbers[:0]
	for _, x := range l.Numbers {
		if !stop[x.Chat] {
			kept = append(kept, x)
		}
	}
	gone := len(l.Numbers) - len(kept)
	l.Numbers = kept
	l.Stopped += gone
	return gone
}

type file struct {
	Lists []List `json:"lists"`
	Sent  []sent `json:"sent,omitempty"` // removed lists' send history
}

// mu serializes changes within the one wasync owner process that writes the file.
var mu sync.Mutex

// Load returns every saved list. A missing file has none; a damaged or
// non-private file is an error.
func Load(dir string) ([]List, error) {
	mu.Lock()
	defer mu.Unlock()
	v, err := load(dir)
	return v.Lists, err
}

func load(dir string) (file, error) {
	if dir == "" {
		return file{}, nil
	}
	path := filepath.Join(dir, FileName)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return file{}, nil
	}
	if err := localfs.CheckPrivateFile(path); err != nil {
		return file{}, fmt.Errorf("%s must be a regular private file: %w", FileName, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return file{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return file{}, err
	}
	if len(data) > maxFileBytes {
		return file{}, fmt.Errorf("%s is too large", FileName)
	}
	var v file
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&v); err != nil {
		return file{}, fmt.Errorf("invalid %s: %w", FileName, err)
	}
	for _, l := range v.Lists {
		if l.AgentID == "" || len(l.Numbers) > MaxNumbers || l.PerHour != 0 && !ValidPerHour(l.PerHour) {
			return file{}, fmt.Errorf("invalid %s: a list has no agent, too many numbers or an invalid pace", FileName)
		}
		for _, x := range l.Numbers {
			if !numberAddress(x.Chat) {
				return file{}, fmt.Errorf("invalid %s: %q is not a phone number address", FileName, x.Chat)
			}
		}
	}
	for _, x := range v.Sent {
		if x.AgentID == "" || !numberAddress(x.Chat) {
			return file{}, fmt.Errorf("invalid %s: a remembered number has no agent or is not a phone number address", FileName)
		}
	}
	v.Sent = recent(v.Sent)
	return v, nil
}

// recent keeps the history removed in the last HistoryKept, newest first, at
// most maxHistory of it.
func recent(all []sent) []sent {
	forget := time.Now().Add(-HistoryKept)
	kept := all[:0]
	for _, x := range all {
		if x.RemovedAt.After(forget) {
			kept = append(kept, x)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].RemovedAt.After(kept[j].RemovedAt) })
	return kept[:min(len(kept), maxHistory)]
}

func numberAddress(chat string) bool {
	_, ok := NumberChat(strings.TrimSuffix(chat, "@s.whatsapp.net"))
	return ok && strings.HasSuffix(chat, "@s.whatsapp.net")
}

// For returns the agent's list when it has one in force at now. Any problem
// reading the file counts as no list, so a send is refused.
func For(dir, agentID string, now time.Time) (List, bool) {
	lists, err := Load(dir)
	if err != nil {
		return List{}, false
	}
	for _, l := range lists {
		if l.AgentID == agentID && l.Active(now) {
			return l, true
		}
	}
	return List{}, false
}

// Listed reports whether chat is on any active list.
func Listed(dir, chat string, now time.Time) bool {
	lists, err := Load(dir)
	if err != nil {
		return false
	}
	for i := range lists {
		if lists[i].Active(now) && lists[i].Find(chat) != nil {
			return true
		}
	}
	return false
}

// Put saves l as the agent's list, replacing any earlier one. Numbers the
// earlier list, or a list removed in the last HistoryKept, already messaged
// keep that history, so approving a list again never lets a number be
// messaged twice without a reply. Numbers the earlier list messaged that l
// leaves out are remembered as if removed.
func Put(dir string, l List) error {
	if l.AgentID == "" || len(l.Numbers) == 0 || len(l.Numbers) > MaxNumbers || l.PerHour != 0 && !ValidPerHour(l.PerHour) {
		return fmt.Errorf("a list needs an agent, 1 to %d numbers and a pace of %d to %d an hour", MaxNumbers, MinPerHour, MaxPerHour)
	}
	mu.Lock()
	defer mu.Unlock()
	v, err := load(dir)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	out := v.Lists[:0]
	for _, old := range v.Lists {
		if old.AgentID != l.AgentID {
			out = append(out, old)
			continue
		}
		v.Sent = append(v.Sent, messaged(old, now)...)
		l.Stopped += old.Stopped
		if old.NextAt != nil && (l.NextAt == nil || old.NextAt.After(*l.NextAt)) {
			l.NextAt = old.NextAt
		}
	}
	kept := v.Sent[:0]
	for _, x := range v.Sent {
		if n := l.Find(x.Chat); x.AgentID == l.AgentID && n != nil {
			if n.FirstSentAt == nil || x.FirstSentAt != nil && x.FirstSentAt.Before(*n.FirstSentAt) {
				n.FirstSentAt = x.FirstSentAt
			}
			if n.LastSentAt == nil || x.LastSentAt != nil && x.LastSentAt.After(*n.LastSentAt) {
				n.LastSentAt = x.LastSentAt
			}
			continue
		}
		kept = append(kept, x)
	}
	v.Lists, v.Sent = append(out, l), kept
	return save(dir, v)
}

// messaged is what l sent, to remember once it is gone.
func messaged(l List, now time.Time) []sent {
	var out []sent
	for _, x := range l.Numbers {
		if x.FirstSentAt != nil || x.LastSentAt != nil {
			out = append(out, sent{AgentID: l.AgentID, Number: x, RemovedAt: now})
		}
	}
	return out
}

// Remove deletes the agent's list and remembers what it sent for HistoryKept.
// Removing a list that is not there is harmless.
func Remove(dir, agentID string) error {
	mu.Lock()
	defer mu.Unlock()
	v, err := load(dir)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	out := v.Lists[:0]
	for _, l := range v.Lists {
		if l.AgentID != agentID {
			out = append(out, l)
			continue
		}
		v.Sent = append(v.Sent, messaged(l, now)...)
	}
	if len(out) == len(v.Lists) {
		return nil
	}
	v.Lists = out
	return save(dir, v)
}

// Pause stops the agent's list from sending, keeping everything else, or
// resumes it when pause is false. Pausing a paused list, or resuming a running
// one, changes nothing.
func Pause(dir, agentID string, pause bool, now time.Time) error {
	return Update(dir, agentID, now, func(l *List) error {
		switch {
		case pause && l.PausedAt == nil:
			at := now.UTC()
			l.PausedAt = &at
		case !pause:
			l.PausedAt = nil
		}
		return nil
	})
}

// ErrNoList means the agent has no list in force.
var ErrNoList = errors.New("no approved list for this agent")

// Update changes the agent's list in force under the file lock and saves it.
// A messaged number the change takes off the list is remembered as if removed.
func Update(dir, agentID string, now time.Time, change func(*List) error) error {
	mu.Lock()
	defer mu.Unlock()
	v, err := load(dir)
	if err != nil {
		return err
	}
	for i := range v.Lists {
		if v.Lists[i].AgentID == agentID && v.Lists[i].Active(now) {
			before := messaged(v.Lists[i], now.UTC())
			if err = change(&v.Lists[i]); err != nil {
				return err
			}
			for _, x := range before {
				if v.Lists[i].Find(x.Chat) == nil {
					v.Sent = append(v.Sent, x)
				}
			}
			return save(dir, v)
		}
	}
	return ErrNoList
}

func save(dir string, v file) (result error) {
	if err := localfs.EnsureDir(dir); err != nil {
		return err
	}
	if v.Lists == nil {
		v.Lists = []List{}
	}
	sort.SliceStable(v.Lists, func(i, j int) bool { return v.Lists[i].Agent < v.Lists[j].Agent })
	v.Sent = recent(v.Sent)
	data, err := json.MarshalIndent(v, "", "  ")
	for err == nil && len(data)+1 > maxFileBytes && len(v.Sent) > 0 {
		v.Sent = v.Sent[:len(v.Sent)-max(1, len(v.Sent)/10)]
		data, err = json.MarshalIndent(v, "", "  ")
	}
	if err != nil {
		return err
	}
	if len(data)+1 > maxFileBytes {
		return fmt.Errorf("the approved lists are too large for %s; remove a list first", FileName)
	}
	tmp := filepath.Join(dir, "."+FileName+".tmp")
	f, err := localfs.OpenFile(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			os.Remove(tmp)
		}
	}()
	if err = f.Truncate(0); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, FileName))
}

// NumberChat turns a phone number in international form ("+1 555 123 0000",
// "15551230000") into the WhatsApp address for that person.
func NumberChat(number string) (string, bool) {
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

// Parse reads a pasted or uploaded list: one phone number per line (commas,
// semicolons and tabs also separate). Blank entries are skipped and repeats
// kept once. Every other entry must be a phone number with its country code,
// otherwise the whole list is refused and the first bad entry is returned.
func Parse(text string) (chats []string, bad string) {
	text = strings.TrimPrefix(text, "\uFEFF")
	seen := map[string]bool{}
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == '\t'
	}) {
		field = strings.Trim(strings.TrimSpace(field), `"'`)
		if field == "" {
			continue
		}
		chat, ok := NumberChat(field)
		if !ok {
			return nil, field
		}
		if !seen[chat] {
			seen[chat] = true
			chats = append(chats, chat)
		}
	}
	return chats, ""
}

// Shown is how a list number is written for the owner: +digits.
func Shown(chat string) string { return "+" + strings.TrimSuffix(chat, "@s.whatsapp.net") }
