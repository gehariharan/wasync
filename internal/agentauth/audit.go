package agentauth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/localfs"
)

const (
	// AuditFileName is the append-only private JSONL audit log in the data dir.
	AuditFileName     = "agent-audit.log"
	auditLockFileName = "agent-audit.lock"
	// AuditMaxBytes triggers rotation; AuditKeep old files are kept (.1, .2).
	AuditMaxBytes  = 10 << 20
	AuditKeep      = 2
	OutcomeAllowed = "allowed"
	OutcomeDenied  = "denied"
	maxFieldLen    = 256
)

// AuditEntry is one audit record. It has no field for message bodies,
// credentials or hashes, and every string is truncated to 256 bytes.
//
// Policy: every v1 mutation attempt (allowed or denied) and every denial is
// audited. Successful reads are not audited, to keep the log small and avoid
// recording browsing behaviour; denied reads are. The exception is a read
// that only an approved recipient list allows (event read_list), which is
// audited either way so the person can see what the list let the agent read.
type AuditEntry struct {
	Time       time.Time `json:"time"`
	Event      string    `json:"event,omitempty"` // "request" (default), "grant" or "revoke"
	ClientID   string    `json:"client_id,omitempty"`
	ClientName string    `json:"client_name,omitempty"`
	Scope      string    `json:"scope,omitempty"`
	Chat       string    `json:"chat,omitempty"`
	Ref        string    `json:"ref,omitempty"`
	Outcome    string    `json:"outcome"`
	Code       string    `json:"error_code,omitempty"`
	RequestID  string    `json:"request_id,omitempty"`
}

// AuditLog appends entries to agent-audit.log. Writers in different
// processes (the owner and the human CLI) serialize through a lock file.
type AuditLog struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
}

// NewAuditLog returns the audit log for dir.
func NewAuditLog(dir string) *AuditLog { return &AuditLog{dir: dir, maxBytes: AuditMaxBytes} }

// Append writes one JSON line, fsyncs it, and rotates at AuditMaxBytes.
// Time defaults to now (always stored in UTC, RFC 3339); Event defaults to
// "request"; RequestID is generated when empty.
func (l *AuditLog) Append(e AuditEntry) (result error) {
	if e.Outcome != OutcomeAllowed && e.Outcome != OutcomeDenied {
		return fmt.Errorf("audit outcome must be allowed or denied")
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC().Truncate(time.Second)
	if e.Event == "" {
		e.Event = "request"
	}
	if e.RequestID == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		e.RequestID = hex.EncodeToString(b)
	}
	for _, field := range []*string{&e.Event, &e.ClientID, &e.ClientName, &e.Scope, &e.Chat, &e.Ref, &e.Code, &e.RequestID} {
		if len(*field) > maxFieldLen {
			*field = (*field)[:maxFieldLen]
		}
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	lock, err := acquire(filepath.Join(l.dir, auditLockFileName))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	path := filepath.Join(l.dir, AuditFileName)
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() && info.Size()+int64(len(line)) > l.maxBytes {
		if err = rotate(path); err != nil {
			return err
		}
	}
	// localfs.OpenFile creates the file privately and refuses symlinks; it
	// does not append, so seek to the end under the cross-process lock.
	f, err := localfs.OpenFile(path)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, f.Close()) }()
	if _, err = f.Seek(0, 2); err != nil {
		return err
	}
	if _, err = f.Write(line); err != nil {
		return err
	}
	return f.Sync()
}

func rotate(path string) error {
	for i := AuditKeep; i >= 1; i-- {
		from := path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", path, i-1)
		}
		to := fmt.Sprintf("%s.%d", path, i)
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
