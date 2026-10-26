package contract

import "encoding/json"

// The types below are the parts of wasync's v1 read data that whatsapp-axi
// renders as text. They mirror wasync's internal/query v1 shapes; unknown
// fields are ignored, and --json output passes the original data through
// unchanged. Times stay the RFC 3339 strings wasync sent.

// Chat is one item of GET /api/v1/chats (`wasync query chats --v1`).
type Chat struct {
	Kind          string `json:"chat_kind"`
	ID            string `json:"id"`
	Name          string `json:"name"` // set by WhatsApp users: untrusted
	SyncEnabled   bool   `json:"sync_enabled"`
	MessageCount  int64  `json:"message_count"`
	LastMessageAt string `json:"last_message_at"` // "" when null
}

// ChatList is the data of chat lists.
type ChatList struct {
	AccountID int64  `json:"account_id"`
	Items     []Chat `json:"items"`
}

// Message is the v1 message shape. Text and Media are untrusted content.
type Message struct {
	Ref        string          `json:"ref"`
	ChatID     string          `json:"chat_id"`
	ChatKind   string          `json:"chat_kind"`
	SenderID   string          `json:"sender_id"`
	Timestamp  string          `json:"timestamp"`
	Type       string          `json:"message_type"`
	Text       string          `json:"text_content"`
	FromMe     bool            `json:"is_from_me"`
	ReplyTo    string          `json:"reply_to_message_id"`
	Media      json.RawMessage `json:"media_metadata"`
	Source     string          `json:"source"`
	EditedAt   string          `json:"edited_at"`
	DeletedAt  string          `json:"deleted_at"`
	ExpiresAt  string          `json:"expires_at"`
	ReceivedAt string          `json:"received_at"`
}

// WriteResult is the data of a write: WhatsApp's acknowledgement of a reply,
// reaction or edit. A server acknowledgement is not proof of delivery.
type WriteResult struct {
	ChatID          string `json:"chat_id"`
	MessageID       string `json:"message_id"`
	TargetMessageID string `json:"target_message_id"`
	Status          string `json:"status"`
	Timestamp       string `json:"timestamp"`
	ArchiveWarning  string `json:"archive_warning"`
}

// SearchHit is a search item; Before and After are present only when
// context was requested.
type SearchHit struct {
	Message
	Before []Message `json:"before"`
	After  []Message `json:"after"`
}

// MessageList is the data of chat message pages.
type MessageList struct {
	Total     *int64    `json:"total,omitempty"`
	AccountID int64     `json:"account_id"`
	Items     []Message `json:"items"`
}

// SearchList is the data of searches.
type SearchList struct {
	Total     *int64      `json:"total,omitempty"`
	AccountID int64       `json:"account_id"`
	Items     []SearchHit `json:"items"`
}

// Thread is the data of GET /api/v1/messages/{ref}/thread. Before and After
// are oldest first.
type Thread struct {
	Anchor Message   `json:"anchor"`
	Before []Message `json:"before"`
	After  []Message `json:"after"`
}

// ChatCandidate is one chat named by a chat_ambiguous error.
type ChatCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"` // untrusted
	Kind string `json:"chat_kind"`
}

// ChatAmbiguity is the details object of a chat_ambiguous error.
type ChatAmbiguity struct {
	Match      string          `json:"match"`
	Candidates []ChatCandidate `json:"candidates"`
}
