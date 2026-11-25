# Installing wasync

The [README](../README.md#install) has the short version. This page has the details for each
platform, the optional agent command, Homebrew, checksums and upgrades. Windows Setup, the
portable ZIP and the Microsoft Store edition are covered in [Windows](windows.md). To build from
source, see [Development](development.md).

## Prebuilt packages

End users do not need Go, Xcode Command Line Tools, or a C compiler. The native
release workflow produces these downloads:

| Platform | Download | Installation |
| --- | --- | --- |
| macOS 15+, Apple Silicon | `wasync_VERSION_darwin_arm64.pkg` | Open the package and follow Installer |
| macOS 15+, Intel | `wasync_VERSION_darwin_amd64.pkg` | Open the package and follow Installer |
| Linux x86-64, glibc 2.35+ | `wasync_VERSION_linux_amd64.tar.gz` | Extract and run `./install.sh` |
| Linux ARM64, glibc 2.35+ | `wasync_VERSION_linux_arm64.tar.gz` | Extract and run `./install.sh` |
| Windows 10/11 x64 | `wasync_VERSION_windows_amd64-setup.exe` or `.zip` | Per-user Setup or portable ZIP (see [Windows](windows.md)) |

The release workflow builds native Intel/Apple Silicon macOS packages and
Linux and Windows packages. Manual builds can remain drafts; publishing also updates the
Homebrew tap and runs the [installation checks](releasing.md#validation). Downloadable
versions appear on the
[GitHub Releases page](https://github.com/gehariharan/wasync/releases).
Source code and prebuilt downloads are public. No GitHub account is needed to install.

## AXI for agents (`wasync-axi`)

wasync is the app and engine for people. **`wasync-axi`** ("AXI for agents") is a
separate, small command that AI agents such as Claude Code, Codex and OpenCode use
to work with your local wasync archive. You do not need it to use wasync, so every
installer asks for it as a choice, off unless you say yes. Both programs always come
from the same release and have the same version.

`wasync-axi` was called `whatsapp-axi`. The installers keep `whatsapp-axi` as an alias
(on Homebrew, the old `whatsapp-axi` formula now installs `wasync-axi` plus the alias),
and the old environment variables, config folder, skill and hooks still work.

| Where you install from | How you choose AXI for agents |
| --- | --- |
| macOS `.pkg` | On the installer's **Customize** step, tick **AXI for agents (wasync-axi)**. It starts unticked. |
| Linux archive | `./install.sh` asks and the default is no. Skip the question with `--with-axi` or `--without-axi`. |
| Windows Setup | Tick **Also install wasync-axi** on the tasks page (unticked by default). For a silent install add `/MERGETASKS="installaxi"`. |
| Windows ZIP | The ZIP already contains `wasync-axi.exe` next to `wasync.exe`; extracting installs and configures nothing. |
| Homebrew | `brew install gehariharan/tap/wasync-axi` (see [Homebrew](#homebrew)). |
| Microsoft Store | Not offered: the Store edition is wasync only (see [Windows](windows.md#microsoft-store-edition)). |

Installing AXI for agents never changes your agents' settings. To let your agents
learn about it, you run (or accept an offer to run) these two commands; add
`--dry-run` to either to preview what it would write:

```sh
wasync-axi setup skill   # adds the wasync-axi skill to your agents
wasync-axi setup hooks   # adds wasync-axi's session hooks to your agents
```

The Linux script and Windows Setup offer these as separate steps, each off by default
(Linux: `--axi-skill` and `--axi-hooks` say yes without asking; Windows: two unticked
boxes on the last page, never run by a silent install). The macOS installer cannot run
commands as you, so it only reminds you on its last page.

## macOS package

The package installs `/usr/local/bin/wasync` and dependency notices under
`/usr/local/share/wasync`; if you tick **AXI for agents** it also installs
`/usr/local/bin/wasync-axi`. It asks for administrator approval. After installation,
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
`/usr/local/bin/wasync` symlink owned by another installation method.

## Linux archive

Linux users can extract the matching archive and install without administrator
access. The archive holds `wasync`, `wasync-axi` and `install.sh`; extracting it
installs nothing:

```sh
tar -xzf wasync_VERSION_linux_amd64.tar.gz
./install.sh                # installs wasync to ~/.local/bin; asks about AXI for agents (default no)
./install.sh --with-axi     # or: install both without being asked
./install.sh --without-axi  # or: install wasync only without being asked
export PATH="$HOME/.local/bin:$PATH"
wasync version
```

`install.sh --bin-dir DIR` installs somewhere else. Without a terminal and without
`--with-axi`, the script installs wasync only. To install by hand instead, copy
`wasync` (and, if you want it, `wasync-axi`) with `install -m 755` into a directory on
your PATH.

On Linux there is no system confirmation prompt (Touch ID, Windows Hello) and no
start-at-login item, so approvals happen in a terminal and you start `wasync serve`
yourself; see [Agents](agents.md#approving-without-a-terminal).

## Checksums and upgrades

The `checksums.txt` release asset contains SHA-256 hashes of each download. On
Linux use `sha256sum --ignore-missing -c checksums.txt` in the download directory;
on macOS compare `shasum -a 256 FILE.pkg` with its line in `checksums.txt`.

Stop wasync before upgrading (`wasync stop`), and start it again afterwards with
`wasync service restart` if it runs at login. Installers only replace program files; archives,
chat choices and pairing state remain in the per-user data directory
(see [Data, files and security](data-and-security.md)).

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
