# Releasing wasync

Both `wasync` and `whatsapp-axi` ship from this repository. The root module owns
the TUI, WhatsApp connection and archive; the nested `axi` module owns the agent
CLI. Repository and release ownership is defined in
[ADR-002](architecture/ADR-002-monorepo.md).

## Validation

PR CI runs core tests on Linux x64/ARM64, macOS Intel/Apple Silicon and Windows,
Windows installer/MSIX tests, AXI tests and reproducible packaging checks, the
real synthetic API/offline pair smoke test, the offline terminal UI smoke check
(`scripts/smoke-installed.py`, the same one that verifies signed releases), and Homebrew publisher tests.
PRs touching Homebrew definitions, generators, module requirements or the
installation workflow also run native Linux, Intel Mac and Apple Silicon
installation checks against the PR checkout's tap definitions before merge.
These install the published versions referenced by those definitions. CI
explicitly trusts only the owned formulas and cask it installs, including the
Linux core dependency. See [Homebrew installation](../README.md#homebrew-installation)
for dependency trust and source-build toolchain behavior.

Locally use `bash scripts/check.sh` with Go 1.27.2 on PATH. CI and packaging
never use a paired account. Review both modules and run no-mistakes before merge.

## Preview publication

Main updates start `auto-release.yml`, except ordinary documentation and generated
Homebrew-only commits. Changes to the embedded `axi/skills/whatsapp-axi/SKILL.md`
are executable changes and still trigger publication. The workflow chooses one
increasing preview version, `0.1.0-preview.YYYYMMDDHHMMSS` (UTC), and invokes the
common `release.yml`. That workflow creates `vVERSION` and `axi/vVERSION`
at the same commit, tests/builds both binaries, executes native AXI smoke tests,
generates both source formulas from the root source archive, and creates a draft.

`auto-release.yml` then signs/notarizes both Mac binaries and the existing core
installers on the `wasync-signing` Mac runner. Certificates and notary credentials
stay in that runner's Keychain. The certificate organization and core installer
identifier are unchanged by the repository rename. AXI's signing identifier is
`com.gehariharan.wasynctuicli.axi`. Legacy standalone AXI Mac archives were unsigned.

`publish-signed.yml` independently verifies the signed draft on both Mac CPUs,
checks the full asset checksum manifest and generated formula contents, and
publishes it. It updates `Casks/wasync.rb`, `Formula/wasync.rb` and
`Formula/whatsapp-axi.rb` on this same repository's main branch with the normal
workflow token, then runs Homebrew installation checks. A failed publishing step
can be retried; it must not change already published binaries or tags.

The root `release.yml` also supports a manually requested version or root tag,
producing a draft only. To publish such a draft, run the signing and verification
stages; do not bypass notarization by publishing it directly. Windows previews
retain their existing unsigned status. Linux core binaries target glibc 2.35+;
AXI binaries are static.

### Signing runner operation

The `wasync-sakthi-air` signing runner must be awake, online and logged in as the
signing user, with its login Keychain unlocked. Signing runs as a user LaunchAgent
in that desktop session.
The job prevents idle sleep while signing, but does not wake an offline Mac or
unlock a locked Keychain. After reboot, log in again. Jobs wait for an offline
runner; a failed build or signing check leaves a draft and keeps the tap on the
previous published version. Homebrew checks run after publication and report
failures in Actions. Do not run untrusted pull-request code on the signing runner.

The workflow uses existing local Apple credentials; no Apple private key or
password is exported to GitHub. First-time signing-tool approvals must be
completed in Terminal on the Air. The runner starts at login and reconnects
automatically. Its installation is outside this repository at
`~/wasync-actions-runner` on the Air.

Releases run one at a time; GitHub keeps only the newest pending push while a
release is running. Manual dispatch can retry publication without a new commit.

### Manual packaging

Run on macOS or Linux with Go, a C compiler, Python 3 and the platform's packaging
tools installed:

```sh
scripts/package.sh 0.1.0-preview.1
```

Outputs are written under `dist/VERSION/OS_ARCH/` (ignored by Git). Each build
contains version, commit and timestamp metadata, dependency license notices, a
compressed executable archive and checksums. macOS builds additionally contain
a native `.pkg`. The build compiles SQLite with FTS5 enabled and embeds the
result in the executable; it requires no separately installed SQLite package.
Linux executables use the build platform's libc, so the workflow uses Ubuntu
22.04 (glibc 2.35). macOS builds target macOS 15 or later.

For AXI's cross-platform archive packaging, see [its packaging helpers](../axi/docs/releasing.md).
For Windows core packaging, see the [Windows installer guide](../README.md#windows-installer-preview).
The coordinated workflow and publication safeguards are described above.

The release job uses its temporary `GITHUB_TOKEN` to publish assets and update
this repository's Homebrew definitions. It no longer needs a deploy key for a
separate tap. Public Homebrew downloads do not require authentication. The
optional private-release download strategy uses `gh`; credentials are never
embedded in casks, download URLs or cache metadata.

### Signing a local core package with a Keychain profile

For a Gatekeeper-accepted core package, use Developer ID Application for the
executable and Developer ID Installer for the package, plus Apple notarization.
An Apple Development certificate or an Xcode account alone is not sufficient.

On the signing Mac, reuse the existing signing identities and `notarytool`
Keychain profile:

```sh
WASYNC_CODESIGN_IDENTITY='Developer ID Application: YOUR NAME (TEAMID)' \
WASYNC_INSTALLER_IDENTITY='Developer ID Installer: YOUR NAME (TEAMID)' \
WASYNC_NOTARY_PROFILE='wasync-notary' \
WASYNC_SIGNING_KEYCHAIN="$HOME/Library/Keychains/login.keychain-db" \
  scripts/package.sh 0.1.0
```

The optional notarization step rejects development/ad hoc signatures, submits
both the exact signed executable (in a temporary ZIP) and installer to Apple,
requires an `Accepted` result, staples and validates the installer ticket, and
checks the installer with Gatekeeper. Checksums are calculated after stapling.
Credentials are read from the signing Mac's Keychain profile; no Apple password
is passed in a script argument or committed to Git.

The raw CLI executable cannot hold a stapled ticket. Its code hash is registered
by notarization; Homebrew downloads the same signed executable and Gatekeeper
uses Apple's notarization lookup. The `.pkg` carries its own stapled ticket.
A signed release must run Homebrew E2E with `allow_unsigned_preview=false`.

Apple references: [Developer ID certificates](https://developer.apple.com/help/account/certificates/create-developer-id-certificates/)
and [Customizing the notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow).

### Release signing on a separate Mac

The signing Mac does not need Go or GitHub credentials. Apple private keys and
the notarization profile remain in its login Keychain. Signing needs an
interactive unlock in the same SSH session; unlocking a previous SSH session
does not make credentials available to later connections.

1. Run **Build prebuilt installers** with a new version to create a draft.
   Download the resulting draft assets using `gh release download vVERSION`.
2. Copy all six macOS assets (two core archives, two core installers and two AXI
   archives) and `scripts/notarize.sh` and
   `scripts/sign-release-macos.sh` to the signing Mac. Run:

   ```sh
   WASYNC_CODESIGN_IDENTITY='Developer ID Application: YOUR NAME (TEAMID)' \
   WASYNC_INSTALLER_IDENTITY='Developer ID Installer: YOUR NAME (TEAMID)' \
   WASYNC_NOTARY_PROFILE=wasync-notary \
   WASYNC_SIGNING_KEYCHAIN="$HOME/Library/Keychains/login.keychain-db" \
     bash scripts/sign-release-macos.sh VERSION assets
   ```

   Enter the Mac login password at the Keychain prompt. Approve signing-key
   access on that Mac if macOS requests it. The script verifies CPU types and
   matching core archive/installer executables, signs and notarizes both binaries
   on both architectures, validates Gatekeeper acceptance, and replaces the six
   assets only after all checks succeed. No executable is launched during signing.
   For first-time signing-key approval, run this from Terminal directly on the
   signing Mac and choose Always Allow for the signing tools when prompted.
   SSH cannot present those approval dialogs. CLI notarization is validated with
   `codesign --verify -R='notarized' --check-notarization`; `spctl --type execute`
   expects an app bundle. Installer validation uses `spctl --type install`.
3. Retrieve the signed assets. Recompute `checksums.txt` across all core and AXI
   archives and installers, and regenerate the cask with `scripts/homebrew-cask.py`
   (`--private` is only for private forks).
   Replace these assets, `checksums.txt`, and `wasync.rb` in the **draft only**.
   Never overwrite a published release.
4. Run **Verify and publish signed release** with the version. It verifies the
   expected signing team, both architectures, notarization, installer tickets,
   checksums, and generated Homebrew definitions before publishing and updating
   the tap. Homebrew E2E then runs on Linux, Intel Mac and Apple Silicon with
   `allow_unsigned_preview=false`.

The signed publication workflow requires no Apple passwords or private keys in GitHub
Actions. Manual SSH signing requires an interactive Keychain unlock in that
session. The automatic pipeline sets `WASYNC_SIGNING_UNATTENDED=1` and runs in
the logged-in desktop session, using its already-unlocked Keychain.

## Homebrew migration

See [Homebrew installation](../README.md#homebrew-installation) for the tap's
custom remote and platform-specific installation commands.
The [consolidation decision](architecture/ADR-002-monorepo.md) owns compatibility
and historical repository retention requirements.
