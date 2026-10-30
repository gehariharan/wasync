package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/localfs"
)

const approvalsUsage = `Usage: wasync approvals [--print]

Open the approval page in your browser: the requests agents are waiting on, and
the choice between asking each time and syncing all chats, without a terminal.
It needs wasync running (the TUI or serve). The link works once and only for two
minutes, so run this command again for a fresh one.

Nothing on the page approves by itself. Each approval still asks for Touch ID or
your password on macOS, or Windows Hello on Windows, and shows exactly what you
are approving. If this computer cannot show that prompt, the page lists requests
but approvals stay in the terminal (wasync actions approve).

  --print   print the link instead of opening the browser
`

// runApprovals opens the approval page. It only fetches a one-time link from
// the running owner; it cannot approve anything.
func (a *App) runApprovals(ctx context.Context, cfg config.Config, args []string) error {
	fs := a.flags("approvals")
	fs.Usage = func() { fmt.Fprint(fs.Output(), approvalsUsage) }
	printOnly := fs.Bool("print", false, "print the link instead of opening the browser")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync approvals [--print]")
	}
	if cfg.NoAPI {
		return fmt.Errorf("the approval page needs the local API; remove --no-api")
	}
	path := filepath.Join(cfg.DataDir, "approve.token")
	if err := localfs.CheckPrivateFile(path); err != nil {
		return fmt.Errorf("start wasync serve or the TUI first: %w", err)
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	response, err := commandRequest(ctx, cfg, "POST", "/api/ui/link", nil, token)
	if err != nil {
		return fmt.Errorf("start wasync serve or the TUI first: %w", err)
	}
	defer response.Body.Close()
	var out struct {
		URL string `json:"url"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<10)).Decode(&out); err != nil || response.StatusCode != 200 || out.URL == "" {
		return fmt.Errorf("the running wasync did not give a link (HTTP %d)", response.StatusCode)
	}
	if *printOnly {
		_, err = fmt.Fprintln(a.Out, out.URL)
		return err
	}
	open := a.OpenURL
	if open == nil {
		open = openBrowser
	}
	if err = open(out.URL); err != nil {
		fmt.Fprintf(a.Err, "could not open a browser (%v); run wasync approvals --print and open the link yourself\n", err)
		return err
	}
	_, err = fmt.Fprintln(a.Err, "Opened the approval page in your browser. The link works once.")
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
