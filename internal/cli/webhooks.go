package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/webhook"
)

// webhooksUsage is the full reference for webhook management, appended to the
// catalog help of webhooks and its subcommands.
const webhooksUsage = `Usage: wasync webhooks add NAME --url https://HOST/PATH [--chat CHAT_JID ...] [--op OP,OP]
       wasync webhooks list [--json]
       wasync webhooks remove NAME
       wasync webhooks test NAME
       wasync webhooks deadletter [NAME] [--json] [--clear]

Push change-journal notifications to an HTTPS endpoint. Human only: every
subcommand (add, list, remove, test, deadletter) requires an interactive terminal
(add and remove also need the endpoint name typed back); there is no --yes, no agent scope grants
this, and none of these commands exists on the API. They edit webhooks.json in
the data directory and never connect to WhatsApp. A running tui or serve owner
reloads the list within seconds; delivery runs only while an owner is running.

Events carry identifiers only: event ID, journal sequence, epoch, cursor,
operation, chat ID, message ID and message ref. They never carry message text
or media. The receiver fetches content with an agent credential (read.messages)
from GET /api/v1/messages/{ref}; the message may no longer exist. Chat IDs
contain phone numbers or group IDs, so limit an endpoint with --chat when the
receiver should not see every chat.

--url must be https with a public hostname. Loopback, private, link-local,
carrier-grade NAT and cloud-metadata addresses are refused, both when the name
is resolved at delivery time and for IP literals; redirects are never followed
and proxy environment variables are ignored.
--chat (repeatable) limits events to those chats. --op limits operations (comma
separated): message.created, message.updated, message.revoked, message.expired,
message.purged, capture.enabled, capture.disabled. Default: all.

add prints the signing secret once (whsec_...). It is stored in webhooks.json,
readable only by your OS user, because HMAC signing needs the raw secret.
Each request carries Wasync-Webhook-Id, Wasync-Webhook-Timestamp (Unix seconds)
and Wasync-Webhook-Signature: v1=HEX, the HMAC-SHA256 of TIMESTAMP "." BODY.
Reject timestamps more than 5 minutes off and dedupe on the event ID: delivery
is at-least-once and in order per endpoint. A 2xx answer acknowledges; 5xx, 408,
429 and network errors retry after 5s, 30s, 2m and 10m; other answers and
exhausted retries move the event to the dead-letter list and delivery goes on.
A new endpoint receives events from when the owner first sees it, not history.

test sends a signed ping event now, from this process, and prints the outcome.
deadletter lists events given up on (identifiers and outcomes only);
--clear empties the list. Delivery attempts and outcomes are written to
agent-audit.log, never bodies or secrets.
`

func (a *App) runWebhooks(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args) || args[0] == "help" {
		_, err := fmt.Fprint(a.Out, webhooksUsage)
		return err
	}
	cfg, err := cfg.Validate()
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		return a.webhooksAdd(ctx, cfg, args[1:])
	case "list":
		return a.webhooksList(cfg, args[1:])
	case "remove":
		return a.webhooksRemove(ctx, cfg, args[1:])
	case "test":
		return a.webhooksTest(ctx, cfg, args[1:])
	case "deadletter":
		return a.webhooksDeadLetter(cfg, args[1:])
	}
	return fmt.Errorf("usage: wasync webhooks add|list|remove|test|deadletter; see wasync webhooks --help")
}

func (a *App) webhooksAdd(ctx context.Context, cfg config.Config, args []string) error {
	name, rest := splitName(args)
	fs := a.flags("webhooks add")
	fs.Usage = func() { fmt.Fprint(fs.Output(), webhooksUsage) }
	target := fs.String("url", "", "https endpoint URL")
	var chats, ops stringList
	fs.Var(&chats, "chat", "only events of this chat JID (repeatable)")
	fs.Var(&ops, "op", "comma-separated operations (repeatable)")
	if err := a.parseFlags(fs, rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 || name == "" || *target == "" {
		return fmt.Errorf("usage: wasync webhooks add NAME --url https://HOST/PATH [--chat CHAT_JID ...] [--op OP,OP]")
	}
	req := webhook.AddRequest{Name: name, URL: *target, Chats: chats, Ops: ops}
	if err := webhook.ValidateAdd(req); err != nil {
		return err
	}
	if !a.Interactive {
		return fmt.Errorf("webhooks add requires an interactive terminal; no endpoint created")
	}
	if existing, err := webhook.ActiveByName(cfg.DataDir, name); err != nil {
		return err
	} else if existing != nil {
		return fmt.Errorf("endpoint %s already exists; remove it first with wasync webhooks remove %s", name, name)
	}
	fmt.Fprintf(a.Err, "Add webhook endpoint %s\nURL: %s\n", name, *target)
	if len(chats) > 0 {
		fmt.Fprintf(a.Err, "Chats: %s\n", strings.Join(chats, ", "))
	} else {
		fmt.Fprintln(a.Err, "Chats: all (every chat's identifiers leave this machine)")
	}
	if len(ops) > 0 {
		fmt.Fprintf(a.Err, "Operations: %s\n", strings.Join(ops, ","))
	} else {
		fmt.Fprintln(a.Err, "Operations: all")
	}
	fmt.Fprintf(a.Err, "Events carry identifiers only, never message text. Type %s then Enter to add: ", name)
	line, err := readLine(ctx, a.In)
	if err != nil {
		return err
	}
	if line != name {
		return fmt.Errorf("confirmation did not match; no endpoint created")
	}
	secret, ep, err := webhook.Add(cfg.DataDir, req, time.Now())
	if err != nil {
		return err
	}
	auditErr := agentauth.NewAuditLog(cfg.DataDir).Append(agentauth.AuditEntry{Event: "webhook.add", ClientID: ep.ID, ClientName: ep.Name, Outcome: agentauth.OutcomeAllowed})
	fmt.Fprintf(a.Err, "WARNING: the signing secret is shown once here; it is also stored in %s, readable only by you. Give it to the receiver and verify signatures. Remove with: wasync webhooks remove %s\n", webhook.FileName, ep.Name)
	if _, err = fmt.Fprintln(a.Out, secret); err != nil {
		return err
	}
	if auditErr != nil {
		fmt.Fprintf(a.Err, "warning: could not write the audit log: %v\n", auditErr)
	}
	return nil
}

func (a *App) webhooksRemove(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: wasync webhooks remove NAME")
	}
	name := args[0]
	if err := agentauth.ValidName(name); err != nil {
		return err
	}
	if !a.Interactive {
		return fmt.Errorf("webhooks remove requires an interactive terminal; nothing removed")
	}
	existing, err := webhook.ActiveByName(cfg.DataDir, name)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("no active endpoint named %s", name)
	}
	fmt.Fprintf(a.Err, "Remove webhook endpoint %s (%s); delivery stops and its secret is deleted.\nType %s then Enter to remove: ", name, existing.ID, name)
	line, err := readLine(ctx, a.In)
	if err != nil {
		return err
	}
	if line != name {
		return fmt.Errorf("confirmation did not match; nothing removed")
	}
	ep, err := webhook.Remove(cfg.DataDir, name, time.Now())
	if err != nil {
		return err
	}
	if auditErr := agentauth.NewAuditLog(cfg.DataDir).Append(agentauth.AuditEntry{Event: "webhook.remove", ClientID: ep.ID, ClientName: ep.Name, Outcome: agentauth.OutcomeAllowed}); auditErr != nil {
		fmt.Fprintf(a.Err, "warning: could not write the audit log: %v\n", auditErr)
	}
	_, err = fmt.Fprintf(a.Out, "removed %s\n", name)
	return err
}

type webhookView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	URL       string     `json:"url"`
	Chats     []string   `json:"chats"`
	Ops       []string   `json:"ops"`
	CreatedAt time.Time  `json:"created_at"`
	RemovedAt *time.Time `json:"removed_at"`
}

func (a *App) webhooksList(cfg config.Config, args []string) error {
	fs := a.flags("webhooks list")
	fs.Usage = func() { fmt.Fprint(fs.Output(), webhooksUsage) }
	asJSON := fs.Bool("json", false, "print JSON")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync webhooks list [--json]")
	}
	if !a.Interactive {
		return fmt.Errorf("webhooks list requires an interactive terminal; endpoint URLs may carry tokens")
	}
	endpoints, err := webhook.List(cfg.DataDir)
	if err != nil {
		return err
	}
	return writeWebhooks(a.Out, endpoints, *asJSON)
}

func writeWebhooks(out io.Writer, endpoints []webhook.Endpoint, asJSON bool) error {
	views := make([]webhookView, 0, len(endpoints))
	for _, e := range endpoints {
		status := "active"
		if !e.Active() {
			status = "removed"
		}
		views = append(views, webhookView{ID: e.ID, Name: e.Name, Status: status, URL: e.URL, Chats: nonNil(e.Chats), Ops: nonNil(e.Ops), CreatedAt: e.CreatedAt, RemovedAt: e.RemovedAt})
	}
	if asJSON {
		return json.NewEncoder(out).Encode(struct {
			Webhooks []webhookView `json:"webhooks"`
		}{views})
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tSTATUS\tURL\tCHATS\tOPS\tCREATED")
	for _, v := range views {
		chats, ops := "all", "all"
		if len(v.Chats) > 0 {
			chats = strings.Join(v.Chats, ",")
		}
		if len(v.Ops) > 0 {
			ops = strings.Join(v.Ops, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Name, v.ID, v.Status, v.URL, chats, ops, v.CreatedAt.UTC().Format(time.RFC3339))
	}
	return w.Flush()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (a *App) webhooksTest(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: wasync webhooks test NAME")
	}
	if !a.Interactive {
		return fmt.Errorf("webhooks test requires an interactive terminal; nothing sent")
	}
	ep, err := webhook.ActiveByName(cfg.DataDir, args[0])
	if err != nil {
		return err
	}
	if ep == nil {
		return fmt.Errorf("no active endpoint named %s", args[0])
	}
	ev, err := webhook.PingEvent(*ep, time.Now())
	if err != nil {
		return err
	}
	sender := a.WebhookSender
	if sender == nil {
		sender = webhook.NewSender(nil)
	}
	res := sender.Send(ctx, *ep, ev)
	audit := agentauth.AuditEntry{Event: "webhook.test", ClientID: ep.ID, ClientName: ep.Name, Ref: ev.ID, Code: res.Code, Outcome: agentauth.OutcomeDenied}
	if res.OK() {
		audit.Outcome = agentauth.OutcomeAllowed
	}
	if err = agentauth.NewAuditLog(cfg.DataDir).Append(audit); err != nil {
		fmt.Fprintf(a.Err, "warning: could not write the audit log: %v\n", err)
	}
	status := ""
	if res.Status != 0 {
		status = fmt.Sprintf(" (HTTP %d %s)", res.Status, http.StatusText(res.Status))
	}
	if !res.OK() {
		return fmt.Errorf("test ping to %s failed: %s%s", ep.Name, res.Code, status)
	}
	_, err = fmt.Fprintf(a.Out, "delivered ping %s to %s%s\n", ev.ID, ep.Name, status)
	return err
}

func (a *App) webhooksDeadLetter(cfg config.Config, args []string) error {
	name, rest := splitName(args)
	fs := a.flags("webhooks deadletter")
	fs.Usage = func() { fmt.Fprint(fs.Output(), webhooksUsage) }
	asJSON := fs.Bool("json", false, "print JSON")
	clear := fs.Bool("clear", false, "empty the dead-letter list")
	if err := a.parseFlags(fs, rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync webhooks deadletter [NAME] [--json] [--clear]")
	}
	if *clear {
		if name != "" {
			return fmt.Errorf("--clear empties the whole list; omit NAME")
		}
		if !a.Interactive {
			return fmt.Errorf("webhooks deadletter --clear requires an interactive terminal; nothing cleared")
		}
		n, err := webhook.ClearDeadLetters(cfg.DataDir)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(a.Out, "cleared %d dead-letter entries\n", n)
		return err
	}
	if !a.Interactive {
		return fmt.Errorf("webhooks deadletter requires an interactive terminal; entries include endpoint URLs and chat IDs")
	}
	id := ""
	if name != "" {
		endpoints, err := webhook.List(cfg.DataDir)
		if err != nil {
			return err
		}
		for _, e := range endpoints {
			if e.Name == name {
				id = e.ID
			}
		}
		if id == "" {
			return fmt.Errorf("no endpoint named %s", name)
		}
	}
	entries, err := webhook.ReadDeadLetters(cfg.DataDir, id)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(a.Out).Encode(struct {
			DeadLetters []webhook.DeadLetter `json:"dead_letters"`
		}{entries})
	}
	w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tENDPOINT\tEVENT\tOP\tCHAT\tATTEMPTS\tCODE\tHTTP")
	for _, d := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%d\n", d.Time.UTC().Format(time.RFC3339), d.EndpointName, d.EventID, d.Op, d.ChatID, d.Attempts, d.Code, d.Status)
	}
	return w.Flush()
}
