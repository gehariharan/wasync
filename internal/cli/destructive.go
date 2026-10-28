package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
)

// Agent-facing destructive commands only enqueue requests. There is no --yes.
func (a *App) runDestructiveRequest(ctx context.Context, cfg config.Config, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	fs := a.flags(args[0])
	in := approval.Input{}
	fs.StringVar(&in.Chat, "chat", "", "exact chat ID")
	fs.StringVar(&in.Message, "message-id", "", "archived message ID")
	all := fs.Bool("all", false, "request a local chat purge")
	scope := fs.String("scope", "", "WhatsApp scope: everyone or my-whatsapp-chat")
	if err := a.parseFlags(fs, args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if args[0] == "clean" {
		if *scope != "" || (*all == (in.Message != "")) {
			return fmt.Errorf("usage: wasync clean --chat CHAT (--message-id ID | --all); interactive approval required")
		}
		in.Kind = "clear-message"
		if *all {
			in.Kind = "purge-chat"
		}
	} else {
		if *all {
			return fmt.Errorf("remote deletion requires an explicit --scope")
		}
		switch *scope {
		case "everyone":
			in.Kind = "revoke-message"
		case "my-whatsapp-chat":
			in.Kind = "delete-whatsapp-chat"
		default:
			return fmt.Errorf("use --scope everyone --message-id ID, or --scope my-whatsapp-chat")
		}
	}
	if err := in.Validate(); err != nil {
		return err
	}
	if cfg.NoAPI {
		return fmt.Errorf("destructive CLI requests require the running sync command channel")
	}
	path := filepath.Join(cfg.DataDir, "send.token")
	if err := localfs.CheckPrivateFile(path); err != nil {
		return fmt.Errorf("start wasync serve or the TUI to receive this request; no data changed")
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	response, err := commandRequest(ctx, cfg, "POST", "/api/actions", body, token)
	if err != nil {
		return fmt.Errorf("could not queue approval request; no destructive CLI operation was executed: %w", err)
	}
	defer response.Body.Close()
	if err = printCommandJSON(a.Out, response.Body); err != nil {
		return err
	}
	if response.StatusCode != 202 {
		return fmt.Errorf("approval request was not accepted; start wasync serve or the TUI")
	}
	return nil
}

func (a *App) runActionStatus(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 2 || (args[0] != "status" && args[0] != "approve") || len(args[1]) != 32 {
		return fmt.Errorf("usage: wasync actions status|approve REQUEST_ID")
	}
	if _, err := hex.DecodeString(args[1]); err != nil {
		return fmt.Errorf("invalid request ID")
	}
	path := filepath.Join(cfg.DataDir, "send.token")
	if err := localfs.CheckPrivateFile(path); err != nil {
		return err
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	response, err := commandRequest(ctx, cfg, "GET", "/api/actions/"+args[1], nil, token)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if args[0] == "approve" {
		if !a.Interactive {
			return fmt.Errorf("approval requires an interactive terminal; no data changed")
		}
		if response.StatusCode != 200 {
			return fmt.Errorf("request unavailable")
		}
		var request approval.Request
		if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&request); err != nil {
			return err
		}
		if request.ID != args[1] || request.Status != "pending_human_confirmation" {
			return fmt.Errorf("request is no longer pending")
		}
		if request.Kind == approval.KindChatAccess {
			return a.approveChatAccess(ctx, cfg, request)
		}
		scope := map[string]string{"clear-message": "Remove one message from this local archive only", "purge-chat": "Permanently purge this local chat archive and its downloaded files", "revoke-message": "Ask WhatsApp to revoke YOUR message for everyone; server limits apply", "delete-whatsapp-chat": "Delete YOUR WhatsApp chat state through the last known message; local archive remains"}[request.Kind]
		if scope == "" {
			return fmt.Errorf("unsupported action")
		}
		code := "DELETE " + request.ID[len(request.ID)-6:]
		fmt.Fprintf(a.Err, "%s\nChat: %s\nMessage: %s\nType %s then Enter to execute: ", scope, request.Chat, request.Message, code)
		line, readErr := readLine(ctx, a.In)
		if readErr != nil {
			return readErr
		}
		if strings.TrimSpace(line) != code {
			return fmt.Errorf("confirmation did not match; no data changed")
		}
		return a.ownerCommand(ctx, cfg, "POST", "/api/actions/"+request.ID+"/approve", nil, "approve.token")
	}
	if err = printCommandJSON(a.Out, response.Body); err != nil {
		return err
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("request status unavailable")
	}
	return nil
}

// approveChatAccess walks the human through an agent's chat access request:
// which chat, which history depth, then the exact effect and a typed phrase.
// Nothing is sent to wasync until the phrase matches.
func (a *App) approveChatAccess(ctx context.Context, cfg config.Config, request approval.Request) error {
	acc := request.Access
	if acc == nil || len(acc.Candidates) == 0 {
		return fmt.Errorf("request has no chat to approve; no data changed")
	}
	level, effect := "READ", "read access to"
	if acc.Send {
		level, effect = "READ and SEND", "read and send access (including reactions and edits of its own messages) to"
	}
	fmt.Fprintf(a.Err, "Agent %s asks for %s access to one WhatsApp chat.\nIt asked for: %s\n", printable(acc.Agent), level, printable(acc.Query))
	pick := 0
	if len(acc.Candidates) > 1 {
		fmt.Fprintln(a.Err, "Chats it may mean:")
		for i, c := range acc.Candidates {
			fmt.Fprintf(a.Err, "  %d) %s  %s\n", i+1, printable(c.Name), printable(c.Chat))
		}
		if acc.Truncated {
			fmt.Fprintln(a.Err, "  (more chats matched; the agent can ask again with a more specific name)")
		}
		fmt.Fprintf(a.Err, "Choose the chat [1-%d]: ", len(acc.Candidates))
		line, err := readLine(ctx, a.In)
		if err != nil {
			return err
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr != nil || n < 1 || n > len(acc.Candidates) {
			return fmt.Errorf("no chat chosen; no data changed")
		}
		pick = n - 1
	}
	chosen := acc.Candidates[pick]
	fmt.Fprint(a.Err, "History depth:\n  1) From now on only (new messages; no older messages requested)\n  2) Full history sync (also asks your phone for this chat's older messages, best effort)\nChoose [1-2]: ")
	line, err := readLine(ctx, a.In)
	if err != nil {
		return err
	}
	history, historyText := "", ""
	switch strings.TrimSpace(line) {
	case "1":
		history, historyText = approval.HistoryNow, "from now on only"
	case "2":
		history, historyText = approval.HistoryFull, "full history sync"
	default:
		return fmt.Errorf("no history depth chosen; no data changed")
	}
	code := "ALLOW " + request.ID[len(request.ID)-6:]
	fmt.Fprintf(a.Err, "\nApproving will turn on capture for %s (%s), history: %s,\nand give agent %s %s only this chat. Nothing else changes.\nType %s then Enter to approve: ", printable(chosen.Name), printable(chosen.Chat), historyText, printable(acc.Agent), effect, code)
	line, err = readLine(ctx, a.In)
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != code {
		return fmt.Errorf("confirmation did not match; no data changed")
	}
	body, err := json.Marshal(map[string]string{"chat": chosen.Chat, "history": history})
	if err != nil {
		return err
	}
	return a.ownerCommand(ctx, cfg, "POST", "/api/actions/"+request.ID+"/approve", body, "approve.token")
}

// printable drops control characters so a chat name from WhatsApp cannot
// rewrite the confirmation the person is reading.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func printCommandJSON(out io.Writer, body io.Reader) error {
	const maxResponse = 4 << 20
	data, err := io.ReadAll(io.LimitReader(body, maxResponse+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponse {
		return fmt.Errorf("command response exceeds 4 MiB; use read-only query commands for paginated results")
	}
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("command channel returned invalid JSON")
	}
	return json.NewEncoder(out).Encode(value)
}
