package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Direct-chat replies, for the approved recipient lists: whether a person
// answered after a message, and who asked to be left alone. A person may be
// keyed under their phone-number address or, by newer WhatsApp, under a hidden
// one with the phone number as the alternate sender (possibly with a device
// suffix), so both are matched.
// Group chats never count.

const directIncoming = ` FROM archived_messages WHERE account_id=? AND is_from_me=0 AND group_id NOT LIKE '%@g.us'`

// LastIncoming returns when the person at chat (a phone-number address) last
// wrote in their direct chat, or the zero time when they never did.
func (s *Store) LastIncoming(ctx context.Context, account int64, chat string) (time.Time, error) {
	var last sql.NullInt64
	user := strings.TrimSuffix(chat, "@s.whatsapp.net")
	err := s.db.QueryRowContext(ctx, "SELECT max(timestamp)"+directIncoming+" AND (group_id=? OR sender_alt=? OR sender_alt LIKE ?)", account, chat, chat, user+":%@s.whatsapp.net").Scan(&last)
	if err != nil || !last.Valid {
		return time.Time{}, err
	}
	return time.UnixMilli(last.Int64).UTC(), nil
}

// StopSenders returns the phone-number addresses of everyone who ever sent the
// whole message STOP (any case, surrounding spaces ignored) in a direct chat.
func (s *Store) StopSenders(ctx context.Context, account int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT group_id,sender_alt,text_content"+directIncoming+" AND length(text_content) BETWEEN 4 AND 64 AND lower(text_content) LIKE '%stop%'", account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var chat, alt, text string
		if err = rows.Scan(&chat, &alt, &text); err != nil {
			return nil, err
		}
		if !strings.EqualFold(strings.TrimSpace(text), "stop") {
			continue
		}
		for _, id := range []string{chat, alt} {
			if user, ok := strings.CutSuffix(id, "@s.whatsapp.net"); ok {
				user, _, _ = strings.Cut(user, ":") // a device suffix
				out[user+"@s.whatsapp.net"] = true
			}
		}
	}
	return out, rows.Err()
}
