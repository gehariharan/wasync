package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/storage"
)

const (
	// StateFileName holds each endpoint's delivery position. Only the owner
	// process writes it.
	StateFileName = "webhook-state.json"
	stateVersion  = 1

	defaultPoll  = 3 * time.Second
	pageSize     = 100
	pagesPerTick = 10
	tickTimeout  = 45 * time.Second
)

// DefaultBackoff is the wait after each failed attempt. An event gets
// len(DefaultBackoff)+1 attempts, then moves to the dead-letter list.
var DefaultBackoff = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// endpointState is one endpoint's delivery checkpoint: the last journal
// sequence handled (delivered, filtered out or dead-lettered) in one archive
// epoch, plus the retry bookkeeping of the event that follows it.
type endpointState struct {
	Epoch    string     `json:"epoch"`
	Sequence int64      `json:"sequence"`
	Attempts int        `json:"attempts,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
}

type stateFile struct {
	Version   int                      `json:"version"`
	Endpoints map[string]endpointState `json:"endpoints"`
}

// Dispatcher delivers journal entries to the active endpoints. Each endpoint
// has its own checkpoint, so a slow or failing endpoint never delays another.
//
// Delivery is at-least-once and in journal order per endpoint: the checkpoint
// moves only after the receiver answered 2xx (or the event was dead-lettered),
// so a crash re-sends at most the event in flight, with the same event ID.
// A new endpoint starts at the journal head when the owner first sees it; it
// receives no history. Only the owner process runs a Dispatcher.
type Dispatcher struct {
	Dir    string
	Store  *storage.Store
	Sender *Sender
	Audit  *agentauth.AuditLog // optional
	Log    *slog.Logger        // optional
	Poll   time.Duration
	// Backoff is the wait after each failed attempt (DefaultBackoff if nil).
	Backoff []time.Duration
	Now     func() time.Time

	mu       sync.Mutex
	state    *stateFile
	lastCode string
}

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Dispatcher) backoff() []time.Duration {
	if d.Backoff != nil {
		return d.Backoff
	}
	return DefaultBackoff
}

// Run delivers until ctx is done.
func (d *Dispatcher) Run(ctx context.Context) {
	poll := d.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		d.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick makes one delivery pass over every active endpoint.
func (d *Dispatcher) Tick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()
	eps, err := Active(d.Dir)
	if err != nil {
		d.fail("registry_error", err)
		return
	}
	if err = d.loadState(); err != nil {
		d.fail("state_error", err)
		return
	}
	d.prune(eps)
	if len(eps) == 0 {
		return
	}
	account, err := d.Store.Account(ctx)
	if err != nil {
		d.fail("archive_error", err)
		return
	}
	if account == nil {
		return
	}
	var wg sync.WaitGroup
	for _, ep := range eps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.deliver(ctx, account, ep)
		}()
	}
	wg.Wait()
	d.mu.Lock()
	d.lastCode = ""
	d.mu.Unlock()
}

// fail reports a pass-level problem once per distinct code, not every tick.
func (d *Dispatcher) fail(code string, err error) {
	if d.Log != nil {
		d.Log.Warn("webhook delivery pass failed", "code", code, "error", err)
	}
	d.mu.Lock()
	repeat := d.lastCode == code
	d.lastCode = code
	d.mu.Unlock()
	if repeat {
		return
	}
	d.audit("webhook.error", Endpoint{}, "", "", false, code)
}

func (d *Dispatcher) audit(event string, ep Endpoint, chat, ref string, ok bool, code string) {
	if d.Audit == nil {
		return
	}
	outcome := agentauth.OutcomeDenied
	if ok {
		outcome = agentauth.OutcomeAllowed
	}
	if err := d.Audit.Append(agentauth.AuditEntry{Event: event, ClientID: ep.ID, ClientName: ep.Name, Chat: chat, Ref: ref, Outcome: outcome, Code: code}); err != nil && d.Log != nil {
		d.Log.Warn("webhook audit write failed", "error", err)
	}
}

func (d *Dispatcher) deliver(ctx context.Context, account *storage.Account, ep Endpoint) {
	info, err := d.Store.Journal(ctx)
	if err != nil {
		d.fail("archive_error", err)
		return
	}
	st, known := d.get(ep.ID)
	if !known {
		d.put(ep.ID, endpointState{Epoch: info.Epoch, Sequence: info.Head})
		d.audit("webhook.start", ep, "", "", true, "")
		return
	}
	if st.Epoch != info.Epoch || st.Sequence < info.LowWater || st.Sequence > info.Head {
		d.reset(ep, info)
		return
	}
	if st.RetryAt != nil && d.now().Before(*st.RetryAt) {
		return
	}
	for pages := 0; pages < pagesPerTick; pages++ {
		page, err := d.Store.Changes(ctx, account.ID, st.Sequence, pageSize)
		if errors.Is(err, storage.ErrCursorExpired) {
			d.reset(ep, info)
			return
		}
		if err != nil {
			d.fail("archive_error", err)
			return
		}
		for _, c := range page.Changes {
			if !ep.Matches(c) {
				st.Sequence = c.Sequence
				continue
			}
			ev := ChangeEvent(ep, account.JID, page.Epoch, c)
			res := d.Sender.Send(ctx, ep, ev)
			if ctx.Err() != nil {
				d.put(ep.ID, st) // shutting down: leave the in-flight event for the next run
				return
			}
			if res.OK() {
				st.Sequence, st.Attempts, st.RetryAt = c.Sequence, 0, nil
				d.put(ep.ID, st)
				d.audit("webhook.delivered", ep, c.ChatID, ev.Ref, true, res.Code)
				continue
			}
			st.Attempts++
			if res.Retry && st.Attempts <= len(d.backoff()) {
				retryAt := d.now().Add(d.backoff()[st.Attempts-1])
				st.RetryAt = &retryAt
				d.put(ep.ID, st)
				d.audit("webhook.retry", ep, c.ChatID, ev.Ref, false, res.Code)
				return
			}
			err := AppendDeadLetter(d.Dir, DeadLetter{Time: d.now(), EndpointID: ep.ID, EndpointName: ep.Name, EventID: ev.ID, Sequence: c.Sequence, Op: c.Op, ChatID: c.ChatID, Ref: ev.Ref, Attempts: st.Attempts, Code: res.Code, Status: res.Status})
			if err != nil {
				// Without a durable dead-letter record, hold the event and retry.
				d.put(ep.ID, st)
				d.fail("deadletter_error", err)
				return
			}
			st.Sequence, st.Attempts, st.RetryAt = c.Sequence, 0, nil
			d.put(ep.ID, st)
			d.audit("webhook.deadletter", ep, c.ChatID, ev.Ref, false, res.Code)
		}
		d.put(ep.ID, st)
		if !page.HasMore {
			return
		}
	}
}

// reset moves an endpoint to the journal head after its position became
// unservable (restored backup, new epoch, or retention). Entries in the gap
// are not delivered; the audit log records the reset.
func (d *Dispatcher) reset(ep Endpoint, info storage.JournalInfo) {
	d.put(ep.ID, endpointState{Epoch: info.Epoch, Sequence: info.Head})
	d.audit("webhook.cursor_reset", ep, "", "", false, "cursor_expired")
}

func (d *Dispatcher) loadState() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state != nil {
		return nil
	}
	s := &stateFile{Version: stateVersion, Endpoints: map[string]endpointState{}}
	path := filepath.Join(d.Dir, StateFileName)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		d.state = s
		return nil
	}
	if err := localfs.CheckPrivateFile(path); err != nil {
		return fmt.Errorf("%s must be a regular private file: %w", StateFileName, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var loaded stateFile
	if err = dec.Decode(&loaded); err != nil || loaded.Version != stateVersion {
		return fmt.Errorf("invalid %s; remove it to restart delivery from the journal head: %v", StateFileName, err)
	}
	if loaded.Endpoints != nil {
		s.Endpoints = loaded.Endpoints
	}
	d.state = s
	return nil
}

func (d *Dispatcher) get(id string) (endpointState, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.state.Endpoints[id]
	return st, ok
}

// put stores and durably writes one endpoint's position.
func (d *Dispatcher) put(id string, st endpointState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state.Endpoints[id] = st
	d.saveLocked()
}

// prune drops positions of endpoints that were removed.
func (d *Dispatcher) prune(active []Endpoint) {
	keep := map[string]bool{}
	for _, ep := range active {
		keep[ep.ID] = true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	changed := false
	for id := range d.state.Endpoints {
		if !keep[id] {
			delete(d.state.Endpoints, id)
			changed = true
		}
	}
	if changed {
		d.saveLocked()
	}
}

func (d *Dispatcher) saveLocked() {
	data, err := json.MarshalIndent(d.state, "", "  ")
	if err == nil {
		err = writeAtomic(d.Dir, StateFileName, append(data, '\n'))
	}
	if err != nil && d.Log != nil {
		d.Log.Warn("webhook state write failed", "error", err)
	}
}
