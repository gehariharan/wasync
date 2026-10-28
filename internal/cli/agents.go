package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
)

// agentsUsage is the full reference for agent credential management, appended
// to the catalog help of agents and its subcommands.
const agentsUsage = `Usage: wasync agents grant NAME [--scope S1,S2] [--chat CHAT_JID ...] [--on-demand] [--expires 720h] [--rate N]
       wasync agents list [--json]
       wasync agents revoke NAME

Manage scoped credentials for agents using the local /api/v1. Human only:
grant and revoke require an interactive terminal and typing the client name;
there is no --yes, and none of these commands is available over the API.
They edit agents.json in the data directory, work whether or not an owner is
running (a running owner reloads changes), and never connect to WhatsApp.

NAME is 1-32 characters of a-z, 0-9 and -.
Scopes: read.chats, read.messages, read.media, observe, send, react, edit.own,
chat.state, group.admin, capture.manage, export. Without --scope the client
gets read.chats,read.messages. There is no destructive scope: clean, delete,
revoke and purge always need human approval.
--chat (repeatable) limits the client to listed chats; it is required for
send, react, edit.own, chat.state, group.admin and capture.manage.
--on-demand starts the client with no chats instead of "all": it can only ask
(whatsapp-axi access request) and you approve each chat, its access level (read,
and separately send) and its history depth in the TUI or with wasync actions
approve. --chat still pre-approves chats; the two combine. Granted chats show
in agents list. Revoke and re-grant the client to remove them.
--rate is requests per minute (default 60). --expires is a duration; omit it
for no expiry.

grant prints the credential once. Only its hash is stored; it cannot be shown
again. list never prints credentials or hashes. Legacy api.token, send.token
and approve.token are never accepted as agent credentials.
`

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func isHelpArg(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "-help" {
			return true
		}
	}
	return false
}

func (a *App) runAgents(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args) || args[0] == "help" {
		_, err := fmt.Fprint(a.Out, agentsUsage)
		return err
	}
	cfg, err := cfg.Validate()
	if err != nil {
		return err
	}
	switch args[0] {
	case "grant":
		return a.agentsGrant(ctx, cfg, args[1:])
	case "list":
		return a.agentsList(cfg, args[1:])
	case "revoke":
		return a.agentsRevoke(ctx, cfg, args[1:])
	}
	return fmt.Errorf("usage: wasync agents grant|list|revoke; see wasync agents --help")
}

// The flag package stops at the first positional argument, so accept NAME
// either before or after the flags.
func splitName(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func (a *App) agentsGrant(ctx context.Context, cfg config.Config, args []string) error {
	name, rest := splitName(args)
	fs := a.flags("agents grant")
	fs.Usage = func() { fmt.Fprint(fs.Output(), agentsUsage) }
	var scopes, chats stringList
	fs.Var(&scopes, "scope", "comma-separated scopes (repeatable)")
	fs.Var(&chats, "chat", "allowed chat JID (repeatable)")
	onDemand := fs.Bool("on-demand", false, "start with no chats; approve each chat when the agent asks")
	expires := fs.Duration("expires", 0, "credential lifetime, e.g. 720h")
	rate := fs.Int("rate", agentauth.DefaultRatePerMinute, "requests per minute")
	if err := a.parseFlags(fs, rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 || name == "" {
		return fmt.Errorf("usage: wasync agents grant NAME [--scope S1,S2] [--chat CHAT_JID ...] [--on-demand] [--expires 720h] [--rate N]")
	}
	if *expires < 0 || (*expires > 0 && *expires < time.Minute) {
		return fmt.Errorf("--expires must be at least 1m")
	}
	req := agentauth.GrantRequest{Name: name, Scopes: scopes, Chats: chats, OnDemand: *onDemand, RatePerMinute: *rate, Expires: *expires}
	if *rate == 0 {
		return fmt.Errorf("--rate must be between 1 and %d", agentauth.MaxRatePerMinute)
	}
	if err := agentauth.ValidateGrant(req); err != nil {
		return err
	}
	if !a.Interactive {
		return fmt.Errorf("agents grant requires an interactive terminal; no credential created")
	}
	if err := localfs.EnsureDir(cfg.DataDir); err != nil {
		return err
	}
	if existing, err := agentauth.ActiveByName(cfg.DataDir, name); err != nil {
		return err
	} else if existing != nil {
		return fmt.Errorf("client %s already exists; revoke it first with wasync agents revoke %s", name, name)
	}
	normalizedScopes, _ := agentauth.ParseScopes(scopes...)
	fmt.Fprintf(a.Err, "Grant agent client %s\nScopes: %s\n", name, strings.Join(normalizedScopes, ","))
	if len(chats) > 0 {
		fmt.Fprintf(a.Err, "Chats: %s\n", strings.Join(chats, ", "))
	}
	if *onDemand {
		fmt.Fprintln(a.Err, "Chats: only those you approve when the agent asks (on demand)")
	} else if len(chats) == 0 {
		fmt.Fprintln(a.Err, "Chats: all (no allowlist)")
	}
	fmt.Fprintf(a.Err, "Rate: %d/min\n", *rate)
	if *expires > 0 {
		fmt.Fprintf(a.Err, "Expires: in %s\n", *expires)
	}
	fmt.Fprintf(a.Err, "Type %s then Enter to grant: ", name)
	line, err := readLine(ctx, a.In)
	if err != nil {
		return err
	}
	if line != name {
		return fmt.Errorf("confirmation did not match; no credential created")
	}
	credential, client, err := agentauth.Grant(cfg.DataDir, req, time.Now())
	if err != nil {
		return err
	}
	auditErr := agentauth.NewAuditLog(cfg.DataDir).Append(agentauth.AuditEntry{Event: "grant", ClientID: client.ID, ClientName: client.Name, Scope: strings.Join(client.Scopes, ","), Outcome: agentauth.OutcomeAllowed})
	fmt.Fprintf(a.Err, "WARNING: this credential is shown once and cannot be recovered. Store it in the agent's secret store; anyone holding it acts with these scopes. Revoke with: wasync agents revoke %s\n", client.Name)
	if _, err = fmt.Fprintln(a.Out, credential); err != nil {
		return err
	}
	if auditErr != nil {
		fmt.Fprintf(a.Err, "warning: could not write the audit log: %v\n", auditErr)
	}
	return nil
}

func (a *App) agentsRevoke(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: wasync agents revoke NAME")
	}
	name := args[0]
	if err := agentauth.ValidName(name); err != nil {
		return err
	}
	if !a.Interactive {
		return fmt.Errorf("agents revoke requires an interactive terminal; nothing revoked")
	}
	existing, err := agentauth.ActiveByName(cfg.DataDir, name)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("no active client named %s", name)
	}
	fmt.Fprintf(a.Err, "Revoke agent client %s (%s); its credential stops working immediately.\nType %s then Enter to revoke: ", name, existing.ID, name)
	line, err := readLine(ctx, a.In)
	if err != nil {
		return err
	}
	if line != name {
		return fmt.Errorf("confirmation did not match; nothing revoked")
	}
	client, err := agentauth.Revoke(cfg.DataDir, name, time.Now())
	if err != nil {
		return err
	}
	if auditErr := agentauth.NewAuditLog(cfg.DataDir).Append(agentauth.AuditEntry{Event: "revoke", ClientID: client.ID, ClientName: client.Name, Outcome: agentauth.OutcomeAllowed}); auditErr != nil {
		fmt.Fprintf(a.Err, "warning: could not write the audit log: %v\n", auditErr)
	}
	_, err = fmt.Fprintf(a.Out, "revoked %s\n", name)
	return err
}

type agentView struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Status        string      `json:"status"`
	Scopes        []string    `json:"scopes"`
	Chats         []string    `json:"chats"`
	OnDemand      bool        `json:"on_demand"`
	Grants        []grantView `json:"granted_chats"`
	RatePerMinute int         `json:"rate_per_minute"`
	CreatedAt     time.Time   `json:"created_at"`
	ExpiresAt     *time.Time  `json:"expires_at"`
	RevokedAt     *time.Time  `json:"revoked_at"`
}

// grantView is one chat a human approved for an on-demand agent.
type grantView struct {
	Chat      string    `json:"chat"`
	Scopes    []string  `json:"scopes"`
	GrantedAt time.Time `json:"granted_at"`
}

func (a *App) agentsList(cfg config.Config, args []string) error {
	fs := a.flags("agents list")
	fs.Usage = func() { fmt.Fprint(fs.Output(), agentsUsage) }
	asJSON := fs.Bool("json", false, "print JSON")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync agents list [--json]")
	}
	clients, err := agentauth.List(cfg.DataDir)
	if err != nil {
		return err
	}
	return writeAgents(a.Out, clients, *asJSON, time.Now())
}

func writeAgents(out io.Writer, clients []agentauth.Client, asJSON bool, now time.Time) error {
	views := make([]agentView, 0, len(clients))
	for _, c := range clients {
		chats := c.Chats
		if chats == nil {
			chats = []string{}
		}
		grants := []grantView{}
		for _, g := range c.Grants {
			grants = append(grants, grantView{Chat: g.Chat, Scopes: g.Scopes, GrantedAt: g.GrantedAt})
		}
		views = append(views, agentView{ID: c.ID, Name: c.Name, Status: c.Status(now), Scopes: c.Scopes, Chats: chats, OnDemand: c.OnDemand, Grants: grants, RatePerMinute: c.RatePerMinute, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt, RevokedAt: c.RevokedAt})
	}
	if asJSON {
		return json.NewEncoder(out).Encode(struct {
			Clients []agentView `json:"clients"`
		}{views})
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tSTATUS\tSCOPES\tCHATS\tRATE/MIN\tCREATED\tEXPIRES")
	for _, v := range views {
		chats := "all"
		if len(v.Chats) > 0 {
			chats = strings.Join(v.Chats, ",")
		} else if v.OnDemand {
			chats = "none yet"
		}
		if v.OnDemand {
			chats += " (on demand)"
		}
		for _, g := range v.Grants {
			chats += " +" + g.Chat + "[" + strings.Join(g.Scopes, "/") + "]"
		}
		expires := "never"
		if v.ExpiresAt != nil {
			expires = v.ExpiresAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Name, v.ID, v.Status, strings.Join(v.Scopes, ","), chats, strconv.Itoa(v.RatePerMinute), v.CreatedAt.UTC().Format(time.RFC3339), expires)
	}
	return w.Flush()
}
