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
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/gehariharan/wasync/internal/approval"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/humancheck"
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
	if len(args) == 1 && args[0] == "list" {
		return a.runActionsList(ctx, cfg)
	}
	if len(args) != 2 || (args[0] != "status" && args[0] != "approve") || len(args[1]) != 32 {
		return fmt.Errorf("usage: wasync actions list | status|approve REQUEST_ID")
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
		if request.IsAgentRequest() {
			return a.approveAgentSend(ctx, cfg, request)
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
		a.humanHint()
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

// runActionsList shows the requests waiting for the owner. Unlike what a serve
// console line prints, it names the chats being asked for, so it needs an
// interactive terminal like the other sensitive listings.
func (a *App) runActionsList(ctx context.Context, cfg config.Config) error {
	if !a.Interactive {
		return fmt.Errorf("actions list requires an interactive terminal; no data changed")
	}
	token, err := os.ReadFile(filepath.Join(cfg.DataDir, "send.token"))
	if err != nil || localfs.CheckPrivateFile(filepath.Join(cfg.DataDir, "send.token")) != nil {
		return fmt.Errorf("start wasync serve or the TUI to receive requests")
	}
	response, err := commandRequest(ctx, cfg, "GET", "/api/actions", nil, token)
	if err != nil {
		return fmt.Errorf("command channel unavailable; start wasync serve or the TUI: %w", err)
	}
	defer response.Body.Close()
	var list struct {
		Items []approval.Request `json:"items"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&list) != nil {
		return fmt.Errorf("pending requests unavailable (HTTP %d)", response.StatusCode)
	}
	if len(list.Items) == 0 {
		_, err = fmt.Fprintln(a.Out, "No requests are waiting.")
		return err
	}
	w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REQUEST_ID\tASKS\tEXPIRES\tDETAIL")
	for _, r := range list.Items {
		asks, detail := printable(r.Kind), printable(r.Chat)
		if r.IsAgentRequest() {
			asks = "agent " + printable(r.Send.Agent) + ": send"
			detail = sendTarget(r) + ": " + strconv.Quote(humancheck.CleanLines(r.Send.Text))
		} else if r.Message != "" {
			detail += " message " + printable(r.Message)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, asks, time.Until(r.Expires).Round(time.Second), detail)
	}
	if err = w.Flush(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Err, "Approve one with: wasync actions approve REQUEST_ID")
	return err
}

// sendTarget says where an agent's send request goes.
func sendTarget(r approval.Request) string {
	if r.Send.NewNumber {
		return "new number " + approval.PhoneNumber(r.Chat)
	}
	return printable(r.Chat) + " (" + r.Send.Target() + ")"
}

// approveAgentSend shows the person exactly what an agent asks to send, and
// where, then needs a typed phrase. Nothing is sent until the phrase matches
// (and, on a computer that has one, the system confirmation).
func (a *App) approveAgentSend(ctx context.Context, cfg config.Config, request approval.Request) error {
	fmt.Fprintf(a.Err, "Agent %s asks to send ONE message to %s.\nMessage:\n%s\n", printable(request.Send.Agent), sendTarget(request), quotedLines(humancheck.CleanLines(request.Send.Text)))
	code := "SEND " + request.ID[len(request.ID)-6:]
	fmt.Fprintf(a.Err, "\nApproving sends exactly this message once, from your WhatsApp. The agent gets no other access.\nType %s then Enter to approve: ", code)
	line, err := readLine(ctx, a.In)
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != code {
		return fmt.Errorf("confirmation did not match; nothing was sent")
	}
	a.humanHint()
	return a.ownerCommand(ctx, cfg, "POST", "/api/actions/"+request.ID+"/approve", nil, "approve.token")
}

// printable drops control characters so a chat name from WhatsApp cannot
// rewrite the confirmation the person is reading.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// quotedLines marks every line of an agent's message, so none of it can pass
// for wasync's own words.
func quotedLines(s string) string {
	return " │ " + strings.ReplaceAll(s, "\n", "\n │ ")
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
