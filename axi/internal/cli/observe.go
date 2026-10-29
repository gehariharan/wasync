package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gehariharan/wasync/axi/internal/config"
	"github.com/gehariharan/wasync/axi/internal/contract"
	"github.com/gehariharan/wasync/axi/internal/installhint"
	"github.com/gehariharan/wasync/axi/internal/output"
	"github.com/gehariharan/wasync/axi/internal/transport"
)

// `observe` follows wasync's change journal (ADR-001 D-4). Positions are
// opaque c1_ cursors issued by wasync, never timestamps. A named consumer
// resumes from its checkpoint, and the checkpoint only moves on an explicit
// `observe ack` or when --commit asks for it. Committing is a write that only
// the running owner's API accepts; offline reads never commit.

const (
	observeDefaultLimit    = 100
	observeMaxLimit        = 1000
	observeDefaultInterval = 2 * time.Second
	observeMinInterval     = 10 * time.Millisecond
	observeMaxInterval     = time.Hour
	observeBackoffFactor   = 8  // idle polls and retries back off up to 8x --interval
	observeMaxEmptySkips   = 20 // empty has_more pages skipped by one read
	observeCursorPrefix    = "c1_"
	observeMaxCursorLen    = 512
)

// Start positions reported in observe output.
const (
	observeFromAfter      = "after"
	observeFromCheckpoint = "checkpoint"
	observeFromOldest     = "oldest"
)

func observeCommand() *command {
	return &command{
		name:        "observe",
		usage:       "whatsapp-axi observe [--consumer NAME] [--after CURSOR | --from-oldest] [--limit N] [--follow [--interval DUR]] [--commit] [--json]; whatsapp-axi observe ack CURSOR --consumer NAME [--json]",
		description: "Read wasync's change journal by opaque cursor, one page or --follow; a named consumer resumes from its checkpoint, which moves only on `observe ack CURSOR --consumer NAME` (running wasync only, never backwards) or --commit",
		flags: []flagSpec{
			{name: "--consumer", value: "NAME", description: "named consumer (a-z, 0-9, -; up to 64); without --after, start after its stored checkpoint; required by `observe ack`"},
			{name: "--after", value: "CURSOR", description: "start after this c1_ change cursor from a previous page; timestamps are never accepted"},
			{name: "--from-oldest", description: "with --consumer: ignore its checkpoint and start at the oldest retained change (after a resnapshot)"},
			{name: "--limit", value: "N", description: "changes per request, 1 to 1000 (default 100)"},
			{name: "--follow", description: "keep polling until interrupted; with --json prints one JSON object per line"},
			{name: "--interval", value: "DUR", description: "--follow poll interval such as 500ms or 5s (default 2s); idle polls and retries back off to 8x"},
			{name: "--commit", description: "commit after printing each page (running wasync only); one-shot JSON adds a separate committed record; default is explicit `observe ack`"},
			jsonFlag,
		},
		maxArgs:  2,
		argsName: "ack CURSOR",
		examples: []string{
			"whatsapp-axi observe",
			"whatsapp-axi observe --consumer replica",
			"whatsapp-axi observe ack c1_... --consumer replica",
			"whatsapp-axi observe --consumer replica --follow --json",
			"whatsapp-axi observe --after c1_... --limit 500",
		},
		run: (*App).runObserve,
	}
}

type observeOptions struct {
	ack        bool
	ackCursor  string
	consumer   string
	after      string
	fromOldest bool
	follow     bool
	commit     bool
	limit      int
	interval   time.Duration
}

const observeAckUsage = "usage: whatsapp-axi observe ack CURSOR --consumer NAME"

// parseObserveArgs checks what the shared flag parser cannot: the `ack`
// subcommand, value ranges and flag combinations.
func parseObserveArgs(inv invocation) (observeOptions, error) {
	o := observeOptions{limit: observeDefaultLimit, interval: observeDefaultInterval}
	args := inv.args
	if len(args) > 0 && args[0] == "ack" {
		o.ack, args = true, args[1:]
	}
	if len(args) > 0 && (!o.ack || len(args) > 1) {
		return o, usageErr("usage: whatsapp-axi observe [flags], or "+observeAckUsage[len("usage: "):], "unexpected argument %q for `observe`", args[len(args)-1])
	}
	if o.ack {
		for name := range inv.flags {
			if name != "--consumer" {
				return o, usageErr(observeAckUsage, "%s does not apply to `observe ack`", name)
			}
		}
		if len(args) == 1 {
			o.ackCursor = args[0]
		}
	}
	o.fromOldest, o.follow, o.commit = inv.has("--from-oldest"), inv.has("--follow"), inv.has("--commit")
	var hasConsumer, hasAfter bool
	o.consumer, hasConsumer = inv.flag("--consumer")
	o.after, hasAfter = inv.flag("--after")
	if hasConsumer {
		if err := checkObserveConsumer(o.consumer); err != nil {
			return o, err
		}
	}
	if hasAfter {
		if err := checkObserveCursor("--after", o.after); err != nil {
			return o, err
		}
	}
	if raw, ok := inv.flag("--limit"); ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > observeMaxLimit {
			return o, usageErr("use --limit 1 to 1000 (default 100)", "--limit must be a number from 1 to %d, got %q", observeMaxLimit, raw)
		}
		o.limit = n
	}
	rawInterval, hasInterval := inv.flag("--interval")
	if hasInterval {
		d, err := time.ParseDuration(rawInterval)
		if err != nil || d < observeMinInterval || d > observeMaxInterval {
			return o, usageErr("use a duration from 10ms to 1h such as 500ms or 5s", "--interval must be a duration from 10ms to 1h, got %q", rawInterval)
		}
		o.interval = d
	}
	if o.ack {
		switch {
		case o.ackCursor == "":
			return o, usageErr(observeAckUsage, "`observe ack` needs the c1_ cursor to commit")
		case o.consumer == "":
			return o, usageErr(observeAckUsage, "`observe ack` needs --consumer NAME")
		}
		return o, checkObserveCursor("CURSOR", o.ackCursor)
	}
	switch {
	case o.after != "" && o.fromOldest:
		return o, usageErr("use --after to continue from a cursor, or --from-oldest to restart, not both", "--after and --from-oldest conflict")
	case o.fromOldest && o.consumer == "":
		return o, usageErr("without --consumer, observe already starts at the oldest retained change", "--from-oldest needs --consumer NAME")
	case o.commit && o.consumer == "":
		return o, usageErr("add --consumer NAME to name the checkpoint to commit", "--commit needs --consumer NAME")
	case hasInterval && !o.follow:
		return o, usageErr("add --follow to poll, or drop --interval", "--interval needs --follow")
	}
	return o, nil
}

func checkObserveConsumer(name string) error {
	ok := len(name) >= 1 && len(name) <= 64
	for _, r := range name {
		ok = ok && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
	}
	if !ok {
		return usageErr("use 1 to 64 characters a-z, 0-9 and -, such as replica", "invalid consumer name %q", name)
	}
	return nil
}

// checkObserveCursor accepts only the shape of an opaque v1 change cursor.
// whatsapp-axi never decodes it; wasync validates it fully.
func checkObserveCursor(what, c string) error {
	ok := strings.HasPrefix(c, observeCursorPrefix) && len(c) > len(observeCursorPrefix) && len(c) <= observeMaxCursorLen
	for _, r := range c {
		ok = ok && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}
	if !ok {
		return usageErr("pass a c1_ cursor from a previous observe page unchanged; timestamps and message ids are not cursors", "%s is not a change cursor (c1_...)", what)
	}
	return nil
}

// observeChange is one journal entry. It identifies a message but never
// carries its content: consumers re-read the message by ref.
type observeChange struct {
	Sequence    int64  `json:"sequence"`
	Cursor      string `json:"cursor"`
	Op          string `json:"op"`
	OpVersion   int    `json:"op_version"`
	ChatID      string `json:"chat_id"`
	MessageID   string `json:"message_id"`
	Ref         string `json:"ref,omitempty"`
	CommittedAt string `json:"committed_at"`
}

func (c *observeChange) sanitize() {
	for _, p := range []*string{&c.Cursor, &c.Op, &c.ChatID, &c.MessageID, &c.Ref, &c.CommittedAt} {
		*p = output.Sanitize(*p)
	}
}

type observeCheckpoint struct {
	Consumer  string  `json:"consumer"`
	Cursor    *string `json:"cursor"`
	UpdatedAt *string `json:"updated_at"`
}

func (cp *observeCheckpoint) cursor() string {
	if cp == nil || cp.Cursor == nil {
		return ""
	}
	return *cp.Cursor
}

type observePage struct {
	Changes    []observeChange
	NextCursor string
	HasMore    bool
}

// observeWriter is the API's checkpoint commit; `wasync query` has none.
type observeWriter interface {
	Put(ctx context.Context, path string, body any, out any) error
}

func observeBadResponse(tr transport.Transport, msg string) error {
	return &contract.Error{Code: contract.CodeInvalidResponse, Message: msg, Help: "check that wasync is up to date: " + installhint.UpgradeCore(), Transport: tr.Name()}
}

func observeFetch(ctx context.Context, tr transport.Transport, after string, limit int) (observePage, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if after != "" {
		q.Set("after", after)
	}
	var resp contract.Response
	if err := tr.Get(ctx, "/changes", q, &resp); err != nil {
		return observePage{}, err
	}
	var data struct {
		Changes []observeChange `json:"changes"`
	}
	if len(resp.Data) == 0 || json.Unmarshal(resp.Data, &data) != nil {
		return observePage{}, observeBadResponse(tr, "wasync changes response has an unexpected shape")
	}
	if resp.Page == nil || checkObserveCursor("next_cursor", resp.Page.NextCursor) != nil {
		return observePage{}, observeBadResponse(tr, "wasync changes response has no valid next_cursor")
	}
	p := observePage{Changes: data.Changes, NextCursor: resp.Page.NextCursor, HasMore: resp.Page.HasMore}
	if p.Changes == nil {
		p.Changes = []observeChange{}
	}
	for i := range p.Changes {
		p.Changes[i].sanitize()
	}
	return p, nil
}

func observeDecodeCheckpoint(tr transport.Transport, cp observeCheckpoint, consumer string) (observeCheckpoint, error) {
	if cp.Consumer != consumer || (cp.Cursor != nil && checkObserveCursor("checkpoint", *cp.Cursor) != nil) {
		return cp, observeBadResponse(tr, "wasync checkpoint response has an unexpected shape")
	}
	if cp.UpdatedAt != nil {
		s := output.Sanitize(*cp.UpdatedAt)
		cp.UpdatedAt = &s
	}
	return cp, nil
}

func observeReadCheckpoint(ctx context.Context, tr transport.Transport, consumer string) (observeCheckpoint, error) {
	var cp observeCheckpoint
	if err := tr.Get(ctx, "/checkpoints/"+consumer, nil, &cp); err != nil {
		return cp, err
	}
	return observeDecodeCheckpoint(tr, cp, consumer)
}

func observeCommit(ctx context.Context, tr transport.Transport, consumer, cursor string) (observeCheckpoint, error) {
	w, ok := tr.(observeWriter)
	if !ok {
		return observeCheckpoint{}, observeNeedsOwner("drop --commit to read offline without committing")
	}
	var cp observeCheckpoint
	if err := w.Put(ctx, "/checkpoints/"+consumer, map[string]string{"cursor": cursor}, &cp); err != nil {
		return cp, err
	}
	return observeDecodeCheckpoint(tr, cp, consumer)
}

func observeNeedsOwner(alternative string) error {
	help := "ask the user to start wasync (the TUI or `wasync serve`); whatsapp-axi never starts it"
	if alternative != "" {
		help += "; or " + alternative
	}
	return &contract.Error{Code: contract.CodeOwnerOffline, Message: "committing a checkpoint needs the running wasync API; offline reads never commit", Help: help, Transport: transport.NameQuery}
}

// observeTransport selects the transport for observe, or explains why
// there is none.
func (a *App) observeTransport(ctx context.Context) (transport.Transport, error) {
	d := a.detect(ctx, "observe")
	switch {
	case d.compatErr != nil:
		return nil, d.compatErr
	case d.sel.Transport != nil:
		return d.sel.Transport, nil
	case d.sel.QueryErr != nil:
		return nil, d.sel.QueryErr
	case d.sel.APIErr != nil:
		return nil, d.sel.APIErr
	}
	return nil, &contract.Error{Code: contract.CodeOwnerOffline, Message: "wasync is not reachable", Help: "run `whatsapp-axi` to see why"}
}

// observeExplain replaces wasync's generic help on errors observe users
// commonly hit with the whatsapp-axi command that fixes them.
func observeExplain(err error, o observeOptions) error {
	var ce *contract.Error
	if !errors.As(err, &ce) {
		return err
	}
	e := *ce
	switch {
	case e.Code == contract.CodeCursorExpired && o.consumer != "":
		e.Help = "Resnapshot: re-read the chats and messages you replicate, then run `whatsapp-axi observe --consumer " + o.consumer + " --from-oldest` and ack its next_cursor (or add --commit) to replace the expired checkpoint"
	case e.Code == contract.CodeCursorExpired:
		e.Help = "Resnapshot: re-read the chats and messages you replicate, then run `whatsapp-axi observe` without --after to start at the oldest retained change"
	case e.Code == contract.CodeInvalidCursor:
		e.Help = "pass a c1_ cursor from a previous observe page unchanged, or run `whatsapp-axi observe` without --after"
	case e.Code == contract.CodeInvalidArgument && strings.Contains(e.Message, "backwards"):
		e.Help = "checkpoints only move forward; run `whatsapp-axi observe --consumer " + o.consumer + "` to see the stored checkpoint, then ack a later cursor"
	case e.Code == contract.CodeCredentialAbsent:
		e.Help = "ask a human to run `wasync agents grant <name> --scope read.chats,read.messages,observe`" + config.SaveHelp
	default:
		return err
	}
	return &e
}

func (a *App) runObserve(ctx context.Context, inv invocation) error {
	o, err := parseObserveArgs(inv)
	if err != nil {
		return err
	}
	if o.ack {
		return a.runObserveAck(ctx, inv.json, o)
	}
	tr, err := a.observeTransport(ctx)
	if err != nil {
		return err
	}
	if _, ok := tr.(observeWriter); o.commit && !ok {
		return observeNeedsOwner("drop --commit to read offline without committing")
	}
	st := observeStart{Transport: tr.Name(), Consumer: o.consumer, Start: observeFromOldest}
	if o.consumer != "" {
		cp, err := observeReadCheckpoint(ctx, tr, o.consumer)
		if err != nil {
			return observeExplain(err, o)
		}
		st.Checkpoint = &cp
	}
	switch {
	case o.after != "":
		st.Start, st.After = observeFromAfter, o.after
	case !o.fromOldest && st.Checkpoint.cursor() != "":
		st.Start, st.After = observeFromCheckpoint, st.Checkpoint.cursor()
	}
	if o.follow {
		return a.observeFollow(ctx, inv.json, o, tr, st)
	}
	return a.observeOnce(ctx, inv.json, o, tr, st)
}

// observeStart is where a read begins and what it is relative to.
type observeStart struct {
	Transport  string             `json:"transport"`
	Consumer   string             `json:"consumer,omitempty"`
	Checkpoint *observeCheckpoint `json:"checkpoint,omitempty"`
	Start      string             `json:"start"` // after, checkpoint or oldest
	After      string             `json:"after"` // "" when starting at the oldest retained change
}

func (st observeStart) write(w *output.Writer) {
	w.Field(0, "transport", st.Transport)
	if st.Consumer != "" {
		w.Field(0, "consumer", st.Consumer)
		if c := st.Checkpoint.cursor(); c != "" {
			w.Field(0, "checkpoint", c)
		} else {
			w.Field(0, "checkpoint", "none")
		}
	}
	w.Field(0, "start", st.Start)
	if st.After != "" {
		w.Field(0, "after", st.After)
	}
}

var observeFields = []string{"sequence", "op", "chat_id", "message_id", "ref", "committed_at"}

func writeObserveChanges(w *output.Writer, changes []observeChange) {
	rows := make([][]string, 0, len(changes))
	for _, c := range changes {
		rows = append(rows, []string{strconv.FormatInt(c.Sequence, 10), c.Op, c.ChatID, c.MessageID, c.Ref, c.CommittedAt})
	}
	w.Table(0, "changes", observeFields, rows)
}

const (
	observeOfflineNote = "offline read through wasync query: unscoped and read-only; checkpoints are never committed offline"
	observeContentHint = "Changes carry no message content: re-read each ref for the current message, and drop your copy when it is no longer returned"
)

func (a *App) observeOnce(ctx context.Context, jsonMode bool, o observeOptions, tr transport.Transport, st observeStart) error {
	cursor := st.After
	var page observePage
	for skips := 0; ; skips++ {
		p, err := observeFetch(ctx, tr, cursor, o.limit)
		if err != nil {
			return observeExplain(err, o)
		}
		page = p
		// An allowlisted agent can get empty pages that still advance; skip
		// a bounded number of them rather than print nothing.
		if len(p.Changes) > 0 || !p.HasMore || skips >= observeMaxEmptySkips {
			break
		}
		cursor = p.NextCursor
	}
	hints := observeOnceHints(o, tr, page)
	w := output.New(a.Out)
	if jsonMode {
		if err := output.JSON(a.Out, struct {
			observeStart
			Changes    []observeChange `json:"changes"`
			NextCursor string          `json:"next_cursor"`
			HasMore    bool            `json:"has_more"`
			Help       []string        `json:"help"`
		}{st, page.Changes, page.NextCursor, page.HasMore, hints}); err != nil {
			return err
		}
	} else {
		st.write(w)
		writeObserveChanges(w, page.Changes)
		w.Field(0, "next_cursor", page.NextCursor)
		w.Field(0, "has_more", strconv.FormatBool(page.HasMore))
		if tr.Name() == transport.NameQuery {
			w.Field(0, "note", observeOfflineNote)
		}
		w.Help(hints...)
		if err := w.Err(); err != nil {
			return err
		}
	}
	if o.commit && page.NextCursor != st.Checkpoint.cursor() {
		cp, err := observeCommit(ctx, tr, o.consumer, page.NextCursor)
		if err != nil {
			return observeExplain(err, o)
		}
		hint := "Checkpoint committed: `whatsapp-axi observe --consumer " + o.consumer + "` resumes after it"
		if jsonMode {
			return output.JSON(a.Out, struct {
				Committed observeCheckpoint `json:"committed"`
				Help      []string          `json:"help"`
			}{cp, []string{hint}})
		}
		w.Field(0, "committed", cp.cursor())
		w.Help(hint)
	}
	return w.Err()
}

func observeOnceHints(o observeOptions, tr transport.Transport, page observePage) []string {
	var h []string
	if len(page.Changes) > 0 {
		h = append(h, observeContentHint)
	}
	consumerFlag := ""
	if o.consumer != "" {
		consumerFlag = " --consumer " + o.consumer
	}
	switch {
	case page.HasMore:
		h = append(h, "More changes are waiting: run `whatsapp-axi observe --after "+page.NextCursor+consumerFlag+"`")
	case len(page.Changes) == 0:
		h = append(h, "No changes after this position yet; run again later, or add --follow to wait for them")
	}
	_, online := tr.(observeWriter)
	switch {
	case o.consumer != "" && online && !o.commit && len(page.Changes) > 0:
		h = append(h, "After processing, save progress with `whatsapp-axi observe ack "+page.NextCursor+" --consumer "+o.consumer+"`; `whatsapp-axi observe --consumer "+o.consumer+"` then resumes there")
	case o.consumer != "" && !online:
		h = append(h, "Checkpoints can only be committed while wasync is running; ask the user to start it to save progress")
	case o.consumer == "" && len(page.Changes) > 0:
		h = append(h, "Add --consumer NAME to keep a checkpoint that survives restarts")
	}
	return h
}

func (a *App) runObserveAck(ctx context.Context, jsonMode bool, o observeOptions) error {
	tr, err := a.observeTransport(ctx)
	if err != nil {
		return err
	}
	if _, ok := tr.(observeWriter); !ok {
		return observeNeedsOwner("")
	}
	cp, err := observeCommit(ctx, tr, o.consumer, o.ackCursor)
	if err != nil {
		return observeExplain(err, o)
	}
	hints := []string{"`whatsapp-axi observe --consumer " + o.consumer + "` now resumes after this checkpoint"}
	if jsonMode {
		return output.JSON(a.Out, map[string]any{"transport": tr.Name(), "checkpoint": cp, "help": hints})
	}
	w := output.New(a.Out)
	w.Field(0, "transport", tr.Name())
	w.Field(0, "consumer", cp.Consumer)
	w.Field(0, "checkpoint", cp.cursor())
	if cp.UpdatedAt != nil {
		w.Field(0, "updated_at", *cp.UpdatedAt)
	}
	w.Help(hints...)
	return w.Err()
}

// observeEvent is one --follow --json line.
type observeEvent struct {
	Type string `json:"type"` // start, change, page, committed, retry, stopped
	*observeStart
	*observeChange
	Checkpoint *observeCheckpoint `json:"checkpoint,omitempty"`
	NextCursor string             `json:"next_cursor,omitempty"`
	HasMore    *bool              `json:"has_more,omitempty"`
	Count      *int               `json:"count,omitempty"`
	ErrorCode  string             `json:"error_code,omitempty"`
	Error      string             `json:"error,omitempty"`
	RetryIn    string             `json:"retry_in,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	Help       []string           `json:"help,omitempty"`
}

// observeCommitTimeout bounds a checkpoint commit that outlives cancellation.
const observeCommitTimeout = 10 * time.Second

func observeSleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// observeTransient reports errors --follow retries with backoff.
func observeTransient(err error) bool {
	switch errCode(err) {
	case contract.CodeOwnerOffline, contract.CodeTransport, contract.CodeRateLimited, contract.CodeArchiveUnavailable, contract.CodeInternal:
		return true
	}
	return false
}

// observeFollow polls until ctx is canceled. Pages with has_more are read
// back to back; empty polls back off from --interval to 8x, and transient
// errors are retried with the same backoff. Cancellation is a clean stop
// (exit 0) that prints the cursor to resume from.
func (a *App) observeFollow(ctx context.Context, jsonMode bool, o observeOptions, tr transport.Transport, st observeStart) error {
	w := output.New(a.Out)
	emit := func(ev observeEvent) error {
		if jsonMode {
			return output.JSON(a.Out, ev)
		}
		return nil
	}
	if jsonMode {
		if err := emit(observeEvent{Type: "start", observeStart: &st, Checkpoint: st.Checkpoint}); err != nil {
			return err
		}
	} else {
		st.write(w)
		w.Field(0, "follow", "polling every "+o.interval.String()+" until interrupted")
		if tr.Name() == transport.NameQuery {
			w.Field(0, "note", observeOfflineNote)
		}
		if err := w.Err(); err != nil {
			return err
		}
	}
	maxDelay := o.interval * observeBackoffFactor
	cursor, committed, want := st.After, st.Checkpoint.cursor(), ""
	idle, errDelay, wait := o.interval, o.interval, time.Duration(0)

	stopped := func() error {
		hint := "Resume with `whatsapp-axi observe --after " + cursor + " --follow`"
		switch {
		case cursor == "":
			hint = "Resume with `whatsapp-axi observe --follow`"
		case o.consumer != "" && committed == cursor:
			hint = "Resume with `whatsapp-axi observe --consumer " + o.consumer + " --follow`"
		case o.consumer != "":
			hint = "Ack processed changes with `whatsapp-axi observe ack CURSOR --consumer " + o.consumer + "`, or resume with `whatsapp-axi observe --after " + cursor + " --consumer " + o.consumer + " --follow`"
		}
		if jsonMode {
			return emit(observeEvent{Type: "stopped", NextCursor: cursor, Reason: "interrupted", Help: []string{hint}})
		}
		w.Field(0, "stopped", "interrupted")
		if cursor != "" {
			w.Field(0, "next_cursor", cursor)
		}
		w.Help(hint)
		return w.Err()
	}
	// retry reports a transient error and schedules the next attempt.
	retry := func(err error) error {
		wait, errDelay = errDelay, min(errDelay*2, maxDelay)
		e := output.ToErrorJSON(err)
		if jsonMode {
			return emit(observeEvent{Type: "retry", ErrorCode: e.ErrorCode, Error: output.Sanitize(e.Error), RetryIn: wait.String()})
		}
		w.Field(0, "retry", e.ErrorCode+": "+e.Error+"; next attempt in "+wait.String())
		return w.Err()
	}
	// commitPending commits want; it reports whether the loop must wait.
	commitPending := func() (bool, error) {
		if want == "" || want == committed {
			return false, nil
		}
		// Finish a commit once started even if the run is being cancelled, so
		// every checkpoint the server stores is also reported to the caller.
		cctx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), observeCommitTimeout)
		cp, err := observeCommit(cctx, tr, o.consumer, want)
		cancelCommit()
		if err != nil {
			if ctx.Err() == nil && observeTransient(err) {
				return true, retry(err)
			}
			return false, err
		}
		committed = want
		if jsonMode {
			return false, emit(observeEvent{Type: "committed", Checkpoint: &cp})
		}
		w.Field(0, "committed", committed)
		return false, w.Err()
	}

	for {
		if !observeSleep(ctx, wait) {
			return stopped()
		}
		if retrying, err := commitPending(); ctx.Err() != nil {
			return stopped()
		} else if err != nil {
			return observeExplain(err, o)
		} else if retrying {
			continue
		}
		page, err := observeFetch(ctx, tr, cursor, o.limit)
		if ctx.Err() != nil {
			return stopped()
		}
		if err != nil {
			if !observeTransient(err) {
				return observeExplain(err, o)
			}
			if err := retry(err); err != nil {
				return err
			}
			continue
		}
		errDelay = o.interval
		if len(page.Changes) > 0 {
			if err := a.observeEmitPage(w, jsonMode, page); err != nil {
				return err
			}
		}
		cursor = page.NextCursor
		if o.commit && (len(page.Changes) > 0 || !page.HasMore) {
			want = cursor
		}
		switch {
		case page.HasMore:
			wait = 0
		case len(page.Changes) > 0:
			idle, wait = o.interval, o.interval
		default:
			wait, idle = idle, min(idle*2, maxDelay)
		}
		if retrying, err := commitPending(); ctx.Err() != nil {
			return stopped()
		} else if err != nil {
			return observeExplain(err, o)
		} else if retrying {
			continue
		}
	}
}

func (a *App) observeEmitPage(w *output.Writer, jsonMode bool, page observePage) error {
	if !jsonMode {
		writeObserveChanges(w, page.Changes)
		w.Field(0, "next_cursor", page.NextCursor)
		return w.Err()
	}
	for i := range page.Changes {
		if err := output.JSON(a.Out, observeEvent{Type: "change", observeChange: &page.Changes[i]}); err != nil {
			return err
		}
	}
	n, more := len(page.Changes), page.HasMore
	return output.JSON(a.Out, observeEvent{Type: "page", NextCursor: page.NextCursor, HasMore: &more, Count: &n})
}
