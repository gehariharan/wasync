# The wasync app

The wasync app is wasync's own local pages in a window of the system web view
(WKWebView on macOS, WebView2 on Windows), with an icon in the menu bar (macOS) or
the notification area (Windows). It is built with [Wails 3](https://wails.io),
pinned to an exact version in `go.mod`. It runs on macOS 15+ and Windows 10/11; on
Linux, wasync stays a terminal program (`wasync setup`).

## What it does

- **Opening the app** shows its window. The window opens on the setup steps until
  the First open screen has been seen, a phone is linked and the steps are finished or
  skipped (a setup left part-way opens again at the furthest step reached), and after that on Home
  (when wasync is for your AI agents only) or the chats. Each window signs in with a fresh one-time link, as `wasync web` does.
- **The icon's menu** has *Open wasync*, a status line ("Syncing 12 chats", "Not
  linked to WhatsApp yet", "Connecting to WhatsApp…") and *Quit wasync*. On Windows a
  click on the icon opens the window; the menu is on the right button.
- **Closing the window** keeps wasync running behind the icon. The window's web view
  is freed; on macOS the Dock icon goes away until the window opens again.
- **Only one copy runs** for each person. Opening the app again shows the window of
  the copy that is running.
- **Quitting** stops the wasync the app started, cleanly, and waits up to 20 seconds
  for it.
- **Links to other websites** and email addresses open in your default browser; the
  window only ever shows wasync's own pages. A link to one of wasync's pages that would
  open a new tab opens in the window, since the browser is not signed in to them.

## How it runs wasync

The app does not contain the sync engine. It starts the `wasync` command that ships
beside it (`wasync.app/Contents/MacOS/wasync`, or `wasync.exe` in the install folder)
as `wasync setup --app`, which is the setup page's owner process with three
differences:

- no browser opens; the app asks for one-time links on that process's standard input
  and reads them on its standard output, which only the app holds, so the app never
  needs the approval credential and no link is ever printed;
- what wasync says for a person goes to standard error, which the app appends to
  `desktop.log` in the data directory, with its own messages;
- when the app quits or goes away, standard input closes and wasync stops.

A wasync that fails is started again after a pause that grows to a minute. One that
stops cleanly (`wasync stop`, or a phone that was unlinked) stays stopped until the
app is opened again. One that stops to install an update (**Update and restart…**)
makes the app quit too; once the update is in place, the helper opens the new app,
which starts its own wasync as usual. One that stops because you chose **Settings >
Remove wasync** makes the app quit as well, and is not started again, so the removal
can take the app away (see [Removing wasync](install.md#removing-wasync)).

If another wasync already holds the data directory (a `wasync serve` login item, or
`wasync tui` in a terminal), the app shows that one's pages instead of starting its
own, reading its links with the approval credential as `wasync web` does, and leaves
it running when the app quits. The status line then ends in "(in another wasync)".

The app takes the same `--data-dir` and `--api-addr` options as wasync, and
`--background` to start with no window, as it does at login.

## Start when I log in

The app's own login item opens the app at login with `--background`: no window, only
the icon. It is separate from the `wasync service install` item, which starts
`wasync serve`.

- macOS: a LaunchAgent, `~/Library/LaunchAgents/com.wasync.app.plist`, shown in
  System Settings, General, Login Items under the app's name. It is not kept alive, so
  quitting the app keeps it quit.
- Windows: the value `wasync-app` under
  `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`. Uninstalling removes it.

When the app started wasync, the **Start wasync when I log in** switch (setup's Keep
running step, or Settings) adds this item instead of the `wasync serve` one. Turning it
on or off asks for no confirmation: the item only starts wasync for your own account and
gives nothing new access. It can only be changed on the computer running wasync, never
from another device on the tailnet.

With both items, whichever starts first holds the data directory and the other cannot
run. So when the app starts and finds a `wasync service install` item that starts
wasync for the same data directory as the app, it moves you to its own item: it adds
the app's item if it is not there yet, then removes the old one. You already chose to
start at login, so it asks nothing, and `desktop.log` says what it did. On macOS,
removing the old item also stops the `wasync serve` it started; on Windows the app asks
that `wasync serve` to stop cleanly, as `wasync stop` does, so the app can start its own.
An old item for a different data directory is left alone.

## Settings the setup screens use

The setup screens read and change two settings with a script, on a signed-in page of
this computer (a device signed in through the tailnet gets 403). Each answer is this
JSON:

```json
{"first_open": true, "start_at_login": {"available": true, "on": false, "where": "a macOS login item"}}
```

| Request | Effect |
| --- | --- |
| `GET /ui/desktop` | Reads both settings. `first_open` is true until the First open screen has been seen for this data directory. `confirm` is kept for older app builds and is always left out: start-at-login asks for no confirmation. |
| `POST /ui/desktop/first-open` with the page's `csrf` field | Records that the First open screen was seen (`first-open.done` in the data directory). |
| `POST /ui/desktop/start-at-login` with `csrf` and `on=true` or `on=false` | Turns start-at-login on or off. |

The app itself reads the First open flag from the data directory to choose its first
page.

## Packages

- **macOS:** `wasync_VERSION_darwin_ARCH.dmg`, a disk image whose window asks you to
  drag `wasync.app` onto Applications (the full steps are in
  [Installing wasync](install.md#macos-app)). The app carries `wasync-desktop` (the app),
  `wasync` and `wasync-axi`; the agent command is always there and does nothing until
  an agent is connected. The app's bundle identifier is the one the `wasync` command
  is signed with (`com.gehariharan.wasynctuicli`), so the Keychain trusts the app, its
  `wasync` and a `wasync` installed elsewhere for the same approval credential.
- **Windows:** Setup installs `wasync-desktop.exe` and `wasync-axi.exe` beside
  `wasync.exe`; the Start menu entry and the finish page's "Open wasync now" open the
  app.

The pictures (app icons, menu-bar glyph, Setup's side pictures and the disk image
background) are drawn from logo D by `packaging/desktop/assetgen`; see the comment at
the top of its `main.go` to draw them again.
