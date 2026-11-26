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

   Type `ALLOW` and confirm the system prompt. Give one name and one login; running `allow` again
   replaces them. A running wasync picks the setting up at once.

2. Share the loopback address with your tailnet over HTTPS:

   ```sh
   tailscale serve --bg http://127.0.0.1:8787
   ```

   Use the port wasync listens on if you changed `--api-addr`. `tailscale serve status` shows
   what is shared.

## Open it from another computer

On any device signed in to your tailnet as the allowed login, open:

```text
https://machine.example.ts.net/ui/
```

It opens on Chats. The app has a name and icon, so you can pin it like an app, for example with
Safari's **File > Add to Dock** on a Mac or **Add to Home Screen** on a phone.

There is no link to copy: the page signs you in because Tailscale says who you are. A session
lasts 30 minutes after the page you last opened; after that, opening any page signs you in again.

## What works remotely

Reading chats, sending as yourself, the Sent page, choosing which chats sync, downloads and
anything that narrows access (stopping an agent's sending, pausing or removing a list, turning
open sending off, rejecting a request) work as they do locally.

Anything that asks for the operating system's confirmation on the computer running wasync needs
a passkey on the device you are using instead: approving an agent's request, sync all chats,
letting an agent send, open sending, approving or resuming a list, connecting an agent and start
at login. Until you set the device up (below), the remote pages show these as read-only and say
how to set it up; you can still do them on the computer running wasync.

Only the web app is served on the tailnet name. The API, `/api/v1` and everything agents and
the CLI use stay on the loopback address, and agent credentials work there exactly as before.

## Approve from another device

Set each device up once. You need the computer running wasync for it, so that nobody who only
reaches your tailnet, and no program on that computer, can add a device:

1. On the computer running wasync, open the web app (`wasync web`), go to **Set up** and choose
   **Add a device** under *Approve from your other devices*. Confirm the system prompt (Touch ID
   or your password). The page shows a one-time code, such as `K7QP-M2XD-9HTA`. It works once,
   for 10 minutes.
2. On the other device, open `https://machine.example.ts.net/ui/setup`, enter the code and a
   name for the device, and choose **Set up this device**. Your browser makes a passkey for
   wasync and asks you to confirm with Touch ID, Face ID, Windows Hello or the device's PIN.

From then on, an approval on that device opens a page that shows the same sentence the system
prompt on the computer would, and **Confirm with passkey** asks for the passkey. Nothing changes
until you confirm, and a page you leave expires after 5 minutes.

A passkey your password manager syncs (for example iCloud Keychain to your iPhone) works on
those devices too, since each of them still asks for your face, fingerprint or PIN. A phone
nearby can also answer for a computer through the browser's "use a phone" option.

The Set up page on either computer lists the devices, when each was added and last used, with a
**Remove** button that takes effect at once and needs no confirmation. Allowing another tailnet name or login forgets the devices set up for the old
one, and `wasync tailnet off` forgets them all.

Adding a device needs the system prompt, so on a computer that cannot show one (Linux, or a
session with no screen) devices cannot be added. The terminal commands (`wasync agents grant`
and `revoke`, `wasync tailnet allow`, `wasync actions approve` and the rest) are unchanged and
work only on the computer running wasync.

## Who counts as you

Requests through `tailscale serve` arrive from loopback, so arriving from loopback proves
nothing. wasync instead reads the `Tailscale-User-Login` header that `tailscale serve` sets to
the signed-in Tailscale user (it removes any copy the browser sent) and accepts only the login
you allowed. Requests are refused when:

- the Host is not the allowed tailnet name on the default HTTPS port (exactly as when the setting
  is off),
- there is no login, or a login you did not allow (tagged devices and devices shared from other
  tailnets carry none or another one),
- they came through Funnel, or
- they ask for anything but the web app, or a form comes from another origin.

Each session belongs to the login that opened it and does not work for another login. Browsers
keep cookies per site, so a session opened on the tailnet name is not sent to the loopback
address either.

The login header is trusted because only `tailscale serve` can reach wasync from another
computer. A program on this computer can connect to the loopback port too, and could send the
same header. Programs running as you can already read your archive, but other accounts on this
computer could use that to open the web app. Turn this on only on a computer where you are the
only user.

That is also why the login alone never approves anything. Approvals from another device need a
passkey that only that device holds, set up with a code that only the computer running wasync
shows after its own system prompt. A program that sends the header itself has neither.

## Turn it off

```sh
wasync tailnet off
tailscale serve --https=443 off
```

`wasync tailnet off` takes effect at once: the tailnet name is refused again, existing remote
sessions stop working and every device set up to approve is forgotten. The second command stops sharing the port (`tailscale serve reset`
removes everything `tailscale serve` shares). `wasync tailnet status` shows the setting.
