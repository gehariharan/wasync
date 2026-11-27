# Installing wasync

The [README](../README.md#install) has the short version. This page has the details for each
platform, the agent command, Homebrew, checksums and upgrades. Windows Setup, the
portable ZIP and the Microsoft Store edition are covered in [Windows](windows.md). To build from
source, see [Development](development.md).

## Prebuilt packages

End users do not need Go, Xcode Command Line Tools, or a C compiler. The native
release workflow produces these downloads:

| Platform | Download | Installation |
| --- | --- | --- |
| macOS 15+, Apple Silicon, the wasync app | `wasync_VERSION_darwin_arm64.dmg` | Open the disk image and drag wasync to Applications |
| macOS 15+, Intel, the wasync app | `wasync_VERSION_darwin_amd64.dmg` | Open the disk image and drag wasync to Applications |
| macOS 15+, Apple Silicon, the command only | `wasync_VERSION_darwin_arm64.pkg` | Open the package and follow Installer |
| macOS 15+, Intel, the command only | `wasync_VERSION_darwin_amd64.pkg` | Open the package and follow Installer |
| Linux x86-64, glibc 2.35+ | `wasync_VERSION_linux_amd64.tar.gz` | Extract and run `./install.sh` |
| Linux ARM64, glibc 2.35+ | `wasync_VERSION_linux_arm64.tar.gz` | Extract and run `./install.sh` |
| Windows 10/11 x64 | `wasync_VERSION_windows_amd64-setup.exe` or `.zip` | Per-user Setup or portable ZIP (see [Windows](windows.md)) |

The release workflow builds native Intel/Apple Silicon macOS packages and
Linux and Windows packages. Manual builds can remain drafts; publishing also updates the
Homebrew tap and runs the [installation checks](releasing.md#validation). Downloadable
versions appear on the
[GitHub Releases page](https://github.com/gehariharan/wasync/releases).
Source code and prebuilt downloads are public. No GitHub account is needed to install.

The wasync app is wasync in its own window, with a menu-bar icon; it carries the
`wasync` and `wasync-axi` commands inside it. See [The wasync app](desktop.md).

## AXI for agents (`wasync-axi`)

wasync is the app and engine for people. **`wasync-axi`** ("AXI for agents") is a
separate, small command that AI agents such as Claude Code, Codex and OpenCode use
to work with your local wasync archive. The wasync app (the macOS disk image
and Windows Setup) always carries it, and the macOS package and the Linux archive's
`install.sh` always install it next to wasync. It stays inactive until you connect an
agent from wasync's **Connect an agent** step. Both programs always come from the same
release and have the same version.

`wasync-axi` was called `whatsapp-axi`. The installers keep `whatsapp-axi` as an alias
(on Homebrew, the old `whatsapp-axi` formula now installs `wasync-axi` plus the alias),
and the old environment variables, config folder, skill and hooks still work.

| Where you install from | How you get AXI for agents |
| --- | --- |
| macOS `.pkg` | Always installed. The installer lists **AXI for agents (wasync-axi)** next to wasync, both locked on. |
| Linux archive | Always installed by `./install.sh`, without asking. |
| macOS `.dmg` (the wasync app) | Always inside `wasync.app`. |
| Windows Setup | Always installed beside `wasync.exe` and the wasync app, including by a silent install. |
| Windows ZIP | The ZIP already contains `wasync-axi.exe` next to `wasync.exe`; extracting installs and configures nothing. |
| Homebrew | `brew install gehariharan/tap/wasync-axi` (see [Homebrew](#homebrew)). |
| Microsoft Store | Not offered: the Store edition is wasync only (see [Windows](windows.md#microsoft-store-edition)). |

No installer changes your agents' settings. When you want an AI agent to use your
archive, connect it from wasync's **Connect an agent** step.

## macOS package

The package installs `/usr/local/bin/wasync` and dependency notices under
`/usr/local/share/wasync`, and **AXI for agents** at `/usr/local/bin/wasync-axi`. It asks
for administrator approval. After installation,
open Terminal and run:

```sh
wasync version
wasync setup
```

If an earlier manual or Homebrew installation is still on PATH, check
`command -v wasync`. This package installs `/usr/local/bin/wasync`; use that
explicit path to verify it, and keep only one installation method active.

Starting with `0.1.0-preview.5`, macOS executables use Developer ID Application
signatures and Apple notarization. Installers use Developer ID Installer
signatures and include validated stapled notarization tickets. Older previews
were unsigned/ad hoc. No installer removes quarantine or bypasses Gatekeeper.

The package rejects the wrong CPU architecture, unsupported macOS versions and an existing
`/usr/local/bin/wasync` symlink owned by another installation method (for example Homebrew).
When another installation method already owns `/usr/local/bin/wasync-axi` as a symlink, the
package keeps that copy and installs wasync as usual.

## Linux archive

Linux users can extract the matching archive and install without administrator
access. The archive holds `wasync`, `wasync-axi` and `install.sh`; extracting it
installs nothing:

```sh
tar -xzf wasync_VERSION_linux_amd64.tar.gz
./install.sh                # installs wasync and wasync-axi to ~/.local/bin
export PATH="$HOME/.local/bin:$PATH"
wasync version
```

`install.sh --bin-dir DIR` installs somewhere else. It asks no questions and changes no
agent settings unless you pass `--axi-skill` or `--axi-hooks`, which run
`wasync-axi setup skill` or `wasync-axi setup hooks` after installing. `--with-axi` is
still accepted and changes nothing; `--without-axi` is refused, because wasync-axi is
always installed and stays inactive until you connect an agent. To install by hand instead, copy `wasync` and `wasync-axi` with
`install -m 755` into a directory on your PATH.

On Linux there is no system confirmation prompt (Touch ID, Windows Hello) and no
start-at-login item, so approvals happen in a terminal and you start `wasync serve`
yourself; see [Agents](agents.md#approving-without-a-terminal).

## Checksums and upgrades

The `checksums.txt` release asset contains SHA-256 hashes of each download. On
Linux use `sha256sum --ignore-missing -c checksums.txt` in the download directory;
on macOS compare `shasum -a 256 FILE.pkg` with its line in `checksums.txt`.

Stop wasync before upgrading (`wasync stop`), and start it again afterwards with
`wasync service restart` if it runs at login. Installers only replace program files; archives,
chat choices and pairing state remain in the per-user data directory:
`~/Library/Application Support/wasync` on macOS, `%LOCALAPPDATA%\wasync` on Windows and
`~/.local/share/wasync` on Linux. Upgrading from a version that used `%APPDATA%\wasync` or
`~/.config/wasync` moves the data there on the first start, after checking the copy; see
[Data, files and security](data-and-security.md#moving-from-an-older-versions-folder).

### Updating from the web app

The **Updates** section of Settings shows the version you have. **Check for
updates** asks GitHub for the newest published release; wasync never checks by itself.
On a copy installed by Windows Setup, and on the macOS app in your Applications folder,
**Update and restart…** asks you to confirm (Touch ID, your password or Windows Hello),
downloads the installer for your computer and checks its SHA-256 against the release's
`checksums.txt` and against GitHub's own digest of the file. Then:

- Windows: when your installed `wasync.exe` is signed, the new Setup must be signed by the
  same publisher (Windows previews are not signed yet, so the checksum is the check). Setup
  runs silently, in the same folder and with the choices you made before.
- macOS: wasync copies the app out of the release's disk image
  (`wasync_VERSION_darwin_ARCH.dmg`), requires it to be notarized and signed by the same Apple
  developer team as the app you have, and puts it in place of your app.

wasync then stops; a short-lived helper installs the update once it has, and starts wasync
again the way it was running: through its login item, as the same background command, or
by opening the app. A terminal UI is left for you to start again. If the install fails,
the version you had starts again, and Settings says what happened. Updating is only
offered on the computer running wasync, never to an agent or from another device.

Homebrew, the Linux archive, the Microsoft Store, the Windows ZIP and the macOS
command-line package update the way you installed them; the page says how.

## Removing wasync

In the wasync app or the web app, **Settings > Remove wasync** removes wasync without a
terminal. In order, it:

1. disconnects every agent: it takes wasync's entry out of each app's settings file (putting
   back any entry wasync replaced) and revokes every agent credential;
2. unlinks this computer from WhatsApp. If it cannot do 1 or 2 (for example, the phone is
   offline), the page says what is left and how to finish it, and waits for **Finish removing**;
3. stops wasync and quits the wasync app, then removes both start-at-login items: the app's own
   and the older `wasync service install` one;
4. only if you ticked **Also delete my chats and data** (it starts unticked) and confirmed with
   Touch ID, your password or Windows Hello: deletes the data directory, the approval
   credential in the macOS Keychain and the agent credentials wasync saved for `wasync-axi`;
5. removes the program the way it was installed:
   - Windows Setup: opens Setup's own uninstaller (the one Settings > Apps runs), which asks
     whether you are sure and removes the program files and the PATH entry;
   - the macOS app: moves `wasync.app` to the Bin. If the macOS package also installed
     `/usr/local/bin/wasync-axi`, macOS asks for an administrator's password to remove it; a
     `wasync-axi` that Homebrew owns is left alone.

Your data is kept unless you ticked the box, so installing wasync again later finds your chats
and settings. A short-lived helper does steps 3 to 5 once wasync has stopped; if wasync or the app
does not stop within two minutes, it gives up and changes nothing. It logs what it did to
`remove.log` in the data directory, or, when the data was deleted, in a `wasync-remove-` folder in
your temporary folder.

Other installs leave the program files for you to remove, and the page says how:

- Homebrew on macOS: `brew uninstall --cask gehariharan/tap/wasync`, and
  `brew uninstall gehariharan/tap/wasync-axi` if you added it.
- Homebrew on Linux: `brew uninstall gehariharan/tap/wasync-axi gehariharan/tap/wasync`.
- Linux archive: delete `wasync` and `wasync-axi` from the folder `install.sh` put them in
  (`~/.local/bin` unless you chose another).
- macOS package: `sudo rm -f /usr/local/bin/wasync /usr/local/bin/wasync-axi
  /usr/local/bin/whatsapp-axi && sudo rm -rf /usr/local/share/wasync`.
- Windows ZIP: delete the folder you unpacked it into.
- Microsoft Store: uninstall it from the Start menu or Settings > Apps.

## Homebrew

The source repository is also the Homebrew tap. Existing users should run the
custom-remote command below once to migrate from the former standalone tap.

Linux Homebrew installs both tools from source:

```sh
brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git
# On Homebrew versions with tap trust, authorize the core dependency:
if brew help trust >/dev/null 2>&1; then brew trust --formula gehariharan/tap/wasync; fi
brew install gehariharan/tap/wasync-axi
```

On macOS, install the signed core cask below. AXI for agents is your choice there:
add it with `brew install gehariharan/tap/wasync-axi` (the cask prints this
reminder after installing). The standalone `wasync-axi` formula stays separate on
purpose, so the agent tool can be installed, upgraded or removed on its own. The
combined release's core source formula is Linux-only. The source formulas use Homebrew's Go and allow
Go to download the project-selected toolchain when needed.

Each release generates `wasync.rb`, a Homebrew **cask** that installs the
prebuilt native macOS executable and verifies its archive checksum. It performs
no source build. The public tap downloads signed release assets without GitHub
authentication:

```sh
brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git
# On Homebrew versions with tap trust:
brew trust --cask gehariharan/tap/wasync
brew install --cask gehariharan/tap/wasync
```

After migrating the tap, upgrade an existing Mac installation with:

```sh
brew update
brew upgrade --cask gehariharan/tap/wasync
wasync version
```

If AXI is installed, upgrade it with `brew upgrade gehariharan/tap/wasync-axi`.
Homebrew upgrades preserve the local archive, capture preferences and pairing.

Releases from `0.1.0-preview.5` onward are Developer ID signed and notarized.
Homebrew's quarantine behavior is preserved; no cask hook disables Gatekeeper.
Existing manual launchers may
precede the Homebrew binary; check `command -v wasync` and use
`/usr/local/bin/wasync` on Intel or `/opt/homebrew/bin/wasync` on Apple Silicon to
select the installed release explicitly.
