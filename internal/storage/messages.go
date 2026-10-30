package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type Message struct {
	ID                                      int64
	GroupID, MessageID, SenderID, SenderAlt string
	Timestamp                               time.Time
	Type, Text                              string
	FromMe                                  bool
	ReplyTo, Media, Source                  string
	// Kind is normal, edit, or delete; mutations refer to MessageID of the original.
	Kind                           string
	MutationAt                     time.Time
	EditedAt, DeletedAt, ExpiresAt *time.Time
	ReceivedAt                     time.Time
}
type WriteResult struct{ Stored, Skipped int }
type Cursor struct {
	Timestamp int64
	ID        int64
}
type MessageQuery struct {
	Before   Cursor
	Search   string
	Limit    int
	Sender   string
	From, To *time.Time
	ID       int64
}
type MessagePage struct {
	Items []Message
	Next  Cursor
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}
func timePointer(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64).UTC()
	return &t
}

func (s *Store) IsEnabled(ctx context.Context, account int64, group string) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx, "SELECT sync_enabled FROM groups WHERE account_id=? AND group_id=?", account, group).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return enabled, err
}

// Apply rechecks consent in the write transaction. A disabled group cannot slip
// through because an event was normalized before a user toggled its preference.
func (s *Store) Apply(ctx context.Context, account int64, messages []Message) (WriteResult, error) {
	return s.apply(ctx, account, 0, messages)
}
func (s *Store) ApplyHistory(ctx context.Context, account, batch int64, messages []Message) (WriteResult, error) {
	return s.apply(ctx, account, batch, messages)
}
func (s *Store) apply(ctx context.Context, account, batch int64, messages []Message) (WriteResult, error) {
	var result WriteResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, m := range messages {
		var enabled bool
		err = tx.QueryRowContext(ctx, "SELECT sync_enabled FROM groups WHERE account_id=? AND group_id=?", account, m.GroupID).Scan(&enabled)
		if err == sql.ErrNoRows || err == nil && !enabled {
			result.Skipped++
			continue
		}
		if err != nil {
			return WriteResult{}, err
		}
		if m.MessageID == "" || m.ExpiresAt != nil && m.ExpiresAt.UnixMilli() <= now {
			result.Skipped++
			continue
		}
		var write sql.Result
		switch m.Kind {
		case "delete":
			write, err = tx.ExecContext(ctx, `INSERT INTO messages(account_id,group_id,message_id,source,received_at,deleted_at)
    VALUES(?,?,?,?,?,?) ON CONFLICT(account_id,group_id,message_id) DO UPDATE SET
    text_content='',media_metadata='',reply_to_message_id='',deleted_at=excluded.deleted_at
    WHERE messages.deleted_at IS NULL`, account, m.GroupID, m.MessageID, m.Source, now, m.MutationAt.UnixMilli())
		case "edit":
			write, err = tx.ExecContext(ctx, `INSERT INTO messages(account_id,group_id,message_id,source,received_at,text_content,message_type,media_metadata,edited_at,expires_at)
    VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,group_id,message_id) DO UPDATE SET
    text_content=excluded.text_content,message_type=excluded.message_type,media_metadata=excluded.media_metadata,
    edited_at=excluded.edited_at
    WHERE messages.deleted_at IS NULL AND (messages.edited_at IS NULL OR messages.edited_at<excluded.edited_at)`, account, m.GroupID, m.MessageID, m.Source, now, m.Text, m.Type, m.Media, m.MutationAt.UnixMilli(), nullableTime(m.ExpiresAt))
		default:
			write, err = tx.ExecContext(ctx, `INSERT INTO messages(account_id,group_id,message_id,sender_id,sender_alt,timestamp,message_type,text_content,is_from_me,reply_to_message_id,media_metadata,source,has_original,expires_at,received_at)
    VALUES(?,?,?,?,?,?,?,?,?,?,?,?,1,?,?) ON CONFLICT(account_id,group_id,message_id) DO UPDATE SET
    sender_id=excluded.sender_id,sender_alt=excluded.sender_alt,timestamp=excluded.timestamp,is_from_me=excluded.is_from_me,
    has_original=1,expires_at=excluded.expires_at,
    message_type=CASE WHEN messages.deleted_at IS NOT NULL THEN 'deleted' WHEN messages.edited_at IS NOT NULL THEN messages.message_type ELSE excluded.message_type END,
    text_content=CASE WHEN messages.deleted_at IS NOT NULL THEN '' WHEN messages.edited_at IS NOT NULL THEN messages.text_content ELSE excluded.text_content END,
    media_metadata=CASE WHEN messages.deleted_at IS NOT NULL THEN '' WHEN messages.edited_at IS NOT NULL THEN messages.media_metadata ELSE excluded.media_metadata END,
    reply_to_message_id=CASE WHEN messages.deleted_at IS NOT NULL THEN '' ELSE excluded.reply_to_message_id END
    WHERE messages.has_original=0`, account, m.GroupID, m.MessageID, m.SenderID, m.SenderAlt, m.Timestamp.UnixMilli(), m.Type, m.Text, m.FromMe, m.ReplyTo, m.Media, m.Source, nullableTime(m.ExpiresAt), now)
		}
		if err != nil {
			return WriteResult{}, fmt.Errorf("persist message: %w", err)
		}
		changed, e := write.RowsAffected()
		if e != nil {
			return WriteResult{}, e
		}
		if changed == 0 {
			result.Skipped++
		} else {
			result.Stored++
			op := OpMessageCreated
			switch m.Kind {
			case "delete":
				op = OpMessageRevoked
			case "edit":
				op = OpMessageUpdated
			}
			if err = journal(ctx, tx, account, m.GroupID, m.MessageID, op, now); err != nil {
				return WriteResult{}, err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sync_state(account_id,group_id,last_received_at,last_history_sync_at,history_state) VALUES(?,?,?,?,?)
   ON CONFLICT(account_id,group_id) DO UPDATE SET last_received_at=excluded.last_received_at,
   last_history_sync_at=COALESCE(excluded.last_history_sync_at,sync_state.last_history_sync_at),
   history_state=CASE WHEN excluded.last_history_sync_at IS NOT NULL THEN 'observed' ELSE sync_state.history_state END`, account, m.GroupID, now, func() any {
			if m.Source == "history" {
				return now
			}
			return nil
		}(), func() string {
			if m.Source == "history" {
				return "observed"
			}
			return "not_observed"
		}())
		if err != nil {
			return WriteResult{}, err
		}
		if m.Kind == "" || m.Kind == "normal" {
			if _, err = tx.ExecContext(ctx, "UPDATE groups SET last_message_at=MAX(COALESCE(last_message_at,0),?) WHERE account_id=? AND group_id=?", m.Timestamp.UnixMilli(), account, m.GroupID); err != nil {
				return WriteResult{}, err
			}
		}
	}
	if batch != 0 {
		if _, err = tx.ExecContext(ctx, "UPDATE history_batches SET imported=imported+?,skipped=skipped+? WHERE id=? AND account_id=?", result.Stored, result.Skipped, batch, account); err != nil {
			return WriteResult{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return WriteResult{}, err
	}
	return result, nil
}

// messageFilter is shared by reads and totals; pagination is applied only to reads.
func messageFilter(account int64, group string, q MessageQuery) (string, []any) {
	query := ` FROM archived_messages WHERE account_id=?`
	args := []any{account}
	if group != "" {
		query += " AND group_id=?"
		args = append(args, group)
	}
	if q.ID != 0 {
		query += " AND id=?"
		args = append(args, q.ID)
	}
	if q.Sender != "" {
		query += " AND sender_id=?"
		args = append(args, q.Sender)
	}
	if q.From != nil {
		query += " AND timestamp>=?"
		args = append(args, q.From.UnixMilli())
	}
	if q.To != nil {
		query += " AND timestamp<?"
		args = append(args, q.To.UnixMilli())
	}
	if q.Before.ID != 0 {
		query += " AND (timestamp<? OR (timestamp=? AND id<?))"
		args = append(args, q.Before.Timestamp, q.Before.Timestamp, q.Before.ID)
	}
	if strings.TrimSpace(q.Search) != "" {
		query += " AND id IN(SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)"
		args = append(args, `"`+strings.ReplaceAll(q.Search, `"`, `""`)+`"`)
	}
	return query, args
}

// CountMessages counts the full filtered result, independent of cursor and limit.
// A nonempty allowlist restricts the count before it leaves the archive owner.
func (s *Store) CountMessages(ctx context.Context, account int64, group string, q MessageQuery, allowedChats []string) (int64, error) {
	q.Before = Cursor{}
	filter, args := messageFilter(account, group, q)
	if len(allowedChats) > 0 {
		filter += " AND group_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(allowedChats)), ",") + ")"
		for _, chat := range allowedChats {
			args = append(args, chat)
		}
	}
	var total int64
	err := s.db.QueryRowContext(ctx, "SELECT count(*)"+filter, args...).Scan(&total)
	return total, err
}

func (s *Store) Messages(ctx context.Context, account int64, group string, q MessageQuery) (MessagePage, error) {
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	filter, args := messageFilter(account, group, q)
	query := "SELECT id,group_id,message_id,sender_id,sender_alt,timestamp,message_type,text_content,is_from_me,reply_to_message_id,media_metadata,source,edited_at,deleted_at,expires_at,received_at" + filter
	query += " ORDER BY timestamp DESC,id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return MessagePage{}, err
	}
	defer rows.Close()
	page := MessagePage{Items: make([]Message, 0)}
	for rows.Next() {
		var m Message
		var ts, received int64
		var edited, deleted, expiry sql.NullInt64
		if err = rows.Scan(&m.ID, &m.GroupID, &m.MessageID, &m.SenderID, &m.SenderAlt, &ts, &m.Type, &m.Text, &m.FromMe, &m.ReplyTo, &m.Media, &m.Source, &edited, &deleted, &expiry, &received); err != nil {
			return MessagePage{}, err
		}
		m.Timestamp = time.UnixMilli(ts).UTC()
		m.ReceivedAt = time.UnixMilli(received).UTC()
		m.EditedAt = timePointer(edited)
		m.DeletedAt = timePointer(deleted)
		m.ExpiresAt = timePointer(expiry)
		page.Items = append(page.Items, m)
	}
	if err = rows.Err(); err != nil {
		return MessagePage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = Cursor{last.Timestamp.UnixMilli(), last.ID}
	}
	return page, nil
}
func (s *Store) GroupSummaries(ctx context.Context, account int64) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.group_id,g.name,g.sync_enabled,COALESCE((SELECT mode FROM media_preferences p WHERE p.account_id=g.account_id AND p.group_id=g.group_id),'inherit'),g.last_message_at,
 (SELECT count(*) FROM archived_messages m WHERE m.account_id=g.account_id AND m.group_id=g.group_id),
 COALESCE(s.history_state,'not_observed'),
 (SELECT count(*) FROM sync_gaps gp WHERE gp.account_id=g.account_id AND g.sync_enabled=1 AND g.enabled_at IS NOT NULL AND (gp.ended_at IS NULL OR gp.ended_at>=g.enabled_at)),
 (SELECT count(*) FROM sync_gaps WHERE account_id=g.account_id),COALESCE(h.state,''),COALESCE(h.last_error,''),COALESCE(h.batch_messages,0)
 FROM groups g LEFT JOIN sync_state s ON s.account_id=g.account_id AND s.group_id=g.group_id LEFT JOIN history_jobs h ON h.account_id=g.account_id AND h.group_id=g.group_id
 WHERE g.account_id=? ORDER BY g.sync_enabled DESC,g.name COLLATE NOCASE,g.group_id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make([]Group, 0)
	for rows.Next() {
		var g Group
		var last sql.NullInt64
		if err = rows.Scan(&g.ID, &g.Name, &g.Enabled, &g.MediaPolicy, &last, &g.MessageCount, &g.HistoryState, &g.GapCount, &g.AccountGapCount, &g.HistoryRequest, &g.HistoryRequestError, &g.HistoryBatchMessages); err != nil {
			return nil, err
		}
		g.LastMessage = timePointer(last)
		groups = append(groups, g)
	}
	return groups, rows.Err()
}
func (s *Store) PurgeGroup(ctx context.Context, account int64, group string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	// Consumers delete their replicas from one message.purged entry per removed
	// row (refs stay resolvable), followed by capture.disabled when capture was on.
	if _, err = tx.ExecContext(ctx, `INSERT INTO change_journal(account_id,chat_id,message_id,op,op_version,committed_at)
 SELECT account_id,group_id,message_id,?,1,? FROM messages WHERE account_id=? AND group_id=? ORDER BY id`, OpMessagePurged, now, account, group); err != nil {
		return fmt.Errorf("append change journal: %w", err)
	}
	var wasEnabled bool
	if err = tx.QueryRowContext(ctx, "SELECT sync_enabled FROM groups WHERE account_id=? AND group_id=?", account, group).Scan(&wasEnabled); err != nil && err != sql.ErrNoRows {
		return err
	}
	if wasEnabled {
		if err = journal(ctx, tx, account, group, "", OpCaptureDisabled, now); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE groups SET sync_enabled=0,last_message_at=NULL WHERE account_id=? AND group_id=?", account, group); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM messages WHERE account_id=? AND group_id=?", account, group); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM history_anchors WHERE account_id=? AND group_id=?", account, group); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM history_jobs WHERE account_id=? AND group_id=?", account, group); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM sync_state WHERE account_id=? AND group_id=?", account, group); err != nil {
		return err
	}
	return tx.Commit()
}

// Expire removes bodies and preserves tombstones so a history replay cannot
// resurrect already-expired content. Pending mutations are retained for 30 days.
// Each expired message gets a message.expired journal entry in the same
// transaction, and journal entries older than JournalRetention are pruned.
func (s *Store) Expire(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO change_journal(account_id,chat_id,message_id,op,op_version,committed_at)
 SELECT account_id,group_id,message_id,?,1,? FROM messages WHERE expires_at<=? AND deleted_at IS NULL ORDER BY id`, OpMessageExpired, now, now); err != nil {
		return fmt.Errorf("append change journal: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE messages SET text_content='',media_metadata='',reply_to_message_id='',deleted_at=COALESCE(deleted_at,?) WHERE expires_at<=? AND deleted_at IS NULL`, now, now); err != nil {
		return err
	}
	// Orphaned pending edits were never visible (archived_messages requires an
	// original), so removing them needs no journal entry.
	if _, err = tx.ExecContext(ctx, "DELETE FROM messages WHERE has_original=0 AND deleted_at IS NULL AND received_at<?", time.Now().Add(-30*24*time.Hour).UnixMilli()); err != nil {
		return err
	}
	if err = pruneJournal(ctx, tx, time.Now().Add(-JournalRetention).UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
