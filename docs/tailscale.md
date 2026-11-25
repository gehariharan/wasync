# Opening the web app from your other computers (Tailscale)

wasync listens only on this computer's loopback address, so `wasync web` works only here. If
you use [Tailscale](https://tailscale.com), you can open the same web app from your other
computers and phones on your own tailnet, through `tailscale serve` on the computer running
wasync. It is off unless you turn it on.

wasync still listens on loopback only. `tailscale serve` accepts HTTPS on this computer's tailnet
name, for devices in your tailnet only, and passes each request to the loopback address. Never
use `tailscale funnel` for this: Funnel puts a service on the public internet, and wasync refuses
every request that comes through it.

## Turn it on

You need this computer's tailnet name and your Tailscale login. Both appear in
`tailscale status --json` (`Self.DNSName`, without the trailing dot, and the `LoginName` of your
user), and the name is also shown in the Tailscale admin console under Machines. HTTPS
certificates must be enabled for your tailnet (admin console, DNS, HTTPS Certificates).

1. With wasync running (`wasync serve`, the login item or the TUI), allow the name and your login:

   ```sh
   wasync tailnet allow --host machine.example.ts.net --login you@example.com
   ```

   Type `ALLOW` and confirm the system prompt. Repeat `--host` or `--login` to allow more than
   one. A running wasync picks the setting up at once.

2. Share the loopback address with your tailnet over HTTPS:

   ```sh
   tailscale serve --bg http://127.0.0.1:8787
   ```

   Use the port wasync listens on if you changed `--api-addr`. `tailscale serve status` shows
   what is shared.

## Open it from another computer

On any device signed in to your tailnet as an allowed login, open:

```text
https://machine.example.ts.net/ui/
```

There is no link to copy: the page signs you in because Tailscale says who you are. A session
lasts 30 minutes; after that, opening any page signs you in again.

## What works remotely

Reading chats, sending as yourself, the Sent page, choosing which chats sync, downloads and
anything that narrows access (stopping an agent's sending, removing a list, turning open sending
off, rejecting a request) work as they do locally.

Anything that needs the operating system's confirmation prompt does not: approving an agent's
request, sync all chats, letting an agent send, open sending, approving a list, connecting an
agent and start at login. That prompt appears on the computer running wasync, where nobody at
the other computer can answer it, so the remote page says to do it there instead.

Only the web app is served on the tailnet name. The API, `/api/v1` and everything agents and
the CLI use stay on the loopback address, and agent credentials work there exactly as before.

## Who counts as you

Requests through `tailscale serve` arrive from loopback, so arriving from loopback proves
nothing. wasync instead reads the `Tailscale-User-Login` header that `tailscale serve` sets to
the signed-in Tailscale user (it removes any copy the browser sent) and accepts only the logins
you allowed. Requests are refused when:

- the Host is not an allowed tailnet name (exactly as when the setting is off),
- there is no login, or a login you did not allow (tagged devices and devices shared from other
  tailnets carry none or another one),
- they came through Funnel, or
- they ask for anything but the web app, or a form comes from another origin.

Each session belongs to the login that opened it and to the tailnet name; it does not work on
the loopback address or for another login.

The login header is trusted because only `tailscale serve` can reach wasync from another
computer. A program on this computer can connect to the loopback port too, and could send the
same header. Programs running as you can already read your archive, but other accounts on this
computer could use that to open the web app. Turn this on only on a computer where you are the
only user.

## Turn it off

```sh
wasync tailnet off
tailscale serve --https=443 off
```

`wasync tailnet off` takes effect at once: the tailnet name is refused again and existing remote
sessions stop working. The second command stops sharing the port (`tailscale serve reset`
removes everything `tailscale serve` shares). `wasync tailnet status` shows the setting.
