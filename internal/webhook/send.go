package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/internal/msgref"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
)

// Request headers. The signature is "v1=" and the lowercase hex HMAC-SHA256,
// keyed with the endpoint secret, of the timestamp, a '.', and the exact
// request body. Receivers should reject timestamps more than a few minutes
// from their clock (replay) and dedupe on the event ID (at-least-once).
const (
	HeaderID        = "Wasync-Webhook-Id"
	HeaderTimestamp = "Wasync-Webhook-Timestamp"
	HeaderSignature = "Wasync-Webhook-Signature"
	// SignatureTolerance is the clock skew Verify accepts.
	SignatureTolerance = 5 * time.Minute
	signatureVersion   = "v1="
	maxDrain           = 4 << 10
)

// Event is the JSON body of a webhook request. It never carries message
// content: Ref is the message ref to fetch from GET /api/v1/messages/{ref}
// under read.messages, and the message may no longer exist when it is read.
type Event struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // "change" or "ping"
	Endpoint    string    `json:"endpoint"`
	Epoch       string    `json:"epoch,omitempty"`
	Sequence    int64     `json:"sequence,omitempty"`
	Cursor      string    `json:"cursor,omitempty"`
	Op          string    `json:"op,omitempty"`
	OpVersion   int       `json:"op_version,omitempty"`
	ChatID      string    `json:"chat_id,omitempty"`
	MessageID   string    `json:"message_id,omitempty"`
	Ref         string    `json:"ref,omitempty"`
	CommittedAt time.Time `json:"committed_at,omitzero"`
	SentAt      time.Time `json:"sent_at,omitzero"`
}

// Delivery outcome codes, recorded in the audit log and dead-letter list.
const (
	CodeDelivered     = "delivered"
	CodeBlocked       = "blocked_address"
	CodeRedirect      = "redirect_not_followed"
	CodeRejected      = "rejected"
	CodeServerError   = "server_error"
	CodeRateLimited   = "rate_limited"
	CodeTimeout       = "timeout"
	CodeNetworkError  = "network_error"
	CodeInvalidTarget = "invalid_url"
)

// Result is the outcome of one delivery attempt.
type Result struct {
	Code   string
	Status int  // HTTP status, 0 when no response arrived
	Retry  bool // a later attempt can succeed
}

// OK reports a 2xx response.
func (r Result) OK() bool { return r.Code == CodeDelivered }

// Sender posts signed events through the SSRF-guarded client.
type Sender struct {
	Client *http.Client
	Now    func() time.Time
}

// NewSender returns a Sender over client (NewClient(ClientOptions{}) if nil).
func NewSender(client *http.Client) *Sender {
	if client == nil {
		client = NewClient(ClientOptions{})
	}
	return &Sender{Client: client, Now: time.Now}
}

// Sign returns the signature header value for a request.
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return signatureVersion + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a received request the way a receiver should: the signature
// matches (constant time) and the timestamp is within SignatureTolerance of
// now. It returns nil for a valid request.
func Verify(secret, timestampHeader, signatureHeader string, body []byte, now time.Time) error {
	ts, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp")
	}
	if skew := now.Sub(time.Unix(ts, 0)); skew > SignatureTolerance || skew < -SignatureTolerance {
		return fmt.Errorf("timestamp outside the %s tolerance", SignatureTolerance)
	}
	want := Sign(secret, ts, body)
	if !hmac.Equal([]byte(want), []byte(signatureHeader)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// Send posts one event. Every attempt is signed afresh with the current time,
// so a captured request cannot be replayed after the tolerance window, while
// the event ID stays the same across retries for receiver deduplication.
func (s *Sender) Send(ctx context.Context, ep Endpoint, ev Event) Result {
	u, err := ValidateURL(ep.URL)
	if err != nil {
		return Result{Code: CodeInvalidTarget}
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return Result{Code: CodeInvalidTarget}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return Result{Code: CodeInvalidTarget}
	}
	ts := s.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wasync-webhook/1")
	req.Header.Set(HeaderID, ev.ID)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(ep.Secret, ts, body))
	res, err := s.Client.Do(req)
	if err != nil {
		return classifyError(ctx, err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, maxDrain))
	return classifyStatus(res.StatusCode)
}

func classifyError(ctx context.Context, err error) Result {
	switch {
	case errors.Is(err, ErrBlockedAddress):
		return Result{Code: CodeBlocked}
	case ctx.Err() != nil:
		return Result{Code: CodeNetworkError, Retry: true}
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout"):
		return Result{Code: CodeTimeout, Retry: true}
	}
	return Result{Code: CodeNetworkError, Retry: true}
}

func classifyStatus(status int) Result {
	switch {
	case status >= 200 && status < 300:
		return Result{Code: CodeDelivered, Status: status}
	case status >= 300 && status < 400:
		return Result{Code: CodeRedirect, Status: status}
	case status == http.StatusRequestTimeout:
		return Result{Code: CodeTimeout, Status: status, Retry: true}
	case status == http.StatusTooManyRequests:
		return Result{Code: CodeRateLimited, Status: status, Retry: true}
	case status >= 400 && status < 500:
		return Result{Code: CodeRejected, Status: status}
	}
	return Result{Code: CodeServerError, Status: status, Retry: true}
}

// ChangeEvent builds the event for a journal entry. The ID is derived from
// the endpoint, archive epoch and sequence, so it is stable across retries,
// restarts and re-deliveries.
func ChangeEvent(ep Endpoint, accountJID, epoch string, c storage.Change) Event {
	ev := Event{
		ID:          fmt.Sprintf("evt_%s_%s_%d", ep.ID, epoch, c.Sequence),
		Type:        "change",
		Endpoint:    ep.ID,
		Epoch:       epoch,
		Sequence:    c.Sequence,
		Cursor:      query.ChangeCursor(accountJID, epoch, c.Sequence),
		Op:          c.Op,
		OpVersion:   c.OpVersion,
		ChatID:      c.ChatID,
		MessageID:   c.MessageID,
		CommittedAt: c.CommittedAt,
	}
	if strings.HasPrefix(c.Op, "message.") && c.MessageID != "" {
		ev.Ref = msgref.Encode(accountJID, c.ChatID, c.MessageID)
	}
	return ev
}

// PingEvent builds the synthetic event wasync webhooks test sends.
func PingEvent(ep Endpoint, now time.Time) (Event, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return Event{}, err
	}
	return Event{ID: "evt_" + ep.ID + "_ping_" + hex.EncodeToString(b), Type: "ping", Endpoint: ep.ID, SentAt: now.UTC().Truncate(time.Second)}, nil
}
