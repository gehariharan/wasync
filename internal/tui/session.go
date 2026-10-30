// Package tui provides the full-screen interface and a single shared sync session.
package tui

import (
	"context"
	"errors"
	"fmt"
	"github.com/gehariharan/wasync/internal/mediafiles"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/accessmode"
	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/humancheck"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

type Client interface {
	AccountJID() string
	LoginWithQR(context.Context, func(string)) error
	EnableCapture(context.Context, *storage.Store) error
	Groups(context.Context) ([]storage.Group, error)
	Status() whatsapp.CaptureStatus
	Maintain(context.Context) error
	Logout(context.Context) error
	StopCapture(error)
}
type Snapshot struct {
	Pending *approval.Request
	// Mode is how agents get chats; ModeChosen is false until the owner picks.
	Mode       accessmode.Mode
	ModeChosen bool
	APIURL     string
	Status     whatsapp.CaptureStatus
	Groups     []storage.Group
	Account    int64
}
type Session struct {
	Approvals *approval.Queue
	APIURL    string
	MediaRoot string
	// DataDir holds agents.json and the audit log; chat access requests need it.
	DataDir string
	// Human, when enabled, makes approving an agent's request wait for the
	// operating system's own confirmation (Touch ID, password, Windows Hello).
	Human      *humancheck.Gate
	ctx        context.Context
	store      *storage.Store
	client     Client
	network    sync.Mutex
	maintained time.Time
}

func NewSession(ctx context.Context, s *storage.Store, c Client) *Session {
	return &Session{ctx: ctx, store: s, client: c}
}
func (s *Session) Connect(display func(string)) error {
	if s.client == nil {
		return fmt.Errorf("Offline archive browsing; capture is stopped")
	}
	if !s.network.TryLock() {
		return fmt.Errorf("a connection operation is already running")
	}
	defer s.network.Unlock()
	if err := s.client.LoginWithQR(s.ctx, display); err != nil {
		return err
	}
	return s.refresh()
}
func (s *Session) Refresh() error {
	if s.client == nil {
		return fmt.Errorf("offline mode: restart without --offline to connect")
	}
	if !s.network.TryLock() {
		return fmt.Errorf("connection is busy")
	}
	defer s.network.Unlock()
	if s.client.Status().Connection != "online" {
		return fmt.Errorf("not connected; press c to connect")
	}
	return s.refresh()
}
func (s *Session) refresh() error {
	groups, err := s.client.Groups(s.ctx)
	if err != nil {
		return err
	}
	account := s.client.Status().AccountID
	if account == 0 {
		return fmt.Errorf("authenticated account not ready")
	}
	return s.store.SaveGroups(s.ctx, account, groups)
}
func (s *Session) Snapshot() (Snapshot, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	snap := Snapshot{APIURL: s.APIURL, Status: whatsapp.CaptureStatus{Connection: "offline browsing"}}
	if s.client != nil {
		snap.Status = s.client.Status()
		if time.Since(s.maintained) > 10*time.Second {
			if err := s.client.Maintain(ctx); err != nil {
				s.client.StopCapture(err)
				return snap, err
			}
			s.maintained = time.Now()
		}
	}
	account, err := s.store.Account(ctx)
	if err != nil {
		return snap, err
	}
	if s.client != nil && (account == nil || snap.Status.AccountID == 0 || snap.Status.AccountID != account.ID || snap.Status.Connection == "pairing") {
		return snap, nil
	}
	if account != nil {
		snap.Account = account.ID
		snap.Pending = s.Approvals.Pending(account.ID)
		if mode, chosen, modeErr := accessmode.Load(s.DataDir); modeErr == nil {
			snap.Mode, snap.ModeChosen = mode, chosen
		} else {
			snap.Mode, snap.ModeChosen = accessmode.Mode{Mode: accessmode.Ask}, true // damaged file: ask each time, do not nag
		}
		snap.Groups, err = s.store.GroupSummaries(ctx, account.ID)
	}
	return snap, err
}
func (s *Session) Toggle(account int64, group string, enabled bool) error {
	return s.store.SetEnabled(s.ctx, account, []string{group}, enabled)
}
func (s *Session) Purge(account int64, group string) error {
	if err := s.store.PurgeGroup(s.ctx, account, group); err != nil {
		return err
	}
	if s.MediaRoot != "" {
		return errors.Join(os.RemoveAll(mediafiles.Directory(s.MediaRoot, account, group)), os.RemoveAll(mediafiles.Directory(whatsapp.ReferenceRoot(s.MediaRoot), account, group)))
	}
	return nil
}
func (s *Session) Messages(account int64, group string, q storage.MessageQuery) (storage.MessagePage, error) {
	return s.store.Messages(s.ctx, account, group, q)
}
func (s *Session) Logout() error {
	if s.client == nil {
		return fmt.Errorf("offline mode cannot unlink a device")
	}
	if !s.network.TryLock() {
		return fmt.Errorf("connection is busy")
	}
	defer s.network.Unlock()
	if s.client.Status().Connection != "online" {
		return fmt.Errorf("connect before unlinking")
	}
	return s.client.Logout(s.ctx)
}

func (s *Session) RequestHistory(account int64, group string) error {
	return s.store.QueueHistory(s.ctx, account, group)
}

// ToggleMany applies the exact group set shown at confirmation time atomically.
func (s *Session) ToggleMany(account int64, ids []string, enabled bool) error {
	return s.store.SetEnabled(s.ctx, account, ids, enabled)
}

func (s *Session) ClearMessage(account int64, chat, message string) error {
	if err := s.store.ClearMessage(s.ctx, account, chat, message); err != nil {
		return err
	}
	return errors.Join(mediafiles.RemoveMessage(s.MediaRoot, account, chat, message), whatsapp.RemoveMediaReference(s.MediaRoot, account, chat, message))
}

func (s *Session) RevokeMessage(account int64, chat, message string) error {
	if s.client == nil {
		return fmt.Errorf("connect before remote deletion")
	}
	if !s.network.TryLock() {
		return fmt.Errorf("connection is busy")
	}
	defer s.network.Unlock()
	return s.revokeMessage(account, chat, message)
}
func (s *Session) revokeMessage(account int64, chat, message string) error {
	m, err := s.store.MessageByWhatsAppID(s.ctx, account, chat, message)
	if err != nil {
		return err
	}
	remote, ok := s.client.(interface {
		RevokeOwnMessage(context.Context, string, storage.Message) error
	})
	if !ok {
		return fmt.Errorf("remote revocation unavailable")
	}
	if err = remote.RevokeOwnMessage(s.ctx, chat, m); err != nil {
		return err
	}
	if err = s.ClearMessage(account, chat, message); err != nil {
		return &partialActionError{err}
	}
	return nil
}

type partialActionError struct{ err error }

func (e *partialActionError) Error() string {
	return "WhatsApp revocation acknowledged; local cleanup failed: " + e.err.Error()
}
func (s *Session) DeleteWhatsAppChat(account int64, chat string) error {
	if s.client == nil {
		return fmt.Errorf("connect before remote deletion")
	}
	if !s.network.TryLock() {
		return fmt.Errorf("connection is busy")
	}
	defer s.network.Unlock()
	return s.deleteWhatsAppChat(account, chat)
}
func (s *Session) deleteWhatsAppChat(account int64, chat string) error {
	page, err := s.store.Messages(s.ctx, account, chat, storage.MessageQuery{Limit: 1})
	if err != nil {
		return err
	}
	if len(page.Items) == 0 {
		return fmt.Errorf("known local message required to bound WhatsApp deletion")
	}
	remote, ok := s.client.(interface {
		DeleteWhatsAppChat(context.Context, string, storage.Message) error
	})
	if !ok {
		return fmt.Errorf("remote chat deletion unavailable")
	}
	return remote.DeleteWhatsAppChat(s.ctx, chat, page.Items[0])
}
func (s *Session) ApproveAction(account int64, id string) (result error) {
	if s.Approvals == nil {
		return fmt.Errorf("approval queue unavailable")
	}
	current, err := s.store.Account(s.ctx)
	if err != nil {
		return err
	}
	if current == nil || current.ID != account {
		return fmt.Errorf("account changed; request not executed")
	}
	pending := s.Approvals.Get(account, id)
	if pending == nil {
		return fmt.Errorf("request unavailable")
	}
	remote := pending.Kind == "revoke-message" || pending.Kind == "delete-whatsapp-chat"
	if remote {
		if s.client == nil || s.client.Status().Connection != "online" || s.client.Status().AccountID != account {
			return fmt.Errorf("connect the matching account before approval; request remains pending")
		}
		if !s.network.TryLock() {
			return fmt.Errorf("connection is busy; request remains pending")
		}
		defer s.network.Unlock()
	}
	request, err := s.Approvals.Take(account, id)
	if err != nil {
		return err
	}
	defer func() {
		status := "completed"
		if result != nil {
			status = "failed_or_unknown"
			var partial *partialActionError
			if errors.As(result, &partial) {
				status = "remote_completed_local_cleanup_failed"
			}
		}
		s.Approvals.Finish(id, status, result)
	}()
	switch request.Kind {
	case approval.KindChatAccess:
		return s.grantChatAccess(account, request)
	case "clear-message":
		return s.ClearMessage(account, request.Chat, request.Message)
	case "purge-chat":
		return s.Purge(account, request.Chat)
	case "revoke-message":
		return s.revokeMessage(account, request.Chat, request.Message)
	case "delete-whatsapp-chat":
		return s.deleteWhatsAppChat(account, request.Chat)
	}
	return fmt.Errorf("unsupported action")
}

// ConfirmApproval asks the person for the system confirmation an approval of
// request id needs. It returns nil at once when this computer has no such prompt.
func (s *Session) ConfirmApproval(account int64, id string) error {
	if !s.Human.Enabled() || s.Approvals == nil {
		return nil
	}
	pending := s.Approvals.Get(account, id)
	if pending == nil || pending.Status != approval.StatusPending {
		return nil
	}
	name := ""
	if groups, err := s.store.Groups(s.ctx, account); err == nil {
		for _, g := range groups {
			if g.ID == pending.Chat {
				name = g.Name
			}
		}
	}
	if err := s.Human.Confirm(s.ctx, pending.Prompt(name)); err != nil {
		return fmt.Errorf("%w; nothing changed", err)
	}
	return nil
}

// ConfirmAccessMode does the same for choosing "sync all chats".
func (s *Session) ConfirmAccessMode(m accessmode.Mode) error {
	if m.Mode != accessmode.All || !s.Human.Enabled() {
		return nil
	}
	if err := s.Human.Confirm(s.ctx, "Approve: "+m.Effect()); err != nil {
		return fmt.Errorf("%w; nothing changed", err)
	}
	return nil
}

func (s *Session) RejectAction(account int64, id string) error {
	if s.Approvals == nil {
		return fmt.Errorf("approval queue unavailable")
	}
	pending := s.Approvals.Get(account, id)
	if err := s.Approvals.Reject(account, id); err != nil {
		return err
	}
	if pending != nil && pending.Kind == approval.KindChatAccess && s.DataDir != "" {
		a := pending.Access
		_ = agentauth.NewAuditLog(s.DataDir).Append(agentauth.AuditEntry{Event: "access_reject", ClientID: a.AgentID, ClientName: a.Agent, Scope: strings.Join(a.Scopes, ","), Chat: pending.Chat, Ref: id[:8], Outcome: agentauth.OutcomeDenied})
	}
	return nil
}

// SetAccessMode applies the owner's choice of how agents get chats. Choosing
// sync all enables capture for every cached chat first; see accessmode.Apply.
func (s *Session) SetAccessMode(account int64, m accessmode.Mode) error {
	if s.DataDir == "" {
		return fmt.Errorf("data directory unknown; nothing changed")
	}
	current, err := s.store.Account(s.ctx)
	if err != nil {
		return err
	}
	if current == nil || current.ID != account {
		return fmt.Errorf("account changed; nothing changed")
	}
	_, err = accessmode.Apply(s.ctx, s.store, s.DataDir, account, m)
	return err
}

// DecideAccess records the human's chat and history-depth choice on a pending
// access request; ApproveAction then applies exactly that choice.
func (s *Session) DecideAccess(account int64, id, chat, history string) error {
	if s.Approvals == nil {
		return fmt.Errorf("approval queue unavailable")
	}
	return s.Approvals.Decide(account, id, chat, history)
}

// grantChatAccess applies an approved access request: capture is enabled for
// the chosen chat, older history is requested only if the human asked for it,
// and only then is the agent's access extended to that one chat. Any failure
// leaves the agent without access.
func (s *Session) grantChatAccess(account int64, request approval.Request) error {
	a := request.Access
	if s.DataDir == "" {
		return fmt.Errorf("agent registry location unknown; nothing granted")
	}
	chat := a.Chosen
	if err := agentauth.CanGrantChatAccess(s.DataDir, a.AgentID, a.Scopes, time.Now()); err != nil {
		return err // before capture changes: an agent that is gone gets nothing
	}
	if storage.ChatKind(chat) == "direct" {
		// Direct chats appear in the cache once seen; a requested number may be new.
		if err := s.store.SaveDiscoveredChat(s.ctx, account, storage.Group{ID: chat, Name: chat}); err != nil {
			return err
		}
	}
	if err := s.store.EnableCapture(s.ctx, account, chat, a.History == approval.HistoryFull); err != nil {
		return err
	}
	audit := agentauth.NewAuditLog(s.DataDir)
	event := "access_grant"
	if a.Auto {
		event = "access_grant_auto" // the owner's "sync all" setting approved it
	}
	entry := agentauth.AuditEntry{Event: event, ClientID: a.AgentID, ClientName: a.Agent, Scope: strings.Join(a.Scopes, ","), Chat: chat, Ref: request.ID[:8], Outcome: agentauth.OutcomeAllowed}
	if err := audit.Append(entry); err != nil {
		return fmt.Errorf("audit log unavailable; access not granted: %w", err)
	}
	if _, err := agentauth.GrantChatAccess(s.DataDir, a.AgentID, chat, a.Scopes, request.ID, time.Now()); err != nil {
		entry.Outcome, entry.Code = agentauth.OutcomeDenied, "failed"
		_ = audit.Append(entry)
		return err
	}
	return nil
}

func (s *Session) SetMediaPolicy(account int64, chat, mode string) error {
	return s.store.SetMediaPolicy(s.ctx, account, chat, mode)
}
func (s *Session) DownloadMedia(account int64, chat, message string) error {
	if s.client == nil || s.client.Status().AccountID != account {
		return fmt.Errorf("connect the selected account before downloading")
	}
	client, ok := s.client.(interface {
		RequestMedia(context.Context, string, string) (whatsapp.MediaResult, error)
	})
	if !ok {
		return fmt.Errorf("media downloads unavailable")
	}
	_, err := client.RequestMedia(s.ctx, chat, message)
	return err
}
