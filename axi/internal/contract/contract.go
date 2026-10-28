// Package contract defines the wasync v1 interface types that whatsapp-axi
// consumes: the version handshake, the response envelope, error codes,
// feature names and identifier helpers. It mirrors the contract in ADR-001;
// whatsapp-axi never defines WhatsApp semantics of its own.
package contract

import (
	"encoding/json"
	"fmt"
	"strings"
)

// APIVersion is the major v1 API version this build of whatsapp-axi speaks.
const APIVersion = 1

// VersionInfo is the data of GET /api/v1/version and `wasync query version --v1 --json`.
type VersionInfo struct {
	APIVersion           int      `json:"api_version"`
	APIMinor             int      `json:"api_minor"`
	WasyncVersion        string   `json:"wasync_version"`
	SchemaVersion        int      `json:"schema_version"`
	ArchiveSchemaVersion *int     `json:"archive_schema_version,omitempty"`
	Features             []string `json:"features"`
}

// HasFeature reports whether wasync advertises name.
func (v VersionInfo) HasFeature(name string) bool {
	for _, f := range v.Features {
		if f == name {
			return true
		}
	}
	return false
}

// API returns "major.minor".
func (v VersionInfo) API() string { return fmt.Sprintf("%d.%d", v.APIVersion, v.APIMinor) }

// Page is the optional pagination block of a success envelope.
type Page struct {
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// Envelope is the union of the v1 success and error envelopes. Both
// transports emit exactly this shape.
type Envelope struct {
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data,omitempty"`
	Page      *Page           `json:"page,omitempty"`
	ErrorCode string          `json:"error_code,omitempty"`
	Error     string          `json:"error,omitempty"`
	Help      string          `json:"help,omitempty"`
	State     json.RawMessage `json:"state,omitempty"`
	Details   json.RawMessage `json:"details,omitempty"`
}

// IsError reports whether the envelope is an error envelope.
func (e Envelope) IsError() bool { return e.ErrorCode != "" }

// Err converts an error envelope into an *Error.
func (e Envelope) Err() *Error {
	return &Error{RequestID: e.RequestID, Code: e.ErrorCode, Message: e.Error, Help: e.Help, State: e.State, Details: e.Details}
}

// Response is what a caller receives when it asks a transport for the whole
// success envelope rather than just its data (pass *Response as out).
type Response struct {
	RequestID string
	Data      json.RawMessage
	Page      *Page
}

// Error codes defined by the wasync v1 contract.
const (
	CodeUnauthorized       = "unauthorized"
	CodeForbidden          = "forbidden"
	CodeNotFound           = "not_found"
	CodeInvalidArgument    = "invalid_argument"
	CodeInvalidCursor      = "invalid_cursor"
	CodeCursorExpired      = "cursor_expired"
	CodeChatAmbiguous      = "chat_ambiguous"
	CodeOwnerOffline       = "owner_offline"
	CodeNoAccount          = "no_account"
	CodeArchiveUnavailable = "archive_unavailable"
	CodeSchemaIncompatible = "schema_incompatible"
	CodeRateLimited        = "rate_limited"
	CodeTargetUnavailable  = "target_unavailable"
	CodeNotEditable        = "not_editable"
	CodeEditWindowExpired  = "edit_window_expired"
	CodeSendFailed         = "send_failed"
	CodeInternal           = "internal"
)

// WasyncCodes lists every error code wasync may return.
var WasyncCodes = []string{
	CodeUnauthorized, CodeForbidden, CodeNotFound, CodeInvalidArgument, CodeInvalidCursor,
	CodeCursorExpired, CodeChatAmbiguous, CodeOwnerOffline, CodeNoAccount, CodeArchiveUnavailable,
	CodeSchemaIncompatible, CodeRateLimited, CodeTargetUnavailable, CodeNotEditable, CodeEditWindowExpired, CodeSendFailed, CodeInternal,
}

// Error codes produced by whatsapp-axi itself (never by wasync).
const (
	CodeWasyncTooOld     = "wasync_too_old"
	CodeAXITooOld        = "whatsapp_axi_too_old"
	CodeWasyncNotFound   = "wasync_not_found"
	CodeTransport        = "transport_error"
	CodeInvalidResponse  = "invalid_response"
	CodeCredentialAbsent = "credential_missing"
	CodeCredentialBad    = "credential_invalid"
	CodeUsage            = "usage"
)

// Feature names advertised in VersionInfo.Features.
const (
	FeatureChats         = "chats"
	FeatureChatsMatch    = "chats.match"
	FeatureMessages      = "messages"
	FeatureSearch        = "search"
	FeatureSearchContext = "search.context"
	FeatureThread        = "thread"
	FeatureMessageRef    = "message.ref"
	FeatureHealthDeep    = "health.deep"
	FeatureAuthScopes    = "auth.scopes"
	FeatureChanges       = "changes"
	FeatureCheckpoints   = "checkpoints"
	FeatureDiagnostics   = "diagnostics"
	FeatureWriteReply    = "write.reply"
	FeatureWriteReact    = "write.react"
	FeatureWriteEdit     = "write.edit"
	FeatureAccessRequest = "access.request"
)

// MessageRefPrefix starts every opaque message ref.
const MessageRefPrefix = "m1_"

// IsMessageRef reports whether s looks like a v1 message ref. Refs are
// opaque: whatsapp-axi never decodes them.
func IsMessageRef(s string) bool {
	return len(s) > len(MessageRefPrefix) && strings.HasPrefix(s, MessageRefPrefix)
}

// Error is a structured error from wasync or from whatsapp-axi itself.
type Error struct {
	RequestID string
	Code      string
	Message   string
	Help      string
	State     json.RawMessage
	Details   json.RawMessage // code-specific object, e.g. ChatAmbiguity
	Transport string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// StateString returns State when it is a JSON string, otherwise its raw JSON.
func (e *Error) StateString() string {
	if len(e.State) == 0 || string(e.State) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(e.State, &s) == nil {
		return s
	}
	return string(e.State)
}

// Errorf builds an *Error with a code and help line.
func Errorf(code, help, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Help: help}
}
