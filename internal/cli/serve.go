package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/avatars"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/prefs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/sentlog"
	"github.com/gehariharan/wasync/internal/setup"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/tui"
	"github.com/gehariharan/wasync/internal/webhook"
	"github.com/gehariharan/wasync/internal/whatsapp"
)

// withAPI closes all HTTP handlers before the caller closes the store/client.
func (a *App) withAPI(ctx context.Context, cfg config.Config, store *storage.Store, status func() whatsapp.CaptureStatus, run func(context.Context, string) error, sender ...whatsapp.Sender) (result error) {
	// Webhook delivery belongs to the owner and does not depend on the API.
	stopWebhooks := a.startWebhooks(ctx, cfg, store)
	defer stopWebhooks()
	if cfg.NoAPI {
		return run(ctx, "")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stoppedByRequest atomic.Bool
	owner := api.Owner{Addr: cfg.APIAddr, Dir: cfg.DataDir, Store: store, Status: status, Approvals: a.Approvals, Approve: a.Approve, Reject: a.Reject, Sync: a.Sync, Media: a.MediaDownload, Ops: a.Operations, Human: a.Human, Setup: a.Setup}
	owner.Info = api.OwnerInfo{Kind: a.ownerRec.Kind, PID: a.ownerRec.PID, DataDir: cfg.DataDir, StartedAt: a.ownerRec.StartedAt, StartedBy: a.ownerRec.StartedBy}
	// Being asked to stop is a clean exit, not a cancellation by the person at the keyboard.
	owner.Stop = func() { stoppedByRequest.Store(true); cancel() }
	if client := a.ownerClient; client != nil {
		if p, ok := client.(interface {
			ProfilePicture(context.Context, string, string) (whatsapp.ProfilePicture, error)
		}); ok {
			owner.Pictures = func(ctx context.Context, chat, knownID string) (avatars.Picture, error) {
				pic, err := p.ProfilePicture(ctx, chat, knownID)
				return avatars.Picture{ID: pic.ID, Data: pic.Data, Unchanged: pic.Unchanged}, err
			}
		}
		if p, ok := client.(interface {
			PhoneNumber(context.Context, string) string
		}); ok {
			owner.PhoneOf = p.PhoneNumber
		}
		owner.Paired = client.Paired
		owner.Unlink = func(ctx context.Context) error {
			if !client.Paired() {
				return api.ErrNotLinked
			}
			return client.Logout(ctx)
		}
	}
	if a.Service != nil {
		if spec, err := a.serviceSpec(cfg); err == nil {
			owner.Service, owner.ServiceSpec = a.Service, spec
		}
	}
	if u := a.updater(runCtx, cfg, owner.Stop); u != nil {
		owner.Update = u
	}
	if len(sender) > 0 {
		owner.Sender = sender[0]
	}
	server, err := api.StartOwner(owner)
	if err != nil {
		return err
	}
	// Tell other commands where this owner's API really listens (the port may have been chosen for us).
	if a.ownerRec.PID != 0 {
		a.ownerRec.APIAddr = strings.TrimPrefix(server.URL, "http://")
		if err = processlock.WriteOwner(cfg.DataDir, a.ownerRec); err != nil {
			return errors.Join(err, server.Close())
		}
	}
	defer func() { result = errors.Join(result, server.Close()) }()
	a.NewLink = server.NewLink
	failed := make(chan error, 1)
	go func() {
		select {
		case err := <-server.Errors:
			if err != nil {
				failed <- err
				cancel()
			}
		case <-runCtx.Done():
		}
	}()
	result = run(runCtx, server.URL)
	if stoppedByRequest.Load() && errors.Is(result, context.Canceled) {
		result = nil
	}
	select {
	case err := <-failed:
		result = errors.Join(result, err)
	default:
	}
	return result
}

// configureMedia points a WhatsApp client at the media folder, the
// --download-media flag and the owner's "Also save photos, videos and files"
// setting, which it reads each time an attachment arrives.
func configureMedia(client any, cfg config.Config) {
	if media, ok := client.(interface{ ConfigureMedia(string, bool) }); ok {
		media.ConfigureMedia(filepath.Join(cfg.DataDir, "media"), cfg.DownloadMedia)
	}
	if media, ok := client.(interface{ ConfigureSaveMedia(func() bool) }); ok {
		settings := prefs.Open(cfg.DataDir)
		media.ConfigureSaveMedia(func() bool {
			// A damaged settings file keeps the default, which is on.
			s, _ := settings.Get()
			return s.MediaOn()
		})
	}
}

// startWebhooks runs the webhook dispatcher until the returned stop function
// is called. It idles cheaply when no endpoint is registered.
func (a *App) startWebhooks(ctx context.Context, cfg config.Config, store *storage.Store) (stop func()) {
	sender := a.WebhookSender
	if sender == nil {
		sender = webhook.NewSender(nil)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	d := &webhook.Dispatcher{Dir: cfg.DataDir, Store: store, Sender: sender, Audit: agentauth.NewAuditLog(cfg.DataDir)}
	go func() {
		defer close(done)
		d.Run(runCtx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (a *App) serveAction(offline bool) action { return a.ownerAction(offline, false) }

// setupAction is serve that also links WhatsApp from a page in the browser and
// opens that page, so a first-time person needs no terminal screen.
func (a *App) setupAction() action { return a.ownerAction(false, true) }

func (a *App) ownerAction(offline, setupMode bool) action {
	return func(ctx context.Context, cfg config.Config, store *storage.Store, log *slog.Logger) (result error) {
		if cfg.NoAPI {
			return fmt.Errorf("serve requires the local API; remove --no-api")
		}
		var client Client
		var live tui.Client
		if !offline {
			var err error
			client, err = a.OpenClient(ctx, filepath.Join(cfg.DataDir, "session.db"), log)
			if err != nil {
				return err
			}
			defer func() { result = errors.Join(result, client.Close()) }()
			if !client.Paired() && !setupMode {
				return fmt.Errorf("pair using wasync login first, or use serve --offline for archive queries")
			}
			var ok bool
			live, ok = client.(tui.Client)
			if !ok {
				return fmt.Errorf("this client does not implement capture")
			}
			if history, ok := client.(interface{ ConfigurePairingHistory(bool) }); ok {
				history.ConfigurePairingHistory(cfg.PairingFullHistory)
			}
			configureMedia(client, cfg)
			if lists, ok := client.(interface{ ConfigureLists(string) }); ok {
				lists.ConfigureLists(cfg.DataDir)
			}
			if err = live.EnableCapture(ctx, store); err != nil {
				return err
			}
		}
		// List messages earlier releases sent but did not keep go into their chats, once.
		if n, err := sentlog.Backfill(ctx, cfg.DataDir, store); err != nil {
			log.Warn("earlier list messages were not added to their chats; this is tried again at the next start", "error", err)
		} else if n > 0 {
			log.Info("added earlier list messages to their chats", "messages", n)
		}
		var status func() whatsapp.CaptureStatus
		if live != nil {
			status = live.Status
		}
		var send whatsapp.Sender
		if active, ok := client.(interface {
			Send(context.Context, whatsapp.SendInput) (whatsapp.SendResult, error)
		}); ok {
			send = active.Send
		}
		a.MediaDownload = nil
		a.Setup = nil
		a.ownerClient = client
		if setupMode {
			a.Setup = &setup.State{}
		}
		a.Operations = operationsOf(client)
		a.Approvals = &approval.Queue{OnAccess: func(r approval.Request) {
			// Headless there is no dialog: say who asked and which request to open,
			// never the chat or number. `wasync actions list` shows the details.
			log.Info("chat access requested", "agent", r.Access.Agent, "request_id", r.ID)
			fmt.Fprintf(a.Out, "Chat access requested by agent %s. Review: wasync approvals (page) or wasync actions list; approve: wasync actions approve %s\n", printable(r.Access.Agent), r.ID)
			a.announce("Agent " + printable(r.Access.Agent) + " is asking for a WhatsApp chat. Run wasync approvals to review.")
		}, OnRequest: func(approval.Request) {
			a.announce("A request to delete data is waiting for you. Run wasync approvals to review.")
		}}
		if err := a.Approvals.Persist(cfg.DataDir); err != nil {
			log.Warn("waiting requests were not restored", "error", err)
		}
		session := tui.NewSession(ctx, store, live)
		session.Approvals = a.Approvals
		session.MediaRoot, session.DataDir = filepath.Join(cfg.DataDir, "media"), cfg.DataDir
		session.Human = a.Human
		a.Approve = session.ApproveAction
		a.Reject = session.RejectAction
		a.Sync = session.Sync
		if media, ok := client.(interface {
			RequestMedia(context.Context, string, string) (whatsapp.MediaResult, error)
		}); ok {
			a.MediaDownload = media.RequestMedia
		}
		err := a.withAPI(ctx, cfg, store, status, func(ctx context.Context, url string) (err error) {
			// Being stopped is a clean exit at any point: while linking, while
			// loading the chat list, or while serving. A failure of the local API
			// also stops this, but withAPI still reports that failure itself.
			defer func() {
				if ctx.Err() != nil {
					err = nil
				}
			}()
			fmt.Fprintf(a.Out, "Local API: %s\nBearer token file: %s\n%s\n", url, filepath.Join(cfg.DataDir, "api.token"), a.approvalCheckLine())
			if setupMode {
				a.openSetupPage()
			}
			if client != nil {
				if setupMode {
					err = a.linkWhatsApp(ctx, live, a.Setup)
				} else {
					err = client.Connect(ctx)
				}
				if err == nil {
					discoveryCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
					_, _, err = discover(discoveryCtx, store, client)
					cancel()
				}
				if err != nil {
					return err
				}
			}
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					if live != nil {
						if err := live.Maintain(ctx); err != nil {
							live.StopCapture(err)
							return err
						}
					} else if err := store.Expire(ctx); err != nil {
						return err
					}
				}
			}
		}, send)
		// Exit cleanly in both cases so a login item does not restart into the same prompt.
		if errors.Is(err, approvecred.ErrAccessDenied) {
			const msg = "Approvals are off: wasync was not allowed to read its approval credential from the Keychain. Run wasync again to turn them back on."
			fmt.Fprintln(a.Err, msg)
			a.announce(msg)
			return nil
		}
		if errors.Is(err, approvecred.ErrForeignItem) {
			msg := fmt.Sprintf("Approvals are off: %v.", err)
			fmt.Fprintln(a.Err, msg)
			a.announce(msg)
			return nil
		}
		return err
	}
}

// linkWhatsApp connects with the saved session or, on a first run, shows the
// QR code on the setup page until the phone scans it. When linking stops, the
// page shows why and keeps running until the person asks for a new code. Only
// a short message about a failure reaches the page; the pairing code is never logged.
func (a *App) linkWhatsApp(ctx context.Context, live tui.Client, state *setup.State) error {
	if live == nil {
		return nil
	}
	for {
		err := live.LoginWithQR(ctx, func(code string) { state.SetQR(code) })
		if err == nil {
			state.SetPaired()
			return nil
		}
		state.SetFailure("Linking stopped: " + err.Error())
		select {
		case <-ctx.Done():
			return err
		case <-state.Retried():
		}
	}
}

// openSetupPage opens the setup page in the browser through a one-time link
// that is never printed.
func (a *App) openSetupPage() {
	if a.NewLink == nil {
		return
	}
	if err := a.openLink(a.NewLink("setup")); err != nil {
		fmt.Fprintf(a.Err, "%v; run wasync setup again in a moment to open it.\n", err)
	}
}
