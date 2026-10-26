package query

import (
	"crypto/rand"
	"encoding/hex"
	"slices"

	"github.com/gehariharan/wasync/internal/buildinfo"
	"github.com/gehariharan/wasync/internal/storage"
)

// V1 envelopes are shared by /api/v1 and wasync query --v1 so agents parse
// both transports identically. Legacy /api/* and query output never use them.
type V1Page struct {
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

type V1Response struct {
	RequestID string  `json:"request_id"`
	Data      any     `json:"data"`
	Page      *V1Page `json:"page,omitempty"`
}

type V1Error struct {
	RequestID string `json:"request_id"`
	ErrorCode string `json:"error_code"`
	Error     string `json:"error"`
	Help      string `json:"help,omitempty"`
	State     string `json:"state,omitempty"`
	Details   any    `json:"details,omitempty"`
}

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
	CodeSendFailed         = "send_failed"
	CodeInternal           = "internal"
)

// Features lists v1 capabilities; feature files add theirs with RegisterFeature.
var Features = []string{"envelope.v1", "query.v1", "version"}

// RegisterFeature adds a capability name from an init function.
func RegisterFeature(name string) {
	if !slices.Contains(Features, name) {
		Features = append(Features, name)
		slices.Sort(Features)
	}
}

type VersionInfo struct {
	APIVersion           int      `json:"api_version"`
	APIMinor             int      `json:"api_minor"`
	WasyncVersion        string   `json:"wasync_version"`
	SchemaVersion        int      `json:"schema_version"`
	ArchiveSchemaVersion *int     `json:"archive_schema_version,omitempty"`
	Features             []string `json:"features"`
}

func Version() VersionInfo {
	return VersionInfo{APIVersion: 1, APIMinor: 0, WasyncVersion: buildinfo.Version, SchemaVersion: storage.SchemaVersion, Features: append([]string(nil), Features...)}
}

func NewRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
