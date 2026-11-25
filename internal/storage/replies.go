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
// and the hidden-address chats of those same people, by chat, so the chat
// index serves them.

const directIncoming = ` FROM archived_messages WHERE account_id=? AND is_from_me=0 AND group_id IN (SELECT value FROM json_each(?))`

// phoneChat returns the phone-number chat address id names, without any
// device suffix, or "" when id is not a phone-number address.
func phoneChat(id string) string {
	user, ok := strings.CutSuffix(id, "@s.whatsapp.net")
	if !ok {
		return ""
	}
	user, _, _ = strings.Cut(user, ":")
	return user + "@s.whatsapp.net"
}

// personChats maps chats (phone-number addresses) and every hidden-address
// chat whose person writes as one of them to that phone-number address. A
// hidden-address chat holds one person, so its latest incoming alternate
// sender names them; that costs one index lookup per hidden-address chat, and
// no other hidden-address chat is read.
func (s *Store) personChats(ctx context.Context, account int64, chats []string) (map[string]string, error) {
	out := make(map[string]string, len(chats))
	for _, c := range chats {
		out[c] = c
	}
	rows, err := s.db.QueryContext(ctx, `SELECT g.group_id,(SELECT m.sender_alt FROM messages m WHERE m.account_id=g.account_id AND m.group_id=g.group_id AND m.is_from_me=0 AND m.sender_alt<>'' ORDER BY m.timestamp DESC LIMIT 1)
 FROM groups g WHERE g.account_id=? AND g.group_id LIKE '%@lid'`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hidden [][2]string
	for rows.Next() {
		var chat string
		var alt sql.NullString
		if err = rows.Scan(&chat, &alt); err != nil {
			return nil, err
		}
		hidden = append(hidden, [2]string{chat, phoneChat(alt.String)})
	}
	for _, h := range hidden {
		if _, listed := out[h[1]]; listed && h[1] != "" {
			out[h[0]] = h[1]
		}
	}
	return out, rows.Err()
}

// inChats returns personChats and the JSON array of its chats for the queries.
func (s *Store) inChats(ctx context.Context, account int64, chats []string) (map[string]string, string, error) {
	person, err := s.personChats(ctx, account, chats)
	if err != nil {
		return nil, "", err
	}
	all := make([]string, 0, len(person))
	for c := range person {
		all = append(all, c)
	}
	b, err := json.Marshal(all)
	return person, string(b), err
}

// LastIncoming returns when the person at chat (a phone-number address) last
// wrote in their direct chat, or the zero time when they never did.
func (s *Store) LastIncoming(ctx context.Context, account int64, chat string) (time.Time, error) {
	_, in, err := s.inChats(ctx, account, []string{chat})
	if err != nil {
		return time.Time{}, err
	}
	var last sql.NullInt64
	if err = s.db.QueryRowContext(ctx, "SELECT max(timestamp)"+directIncoming, account, in).Scan(&last); err != nil || !last.Valid {
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
	person, in, err := s.inChats(ctx, account, chats)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT group_id,text_content"+directIncoming+" AND length(text_content) BETWEEN 4 AND 64 AND lower(text_content) LIKE '%stop%'", account, in)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var chat, text string
		if err = rows.Scan(&chat, &text); err != nil {
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(text), "stop") {
			out[person[chat]] = true
		}
	}
	return out, rows.Err()
}
