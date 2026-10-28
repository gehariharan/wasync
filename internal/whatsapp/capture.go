package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type CaptureStatus struct {
	Connection        string
	Error             string
	Stored, Skipped   int64
	AccountID         int64
	HistoryProcessing bool
	Observation       Observation
	// Health facts (additive; the legacy /api/status builds its own map).
	// Zero times mean never observed during this process.
	LastEventAt      time.Time
	LastConnectedAt  time.Time
	LastKeepAliveOK  time.Time
	KeepAliveFailing bool
	Paired           bool
}
type capture struct {
	metadataOnly    bool
	ctx             context.Context
	store           *storage.Store
	mu              sync.Mutex
	account, run    atomic.Int64
	stored, skipped atomic.Int64
	history         atomic.Bool
	failed          atomic.Bool
}

// EnableCapture must be called before connecting. Existing allowlists are ready
// before the server can deliver live or historical messages.
func (c *Client) EnableCapture(ctx context.Context, s *storage.Store) error {
	return c.enableCapture(ctx, s, true)
}

// EnableDiscovery persists only chat names and group history anchors during CLI login.
// It never archives bodies, even for an already-enabled chat.
func (c *Client) EnableDiscovery(ctx context.Context, store *storage.Store) error {
	c.capture = &capture{ctx: ctx, store: store, metadataOnly: true}
	if c.Paired() {
		return c.capture.initialize(c.AccountJID())
	}
	return nil
}
func (c *Client) EnableCaptureForSend(ctx context.Context, s *storage.Store) error {
	return c.enableCapture(ctx, s, false)
}
func (c *Client) enableCapture(ctx context.Context, s *storage.Store, history bool) error {
	c.capture = &capture{ctx: ctx, store: s}
	if c.Paired() {
		if err := c.capture.initialize(c.AccountJID()); err != nil {
			return err
		}
	}
	if history {
		c.startHistoryWorker(ctx)
	}
	return c.startMediaWorker(ctx)
}
func (p *capture) initialize(jid string) error {
	if p.account.Load() != 0 {
		return nil
	}
	if jid == "" {
		return fmt.Errorf("no authenticated account identity")
	}
	id, err := p.store.UseAccount(p.ctx, jid)
	if err != nil {
		return err
	}
	if p.metadataOnly {
		p.account.Store(id)
		return nil
	}
	run, err := p.store.StartCapture(p.ctx, id)
	if err != nil {
		return err
	}
	if err = p.store.Expire(p.ctx); err != nil {
		return err
	}
	p.account.Store(id)
	p.run.Store(run)
	return nil
}
func (c *Client) handleEvent(evt any) bool {
	c.health.record(evt, time.Now())
	c.rememberEventAnchors(evt)
	if c.capture != nil {
		if err := c.captureEvent(evt); err != nil {
			c.capture.failed.Store(true)
			c.connection.Store("capture stopped")
			c.lastError.Store("Archive operation failed. Capture stopped; check disk space and restart.")
			c.log.Error("capture stopped after archive failure")
			select {
			case c.fatal <- fmt.Errorf("archive operation failed; capture stopped: %w", err):
			default:
			}
			go c.client.Disconnect()
			return false
		}
	}
	c.onEvent(evt)
	return true
}
func (c *Client) captureEvent(evt any) error {
	p := c.capture
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed.Load() {
		return fmt.Errorf("capture already stopped")
	}
	if p.account.Load() == 0 {
		if _, ok := evt.(*events.Connected); ok {
			if err := p.initialize(c.AccountJID()); err != nil {
				return err
			}
		} else {
			return nil
		}
	}
	account := p.account.Load()
	if err := c.persistEventAnchors(evt, account); err != nil {
		return err
	}
	if err := c.syncContactNames(evt, account); err != nil {
		return err
	}
	if p.metadataOnly {
		switch e := evt.(type) {
		case *events.Message:
			name := ""
			if !e.Info.IsFromMe {
				name = e.Info.PushName
			}
			return c.discoverDirect(p.ctx, account, e.Info.Chat, name)
		case *events.HistorySync:
			for _, conv := range e.Data.GetConversations() {
				jid, err := types.ParseJID(conv.GetID())
				if err == nil {
					name := conv.GetDisplayName()
					if name == "" {
						name = conv.GetName()
					}
					if err = c.discoverDirect(p.ctx, account, jid, name); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	switch e := evt.(type) {
	case *events.Connected:
		return p.store.Resume(p.ctx, account)
	case *events.Disconnected:
		return p.store.Gap(p.ctx, account, "network disconnected; recovery unverified")
	case events.PermanentDisconnect:
		return p.store.Gap(p.ctx, account, "session rejected; recovery unverified")
	case *events.UndecryptableMessage:
		if SupportedChat(e.Info.Chat) {
			enabled, err := p.store.IsEnabled(p.ctx, account, e.Info.Chat.String())
			if err != nil {
				return err
			}
			if enabled {
				return p.store.Gap(p.ctx, account, "a selected-group message could not be decrypted")
			}
		}
	case *events.Message:
		if !SupportedChat(e.Info.Chat) {
			return nil
		}

		name := ""
		if !e.Info.IsFromMe {
			name = e.Info.PushName
		}
		if err := c.discoverDirect(p.ctx, account, e.Info.Chat, name); err != nil {
			return err
		}
		enabled, err := p.store.IsEnabled(p.ctx, account, e.Info.Chat.String())
		if err != nil {
			return err
		}
		if !enabled {
			return nil
		}
		m, ok := Normalize(e, "live", time.Now())
		if !ok {
			p.skipped.Add(1)
			return nil
		}
		r, err := p.store.Apply(p.ctx, account, []storage.Message{m})
		p.stored.Add(int64(r.Stored))
		p.skipped.Add(int64(r.Skipped))
		if err != nil {
			return err
		}
		if m.Kind == "delete" || m.Kind == "edit" {
			c.removeMedia(account, m.GroupID, m.MessageID)
			return nil
		}
		return c.queueMedia(account, e, m)
	case *events.HistorySync:
		if e.Data == nil {
			return nil
		}
		p.history.Store(true)
		defer p.history.Store(false)
		batch, err := p.store.BeginHistory(p.ctx, account, e.Data.GetSyncType().String(), e.Data.GetChunkOrder(), e.Data.GetProgress())
		if err != nil {
			return err
		}
		stored, skipped, failures := 0, 0, 0
		pending := make([]storage.Message, 0, 100)
		pendingMedia := make([]*events.Message, 0, 100)
		flush := func() error {
			if len(pending) == 0 {
				return nil
			}
			r, err := p.store.ApplyHistory(p.ctx, account, batch, pending)
			stored += r.Stored
			skipped += r.Skipped
			p.stored.Add(int64(r.Stored))
			p.skipped.Add(int64(r.Skipped))
			if err != nil {
				return err
			}
			for i, event := range pendingMedia {
				if pending[i].Kind == "delete" || pending[i].Kind == "edit" {
					c.removeMedia(account, pending[i].GroupID, pending[i].MessageID)
					continue
				}
				if e := c.queueMedia(account, event, pending[i]); e != nil {
					return e
				}
			}
			pending = pending[:0]
			pendingMedia = pendingMedia[:0]
			return nil
		}
		for _, conv := range e.Data.GetConversations() {
			jid, err := types.ParseJID(conv.GetID())
			if err != nil || !SupportedChat(jid) {
				continue
			}
			name := conv.GetDisplayName()
			if name == "" {
				name = conv.GetName()
			}
			if err := c.discoverDirect(p.ctx, account, jid, name); err != nil {
				return err
			}
			enabled, err := p.store.IsEnabled(p.ctx, account, jid.String())
			if err != nil {
				return err
			}
			if !enabled {
				skipped += len(conv.GetMessages())
				continue
			}
			for _, item := range conv.GetMessages() {
				web := item.GetMessage()
				if web == nil {
					failures++
					continue
				}
				event, err := c.client.ParseWebMessage(jid, web)
				if err != nil {
					failures++
					continue
				}
				m, ok := Normalize(event, "history", time.Now())
				if !ok {
					skipped++
					continue
				}
				pending = append(pending, m)
				pendingMedia = append(pendingMedia, event)
				if len(pending) == 100 {
					if err = flush(); err != nil {
						return err
					}
				}
			}
		}
		if err = flush(); err != nil {
			return err
		}
		if failures > 0 {
			c.lastError.Store(fmt.Sprintf("History batch had %d unparseable messages; coverage remains uncertain.", failures))
		}
		if err = p.store.EndHistory(p.ctx, batch, stored, skipped, failures); err != nil {
			return err
		}
		return c.finishHistoryPage(p.ctx, e)
	}
	return nil
}
func (c *Client) Status() CaptureStatus {
	status := CaptureStatus{Connection: "offline", Observation: c.Observation()}
	if v := c.connection.Load(); v != nil {
		status.Connection = v.(string)
	}
	if v := c.lastError.Load(); v != nil {
		status.Error = v.(string)
	}
	c.health.fill(&status)
	if p := c.capture; p != nil {
		status.Stored = p.stored.Load()
		status.Skipped = p.skipped.Load()
		status.AccountID = p.account.Load()
		status.HistoryProcessing = p.history.Load()
	}
	return status
}
func (c *Client) Maintain(ctx context.Context) error {
	p := c.capture
	if p == nil || p.account.Load() == 0 {
		return nil
	}
	if err := p.store.Expire(ctx); err != nil {
		return err
	}
	return p.store.Heartbeat(ctx, p.run.Load())
}
func (c *Client) finishCapture() error {
	p := c.capture
	if p == nil || p.run.Load() == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if !p.failed.Load() {
		err = p.store.EndCapture(ctx, p.run.Load())
	}
	return errors.Join(err, p.store.Expire(ctx))
}

func (c *Client) StopCapture(err error) {
	if c.capture != nil {
		c.capture.failed.Store(true)
	}
	c.connection.Store("capture stopped")
	c.lastError.Store("Archive operation failed. Capture stopped; check disk space and restart.")
	c.log.Error("capture stopped after archive failure")
	select {
	case c.fatal <- err:
	default:
	}
	go c.client.Disconnect()
}
