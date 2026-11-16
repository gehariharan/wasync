package whatsapp

import (
	"context"
	"fmt"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const historyPageSize = 50

// Cursor metadata is cached in RAM and persisted separately from message bodies.
// This bootstraps groups enabled after history delivery, including after restart.
func (c *Client) rememberAnchor(info types.MessageInfo) {
	if !SupportedChat(info.Chat) || info.ID == "" || info.Timestamp.Unix() <= 0 {
		return
	}
	c.anchorMu.Lock()
	defer c.anchorMu.Unlock()
	if c.anchors == nil {
		c.anchors = make(map[string]types.MessageInfo)
	}
	key := info.Chat.String()
	old, exists := c.anchors[key]
	if !exists && len(c.anchors) >= 4096 {
		return
	}
	if !exists || info.Timestamp.After(old.Timestamp) {
		c.anchors[key] = types.MessageInfo{MessageSource: types.MessageSource{Chat: info.Chat, IsGroup: info.Chat.Server == types.GroupServer, IsFromMe: info.IsFromMe}, ID: info.ID, Timestamp: info.Timestamp}
	}
}
func (c *Client) rememberEventAnchors(evt any) {
	switch e := evt.(type) {
	case *events.Message:
		if e.Message != nil && unwrap(e.RawMessage).GetProtocolMessage() == nil && e.Message.GetProtocolMessage() == nil && !e.IsEdit {
			c.rememberAnchor(e.Info)
		}
	case *events.HistorySync:
		for _, conv := range e.Data.GetConversations() {
			jid, err := types.ParseJID(conv.GetID())
			if err != nil || !SupportedChat(jid) {
				continue
			}
			for _, item := range conv.GetMessages() {
				web := item.GetMessage()
				if web.GetMessage() == nil || unwrap(web.GetMessage()).GetProtocolMessage() != nil {
					continue
				}
				c.rememberAnchor(types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, IsGroup: jid.Server == types.GroupServer, IsFromMe: web.GetKey().GetFromMe()}, ID: web.GetKey().GetID(), Timestamp: time.Unix(int64(web.GetMessageTimestamp()), 0)})
			}
		}
	}
}
func (c *Client) startHistoryWorker(ctx context.Context) {
	workerCtx, cancel := context.WithCancel(ctx)
	c.historyCancel = cancel
	c.historyDone = make(chan struct{})
	go func() {
		defer close(c.historyDone)
		timer := time.NewTicker(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-timer.C:
				if c.Status().Connection != "online" || c.capture.failed.Load() || c.capture.account.Load() == 0 {
					continue
				}
				if err := c.historyStep(workerCtx); err != nil {
					if workerCtx.Err() == nil {
						c.StopCapture(fmt.Errorf("history queue failed: %w", err))
					}
					return
				}
			}
		}
	}()
}
func (c *Client) historyStep(ctx context.Context) error {
	p := c.capture
	account := p.account.Load()
	if err := p.store.TimeoutHistoryRequests(ctx, account, time.Now()); err != nil {
		return err
	}
	pending, err := p.store.PendingHistory(ctx, account)
	if err != nil || pending != nil {
		return err
	}
	job, err := p.store.NextHistory(ctx, account)
	if err != nil || job == nil {
		return err
	}
	anchor := job.Anchor
	local, err := p.store.OldestHistoryAnchor(ctx, account, job.GroupID)
	if err != nil {
		return err
	}
	if local.ID != "" && (anchor.ID == "" || local.Timestamp.Before(anchor.Timestamp)) {
		anchor = local
	}
	if anchor.ID == "" {
		known, e := p.store.KnownHistoryAnchor(ctx, account, job.GroupID)
		if e != nil {
			return e
		}
		anchor = known
	}
	if anchor.ID == "" {
		c.anchorMu.Lock()
		info, ok := c.anchors[job.GroupID]
		c.anchorMu.Unlock()
		if ok {
			anchor = storage.HistoryAnchor{ID: info.ID, Timestamp: info.Timestamp, FromMe: info.IsFromMe}
		}
	}
	if anchor.ID == "" {
		return p.store.WaitForHistoryAnchor(ctx, account, job.GroupID)
	}
	jid, err := types.ParseJID(job.GroupID)
	if err != nil || !SupportedChat(jid) {
		return fmt.Errorf("invalid history group JID")
	}
	own, err := p.store.Account(ctx)
	if err != nil {
		return err
	}
	if own == nil || own.ID != account {
		return fmt.Errorf("history account changed")
	}
	target, err := types.ParseJID(own.JID)
	if err != nil {
		return err
	}
	requestID := c.wa().GenerateMessageID()
	started, err := p.store.StartHistoryRequest(ctx, account, job.GroupID, requestID, anchor)
	if err != nil || !started {
		return err
	}
	info := types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, IsGroup: jid.Server == types.GroupServer, IsFromMe: anchor.FromMe}, ID: anchor.ID, Timestamp: anchor.Timestamp}
	message := c.wa().BuildHistorySyncRequest(&info, historyPageSize)
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	// Equivalent to SendPeerMessage, with a preassigned ID so even a fast response
	// can be correlated before the send acknowledgement arrives. Only an own-device
	// protocol message is sent, never a conversation message to the group.
	sendErr := c.sendHistory(sendCtx, target, message, whatsmeow.SendRequestExtra{Peer: true, ID: requestID, Timeout: 30 * time.Second})
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return p.store.HistoryRequestSent(ctx, account, requestID, sendErr)
}
func (c *Client) finishHistoryPage(ctx context.Context, e *events.HistorySync) error {
	if e.Data.GetSyncType() != waHistorySync.HistorySync_ON_DEMAND && e.Notification.GetSyncType() != waE2E.HistorySyncType_ON_DEMAND {
		return nil
	}
	p := c.capture
	account := p.account.Load()
	job, err := p.store.PendingHistory(ctx, account)
	if err != nil || job == nil {
		return err
	}
	responseID := e.Notification.GetPeerDataRequestSessionID()
	if responseID == "" {
		responseID = e.Notification.GetOriginalMessageID()
	}
	if responseID != "" && responseID != job.RequestID {
		return nil
	}
	var oldest storage.HistoryAnchor
	returned := 0
	found := false
	for _, conv := range e.Data.GetConversations() {
		if conv.GetID() != job.GroupID {
			continue
		}
		found = true
		for _, item := range conv.GetMessages() {
			web := item.GetMessage()
			if web == nil {
				continue
			}
			returned++
			if web.GetKey().GetID() == "" || web.GetMessageTimestamp() == 0 {
				continue
			}
			a := storage.HistoryAnchor{ID: web.GetKey().GetID(), Timestamp: time.Unix(int64(web.GetMessageTimestamp()), 0), FromMe: web.GetKey().GetFromMe()}
			if oldest.ID == "" || a.Timestamp.Before(oldest.Timestamp) || a.Timestamp.Equal(oldest.Timestamp) && a.ID < oldest.ID {
				oldest = a
			}
		}
	}
	// Without correlation metadata, only a response containing the requested group
	// is useful evidence. Empty uncorrelated responses leave the request pending.
	if responseID == "" && !found {
		return nil
	}
	return p.store.FinishHistoryRequest(ctx, account, *job, oldest, returned)
}

// historySender is injected only by local tests; production wraps the library's
// typed send API and is restricted by historyStep to its own-device request.
type historySender func(context.Context, types.JID, *waE2E.Message, whatsmeow.SendRequestExtra) error

func (c *Client) persistEventAnchors(evt any, account int64) error {
	groups := make(map[string]bool)
	switch e := evt.(type) {
	case *events.Message:
		if SupportedChat(e.Info.Chat) {
			groups[e.Info.Chat.String()] = true
		}
	case *events.HistorySync:
		for _, conv := range e.Data.GetConversations() {
			jid, err := types.ParseJID(conv.GetID())
			if err == nil && SupportedChat(jid) {
				groups[jid.String()] = true
			}
		}
	}
	for chat := range groups {
		jid, _ := types.ParseJID(chat)
		if jid.Server != types.GroupServer {
			enabled, err := c.capture.store.IsEnabled(c.capture.ctx, account, chat)
			if err != nil {
				return err
			}
			if !enabled {
				delete(groups, chat)
			}
		}
	}
	anchors := make(map[string]storage.HistoryAnchor)
	c.anchorMu.Lock()
	for group := range groups {
		if info, ok := c.anchors[group]; ok {
			anchors[group] = storage.HistoryAnchor{ID: info.ID, Timestamp: info.Timestamp, FromMe: info.IsFromMe}
		}
	}
	c.anchorMu.Unlock()
	return c.capture.store.SaveHistoryAnchors(c.capture.ctx, account, anchors)
}
