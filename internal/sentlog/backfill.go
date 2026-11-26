package sentlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

// BackfillMarker is written to the data directory once Backfill has run, so it
// runs only once; a chat the owner purges later never gets these back.
const BackfillMarker = "sent-log-backfill.done"

// Backfill puts the list messages earlier releases sent but did not keep into
// their chats, once: each list message WhatsApp accepted that is in the sent log
// with its message ID but not in the archive is added as an outgoing message
// at the time it was sent, with no text and marked with the agent that sent
// it, in a chat that is being captured. A chat that is not captured is left
// alone, and a real copy WhatsApp delivers later replaces the one added here.
// It returns how many were added.
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
		if !e.List || e.Outcome != OutcomeSent || e.MessageID == "" || seen[key] {
			continue
		}
		seen[key] = true
		by := e.Actor
		if by == Owner {
			by = ""
		}
		missing = append(missing, storage.Message{GroupID: e.Chat, MessageID: e.MessageID, SenderID: account.JID, Timestamp: e.Time, Type: "unknown", FromMe: true, Source: storage.SourceSentLog, SentBy: by})
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
