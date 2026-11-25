package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/gehariharan/wasync/internal/agentauth"
	"github.com/gehariharan/wasync/internal/config"
	"github.com/gehariharan/wasync/internal/tailnet"
)

const tailnetUsage = `Usage: wasync tailnet allow --host NAME --login LOGIN [--host NAME ...] [--login LOGIN ...]
       wasync tailnet off
       wasync tailnet status

Open the web app from your other computers on your Tailscale network, through
` + "`tailscale serve`" + ` on this computer. wasync keeps listening on loopback only.

  allow   --host is this computer's tailnet name, as in its HTTPS address
          (for example machine.example.ts.net). --login is the Tailscale login
          that counts as you (for example you@example.com). Both repeat.
          Human only: needs an interactive terminal, typing ALLOW and, where
          available, the system prompt; there is no --yes.
  off     Stop answering on the tailnet name at once.
  status  Show the setting.

Then share the loopback address with your tailnet over HTTPS (never use
tailscale funnel, which would make it public):

  tailscale serve --bg http://127.0.0.1:8787

and open https://NAME/ui/ on the other computer. Approvals, giving an agent
more power and start at login still need the computer running wasync. To stop
sharing, run wasync tailnet off and tailscale serve --https=443 off.
`

func (a *App) runTailnet(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 || isHelpArg(args) || args[0] == "help" {
		_, err := fmt.Fprint(a.Out, tailnetUsage)
		return err
	}
	cfg, err := cfg.Validate()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			break
		}
		s, on, err := tailnet.Load(cfg.DataDir)
		if err != nil {
			return err
		}
		if !on {
			_, err = fmt.Fprintln(a.Out, "tailnet access: off")
			return err
		}
		_, err = fmt.Fprintf(a.Out, "tailnet access: on\nhosts: %s\nlogins: %s\n", strings.Join(s.Hosts, ", "), strings.Join(s.Logins, ", "))
		return err
	case "off":
		if len(args) != 1 {
			break
		}
		if err := tailnet.Remove(cfg.DataDir); err != nil {
			return err
		}
		_ = agentauth.NewAuditLog(cfg.DataDir).Append(agentauth.AuditEntry{Event: "tailnet_off", Outcome: agentauth.OutcomeAllowed})
		_, err := fmt.Fprintln(a.Out, "tailnet access: off. Also stop sharing with: tailscale serve --https=443 off")
		return err
	case "allow":
		return a.tailnetAllow(ctx, cfg, args[1:])
	}
	return fmt.Errorf("usage: wasync tailnet allow|off|status; see wasync tailnet --help")
}

func (a *App) tailnetAllow(ctx context.Context, cfg config.Config, args []string) error {
	fs := a.flags("tailnet allow")
	fs.Usage = func() { fmt.Fprint(fs.Output(), tailnetUsage) }
	var hosts, logins stringList
	fs.Var(&hosts, "host", "this computer's tailnet name (repeatable)")
	fs.Var(&logins, "login", "Tailscale login that counts as you (repeatable)")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: wasync tailnet allow --host NAME --login LOGIN")
	}
	s, err := tailnet.Setting{Hosts: hosts, Logins: logins}.Validate()
	if err != nil {
		return err
	}
	if !a.Interactive {
		return fmt.Errorf("tailnet allow requires an interactive terminal; nothing changed")
	}
	effect := "let " + strings.Join(s.Logins, ", ") + " open the wasync web app, read your chats and send as you, from any device on your tailnet at https://" + strings.Join(s.Hosts, ", https://") + "/ui/"
	fmt.Fprintf(a.Err, "This will %s.\nType ALLOW then Enter to continue: ", effect)
	line, err := readLine(ctx, a.In)
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "ALLOW" {
		return fmt.Errorf("confirmation did not match; nothing changed")
	}
	if err = a.confirmHuman(ctx, "Approve: "+effect); err != nil {
		return fmt.Errorf("%w; nothing changed", err)
	}
	if err = tailnet.Save(cfg.DataDir, s); err != nil {
		return err
	}
	_ = agentauth.NewAuditLog(cfg.DataDir).Append(agentauth.AuditEntry{Event: "tailnet_allow", Outcome: agentauth.OutcomeAllowed})
	_, err = fmt.Fprintf(a.Out, "tailnet access: on. Now run: tailscale serve --bg http://%s\nThen open https://%s/ui/ on your other computer.\n", cfg.APIAddr, s.Hosts[0])
	return err
}
