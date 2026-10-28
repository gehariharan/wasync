package agentauth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Authorizer authenticates agent credentials and enforces scopes, chat
// allowlists and per-client rate limits. It re-reads agents.json whenever the
// file changes, so grants and revokes apply without restarting the owner.
// It is safe for concurrent use.
type Authorizer struct {
	dir   string
	audit *AuditLog
	now   func() time.Time

	mu      sync.Mutex
	info    os.FileInfo // identity of the loaded registry; nil when absent
	loaded  bool
	clients map[string]record
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	rate   int
}

// NewAuthorizer returns an authorizer for the registry and audit log in dir.
// The registry is loaded lazily, so a missing file simply rejects everything.
func NewAuthorizer(dir string) *Authorizer {
	return &Authorizer{dir: dir, audit: NewAuditLog(dir), now: time.Now, buckets: map[string]*bucket{}}
}

// AuditLog returns the authorizer's audit log.
func (a *Authorizer) AuditLog() *AuditLog { return a.audit }

// Audit appends one entry to the private audit log; see AuditLog.Append.
func (a *Authorizer) Audit(entry AuditEntry) error { return a.audit.Append(entry) }

// refresh reloads agents.json when its identity, size or mtime changed.
// Callers hold a.mu. A registry that fails validation (for example lost its
// private permissions) fails closed: every credential is rejected.
func (a *Authorizer) refresh() error {
	path := filepath.Join(a.dir, FileName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		a.info, a.loaded, a.clients = nil, true, map[string]record{}
		return nil
	}
	if err != nil {
		a.loaded = false
		return err
	}
	if a.loaded && a.info != nil && os.SameFile(a.info, info) && a.info.ModTime().Equal(info.ModTime()) && a.info.Size() == info.Size() && a.info.Mode() == info.Mode() {
		return nil
	}
	reg, opened, err := load(a.dir)
	if err != nil {
		a.loaded, a.info, a.clients = false, nil, nil
		return err
	}
	clients := make(map[string]record, len(reg.Clients))
	for _, r := range reg.Clients {
		clients[r.ID] = r
	}
	a.info, a.loaded, a.clients = opened, true, clients
	return nil
}

// Authenticate verifies an Authorization header of the form
// "Bearer wsa_<clientid>_<secret>". Legacy tokens are always rejected. The
// secret hash is compared in constant time; revoked and expired clients are
// rejected only after the secret verifies, so nothing leaks to guessers.
//
// It returns *Error (CodeUnauthorized) for credential problems, or another
// error when the registry cannot be read safely (map that to 500; it fails closed).
func (a *Authorizer) Authenticate(header string) (*Client, error) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return nil, unauthorized("Authorization: Bearer wsa_... credential required")
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, CredentialPrefix) {
		return nil, unauthorized("legacy wasync tokens are not accepted; an agent credential (wsa_...) is required")
	}
	id, secret, err := ParseCredential(token)
	if err != nil {
		return nil, unauthorized("malformed agent credential")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err = a.refresh(); err != nil {
		return nil, fmt.Errorf("agent registry unavailable: %w", err)
	}
	r, found := a.clients[id]
	stored := r.SecretSHA256
	if !found {
		stored = strings.Repeat("0", 64) // keep timing uniform for unknown IDs
	}
	if subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(stored)) != 1 || !found {
		return nil, unauthorized("invalid agent credential")
	}
	switch r.Status(a.now()) {
	case "revoked":
		e := unauthorized("agent credential for " + r.Name + " was revoked")
		e.Help = "A human must issue a new credential in an interactive terminal: wasync agents grant " + r.Name + " --scope " + strings.Join(r.Scopes, ",") + chatFlags(r.Chats)
		return nil, e
	case "expired":
		e := unauthorized("agent credential for " + r.Name + " expired")
		e.Help = "A human must re-grant it in an interactive terminal: " + regrantCommand(&r.Client, "", "", 0)
		return nil, e
	}
	c := cloneClient(r.Client)
	return &c, nil
}

// Authorize checks that client c may use scope on chatJID, then consumes one
// rate-limit token. The client is re-read from the current registry so scope
// changes, revocation and expiry apply immediately.
//
// Allowlist rules: scopes that require an allowlist (send, react, edit.own,
// chat.state, group.admin, capture.manage) need a non-empty chatJID in the
// allowlist. When the client has an allowlist, every other scope is also
// restricted to it; an empty chatJID is then permitted only for scopes without
// a required allowlist, and the handler must filter results with
// Client.AllowsChat (for example a chat list).
func (a *Authorizer) Authorize(c *Client, scope string, chatJID string) error {
	if c == nil {
		return unauthorized("agent credential required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.refresh(); err != nil {
		return fmt.Errorf("agent registry unavailable: %w", err)
	}
	r, found := a.clients[c.ID]
	now := a.now()
	if !found || r.Status(now) != "active" {
		return unauthorized("agent credential is no longer valid")
	}
	current := r.Client
	if !ValidScope(scope) {
		return &Error{Code: CodeForbidden, Status: 403, Scope: scope, Chat: chatJID, Message: fmt.Sprintf("scope %q does not exist", scope), Help: "This operation is not available to agent credentials; destructive actions require human approval in the TUI or wasync actions approve."}
	}
	if !current.HasScope(scope) {
		return &Error{Code: CodeForbidden, Status: 403, Scope: scope, Chat: chatJID, Message: fmt.Sprintf("client %s lacks scope %s", current.Name, scope), Help: fmt.Sprintf("Missing scope %s. A human must re-grant the client in an interactive terminal: %s", scope, regrantCommand(&current, scope, chatJID, 0))}
	}
	restricted := RequiresAllowlist(scope) || current.Restricted()
	if restricted && chatJID == "" && RequiresAllowlist(scope) {
		return &Error{Code: CodeForbidden, Status: 403, Scope: scope, Message: fmt.Sprintf("scope %s requires a chat", scope), Help: "Name the target chat; scope " + scope + " is limited to the client's chat allowlist."}
	}
	if restricted && chatJID != "" && !current.AllowsChatScope(scope, chatJID) {
		return &Error{Code: CodeForbidden, Status: 403, Scope: scope, Chat: chatJID, Message: fmt.Sprintf("client %s is not allowed chat %s", current.Name, chatJID), Help: notAllowedHelp(&current, scope, chatJID)}
	}
	if wait := a.take(current, now); wait > 0 {
		seconds := int(math.Ceil(wait.Seconds()))
		return &Error{Code: CodeRateLimited, Status: 429, Scope: scope, Chat: chatJID, RetryAfter: wait, Message: fmt.Sprintf("client %s exceeded %d requests per minute", current.Name, current.RatePerMinute), Help: fmt.Sprintf("Retry after %ds. A human can raise the limit in an interactive terminal: %s", seconds, regrantCommand(&current, "", "", current.RatePerMinute*2))}
	}
	return nil
}

// AllowAccessRequest checks that c is still a valid credential and consumes one
// rate-limit token. Any active client may ask for chat access, because asking
// grants nothing: a human decides. The rate limit and the pending cap keep a
// misbehaving agent from flooding the person with prompts.
func (a *Authorizer) AllowAccessRequest(c *Client) error {
	if c == nil {
		return unauthorized("agent credential required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.refresh(); err != nil {
		return fmt.Errorf("agent registry unavailable: %w", err)
	}
	r, found := a.clients[c.ID]
	now := a.now()
	if !found || r.Status(now) != "active" {
		return unauthorized("agent credential is no longer valid")
	}
	if wait := a.take(r.Client, now); wait > 0 {
		seconds := int(math.Ceil(wait.Seconds()))
		return &Error{Code: CodeRateLimited, Status: 429, RetryAfter: wait, Message: fmt.Sprintf("client %s exceeded %d requests per minute", r.Name, r.RatePerMinute), Help: fmt.Sprintf("Retry after %ds.", seconds)}
	}
	return nil
}

// AuthorizeAndAudit calls Authorize and records the decision: denials are
// always audited; allowed requests are audited when mutation is true. Reads
// that succeed are not audited. If the audit write fails for an allowed
// mutation, the mutation is refused (the returned error is not an *Error).
func (a *Authorizer) AuthorizeAndAudit(c *Client, scope, chatJID, ref, requestID string, mutation bool) error {
	err := a.Authorize(c, scope, chatJID)
	entry := AuditEntry{Scope: scope, Chat: chatJID, Ref: ref, RequestID: requestID, Outcome: OutcomeAllowed}
	if c != nil {
		entry.ClientID, entry.ClientName = c.ID, c.Name
	}
	var authErr *Error
	switch {
	case errors.As(err, &authErr):
		entry.Outcome, entry.Code = OutcomeDenied, authErr.Code
	case err != nil:
		entry.Outcome, entry.Code = OutcomeDenied, "internal"
	case !mutation:
		return nil
	}
	if auditErr := a.audit.Append(entry); auditErr != nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("audit log unavailable; mutation refused: %w", auditErr)
	}
	return err
}

// take consumes one token; it returns how long to wait when none is left.
// Capacity equals the per-minute rate, refilled continuously.
func (a *Authorizer) take(c Client, now time.Time) time.Duration {
	b := a.buckets[c.ID]
	if b == nil || b.rate != c.RatePerMinute {
		b = &bucket{tokens: float64(c.RatePerMinute), last: now, rate: c.RatePerMinute}
		a.buckets[c.ID] = b
	}
	perSecond := float64(c.RatePerMinute) / 60
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(float64(c.RatePerMinute), b.tokens+elapsed*perSecond)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	return time.Duration((1 - b.tokens) / perSecond * float64(time.Second))
}

func cloneClient(c Client) Client {
	c.Scopes = append([]string(nil), c.Scopes...)
	c.Chats = append([]string(nil), c.Chats...)
	if c.Grants != nil {
		grants := make([]ChatGrant, len(c.Grants))
		for i, g := range c.Grants {
			g.Scopes = append([]string(nil), g.Scopes...)
			grants[i] = g
		}
		c.Grants = grants
	}
	if c.ExpiresAt != nil {
		t := *c.ExpiresAt
		c.ExpiresAt = &t
	}
	c.RevokedAt = nil
	return c
}

func chatFlags(chats []string) string {
	var b strings.Builder
	for _, c := range chats {
		b.WriteString(" --chat " + c)
	}
	return b.String()
}
