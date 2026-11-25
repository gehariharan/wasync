# Using wasync from the Codex desktop app

This repository is also a Codex plugin marketplace. The **whatsapp** plugin gives Codex
`whatsapp_*` tools served by `whatsapp-axi mcp` (the old name of `wasync-axi mcp`): read,
search, follow changes, and reply, react and edit only where you allow it, plus asking for
access to a chat and starting setup. Ask Codex to "set up WhatsApp": it opens the setup page,
you scan the QR code and choose chats, and later every chat Codex asks for is approved by you on
your own computer (unless you set wasync to sync all chats, where asking grants it). No tool
can approve, grant or reveal a credential. See
[ADR-003](architecture/ADR-003-mcp-tool-server.md) and the [threat model](architecture/threat-model.md).

The plugin needs `wasync` and `wasync-axi` installed (see [Installing wasync](install.md)), with
the old name `whatsapp-axi` on the PATH the Codex app uses.

## Let Codex set it up

If you use the **Codex desktop app** on macOS or Windows, you can ask Codex to install wasync
and set up WhatsApp for you. Paste the prompt from the
[README](../README.md#let-your-agent-set-this-up) into a Codex conversation.

Codex's default settings run commands in a sandbox with no network and no writing outside your
project, so Codex asks your approval before the commands below. In order, they are:

1. **Install wasync and wasync-axi** (no administrator password).
   - macOS: `brew tap --custom-remote gehariharan/tap https://github.com/gehariharan/wasync.git`,
     then `brew trust --cask gehariharan/tap/wasync` if your Homebrew has `brew trust`, then
     `brew install --cask gehariharan/tap/wasync` and `brew install gehariharan/tap/whatsapp-axi`
     (the formula that also provides the `whatsapp-axi` name the plugin runs).
     Homebrew's `wasync-axi` builds from source, so that step takes a few minutes.
   - Windows: `Invoke-WebRequest https://raw.githubusercontent.com/gehariharan/wasync/main/scripts/install-windows.ps1 -OutFile $env:TEMP\install-wasync.ps1`
     then `powershell -ExecutionPolicy Bypass -File $env:TEMP\install-wasync.ps1`. The script finds
     the newest release, checks the download against the release's published SHA-256 checksum,
     and runs Setup quietly for your account with the PATH entry and `wasync-axi` (`-DryRun`
     shows what it would do). It changes no agent settings.
2. **Add the plugin**, the same on both: `codex plugin marketplace add gehariharan/wasync`, then
   `codex plugin add whatsapp@wasync`.
3. **Start the setup page.** Codex runs wasync by its full path, so it does not need the new
   PATH, as a background process that keeps running:
   - macOS: `nohup "$(brew --prefix)/bin/wasync" setup >/dev/null 2>&1 &`
   - Windows: `Start-Process -WindowStyle Hidden "FOLDER\wasync.exe" setup`, where `FOLDER` is the
     folder the install script printed after `Installed to:` (usually
     `%LOCALAPPDATA%\Programs\wasync`, or the folder an earlier install used)

   This must run **outside the sandbox**: inside it wasync cannot reach WhatsApp, cannot open
   the page's port on your computer and cannot create the folder it keeps its data in. Codex
   asks your approval to run it outside the sandbox. The page opens in your browser.
4. **Scan and choose.** These are the steps only you can do:
   1. scan the QR code with WhatsApp on your phone (*Linked devices → Link a device*);
   2. choose which chats wasync may keep;
   3. press **Connect** for the agent and confirm with Touch ID, your password or Windows Hello;
   4. later, approve each chat Codex asks for, the same way.
5. **Restart Codex, last.** Fully quit and reopen the Codex app so it loads the plugin's
   WhatsApp tools and sees the new PATH.

If you decline running setup outside the sandbox, or your Codex settings do not allow it, Codex
cannot start the page in this conversation. Instead, fully quit and reopen Codex after step 2,
then ask it to "set up WhatsApp": its WhatsApp tools open the same page and you do step 4 there.
Either way, restarting Codex is the last step of the pasted prompt.

No tool Codex has can approve anything, create a credential or read one (see
[ADR-003](architecture/ADR-003-mcp-tool-server.md)). If Codex says it has no WhatsApp tools
after the restart, the plugin could not start `whatsapp-axi`: check `whatsapp-axi --version` in
a terminal and that the Codex app was started after the install.

## Adding the plugin by hand

In the Codex app, or with the command line:

```sh
codex plugin marketplace add gehariharan/wasync
codex plugin add whatsapp@wasync
```

> **Windows:** continuous integration installs the Setup program directly and runs the
> `wasync-axi mcp` tool server and `wasync serve` on a Windows runner. It runs the install
> script only as a dry run: the script's real download, checksum comparison and quiet install
> have not been run end to end by the author. The Codex desktop app, Windows Hello, the
> password prompt and the setup page in a real browser have not yet been exercised on a real
> Windows PC by the author either.
