package storage

import (
	"context"
	"database/sql"
	"time"
)

// PickerChat is one chat as the chat picker lists it: what it is called,
// whether it is synced and when it was last active. It carries no content.
type PickerChat struct {
	ID      string
	Name    string
	Enabled bool
	// LastActive is the newest of the last archived message and the last
	// activity WhatsApp reported for the chat; nil when neither is known.
	LastActive *time.Time
}

// PickerChats lists every known chat of the account for the chat picker, in no
// particular order. Unlike GroupSummaries it counts nothing per chat, so it
// stays cheap for thousands of chats.
func (s *Store) PickerChats(ctx context.Context, account int64) ([]PickerChat, error) {
	return s.pickerChats(ctx, pickerQuery+" WHERE g.account_id=?", account)
}

// PickerChat returns one known chat as the picker lists it; ok is false when
// the account has no such chat.
func (s *Store) PickerChat(ctx context.Context, account int64, chat string) (c PickerChat, ok bool, err error) {
	chats, err := s.pickerChats(ctx, pickerQuery+" WHERE g.account_id=? AND g.group_id=?", account, chat)
	if err != nil || len(chats) == 0 {
		return PickerChat{}, false, err
	}
	return chats[0], true, nil
}

const pickerQuery = `SELECT g.group_id,g.name,g.sync_enabled,
 NULLIF(MAX(COALESCE(g.last_message_at,0),COALESCE(a.last_active_at,0)),0)
 FROM groups g LEFT JOIN chat_activity a ON a.account_id=g.account_id AND a.chat_id=g.group_id`

func (s *Store) pickerChats(ctx context.Context, query string, args ...any) ([]PickerChat, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chats := make([]PickerChat, 0)
	for rows.Next() {
		var c PickerChat
		var last sql.NullInt64
		if err = rows.Scan(&c.ID, &c.Name, &c.Enabled, &last); err != nil {
			return nil, err
		}
		c.LastActive = timePointer(last)
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// SaveChatActivity records when chats were last active, as WhatsApp reports
// it. A time only ever moves forward, and zero times are ignored. It works for
// chats that are not synced and even not yet discovered, so a chat list that
// arrives at link time can be ordered before its chats are known by name.
func (s *Store) SaveChatActivity(ctx context.Context, account int64, active map[string]time.Time) error {
	if len(active) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for chat, at := range active {
		if chat == "" || at.Unix() <= 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO chat_activity(account_id,chat_id,last_active_at) VALUES(?,?,?)
 ON CONFLICT(account_id,chat_id) DO UPDATE SET last_active_at=MAX(last_active_at,excluded.last_active_at)`, account, chat, at.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
