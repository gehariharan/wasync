package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The original SQL group_id remains a chat JID for archive/query compatibility.
func ChatKind(id string) string {
	if strings.HasSuffix(id, "@g.us") {
		return "group"
	}
	return "direct"
}

// SaveDiscoveredChat records only chat metadata. Capture is disabled by default.
func (s *Store) SaveDiscoveredChat(ctx context.Context, account int64, chat Group) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO groups(account_id,group_id,name,created_at,updated_at) VALUES(?,?,?,?,?)
 ON CONFLICT(account_id,group_id) DO UPDATE SET name=CASE WHEN excluded.name<>excluded.group_id THEN excluded.name ELSE groups.name END,updated_at=excluded.updated_at WHERE excluded.name<>excluded.group_id AND excluded.name<>groups.name`, account, chat.ID, chat.Name, time.Now().UnixMilli(), time.Now().UnixMilli())
	return err
}

// SaveDirectNames sets the display name of already-known direct chats. It never
// creates a chat and never touches groups, so syncing contacts cannot add chats
// to the list. Unchanged names are left alone.
func (s *Store) SaveDirectNames(ctx context.Context, account int64, names map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for id, name := range names {
		if name == "" || ChatKind(id) != "direct" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE groups SET name=?,updated_at=? WHERE account_id=? AND group_id=? AND name<>?`, name, now, account, id, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetMediaDownload changes only metadata for a still-enabled, non-deleted original.
func (s *Store) SetMediaDownload(ctx context.Context, account int64, chat, message, state, path string) (bool, error) {
	return s.setMediaDownload(ctx, account, chat, message, state, path, false, false)
}
func (s *Store) CompleteMediaDownload(ctx context.Context, account int64, chat, message, path string, automatic, global bool) (bool, error) {
	return s.setMediaDownload(ctx, account, chat, message, "downloaded", path, automatic, global)
}
func (s *Store) setMediaDownload(ctx context.Context, account int64, chat, message, state, path string, automatic, global bool) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE messages SET media_metadata=json_set(CASE WHEN json_valid(media_metadata) THEN media_metadata ELSE '{}' END,'$.download_state',?,'$.local_path',?,'$.relative_path',?)
 WHERE account_id=? AND group_id=? AND message_id=? AND has_original=1 AND deleted_at IS NULL AND edited_at IS NULL AND (expires_at IS NULL OR expires_at>?)
 AND EXISTS(SELECT 1 FROM groups WHERE account_id=? AND group_id=? AND sync_enabled=1)
 AND (NOT ? OR EXISTS(SELECT 1 FROM groups g LEFT JOIN media_preferences p ON p.account_id=g.account_id AND p.group_id=g.group_id WHERE g.account_id=? AND g.group_id=? AND (p.mode='auto' OR (COALESCE(p.mode,'inherit')='inherit' AND ?))))`, state, path, mediaKey(path), account, chat, message, time.Now().UnixMilli(), account, chat, automatic, account, chat, global)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

// ClearMessage clears local content and keeps a tombstone against history replay.
// It deliberately sends no WhatsApp operation and works for disabled chats too.
// It appends message.purged to the change journal in the same transaction.
func (s *Store) ClearMessage(ctx context.Context, account int64, chat, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE messages SET text_content='',media_metadata='',reply_to_message_id='',message_type='deleted',deleted_at=COALESCE(deleted_at,?) WHERE account_id=? AND group_id=? AND message_id=?`, time.Now().UnixMilli(), account, chat, message)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("message is not in this account's local archive")
	}
	if err = journal(ctx, tx, account, chat, message, OpMessagePurged, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MediaEligible(ctx context.Context, account int64, chat, message string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m JOIN groups g ON g.account_id=m.account_id AND g.group_id=m.group_id WHERE m.account_id=? AND m.group_id=? AND m.message_id=? AND m.has_original=1 AND m.deleted_at IS NULL AND m.edited_at IS NULL AND g.sync_enabled=1 AND (m.expires_at IS NULL OR m.expires_at>?)`, account, chat, message, time.Now().UnixMilli()).Scan(&count)
	return count > 0, err
}

// mediaKey identifies content within media/ independently of a data-directory move.
// Managed filenames contain account/chat/message/content hashes, not user filenames.
func mediaKey(path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(strings.ReplaceAll(path, `\`, "/"), "/")
	if len(parts) < 3 {
		return path
	}
	return strings.Join(parts[len(parts)-3:], "/")
}
func (s *Store) MediaReferenced(ctx context.Context, path string) (bool, error) {
	var count int
	key := mediaKey(path)
	// The suffix fallback preserves files recorded by versions with absolute paths only.
	// ManagedPath has already validated the account/hash/filename identity in the caller.
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
 WHERE deleted_at IS NULL AND (
 json_extract(CASE WHEN json_valid(media_metadata) THEN media_metadata ELSE '{}' END,'$.relative_path')=?
 OR json_extract(CASE WHEN json_valid(media_metadata) THEN media_metadata ELSE '{}' END,'$.local_path')=?
 OR substr(replace(json_extract(CASE WHEN json_valid(media_metadata) THEN media_metadata ELSE '{}' END,'$.local_path'),'\','/'),-?)=?)`, key, path, len(key)+1, "/"+key).Scan(&count)
	return count > 0, err
}

func (s *Store) MessageByWhatsAppID(ctx context.Context, account int64, chat, message string) (Message, error) {
	var id int64
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM messages WHERE account_id=? AND group_id=? AND message_id=? AND has_original=1", account, chat, message).Scan(&id); err != nil {
		return Message{}, err
	}
	page, err := s.Messages(ctx, account, chat, MessageQuery{ID: id, Limit: 1})
	if err != nil {
		return Message{}, err
	}
	if len(page.Items) != 1 {
		return Message{}, fmt.Errorf("message is no longer available")
	}
	return page.Items[0], nil
}
