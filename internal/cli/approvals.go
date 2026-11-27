package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"

	"github.com/gehariharan/wasync/internal/approvecred"
	"github.com/gehariharan/wasync/internal/config"
)

const approvalsUsage = `Usage: wasync approvals

Open the approval page in your browser, without a terminal: the messages agents
are asking to send, each approved on its own, and your own delete confirmations.
It needs wasync running (the TUI or serve). The link works once and only for two
minutes, so run this command again for a fresh one.

Nothing on the page approves by itself. Each approval still asks for Touch ID or
your password on macOS, or Windows Hello or your password on Windows, and shows exactly what you
are approving. If this computer cannot show that prompt, the page lists requests
but approvals stay in the terminal (wasync actions approve).
`

// runApprovals opens the approval page. It only fetches a one-time link from
// the running owner; it cannot approve anything.
func (a *App) runApprovals(ctx context.Context, cfg config.Config, args []string) error {
	fs := a.flags("approvals")
	fs.Usage = func() { fmt.Fprint(fs.Output(), approvalsUsage) }
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync approvals")
	}
	return a.openPage(ctx, cfg, "")
}

const webUsage = `Usage: wasync web

Open the web app in your browser: the chats wasync is syncing and their
recent messages, and a picker to choose which chats to sync. It needs wasync
running (the TUI or serve). The link works once and only for two minutes, so run
this command again for a fresh one.

The page shows message text, so open it only on a computer you trust. Choosing
chats changes what wasync keeps; agents still see only what you gave them.
`

// runWeb opens the chats page the same way runApprovals opens the approval page.
func (a *App) runWeb(ctx context.Context, cfg config.Config, args []string) error {
	fs := a.flags("web")
	fs.Usage = func() { fmt.Fprint(fs.Output(), webUsage) }
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync web")
	}
	return a.openPage(ctx, cfg, "chats")
}

// openPage asks the running owner for a one-time link to a page and opens it
// in the browser. The link is never printed, so a program that ran this command
// does not learn it.
func (a *App) openPage(ctx context.Context, cfg config.Config, page string) error {
	link, err := a.pageLink(ctx, cfg, page)
	if err != nil {
		return err
	}
	return a.openLink(link)
}

// pageLink asks the running owner for a one-time link to a page.
func (a *App) pageLink(ctx context.Context, cfg config.Config, page string) (string, error) {
	if cfg.NoAPI {
		return "", fmt.Errorf("the page needs the local API; remove --no-api")
	}
	secret, err := approvecred.Read(cfg.DataDir)
	if err != nil {
		return "", fmt.Errorf("start wasync serve or the TUI first: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"page": page})
	response, err := commandRequest(ctx, cfg, "POST", "/api/ui/link", body, []byte(secret))
	if err != nil {
		return "", fmt.Errorf("start wasync serve or the TUI first: %w", err)
	}
	defer response.Body.Close()
	var out struct {
		URL string `json:"url"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<10)).Decode(&out); err != nil || response.StatusCode != 200 || out.URL == "" {
		return "", fmt.Errorf("the running wasync did not give a link (HTTP %d)", response.StatusCode)
	}
	return out.URL, nil
}

func (a *App) openLink(link string) error {
	open := a.OpenURL
	if open == nil {
		open = openBrowser
	}
	if err := open(link); err != nil {
		return fmt.Errorf("could not open the page in a browser: %w", err)
	}
	_, err := fmt.Fprintln(a.Err, "Opened the page in your browser. The link works once.")
	return err
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("/usr/bin/open", url).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Run()
	}
	return exec.Command("xdg-open", url).Run()
}

// humanHint tells the person at the terminal that a system prompt follows the
// typed phrase, so they know to look at the screen rather than wait.
func (a *App) humanHint() {
	if a.Human.Enabled() {
		fmt.Fprintf(a.Err, "Now confirm on this computer: %s. The prompt names exactly what you are approving.\n", a.Human.Name())
	}
}

// confirmHuman asks the person at this computer to confirm prompt with the
// system's own check, where there is one. It returns nil at once when this
// computer has none, so the typed phrase in a terminal stays the only check.
func (a *App) confirmHuman(ctx context.Context, prompt string) error {
	if !a.Human.Enabled() {
		return nil
	}
	a.humanHint()
	return a.Human.Confirm(ctx, prompt)
}

// announce tells the person something is waiting. It is a nudge only.
func (a *App) announce(text string) {
	if a.Notify != nil {
		a.Notify("wasync", text)
	}
}

// approvalCheckLine says, once at start-up, what stands between an agent's
// request and an approval on this computer.
func (a *App) approvalCheckLine() string {
	if a.Human.Enabled() {
		return "Approvals: each one asks for " + a.Human.Name() + " on this computer, and the approval page (wasync approvals) works."
	}
	return "Approvals: this computer cannot show a system confirmation, so a typed phrase in a terminal is the only check. Do not run agents with access to your data folder."
}
