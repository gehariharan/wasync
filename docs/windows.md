# wasync on Windows

wasync runs on Windows 10/11 x64. There are three ways to get it: the per-user Setup program,
the portable ZIP, and the Microsoft Store edition. The general install guide is in
[Installing wasync](install.md).

## Setup program and portable ZIP (preview)

The release workflow builds Windows 10/11 x64 using a native Windows runner,
CGO SQLite with FTS5, and a statically linked MinGW runtime. Downloads:

- `wasync_VERSION_windows_amd64-setup.exe`: per-user Inno Setup installer.
- `wasync_VERSION_windows_amd64.zip`: portable `wasync.exe`, `wasync-desktop.exe` (the wasync app), `wasync-axi.exe` and license notices.

Setup installs the [wasync app](desktop.md) beside `wasync.exe`. Its licence page starts with
the note that wasync is not WhatsApp, and its finish page has a ticked **Open wasync now**
box. The **wasync** Start menu entry opens the app, which shows the setup pages to link
WhatsApp, and keeps running in the notification area when you close its window. The
installer offers a user PATH entry and, as a separate unticked
task, **AXI for agents** (`wasync-axi.exe`; keep the PATH entry ticked so agents
can find it). If you pick it, the last page offers two more unticked boxes that run
`wasync-axi setup skill` and `wasync-axi setup hooks` as you; a silent install
never runs them. Silent examples:
`wasync_VERSION_windows_amd64-setup.exe /VERYSILENT /MERGETASKS="installaxi"` adds
AXI for agents, and omitting `installaxi` leaves it out. No administrator access is
needed.

If you set up your agents, run `wasync-axi setup hooks --remove` and
`wasync-axi setup skill --remove` before uninstalling. Quit the wasync app (or stop wasync
with `wasync stop`) before upgrading by hand or uninstalling; **Update and restart…** on the Set up page does both steps
for you (see [Updating from the web app](install.md#updating-from-the-web-app)). Uninstall removes program files, its
PATH entry and the app's start-at-login item; it preserves `%APPDATA%\wasync`, including the archive and device
session. The installer owns only the PATH entry it adds; a preexisting equivalent
entry is preserved on uninstall. The portable ZIP uses the same data directory unless
`--data-dir` is set. Local data uses a private Windows ACL for your user and SYSTEM;
concurrent capture uses an exclusive Windows file lock. Native Windows ARM64 is not yet packaged.

**Windows previews are unsigned while Microsoft Artifact Signing is being set up.**
Signing for macOS remains unchanged. Windows release signing is a separate step
and is not yet configured; signing credentials are never bundled in downloads.

### Install script

`scripts/install-windows.ps1` finds the newest release, checks the download against the
release's published SHA-256 checksum, and runs Setup quietly for your account with the PATH
entry and `wasync-axi` (`-DryRun` shows what it would do). It changes no agent settings:

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/gehariharan/wasync/main/scripts/install-windows.ps1 -OutFile $env:TEMP\install-wasync.ps1
powershell -ExecutionPolicy Bypass -File $env:TEMP\install-wasync.ps1
```

### What has and has not been tested on Windows

Continuous integration installs the Setup program directly and runs the
`wasync-axi mcp` tool server and `wasync serve` on a Windows runner. It runs the install
script only as a dry run: the script's real download, checksum comparison and quiet install
have not been run end to end by the author. The Codex desktop app, Windows Hello, the
password prompt, the start-at-login item, the desktop notification and the setup page in a
real browser have not yet been exercised on a real Windows PC by the author either. They
build and are covered by the Windows checks, but treat them as untested until you have tried
them.

### Building the Windows packages (maintainers)

Maintainers can build using Go, Python 3, MinGW GCC and Inno Setup 6 (the wasync app,
`wasync-desktop.exe`, needs no C compiler):

```powershell
$env:CC = 'C:/msys64/ucrt64/bin/gcc.exe'
$env:PATH = 'C:/msys64/ucrt64/bin;' + $env:PATH
./scripts/package-windows.ps1 -Version 0.1.0-preview.1
```

See the [release guide](releasing.md) for the rest of the release process.

## Microsoft Store edition

The Store edition is an x64 desktop console application with a `wasync.exe` app
execution alias and the same commands as the other builds. **It contains wasync only, not
AXI for agents.** A Store package cannot ask the user a question at install time, so
bundling `wasync-axi` would install it for everyone, which is not a choice. A
separate Store add-on that people could pick would be a new Partner Center product
and a new certification, which only the Store account owner can do. Store users who
want it can take `wasync-axi.exe` from the Windows ZIP on the
[Releases page](https://github.com/gehariharan/wasync/releases); with the Store
execution alias on PATH, it finds `wasync` the same way it does for other installs.
That Store-plus-AXI combination has not been tested on a real Store installation. Windows 10 build 19041 or newer is
required. Store publication is a separate certification process; building a
package does not create a Store listing or guarantee approval.

For Windows Server, this manifest targets Server 2022 or newer with Desktop
Experience; native installation still needs validation on your server image.
Server 2019 is below this package's minimum OS version and does not support the
execution alias extension. For Server Core or unattended deployments, use the
portable ZIP distribution. Server sideloading requires a trusted signed MSIX;
the unsigned Store submission output cannot be installed directly. See
[Microsoft's Server guidance](https://learn.microsoft.com/en-us/windows/msix/msix-server-2019).

The privacy policy the Store listing points to is in the
[README](../README.md#store-privacy-policy).

### Store archive location and migration

The Store build defaults to `%USERPROFILE%\.wasync`, a private directory outside
MSIX-managed app data. Upgrades and uninstall retain the archive, linked-device
session, chat choices and downloads. This avoids requesting the additional restricted
capability for disabling MSIX filesystem virtualization. `runFullTrust` remains
necessary for this desktop application and requires Store review.

The EXE/ZIP build keeps `%APPDATA%\wasync`. These are separate installations;
there is no automatic session copy or background login. Before switching, stop
both versions and back up the entire old data directory, including SQLite
sidecars, session database and media. Copy it to `%USERPROFILE%\.wasync` while
both apps are stopped, then start the Store edition and verify `wasync status`.
Copying a live WAL database or running two device sessions can lose data or
cause conflicts. `WASYNC_DATA_DIR`/`--data-dir` can override the location; use a
private external directory if data must survive MSIX uninstall. Remove an old
EXE installation or its PATH entry when switching so the intended command wins.

### Store signing

The Microsoft Store signs MSIX submissions after certification. No Azure signing
subscription, commercial certificate, PFX or hardware token is required for this
route. The GitHub EXE/ZIP downloads keep their existing signing status.
Unsigned Store build artifacts are submission inputs, not public sideload installers.

### Reserve the product and configure its identity (maintainers)

1. Register and verify a developer account at <https://storedeveloper.microsoft.com>.
2. Reserve the MSIX product name **wasync**. Open **Product identity** and copy the
   exact case-sensitive `Package/Identity/Name`, `Package/Identity/Publisher`, and
   `PublisherDisplayName`. These values are public package metadata, not passwords.
3. Set repository variables `WASYNC_STORE_IDENTITY_NAME`, `WASYNC_STORE_PUBLISHER`,
   and `WASYNC_STORE_PUBLISHER_DISPLAY_NAME` to those values.
4. Run **Build Microsoft Store submission** in GitHub Actions. Supply an application
   version such as `0.1.0-store.1` and a separate increasing Store version such as
   `1.0.1.0`. Store versions use four integers, the first is nonzero, all are at most
   65535, and the last is zero. A later Store update must have a higher version.
5. Download the `store-submission-VERSION` artifact. It contains an unsigned `.msix`,
   `.msixupload` upload container, checksums and `submission-info.json`. Verify the
   identity receipt against Partner Center before uploading the package.
6. Use the draft copy and certification notes in
   [packaging/store/listing.json](../packaging/store/listing.json). Provide real,
   privacy-safe screenshots of the running application, complete age ratings and
   the `runFullTrust` justification, run Windows App Certification Kit, then submit
   for Microsoft review. No Partner Center credentials are stored in this repo.

The workflow refuses missing identity values and the CI-only test identity. It
produces an artifact for manual review/upload; it does not automatically publish
or claim certification. Native CI separately packages an isolated test identity,
uses a short-lived test certificate on its disposable runner, and exercises app
alias activation, local search, package upgrade and retained data after uninstall.
Its test packages are labelled **not for Store** and must never be submitted.

For a native Windows maintainer build with the Windows SDK installed:

```powershell
./scripts/package-msix.ps1 -Version 0.1.0-store.1 -PackageVersion 1.0.1.0 `
  -IdentityName 'COPY_FROM_PARTNER_CENTER' `
  -Publisher 'CN=COPY_FROM_PARTNER_CENTER' `
  -PublisherDisplayName 'COPY_FROM_PARTNER_CENTER'
```
