package sentlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

// Source marks an archived message that was filled in from the sent log
// rather than captured: wasync sent it before it kept what it sends in the
// chat, so its text is not known.
const Source = "sent_log"

// BackfillMarker is written to the data directory once Backfill has run, so it
// runs only once; a chat the owner purges later never gets these back.
const BackfillMarker = "sent-log-backfill.done"

// Backfill puts the messages earlier releases sent but did not keep into
// their chats, once: each message WhatsApp accepted that is in the sent log
// with its message ID but not in the archive is added as an outgoing message
// at the time it was sent, with no text, in a chat that is being captured. A
// chat that is not captured is left alone. It returns how many were added.
func Backfill(ctx context.Context, dir string, store *storage.Store) (int, error) {
	marker := filepath.Join(dir, BackfillMarker)
	if _, err := os.Lstat(marker); err == nil || !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	account, err := store.Account(ctx)
	if err != nil || account == nil {
		return 0, err // nothing to fill in until a phone is linked
	}
	entries, err := New(dir).Recent(int(^uint(0) >> 1))
	if err != nil {
		return 0, err
	}
	var missing []storage.Message
	seen := map[[2]string]bool{}
	for _, e := range entries {
		key := [2]string{e.Chat, e.MessageID}
		if e.Outcome != OutcomeSent || e.MessageID == "" || seen[key] {
			continue
		}
		seen[key] = true
		missing = append(missing, storage.Message{GroupID: e.Chat, MessageID: e.MessageID, SenderID: account.JID, Timestamp: e.Time, Type: "unknown", FromMe: true, Source: Source})
	}
	// Apply keeps whatever the archive already has for a message ID and skips
	// chats that are not captured.
	result, err := store.Apply(ctx, account.ID, missing)
	if err != nil {
		return 0, err
	}
	if err = localfs.EnsureFile(marker); err != nil {
		return result.Stored, err
	}
	return result.Stored, nil
}

// Agents maps the message IDs an agent sent in chat to that agent's name, so
// a chat can show who sent each of them. Messages the owner sent are left out.
func (l *Log) Agents(chat string) (map[string]string, error) {
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]string{}
	for _, path := range []string{l.path() + ".1", l.path()} {
		entries, err := readFile(path)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Chat == chat && e.MessageID != "" && e.Actor != Owner {
				out[e.MessageID] = e.Actor
			}
		}
	}
	return out, nil
}
