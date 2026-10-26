// Package cli launches the TUI and provides scriptable local commands.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gehariharan/wasync/internal/api"
	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/buildinfo"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
	"github.com/gehariharan/wasync/internal/processlock"
	"github.com/gehariharan/wasync/internal/storage"
	"github.com/gehariharan/wasync/internal/whatsapp"
	"go.mau.fi/whatsmeow/types"
	"golang.org/x/term"
)

type Client interface {
	Close() error
	Paired() bool
	AccountJID() string
	Connect(context.Context) error
	Login(context.Context, io.Writer) error
	Logout(context.Context) error
	Groups(context.Context) ([]storage.Group, error)
	Observe(context.Context, time.Duration) error
	Observation() whatsapp.Observation
}
type App struct {
	Approve       func(int64, string) error
	MediaDownload whatsapp.MediaDownloader
	Sync          whatsapp.Syncer
	Operations    whatsapp.Operations
	Approvals     *approval.Queue
	In            io.Reader
	Out, Err      io.Writer
	Interactive   bool
	OpenClient    func(context.Context, string, *slog.Logger) (Client, error)
}

func New() *App {
	return &App{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Interactive: term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())), OpenClient: func(ctx context.Context, path string, log *slog.Logger) (Client, error) {
		return whatsapp.Open(ctx, path, log)
	}}
}

const usage = `wasync — personal WhatsApp group sync client

Usage: wasync [--data-dir PATH] [--timeout 3m] [--api-addr 127.0.0.1:8787] [--no-api] COMMAND

  tui [--offline]                            Full-screen group browser and capture (default)
  run                                        Start the TUI with capture
  serve [--offline]                          Headless capture and local API
  login [--select=true|false] [--observe 30s]  Pair or reuse session, discover groups
  logout                                     Unlink device; preserve cached groups
  groups list [--refresh] [--json]            List cached groups; optionally connect
  groups enable GROUP_ID|--all               Enable selected or all cached groups
  groups disable GROUP_ID|--all              Disable capture preference
  chats list|enable|disable                   Manage group and direct chat capture
  clean --chat CHAT_ID (--message-id ID|--all)  Request local cleanup; human CLI/TUI approval
  delete --chat CHAT_ID --scope SCOPE        Request WhatsApp deletion; human CLI/TUI approval
  sync [--wait 30s] [--history] [--chat ID]    Capture now, reusing any running owner
  actions approve REQUEST_ID                 Interactive typed approval, no --yes
  actions status REQUEST_ID                  Inspect a pending/completed request
  media policy --chat ID [--mode auto|manual|inherit]  Saved per-chat media setting
  media download --chat ID --message-id ID    One-time attachment download (live owner)
  send --to CHAT_ID [--text TEXT] [--file PATH --type image|document]
  reply --chat CHAT_ID --message-id ID --text TEXT [--mention USER_JID ...]
  react --chat CHAT_ID --message-id ID (--emoji EMOJI|--remove)
  edit --chat CHAT_ID --message-id ID --text TEXT
  status                                     Inspect local pairing state
  query [groups|chats|messages|search|status]       Read-only JSON queries for agents
  query [version|message|thread|changes|checkpoint|doctor]  More queries; --v1 envelope
  agents grant|list|revoke                   Scoped agent credentials; human, interactive
  version                                    Show installed build information
  help [COMMAND [SUBCOMMAND]]                 Detailed command help, without connecting
  help --json                                Machine-readable command catalog for agents

Global flags (before COMMAND):
  --data-dir PATH          Dedicated private data directory
  --timeout 3m             Overall command timeout
  --api-addr HOST:PORT     Loopback API address (default 127.0.0.1:8787)
  --no-api                Disable the TUI API and command channel
  --download-media        Default automatic media downloads; per-chat policy overrides
  --pairing-full-history  Request fuller history on NEW pairing only (best effort)
  --version               Show installed build information

The TUI captures enabled groups and imports history when WhatsApp delivers it.
Login/observation commands remain diagnostics and discard message bodies.
Global flags precede the command. Ctrl-C cancels network operations.
`

func (a *App) flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(a.Err)
	f.Usage = func() { _ = a.writeHelp(f.Output(), name) }
	return f
}
func (a *App) Run(parent context.Context, args []string) (result error) {
	defer func() {
		if errors.Is(result, flag.ErrHelp) {
			result = nil
		}
	}()
	cfg, err := config.Default()
	if err != nil {
		return err
	}
	root := a.flags("wasync")
	showVersion := root.Bool("version", false, "show installed build information")
	root.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "dedicated private data directory")
	root.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "overall command timeout, including selection and observation")
	root.StringVar(&cfg.APIAddr, "api-addr", cfg.APIAddr, "local API loopback address")
	root.BoolVar(&cfg.PairingFullHistory, "pairing-full-history", os.Getenv("WASYNC_PAIRING_FULL_HISTORY") == "1", "ask for fuller pairing-time history (best effort; new pairing only)")
	root.BoolVar(&cfg.DownloadMedia, "download-media", os.Getenv("WASYNC_DOWNLOAD_MEDIA") == "1", "download images/documents/video/audio/stickers from enabled chats (local plaintext; WhatsApp transfer limits apply)")
	root.BoolVar(&cfg.NoAPI, "no-api", false, "disable the API alongside the TUI")
	root.Usage = func() { fmt.Fprint(root.Output(), usage) }
	if err = a.parseFlags(root, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args = root.Args()
	if handled, helpErr := a.dispatchHelp(args); handled {
		return helpErr
	}
	// Agent credential management is file-based and never opens WhatsApp or
	// the archive.
	if len(args) > 0 && args[0] == "agents" {
		return a.runAgents(parent, cfg, args[1:])
	}
	if *showVersion {
		if len(args) != 0 {
			return fmt.Errorf("usage: wasync --version")
		}
		_, err := fmt.Fprintln(a.Out, buildinfo.String())
		return err
	}
	if len(args) > 0 && args[0] == "version" {
		if len(args) != 1 {
			return fmt.Errorf("usage: wasync version")
		}
		_, err := fmt.Fprintln(a.Out, buildinfo.String())
		return err
	}
	if len(args) == 0 {
		args = []string{"tui"}
	}
	cfg, err = cfg.Validate()
	if err != nil {
		return err
	}
	if args[0] == "actions" {
		return a.runActionStatus(parent, cfg, args[1:])
	}
	if args[0] == "clean" || args[0] == "delete" {
		return a.runDestructiveRequest(parent, cfg, args)
	}
	if args[0] == "sync" {
		return a.runSync(parent, cfg, args[1:])
	}
	if args[0] == "media" {
		return a.runMedia(parent, cfg, args[1:])
	}
	if args[0] == "send" {
		return a.runSend(parent, cfg, args[1:])
	}
	if args[0] == "reply" {
		return a.runReply(parent, cfg, args[1:])
	}
	if args[0] == "react" {
		return a.runReact(parent, cfg, args[1:])
	}
	if args[0] == "edit" {
		return a.runEdit(parent, cfg, args[1:])
	}
	if args[0] == "query" {
		ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
		defer cancel()
		return a.runQuery(ctx, cfg, args[1:])
	}
	if !cfg.NoAPI {
		if err := api.ValidateAddr(cfg.APIAddr); err != nil {
			return err
		}
	}
	// Validate command shape before creating or modifying local files.
	action, err := a.parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if (args[0] == "tui" || args[0] == "run") && !a.Interactive {
		return fmt.Errorf("the TUI requires an interactive terminal; use wasync help for scriptable commands")
	}
	if args[0] == "serve" && cfg.NoAPI {
		return fmt.Errorf("serve requires the local API; remove --no-api")
	}
	if err = localfs.EnsureDir(cfg.DataDir); err != nil {
		return err
	}
	lock, err := processlock.Acquire(filepath.Join(cfg.DataDir, "device.lock"))
	if errors.Is(err, processlock.ErrBusy) && (args[0] == "groups" || args[0] == "chats") {
		return a.targetsThroughOwner(parent, cfg, args)
	}
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	ctx := parent
	if args[0] != "tui" && args[0] != "run" && args[0] != "serve" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, cfg.Timeout)
		defer cancel()
	}
	store, err := storage.Open(ctx, filepath.Join(cfg.DataDir, "archive.db"))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	log := slog.New(slog.NewJSONHandler(a.Err, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return action(ctx, cfg, store, log)
}

type action func(context.Context, config.Config, *storage.Store, *slog.Logger) error

func (a *App) parse(args []string) (action, error) {
	switch args[0] {
	case "tui", "run", "serve":
		fs := a.flags(args[0])
		offline := fs.Bool("offline", false, "browse the local archive without a WhatsApp connection")
		if err := a.parseFlags(fs, args[1:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 0 {
			return nil, fmt.Errorf("usage: wasync tui [--offline]")
		}
		if args[0] == "serve" {
			return a.serveAction(*offline), nil
		}
		return a.tuiAction(*offline), nil
	case "login":
		fs := a.flags("login")
		selectGroups := fs.Bool("select", a.Interactive, "offer additive group selection in terminal")
		observe := fs.Duration("observe", 0, "keep connection open to count events without archiving")
		if err := a.parseFlags(fs, args[1:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 0 || *observe < 0 {
			return nil, fmt.Errorf("usage: wasync login [--select=true|false] [--observe 30s]")
		}
		return func(ctx context.Context, cfg config.Config, s *storage.Store, l *slog.Logger) error {
			return a.withClient(ctx, cfg, l, func(c Client) error {
				if discovery, ok := c.(interface {
					EnableDiscovery(context.Context, *storage.Store) error
				}); ok {
					if err := discovery.EnableDiscovery(ctx, s); err != nil {
						return err
					}
				}
				if err := c.Login(ctx, a.Out); err != nil {
					return err
				}
				account, groups, err := discover(ctx, s, c)
				if err != nil {
					return fmt.Errorf("session saved, but group discovery failed; retry groups list --refresh: %w", err)
				}
				fmt.Fprintln(a.Out, "Session authenticated. Groups cached; this diagnostic command does not archive messages.")
				if err = a.printGroups(groups, false); err != nil {
					return err
				}
				if *selectGroups && len(groups) > 0 {
					fmt.Fprintln(a.Out, "Enter group numbers to enable (comma separated), or press Enter to leave preferences unchanged:")
					line, err := readLine(ctx, a.In)
					if err != nil {
						return err
					}
					ids, err := parseSelection(line, groups)
					if err != nil {
						return err
					}
					if err = s.SetEnabled(ctx, account, ids, true); err != nil {
						return err
					}
					fmt.Fprintf(a.Out, "Saved %d group preference(s). Launch wasync to start capture.\n", len(ids))
				}
				if *observe > 0 {
					fmt.Fprintf(a.Out, "Observing connectivity for %s; event bodies are discarded. History replay is not guaranteed.\n", *observe)
				}
				if err = c.Observe(ctx, *observe); err != nil {
					return err
				}
				o := c.Observation()
				fmt.Fprintf(a.Out, "Observed events (all chats): live=%d history_batches=%d history_messages=%d. No content saved.\n", o.LiveEvents, o.HistoryBatches, o.HistoryMessages)
				return nil
			})
		}, nil
	case "logout":
		if len(args) != 1 {
			return nil, fmt.Errorf("usage: wasync logout")
		}
		return func(ctx context.Context, cfg config.Config, s *storage.Store, l *slog.Logger) error {
			return a.withClient(ctx, cfg, l, func(c Client) error {
				if !c.Paired() {
					fmt.Fprintln(a.Out, "No saved session to unlink.")
					return nil
				}
				if err := c.Connect(ctx); err != nil {
					return err
				}
				if err := c.Logout(ctx); err != nil {
					return err
				}
				fmt.Fprintln(a.Out, "Device unlinked. Cached group preferences remain local.")
				return nil
			})
		}, nil
	case "status":
		if len(args) != 1 {
			return nil, fmt.Errorf("usage: wasync status")
		}
		return func(ctx context.Context, cfg config.Config, s *storage.Store, l *slog.Logger) error {
			account, err := s.Account(ctx)
			if err != nil {
				return err
			}
			paired := false
			session := filepath.Join(cfg.DataDir, "session.db")
			if _, err = os.Lstat(session); err == nil {
				err = a.withClient(ctx, cfg, l, func(c Client) error { paired = c.Paired(); return nil })
				if err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintf(a.Out, "Saved session: %t (local state only; no network check)\nData directory: %s\n", paired, cfg.DataDir)
			if account == nil {
				fmt.Fprintln(a.Out, "No groups discovered. Run wasync login.")
			} else {
				groups, err := s.GroupSummaries(ctx, account.ID)
				if err != nil {
					return err
				}
				enabled := 0
				for _, g := range groups {
					if g.Enabled {
						enabled++
					}
				}
				fmt.Fprintf(a.Out, "Cached groups: %d; enabled preferences: %d\n", len(groups), enabled)
				if account.LastDiscovery != nil {
					fmt.Fprintf(a.Out, "Last discovery: %s (cached)\n", account.LastDiscovery.Format(time.RFC3339))
				}
			}
			fmt.Fprintln(a.Out, "Launch wasync to capture enabled groups. History completeness: unknown.")
			return nil
		}, nil
	case "groups":
		return a.parseGroups(args[1:])
	case "chats":
		return a.parseTargets(args[1:], true)
	default:
		return nil, fmt.Errorf("unknown command %q; run wasync help", args[0])
	}
}
func (a *App) parseGroups(args []string) (action, error) { return a.parseTargets(args, false) }
func (a *App) parseTargets(args []string, allChats bool) (action, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: wasync groups list|enable|disable")
	}
	switch args[0] {
	case "list":
		name := "groups list"
		if allChats {
			name = "chats list"
		}
		fs := a.flags(name)
		refresh := fs.Bool("refresh", false, "connect using saved session to refresh metadata")
		jsonOut := fs.Bool("json", false, "print cached groups as JSON")
		if err := a.parseFlags(fs, args[1:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 0 {
			return nil, fmt.Errorf("usage: wasync groups list [--refresh] [--json]")
		}
		return func(ctx context.Context, cfg config.Config, s *storage.Store, l *slog.Logger) error {
			if *refresh {
				if err := a.withClient(ctx, cfg, l, func(c Client) error {
					if err := c.Connect(ctx); err != nil {
						return err
					}
					_, _, err := discover(ctx, s, c)
					return err
				}); err != nil {
					return err
				}
			}
			account, err := s.Account(ctx)
			if err != nil {
				return err
			}
			if account == nil {
				return fmt.Errorf("no cached groups; run wasync login first")
			}
			groups, err := s.GroupSummaries(ctx, account.ID)
			if err != nil {
				return err
			}
			if !allChats {
				filtered := groups[:0]
				for _, g := range groups {
					if storage.ChatKind(g.ID) == "group" {
						filtered = append(filtered, g)
					}
				}
				groups = filtered
			}
			return a.printGroups(groups, *jsonOut)
		}, nil
	case "enable", "disable":
		if len(args) != 2 {
			return nil, fmt.Errorf("usage: wasync groups %s GROUP_ID|--all", args[0])
		}
		jid, err := whatsapp.ParseChat(args[1])
		if args[1] != "--all" && (err != nil || !allChats && jid.Server != types.GroupServer) {
			return nil, fmt.Errorf("expected a group JID such as 123456789@g.us")
		}
		enable := args[0] == "enable"
		id := args[1]
		return func(ctx context.Context, cfg config.Config, s *storage.Store, l *slog.Logger) error {
			account, err := s.Account(ctx)
			if err != nil {
				return err
			}
			if account == nil {
				return fmt.Errorf("no discovered account; run wasync login first")
			}
			if allChats && id != "--all" && jid.Server != types.GroupServer {
				if err = s.SaveDiscoveredChat(ctx, account.ID, storage.Group{ID: id, Name: id}); err != nil {
					return err
				}
			}
			ids := []string{id}
			if id == "--all" {
				groups, e := s.Groups(ctx, account.ID)
				if e != nil {
					return e
				}
				ids = nil
				for _, group := range groups {
					if allChats || storage.ChatKind(group.ID) == "group" {
						ids = append(ids, group.ID)
					}
				}
			}
			if err = s.SetEnabled(ctx, account.ID, ids, enable); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "Saved %d group preference(s), enabled=%t. Launch wasync to capture messages; enabling queues available history.\n", len(ids), enable)
			return nil
		}, nil
	default:
		return nil, fmt.Errorf("unknown groups command %q", args[0])
	}
}
func (a *App) withClient(ctx context.Context, cfg config.Config, log *slog.Logger, fn func(Client) error) (err error) {
	c, err := a.OpenClient(ctx, filepath.Join(cfg.DataDir, "session.db"), log)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	if history, ok := c.(interface{ ConfigurePairingHistory(bool) }); ok {
		history.ConfigurePairingHistory(cfg.PairingFullHistory)
	}
	return fn(c)
}
func discover(ctx context.Context, s *storage.Store, c Client) (int64, []storage.Group, error) {
	if c.AccountJID() == "" {
		return 0, nil, fmt.Errorf("authenticated account has no JID")
	}
	account, err := s.UseAccount(ctx, c.AccountJID())
	if err != nil {
		return 0, nil, err
	}
	groups, err := c.Groups(ctx)
	if err != nil {
		return account, nil, err
	}
	if err = s.SaveGroups(ctx, account, groups); err != nil {
		return account, nil, err
	}
	groups, err = s.Groups(ctx, account)
	filtered := groups[:0]
	for _, g := range groups {
		if storage.ChatKind(g.ID) == "group" {
			filtered = append(filtered, g)
		}
	}
	return account, filtered, err
}
func (a *App) printGroups(groups []storage.Group, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(a.Out).Encode(groups)
	}
	w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "#\tGROUP ID\tENABLED\tNAME")
	for i, g := range groups {
		fmt.Fprintf(w, "%d\t%s\t%t\t%s\n", i+1, g.ID, g.Enabled, strconv.Quote(g.Name))
	}
	if len(groups) == 0 {
		fmt.Fprintln(w, "No groups returned by discovery.")
	}
	return w.Flush()
}
func parseSelection(line string, groups []storage.Group) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	ids := make([]string, 0)
	seen := make(map[int]bool)
	for _, part := range strings.Split(line, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > len(groups) {
			return nil, fmt.Errorf("invalid group number %q; use groups enable GROUP_ID to save preferences", part)
		}
		if !seen[n] {
			seen[n] = true
			ids = append(ids, groups[n-1].ID)
		}
	}
	return ids, nil
}
func readLine(ctx context.Context, in io.Reader) (string, error) {
	type answer struct {
		line string
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(in, 64*1024)).ReadString('\n')
		if errors.Is(err, io.EOF) {
			err = nil
		}
		done <- answer{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case a := <-done:
		return strings.TrimSpace(a.line), a.err
	}
}
