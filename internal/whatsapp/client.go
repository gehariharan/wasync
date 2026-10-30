// Package whatsapp isolates whatsmeow and its device store from the CLI.
package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gehariharan/wasync/internal/storage"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

var ErrNotPaired = errors.New("no saved WhatsApp session; run wasync login")

type Observation struct {
	LiveEvents      int64
	HistoryBatches  int64
	HistoryMessages int64
}
type Client struct {
	mediaConnected  func() bool
	mediaTransfer   func(context.Context, whatsmeow.DownloadableMessage, whatsmeow.File) error
	mediaRequestMu  sync.Mutex
	mediaPending    map[string]bool
	mediaRoot       string
	mediaEnabled    bool
	mediaQueue      chan mediaJob
	mediaCancel     context.CancelFunc
	mediaDone       chan struct{}
	anchorMu        sync.Mutex
	anchors         map[string]types.MessageInfo
	historyCancel   context.CancelFunc
	historyDone     chan struct{}
	sendHistory     historySender
	client          *whatsmeow.Client
	container       *sqlstore.Container
	connected       chan struct{}
	fatal           chan error
	log             *slog.Logger
	live            atomic.Int64
	historyBatches  atomic.Int64
	historyMessages atomic.Int64
	capture         *capture
	connection      atomic.Value
	lastError       atomic.Value
	health          healthFacts
	names           nameSource // test seam; nil uses the device's contact and LID stores
	pairing         pairer     // test seam; nil uses the whatsmeow client
}

func Open(ctx context.Context, path string, log *slog.Logger) (*Client, error) {
	if err := storage.Prepare(path); err != nil {
		return nil, err
	}
	// Upstream logs can contain protocol payloads. Only our explicit lifecycle events are logged.
	container, err := sqlstore.New(ctx, "sqlite3", storage.DSN(path), waLog.Noop)
	if err != nil {
		return nil, fmt.Errorf("open device store: %w", err)
	}
	devices, err := container.GetAllDevices(ctx)
	if err != nil {
		container.Close()
		return nil, err
	}
	if len(devices) > 1 {
		container.Close()
		return nil, fmt.Errorf("device store has multiple sessions; only one account can be linked at a time")
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		container.Close()
		return nil, err
	}
	c := &Client{client: whatsmeow.NewClient(device, waLog.Noop), container: container, connected: make(chan struct{}, 1), fatal: make(chan error, 1), log: log}
	c.sendHistory = func(ctx context.Context, to types.JID, m *waE2E.Message, extra whatsmeow.SendRequestExtra) error {
		_, err := c.client.SendMessage(ctx, to, m, extra)
		return err
	}
	c.client.BackgroundEventCtx = ctx
	c.client.InitialAutoReconnect = true
	c.client.SynchronousAck = true
	c.connection.Store("offline")
	c.lastError.Store("")
	c.health.paired.Store(device.ID != nil)
	c.client.AddEventHandlerWithSuccessStatus(c.handleEvent)
	return c, nil
}
func (c *Client) Close() error {
	if c.mediaCancel != nil {
		c.mediaCancel()
	}
	if c.historyCancel != nil {
		c.historyCancel()
	}
	c.client.Disconnect()
	if c.historyDone != nil {
		<-c.historyDone
	}
	if c.mediaDone != nil {
		<-c.mediaDone
	}
	return errors.Join(c.finishCapture(), c.container.Close())
}
func (c *Client) Paired() bool { return c.client.Store.ID != nil }
func (c *Client) AccountJID() string {
	if !c.Paired() {
		return ""
	}
	return c.client.Store.ID.ToNonAD().String()
}
func (c *Client) onEvent(evt any) {
	switch v := evt.(type) {
	case *events.Connected:
		c.connection.Store("online")
		c.log.Info("whatsapp authenticated")
		select {
		case c.connected <- struct{}{}:
		default:
		}
	case events.PermanentDisconnect:
		c.connection.Store("attention required")
		c.log.Warn("whatsapp connection requires attention", "event", fmt.Sprintf("%T", v))
		var err error
		switch v.(type) {
		case *events.LoggedOut:
			err = fmt.Errorf("WhatsApp unlinked this session; run wasync login again")
		case *events.StreamReplaced:
			err = fmt.Errorf("WhatsApp session replaced; stop any other client using this device store")
		case *events.ClientOutdated:
			err = fmt.Errorf("WhatsApp rejected this client version; update the pinned dependency")
		case *events.TemporaryBan:
			err = fmt.Errorf("WhatsApp temporarily restricted this account; stop and check the phone")
		default:
			err = fmt.Errorf("WhatsApp rejected the connection (%T); check the phone before retrying", v)
		}
		c.lastError.Store(err.Error())
		select {
		case c.fatal <- err:
		default:
		}
	case *events.Disconnected:
		c.connection.Store("reconnecting")
		c.log.Warn("whatsapp disconnected; any history or downtime coverage remains uncertain")
	case *events.Message:
		c.live.Add(1)
	case *events.HistorySync:
		c.historyBatches.Add(1)
		if v.Data != nil {
			for _, conv := range v.Data.GetConversations() {
				c.historyMessages.Add(int64(len(conv.GetMessages())))
			}
		}
	}
}
func (c *Client) Connect(ctx context.Context) error {
	if c.capture != nil && c.capture.failed.Load() {
		return fmt.Errorf("capture stopped after an archive failure; restart wasync before reconnecting")
	}
	if !c.Paired() {
		return ErrNotPaired
	}
	c.connection.Store("connecting")
	if err := c.client.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect WhatsApp: %w", err)
	}
	return c.waitAuthenticated(ctx)
}
func (c *Client) Login(ctx context.Context, out io.Writer) error {
	return c.login(ctx, func(code string) { qrterminal.GenerateHalfBlock(code, qrterminal.L, out) }, func(message string) { fmt.Fprintln(out, message) })
}

// LoginWithQR lets a TUI render QR codes without writing over the alternate screen.
func (c *Client) LoginWithQR(ctx context.Context, display func(string)) error {
	return c.login(ctx, display, func(string) {})
}

// pairer is the part of the whatsmeow client that QR linking uses.
type pairer interface {
	GetQRChannel(context.Context) (<-chan whatsmeow.QRChannelItem, error)
	ConnectContext(context.Context) error
	Disconnect()
}

// login links by QR code. When it fails it leaves the connection closed and
// keeps reading the abandoned code channel, so another attempt can start.
func (c *Client) login(ctx context.Context, display func(string), notice func(string)) (err error) {
	if c.Paired() {
		return c.Connect(ctx)
	}
	var p pairer = c.client
	if c.pairing != nil {
		p = c.pairing
	}
	qr, err := p.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("start QR pairing: %w", err)
	}
	defer func() {
		if err != nil {
			p.Disconnect()
			c.connection.Store("offline")
			go func() {
				for range qr {
				}
			}()
		}
	}()
	if err = p.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect for pairing: %w", err)
	}
	c.connection.Store("pairing")
	notice("On your phone: WhatsApp → Linked Devices → Link a Device. Scan the QR below.")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-c.fatal:
			return err
		case item, ok := <-qr:
			if !ok {
				return fmt.Errorf("QR pairing ended before authentication; run login again")
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				// QR payload is shown only as a terminal image, never as text or structured logs.
				display(item.Code)
			case whatsmeow.QRChannelSuccess.Event:
				notice("Pairing saved. Waiting for the authenticated connection…")
				return c.waitAuthenticated(ctx)
			case whatsmeow.QRChannelScannedWithoutMultidevice.Event:
				notice("Enable linked devices on your phone and scan again.")
			case whatsmeow.QRChannelEventError:
				return fmt.Errorf("QR pairing failed; check the phone and try again")
			default:
				return fmt.Errorf("QR pairing stopped (%s); check the phone and run login again", item.Event)
			}
		}
	}
}
func (c *Client) waitAuthenticated(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-c.fatal:
		return err
	case <-c.connected:
		return nil
	}
}
func (c *Client) Groups(ctx context.Context) ([]storage.Group, error) {
	groups, err := c.client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover WhatsApp groups: %w", err)
	}
	result := make([]storage.Group, 0, len(groups))
	for _, g := range groups {
		result = append(result, storage.Group{ID: g.JID.String(), Name: g.Name})
	}
	return result, nil
}

// Observe keeps connectivity alive for a bounded, explicit diagnostic window.
// No message bodies or history payloads are persisted.
func (c *Client) Observe(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-c.fatal:
		return err
	case <-timer.C:
		return nil
	}
}
func (c *Client) Observation() Observation {
	return Observation{c.live.Load(), c.historyBatches.Load(), c.historyMessages.Load()}
}

func (c *Client) Logout(ctx context.Context) error {
	if err := c.client.Logout(ctx); err != nil {
		if !c.client.IsConnected() {
			c.connection.Store("offline")
		}
		return fmt.Errorf("unlink failed; local session retained unless WhatsApp removed it: %w", err)
	}
	c.connection.Store("unlinked")
	c.health.paired.Store(false)
	return nil
}
