// Package query defines archive query responses shared by the CLI and HTTP API.
package query

import (
	"encoding/json"
	"github.com/gehariharan/wasync/internal/storage"
	"time"
)

type Group struct {
	Kind                 string     `json:"chat_kind"`
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	MediaPolicy          string     `json:"media_policy"`
	Enabled              bool       `json:"sync_enabled"`
	MessageCount         int64      `json:"message_count"`
	LastMessage          *time.Time `json:"last_message_at"`
	HistoryState         string     `json:"history_state"`
	HistoryRequest       string     `json:"history_request_state"`
	HistoryBatchMessages int        `json:"history_batch_messages"`
	PotentialAccountGaps int64      `json:"potential_account_gaps"`
	// PotentialChatGaps counts the account's gaps that overlap the time capture
	// has been on for this chat, so it differs between chats enabled at
	// different times. potential_account_gaps is the same for every chat.
	PotentialChatGaps int64 `json:"potential_chat_gaps"`
}

func FormatGroup(g storage.Group) Group {
	return Group{storage.ChatKind(g.ID), g.ID, g.Name, g.MediaPolicy, g.Enabled, g.MessageCount, g.LastMessage, g.HistoryState, g.HistoryRequest, g.HistoryBatchMessages, g.AccountGapCount, g.GapCount}
}

type Message struct {
	ID         int64           `json:"id"`
	AccountID  int64           `json:"account_id"`
	GroupID    string          `json:"group_id"`
	ChatID     string          `json:"chat_id"`
	MessageID  string          `json:"message_id"`
	SenderID   string          `json:"sender_id"`
	SenderAlt  string          `json:"sender_alt,omitempty"`
	Timestamp  time.Time       `json:"timestamp"`
	Type       string          `json:"message_type"`
	Text       string          `json:"text_content"`
	FromMe     bool            `json:"is_from_me"`
	ReplyTo    string          `json:"reply_to_message_id,omitempty"`
	Media      json.RawMessage `json:"media_metadata,omitempty"`
	Source     string          `json:"source"`
	EditedAt   *time.Time      `json:"edited_at,omitempty"`
	DeletedAt  *time.Time      `json:"deleted_at,omitempty"`
	ExpiresAt  *time.Time      `json:"expires_at,omitempty"`
	ReceivedAt time.Time       `json:"received_at"`
}

func FormatMessage(account int64, m storage.Message) Message {
	var media json.RawMessage
	if json.Valid([]byte(m.Media)) {
		media = json.RawMessage(m.Media)
	}
	return Message{m.ID, account, m.GroupID, m.GroupID, m.MessageID, m.SenderID, m.SenderAlt, m.Timestamp, m.Type, m.Text, m.FromMe, m.ReplyTo, media, m.Source, m.EditedAt, m.DeletedAt, m.ExpiresAt, m.ReceivedAt}
}
