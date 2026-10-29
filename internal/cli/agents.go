package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/axicred"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/query"
	"github.com/gehariharan/wasync/internal/storage"
)

// agentsUsage is the full reference for agent credential management, appended
// to the catalog help of agents and its subcommands.
const agentsUsage = `Usage: wasync agents grant NAME [--scope S1,S2] [--chat NAME ...] [--on-demand] [--expires 720h] [--rate N]
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
The scopes send, react, edit.own, chat.state, group.admin and capture.manage need a chat
list. Normally use --on-demand: the client starts with no chats and asks for
one by name (whatsapp-axi access request); you approve each chat, its access
level (read, and separately send) and its history depth in the TUI or with
wasync actions approve. Granted chats show in agents list. Revoke and
re-grant the client to remove them.
--chat (repeatable) pre-approves a chat now. Give its name as it appears in
your archive (e.g. --chat "Family"); grant shows the matching chat with its
JID and asks you to confirm. A raw JID also works. An ambiguous or unknown
name is refused. The two options combine.
--rate is requests per minute (default 60). --expires is a duration; omit it
for no expiry.

After granting, wasync offers to save the credential where whatsapp-axi
reads it (WHATSAPP_AXI_CONFIG_DIR, else the user config directory), private
and written atomically, so you do not copy it by hand. It never replaces a
different saved credential without asking. If you decline it is printed once
instead. Only its hash is stored; it cannot be shown again. list never prints credentials or hashes. Legacy api.token, send.token
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
	fs.Var(&chats, "chat", "chat to allow, by name or JID (repeatable)")
	onDemand := fs.Bool("on-demand", false, "start with no chats; approve each chat when the agent asks")
	expires := fs.Duration("expires", 0, "credential lifetime, e.g. 720h")
	rate := fs.Int("rate", agentauth.DefaultRatePerMinute, "requests per minute")
	if err := a.parseFlags(fs, rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 || name == "" {
		return fmt.Errorf("usage: wasync agents grant NAME [--scope S1,S2] [--chat NAME ...] [--on-demand] [--expires 720h] [--rate N]")
	}
	if *expires < 0 || (*expires > 0 && *expires < time.Minute) {
		return fmt.Errorf("--expires must be at least 1m")
	}
	if *rate == 0 {
		return fmt.Errorf("--rate must be between 1 and %d", agentauth.MaxRatePerMinute)
	}
	// Chat names are looked up in the archive after the interactive check, so
	// only validate now when every --chat is already a JID.
	var chatNames, chatIDs []string
	for _, c := range chats {
		if agentauth.ValidChatJID(strings.TrimSpace(c)) == nil {
			chatIDs = append(chatIDs, c)
		} else {
			chatNames = append(chatNames, strings.TrimSpace(c))
		}
	}
	req := agentauth.GrantRequest{Name: name, Scopes: scopes, Chats: chatIDs, OnDemand: *onDemand, RatePerMinute: *rate, Expires: *expires}
	if len(chatNames) == 0 {
		if err := agentauth.ValidateGrant(req); err != nil {
			return err
		}
	} else if _, err := agentauth.ParseScopes(scopes...); err != nil {
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
	if len(chatNames) > 0 {
		resolved, err := a.resolveChatNames(ctx, cfg, chatNames)
		if err != nil {
			return err
		}
		req.Chats = append(req.Chats, resolved...)
		chats = req.Chats
		if err := agentauth.ValidateGrant(req); err != nil {
			return err
		}
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
	if auditErr != nil {
		fmt.Fprintf(a.Err, "warning: could not write the audit log: %v\n", auditErr)
	}
	if a.offerSaveAxiCredential(ctx, client.Name, credential) {
		return nil
	}
	fmt.Fprintf(a.Err, "WARNING: this credential is shown once and cannot be recovered. Store it in the agent's secret store; anyone holding it acts with these scopes. Revoke with: wasync agents revoke %s\n", client.Name)
	if _, err = fmt.Fprintln(a.Out, credential); err != nil {
		return err
	}
	fmt.Fprintf(a.Err, "To use it with whatsapp-axi, put it in WHATSAPP_AXI_TOKEN or save it to the file whatsapp-axi reads (%s).\n", a.axiCredentialPathText())
	return nil
}

func (a *App) getenv(key string) string {
	if a.Getenv != nil {
		return a.Getenv(key)
	}
	return os.Getenv(key)
}

func (a *App) axiCredentialPathText() string {
	if p, err := axicred.Path(a.getenv); err == nil {
		return p
	}
	return "WHATSAPP_AXI_CONFIG_DIR/credential"
}

// askYesNo prints a prompt and reads one line. Empty input takes def; end of
// input, a read error or anything but y/yes/n/no counts as "no".
func (a *App) askYesNo(ctx context.Context, prompt string, def bool) bool {
	fmt.Fprint(a.Err, prompt)
	line, err := readLine(ctx, a.In)
	if err != nil {
		return false
	}
	switch strings.ToLower(line) {
	case "":
		return def && err == nil
	case "y", "yes":
		return true
	}
	return false
}

// offerSaveAxiCredential asks whether to store a new credential where
// whatsapp-axi reads it, and reports whether it did. Any problem or a "no"
// falls back to printing the credential, so it is never lost. The secret is
// never written to the terminal here.
func (a *App) offerSaveAxiCredential(ctx context.Context, name, credential string) bool {
	path, err := axicred.Path(a.getenv)
	if err != nil {
		fmt.Fprintf(a.Err, "note: cannot find whatsapp-axi's credential file (%v)\n", err)
		return false
	}
	existing, err := axicred.Existing(path)
	if err != nil {
		fmt.Fprintf(a.Err, "note: cannot read %s (%v); not saving there\n", path, err)
		return false
	}
	if existing != "" {
		if !a.askYesNo(ctx, fmt.Sprintf("whatsapp-axi already has a different credential saved at %s.\nReplace it with the one for %s? [y/N]: ", path, name), false) {
			return false
		}
	} else if !a.askYesNo(ctx, fmt.Sprintf("Save this credential for whatsapp-axi now, at %s? [Y/n]: ", path), true) {
		return false
	}
	if err := axicred.Save(path, credential); err != nil {
		fmt.Fprintf(a.Err, "warning: could not save the credential: %v\n", err)
		return false
	}
	fmt.Fprintf(a.Err, "Saved the credential for %s to %s (private, owner-only). It is not shown again; revoke with: wasync agents revoke %s\n", name, path, name)
	if a.getenv(axicred.EnvToken) != "" {
		fmt.Fprintf(a.Err, "note: %s is set in this environment and takes priority over the saved file; unset it so whatsapp-axi uses the new credential\n", axicred.EnvToken)
	}
	return true
}

// resolveChatNames turns --chat names into JIDs using the archive's chat
// names. Each match is shown with its JID and needs a typed yes; an unknown or
// ambiguous name is refused with the candidates.
func (a *App) resolveChatNames(ctx context.Context, cfg config.Config, names []string) ([]string, error) {
	path := filepath.Join(cfg.DataDir, "archive.db")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("cannot look up chat %q: there is no archive yet. Pair and sync wasync first, or use --on-demand and let the agent ask for the chat", names[0])
	}
	store, err := storage.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("cannot look up chat names: %w", err)
	}
	defer store.Close()
	account, err := store.Account(ctx)
	if err != nil {
		return nil, fmt.Errorf("read archive account: %w", err)
	}
	if account == nil {
		return nil, fmt.Errorf("cannot look up chat %q: the archive has no account yet. Pair wasync first, or use --on-demand", names[0])
	}
	groups, err := store.GroupSummaries(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, n := range names {
		found := query.MatchChats(groups, n)
		switch {
		case len(found) == 0:
			return nil, fmt.Errorf("no chat in your archive matches %q; check the name with `wasync query chats`, or use --on-demand so the agent asks for it", n)
		case len(found) > 1:
			var b strings.Builder
			fmt.Fprintf(&b, "%q matches %d chats; repeat --chat with a more exact name or one of these JIDs:", n, len(found))
			for _, g := range found {
				fmt.Fprintf(&b, "\n  %s  %s (%s)", g.ID, g.Name, storage.ChatKind(g.ID))
			}
			return nil, errors.New(b.String())
		}
		g := found[0]
		if !a.askYesNo(ctx, fmt.Sprintf("Allow chat %q: %s (%s)? [y/N]: ", g.Name, g.ID, storage.ChatKind(g.ID)), false) {
			return nil, fmt.Errorf("chat %q not confirmed; no credential created", g.Name)
		}
		ids = append(ids, g.ID)
	}
	return ids, nil
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
