package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// One catalog backs both command help and agent discovery. Usage includes every
// accepted command-specific flag; global flags are described by the root help.
type commandHelp struct {
	Path        string `json:"path"`
	Usage       string `json:"usage"`
	Description string `json:"description"`
	Details     string `json:"details,omitempty"`
}

var commandCatalog = []commandHelp{
	{"tui", "tui [--offline]", "Open the interactive TUI (default when no command is supplied).", "Capture enabled chats while connected; --offline browses the local archive. An interactive terminal is required."},
	{"run", "run [--offline]", "Alias for tui.", "Capture continues until exit; --offline browses without connecting."},
	{"serve", "serve [--offline]", "Run headless capture and the loopback API.", "--offline serves the local archive without connecting. Cannot be combined with --no-api. Keep this or the TUI running for owner-channel commands."},
	{"login", "login [--select=true|false] [--observe DURATION]", "Pair by QR or reuse the saved session, discover groups, then open the TUI.", "In an interactive terminal a successful login opens the TUI with capture for enabled groups, as plain wasync does. --select=false or --observe keeps the diagnostic output instead: a group table, with an optional selection prompt when --select=true. --select defaults to terminal interactivity. --observe defaults to 0 and counts events without saving message bodies. Without a terminal, login prints the group table and exits; use tui or serve for capture."},
	{"logout", "logout", "Unlink the saved WhatsApp device session.", "Connects to WhatsApp when a saved session exists. Local archives and chat preferences remain on disk."},
	{"groups", "groups list|enable|disable", "Discover and select group chats for capture.", "Use help groups list, help groups enable, or help groups disable."},
	{"groups list", "groups list [--refresh] [--json]", "List cached groups and capture preferences.", "--refresh connects using the saved session to refresh metadata; otherwise reads local state."},
	{"groups enable", "groups enable GROUP_ID|--all", "Persist capture preference for one group or all cached groups.", "Enabling queues best-effort history when capture runs; it cannot guarantee a complete archive. Example: wasync groups enable '123456789@g.us'"},
	{"groups disable", "groups disable GROUP_ID|--all", "Disable capture for one group or all cached groups.", "Previously archived messages remain local."},
	{"chats", "chats list|enable|disable", "Manage capture for groups and direct chats.", "Direct-chat discovery is best effort, not a complete WhatsApp inbox listing."},
	{"chats list", "chats list [--refresh] [--json]", "List cached groups and direct chats.", "--refresh connects to refresh group metadata; direct chats are discovered from delivered events."},
	{"chats enable", "chats enable CHAT_ID|--all", "Persist capture preference for one chat or all cached chats.", "An explicit direct-chat ID can be added. Enabling does not start a background process: keep tui or serve running."},
	{"chats disable", "chats disable CHAT_ID|--all", "Disable capture for one chat or all cached chats.", "Previously archived messages remain local."},
	{"sync", "sync [--wait DURATION] [--history] [--chat CHAT_ID]", "Capture now, reusing a running owner when available.", "--wait defaults to 30s, allowed 0 to 60s. --history requests best-effort history for enabled chats; --chat restricts it. History needs a known message anchor and phone availability; a successful request does not imply full recovery."},
	{"media", "media policy|download", "Configure automatic media downloads or download one attachment.", "Use help media policy or help media download. No fixed application media-size cap; WhatsApp transfer limits and remote availability apply."},
	{"media policy", "media policy --chat CHAT_ID [--mode auto|manual|inherit]", "Read or persist a per-chat media policy; returns JSON.", "auto downloads media on future deliveries, including delivered history; manual requires an explicit download; inherit follows --download-media / WASYNC_DOWNLOAD_MEDIA. Omit --mode to read the saved policy. This setting persists across restarts and does not bulk-download older archived messages or enable capture."},
	{"media download", "media download --chat CHAT_ID --message-id MESSAGE_ID", "Queue a one-time attachment download; returns JSON.", "Requires a running connected tui or serve owner with its command channel, enabled capture, and retained download metadata. Does not change the saved policy. Older archives may lack the metadata, and WhatsApp may no longer serve the attachment."},
	{"send", "send --to CHAT_ID [--text TEXT] [--file PATH] [--type image|document]", "Explicitly send a text message or local attachment.", "At least --text or --file is required. With a file, --text is the caption and --type defaults to document; image supports JPEG/PNG. Reuses a running owner or connects with the saved session. A server acknowledgement is not proof of recipient delivery; do not automatically retry uncertain sends."},
	{"reply", "reply --chat CHAT_ID --message-id MESSAGE_ID --text TEXT [--mention USER_JID ...]", "Quote an archived message in a reply.", "The target must already be in the local archive of that chat. --mention (repeatable, up to 20) takes a user JID such as 123456789@s.whatsapp.net and the text must contain @123456789 so the mention is visible. Reuses a running owner or connects with the saved session. A server acknowledgement is not proof of recipient delivery; do not automatically retry uncertain sends."},
	{"react", "react --chat CHAT_ID --message-id MESSAGE_ID (--emoji EMOJI|--remove)", "React to an archived message, or clear your reaction.", "The target must already be in the local archive of that chat. --emoji takes one emoji; --remove clears the reaction you gave earlier; give exactly one of them. WhatsApp keeps one reaction per person per message, so a new emoji replaces the old one. Reuses a running owner or connects with the saved session. A server acknowledgement is not proof of delivery; do not automatically retry uncertain sends."},
	{"edit", "edit --chat CHAT_ID --message-id MESSAGE_ID --text TEXT", "Replace the text of one of your own sent messages.", "The target must already be in the local archive of that chat, be a text message you sent, and still be inside WhatsApp's edit window (about 20 minutes after it was first sent); otherwise nothing is sent and the error says why. Reuses a running owner or connects with the saved session. A server acknowledgement is not proof that recipients saw the change; do not automatically retry uncertain sends."},
	{"clean", "clean --chat CHAT_ID (--message-id MESSAGE_ID|--all)", "Request deletion from the local archive only.", "Requires a running owner. Returns an approval request without deleting anything. A human must approve through the TUI or interactive actions approve; there is no --yes."},
	{"delete", "delete --chat CHAT_ID --scope everyone --message-id MESSAGE_ID\n       wasync delete --chat CHAT_ID --scope my-whatsapp-chat", "Request a WhatsApp deletion with human approval.", "everyone requests revocation of your own message, subject to WhatsApp limits. my-whatsapp-chat deletes your WhatsApp chat state and leaves the local archive. Requires a running owner and subsequent human approval; there is no --yes."},
	{"actions", "actions list | status|approve REQUEST_ID", "Inspect or interactively approve a destructive request or an agent's chat access request.", "Requires the running owner's command channel. REQUEST_ID is the 32-character hexadecimal ID returned by clean or delete, or printed by whatsapp-axi access request."},
	{"actions list", "actions list", "List the requests waiting for you, with what each asks for.", "Requires an interactive terminal and the running owner's command channel. Shows the agent, whether it asks to read or also send, and the candidate chats, so run it yourself; the serve console line carries only the agent name and request ID."},
	{"actions status", "actions status REQUEST_ID", "Read the status of a pending or completed request as JSON.", "Does not approve or execute the request."},
	{"actions approve", "actions approve REQUEST_ID", "Ask a human to approve a pending destructive request or chat access request.", "Requires an interactive terminal and a typed confirmation. Agents cannot bypass confirmation with --yes. For a chat access request you also choose the chat (when several match) and the history depth: from now on only, or a full history sync."},
	{"status", "status", "Inspect saved local pairing state and cached groups.", "Does not check network connectivity. For read-only agent queries while capture runs, prefer query status."},
	{"query", "query groups|chats|messages|search|status", "Read-only JSON queries for agents, without an API or WhatsApp connection.", "Use help query messages or help query search for filters and pagination. All results are JSON; exit codes: 0 success (including empty items), 1 error, 130 cancellation. Queries never download media. Also: query version, message, thread, changes, checkpoint and doctor (see help query COMMAND). --v1 on groups, chats, messages, search, status and message emits the versioned v1 envelope; version, thread, changes, checkpoint and doctor always use it."},
	{"query groups", "query groups [--enabled] [--json]", "Read cached group summaries as JSON.", "--enabled restricts results to enabled groups. No WhatsApp connection or running owner required."},
	{"query chats", "query chats [--enabled] [--json]", "Read cached group and direct-chat summaries as JSON.", "--enabled restricts results to enabled chats. No WhatsApp connection or running owner required. --v1 emits the v1 envelope; with --v1, --match QUERY resolves a chat by JID or name (v1 only)."},
	{"query messages", "query messages --chat CHAT_ID [--since RFC3339] [--until RFC3339] [--sender SENDER_ID] [--limit N] [--cursor CURSOR] [--json]", "Read archived messages as paginated JSON, newest first.", "--group is a legacy alias for --chat; do not combine them. --since is inclusive, --until exclusive. --limit is 1 to 200 (default 50). Reuse next_cursor with identical filters; empty items is success. Never downloads attachments."},
	{"query search", "query search PHRASE [--chat CHAT_ID] [--since RFC3339] [--until RFC3339] [--sender SENDER_ID] [--limit N] [--cursor CURSOR] [--json]", "Search archived text and captions using SQLite FTS; returns JSON.", "Quote PHRASE as one argument (maximum 1024 bytes). --group is a legacy alias for --chat. Dates are inclusive --since and exclusive --until; --limit is 1 to 200 (default 50). Pass next_cursor with identical filters. Does not index document bodies or download media. --v1 emits the v1 envelope; with --v1, --context N (0 to 5) adds messages before and after each hit (v1 only)."},
	{"query status", "query status [--json]", "Read persisted sync observations as JSON.", "Current network connectivity is unknown; reconnects do not prove missed messages were recovered."},
	{"query version", "query version [--json]", "Report API, build and archive schema versions as a v1 envelope.", "Report API, build and schema versions as a v1 envelope without opening the archive for writes. Always emits the v1 envelope; --v1 is accepted and ignored."},
	{"query message", "query message --ref REF [--v1] [--json]", "Read one archived message by its stable reference; returns JSON.", "REF is an account-scoped m1_... reference from v1 results. References survive restarts, purge and re-capture; local row ids do not. Expired content is never returned; revoked messages return their tombstone. With --v1 the data is the v1 message shape (legacy fields plus ref and chat_kind)."},
	{"query thread", "query thread --ref REF [--before N] [--after N] [--json]", "Read a message with surrounding context from its chat as a v1 envelope.", "Returns REF as anchor with up to --before earlier and --after later messages (0 to 20 each, default 5) from the same chat, oldest first. Always emits the v1 envelope. Expired content is never returned; revoked messages return their tombstone."},
	{"query changes", "query changes [--after CURSOR] [--limit N] [--v1] [--json]", "Read the change journal of the current account as a v1 envelope, oldest first.", "Entries list sequence, cursor, op, chat_id, message_id and a message ref, never message content. Pass page.next_cursor (c1_...) to --after; omit --after to start from the oldest retained change. --limit is 1 to 1000 (default 100). cursor_expired means the cursor is older than the 30-day retention or from another archive or account: resnapshot. Offline reads cannot commit checkpoints; commit through the running owner's API (PUT /api/v1/checkpoints/{consumer}, scope observe)."},
	{"query checkpoint", "query checkpoint --consumer NAME [--v1] [--json]", "Read a consumer's committed change-journal checkpoint as a v1 envelope.", "NAME is 1 to 64 characters of a-z, 0-9 and -. Read only: committing is a write and goes through the running owner's API (PUT /api/v1/checkpoints/{consumer}, scope observe)."},
	{"query doctor", "query doctor [--json]", "Report installation, data directory, archive, pairing, owner and agent checks as a v1 envelope.", "Each check is pass, warn or fail with a fix command. Never connects to WhatsApp or the network and never creates or changes files. An owner is detected by probing device.lock without holding it; its live connection state is available only from GET /api/v1/health/deep."},
	{"agents", "agents grant|list|revoke", "Manage scoped credentials for agents using the local /api/v1.", "Human only: grant and revoke must be run by a human in an interactive terminal and require typing the client name; there is no --yes, and none of these commands is available over the API. They edit agents.json in the data directory, work whether or not an owner is running (a running owner reloads changes), and never connect to WhatsApp. Use help agents grant, help agents list, or help agents revoke."},
	{"agents grant", "agents grant NAME [--scope S1,S2] [--chat CHAT_JID ...] [--on-demand] [--expires DURATION] [--rate N]", "Create a scoped agent credential; human only, interactive terminal required.", "NAME is 1-32 characters of a-z, 0-9 and -. Scopes: read.chats, read.messages, read.media, observe, send, react, edit.own, chat.state, group.admin, capture.manage, export (default read.chats,read.messages). There is no destructive scope. --chat (repeatable) limits the client to listed chats and is required for send, react, edit.own, chat.state, group.admin and capture.manage unless --on-demand is given. --on-demand starts the client with no chats: it asks (whatsapp-axi access request) and you approve each chat, read and send separately, with a history depth, in the TUI or actions approve. --rate is requests per minute (default 60); omit --expires for no expiry. The credential is printed once; only its hash is stored. Legacy api.token, send.token and approve.token are never accepted as agent credentials."},
	{"agents list", "agents list [--json]", "List agent clients, scopes and limits.", "Never prints credentials or hashes. Does not require an interactive terminal."},
	{"agents revoke", "agents revoke NAME", "Revoke an agent credential; human only, interactive terminal required.", "Requires typing the client name to confirm; there is no --yes. A running owner reloads the change."},
	{"webhooks", "webhooks add|list|remove|test|deadletter", "Push identifier-only change notifications to HTTPS endpoints.", "Human only: every subcommand (add, list, remove, test, deadletter) must be run by a human in an interactive terminal; there is no --yes, no agent scope, and no API route. Events never carry message text; receivers fetch content through the scoped /api/v1. Delivery runs while tui or serve is running. Use help webhooks add, help webhooks list, help webhooks remove, help webhooks test, or help webhooks deadletter."},
	{"webhooks add", "webhooks add NAME --url https://HOST/PATH [--chat CHAT_JID ...] [--op OP,OP]", "Register a webhook endpoint and print its signing secret once; human only.", "HTTPS with a public hostname only; loopback, private, link-local, carrier-grade NAT and metadata addresses are refused, redirects are not followed. --chat (repeatable) and --op limit events. Requires typing the endpoint name; there is no --yes. The secret is also stored in webhooks.json, readable only by your OS user."},
	{"webhooks list", "webhooks list [--json]", "List webhook endpoints, filters and status.", "Never prints signing secrets. Requires an interactive terminal because endpoint URLs may carry tokens."},
	{"webhooks remove", "webhooks remove NAME", "Stop delivery to an endpoint and delete its secret; human only.", "Requires typing the endpoint name; there is no --yes. A running owner reloads the change."},
	{"webhooks test", "webhooks test NAME", "Send a signed ping event to an endpoint now; human only.", "Uses the same SSRF-guarded sender as delivery and reports the outcome. Requires an interactive terminal."},
	{"webhooks deadletter", "webhooks deadletter [NAME] [--json] [--clear]", "List events that could not be delivered, or empty the list.", "Entries are identifiers and outcomes only. Requires an interactive terminal because entries include endpoint URLs and chat IDs."},
	{"version", "version", "Show installed build information.", "Also available as wasync --version."},
	{"help", "help [COMMAND [SUBCOMMAND]]\n       wasync help --json", "Discover all commands without connecting or opening the archive.", "Use wasync COMMAND [SUBCOMMAND] --help as an alternative. --json emits a versioned command catalog, root help, and query conventions. Global flags precede the command."},
}

// extendedHelp appends a command's full reference text to its catalog help.
var extendedHelp = map[string]string{
	"query message":    queryMessageUsage,
	"query thread":     queryThreadUsage,
	"query changes":    queryChangesUsage,
	"query checkpoint": queryCheckpointUsage,
	"query doctor":     queryDoctorUsage,
	"agents":           agentsUsage,
	"agents grant":     agentsUsage,
	"agents list":      agentsUsage,
	"agents revoke":    agentsUsage,

	"webhooks":            webhooksUsage,
	"webhooks add":        webhooksUsage,
	"webhooks list":       webhooksUsage,
	"webhooks remove":     webhooksUsage,
	"webhooks test":       webhooksUsage,
	"webhooks deadletter": webhooksUsage,
}

func (a *App) printHelp(path string) error {
	return a.writeHelp(a.Out, path)
}

func (a *App) writeHelp(out io.Writer, path string) error {
	if path == "" {
		_, err := fmt.Fprint(out, usage)
		return err
	}
	for _, command := range commandCatalog {
		if command.Path == path {
			_, err := fmt.Fprintf(out, "Usage: wasync %s\n\n%s\n\n%s\n\nGlobal flags precede the command. See wasync help for global flags.\n", command.Usage, command.Description, command.Details)
			if extended := extendedHelp[path]; err == nil && extended != "" {
				_, err = fmt.Fprint(out, "\n"+extended)
			}
			return err
		}
	}
	return fmt.Errorf("unknown help topic %q; run wasync help --json to list commands", path)
}

// Recognize explicit help paths before configuration validation or local I/O.
// Only interpret a trailing help flag when all preceding arguments are command
// path words; a value such as `send --text --help` must remain a literal value.
func (a *App) dispatchHelp(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if args[0] == "help" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			return true, a.printHelp("help")
		}
		if len(args) == 2 && args[1] == "--json" {
			return true, json.NewEncoder(a.Out).Encode(struct {
				SchemaVersion int           `json:"schema_version"`
				RootHelp      string        `json:"root_help"`
				QueryHelp     string        `json:"query_help"`
				Commands      []commandHelp `json:"commands"`
			}{1, usage, queryUsage, commandCatalog})
		}
		return true, a.printHelp(strings.Join(args[1:], " "))
	}
	last := args[len(args)-1]
	if last != "--help" && last != "-h" {
		return false, nil
	}
	for _, arg := range args[:len(args)-1] {
		if strings.HasPrefix(arg, "-") {
			return false, nil
		}
	}
	path := strings.Join(args[:len(args)-1], " ")
	for _, command := range commandCatalog {
		if command.Path == path {
			return true, a.printHelp(path)
		}
	}
	// A positional argument may precede --help (notably query search PHRASE).
	// Let the command's parser distinguish its arguments from command path words.
	return false, nil
}

// The flag package emits usage both for explicit help and for parse failures.
// Buffer it until the result is known so successful help goes to stdout and
// invalid invocations leave stdout clean for machine-readable command results.
func (a *App) parseFlags(f *flag.FlagSet, args []string) error {
	var output bytes.Buffer
	original := f.Output()
	f.SetOutput(&output)
	defer f.SetOutput(original)
	err := f.Parse(args)
	destination := a.Err
	if errors.Is(err, flag.ErrHelp) {
		destination = a.Out
	}
	_, writeErr := io.Copy(destination, &output)
	if writeErr != nil {
		return writeErr
	}
	return err
}
