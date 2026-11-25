package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Direct-chat replies, for the approved recipient lists: whether a person
// answered after a message, and who asked to be left alone. A person may be
// keyed under their phone-number address or, by newer WhatsApp, under a hidden
// one with the phone number as the alternate sender (possibly with a device
// suffix), so both are matched.
// Group chats never count. Both lookups read only the given phone-number chats
// and the hidden-address chats, so the chat index serves them.

const (
	directIncoming = ` FROM archived_messages WHERE account_id=? AND is_from_me=0`
	hiddenChats    = `SELECT group_id FROM groups WHERE account_id=? AND group_id LIKE '%@lid'`
)

// LastIncoming returns when the person at chat (a phone-number address) last
// wrote in their direct chat, or the zero time when they never did.
func (s *Store) LastIncoming(ctx context.Context, account int64, chat string) (time.Time, error) {
	var last sql.NullInt64
	user := strings.TrimSuffix(chat, "@s.whatsapp.net")
	err := s.db.QueryRowContext(ctx, "SELECT max(timestamp)"+directIncoming+" AND (group_id=? OR group_id IN ("+hiddenChats+") AND (sender_alt=? OR sender_alt LIKE ?))", account, chat, account, chat, user+":%@s.whatsapp.net").Scan(&last)
	if err != nil || !last.Valid {
		return time.Time{}, err
	}
	return time.UnixMilli(last.Int64).UTC(), nil
}

// StopSenders returns which of chats (phone-number addresses) ever sent the
// whole message STOP (any case, surrounding spaces ignored) in a direct chat.
func (s *Store) StopSenders(ctx context.Context, account int64, chats []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(chats) == 0 {
		return out, nil
	}
	wanted := make(map[string]bool, len(chats))
	for _, c := range chats {
		wanted[c] = true
	}
	list, err := json.Marshal(chats)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT group_id,sender_alt,text_content"+directIncoming+" AND (group_id IN (SELECT value FROM json_each(?)) OR group_id IN ("+hiddenChats+")) AND length(text_content) BETWEEN 4 AND 64 AND lower(text_content) LIKE '%stop%'", account, string(list), account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
				if id = user + "@s.whatsapp.net"; wanted[id] {
					out[id] = true
				}
			}
		}
	}
	return out, rows.Err()
}
