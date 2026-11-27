# CI-only test of the Windows Setup program and portable ZIP. Never uses a real WhatsApp account.
#   ./scripts/test-windows-installer.ps1 -Version 0.1.0-preview.pr
param([Parameter(Mandatory=$true)][string]$Version)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') { throw 'Run this fixture only on a disposable GitHub-hosted Windows runner' }
Set-Location (Join-Path $PSScriptRoot '..')
$dist = Join-Path (Get-Location) "dist/$Version/windows_amd64"
$setup = Join-Path $dist "wasync_${Version}_windows_amd64-setup.exe"
$zip = Join-Path $dist "wasync_${Version}_windows_amd64.zip"
foreach ($file in $setup, $zip) { if (-not (Test-Path $file)) { throw "Missing package: $file" } }
$temp = Join-Path $env:RUNNER_TEMP "wasync-installer-test-$([guid]::NewGuid())"
New-Item -ItemType Directory $temp | Out-Null
# Agent settings that wasync-axi setup would create. None may appear from installing alone.
$agentFolders = @('.claude', '.codex', '.config/opencode') | ForEach-Object { Join-Path $env:USERPROFILE $_ }
foreach ($folder in $agentFolders) { if (Test-Path $folder) { throw "Refusing to run: $folder already exists on this runner" } }
function Install-Wasync($directory, $tasks) {
 # /MERGETASKS keeps Setup's defaults (what a silent user gets) and adjusts only what we name.
 # The PATH task is switched off so the runner's environment is left alone.
 $process = Start-Process -FilePath $setup -Wait -PassThru -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/DIR=$directory", "/MERGETASKS=`"$tasks`""
 if ($process.ExitCode -ne 0) { throw "Setup failed with exit code $($process.ExitCode)" }
}
function Uninstall-Wasync($directory) {
 $process = Start-Process -FilePath (Join-Path $directory 'unins000.exe') -Wait -PassThru -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART'
 if ($process.ExitCode -ne 0) { throw "Uninstall failed with exit code $($process.ExitCode)" }
 # The uninstaller hands off to a temporary copy of itself and returns early, so wait for it to finish.
 $deadline = [DateTime]::UtcNow.AddSeconds(60)
 while ((Test-Path (Join-Path $directory 'wasync.exe')) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 500 }
}
try {
 # 1. Default choices: wasync, the wasync app and AXI for agents, which is always installed.
 $plain = Join-Path $temp 'plain'
 Install-Wasync $plain '!addtopath'
 if (-not (Test-Path (Join-Path $plain 'wasync.exe'))) { throw 'Setup did not install wasync.exe' }
 if (-not (Test-Path (Join-Path $plain 'wasync-axi.exe'))) { throw 'Setup did not install wasync-axi.exe' }
 foreach ($folder in $agentFolders) { if (Test-Path $folder) { throw "Setup changed agent settings: $folder" } }
 if (((& (Join-Path $plain 'wasync.exe') version) | Out-String) -notmatch [regex]::Escape("wasync $Version ")) { throw 'Installed wasync reports the wrong version' }
 # The wasync app is installed beside wasync, and the Start menu opens it (not the terminal UI).
 $desktop = Join-Path $plain 'wasync-desktop.exe'
 if (-not (Test-Path $desktop)) { throw 'Setup did not install the wasync app (wasync-desktop.exe)' }
 $versionFile = Join-Path $temp 'desktop-version.txt'
 $process = Start-Process -FilePath $desktop -ArgumentList '--version' -Wait -PassThru -RedirectStandardOutput $versionFile
 if ($process.ExitCode -ne 0 -or (Get-Content -Raw $versionFile) -notmatch [regex]::Escape("wasync $Version ")) { throw 'The wasync app reports the wrong version' }
 $shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'wasync\wasync.lnk'
 if (-not (Test-Path $shortcut)) { throw "Setup made no Start menu shortcut at $shortcut" }
 $target = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcut).TargetPath
 if ($target -ne $desktop) { throw "The Start menu opens $target, not the wasync app" }
 # Uninstalling removes the app's start-at-login item, which would otherwise point at a removed program.
 $runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
 if (Get-ItemProperty -Path $runKey -Name 'wasync-app' -ErrorAction SilentlyContinue) { throw 'Refusing to run: this runner already has a wasync-app startup item' }
 New-ItemProperty -Path $runKey -Name 'wasync-app' -Value "`"$desktop`" --background" -PropertyType String | Out-Null
 Uninstall-Wasync $plain
 if (Get-ItemProperty -Path $runKey -Name 'wasync-app' -ErrorAction SilentlyContinue) { throw 'Uninstall left the wasync app starting at login' }
 if (Test-Path (Join-Path $plain 'wasync.exe')) { throw 'Uninstall left wasync.exe behind' }

 # 2. A second install: both tools at one version, and still no agent settings changed. It names
 # installaxi, the task earlier Setup programs had, as scripts/install-windows.ps1 still does for
 # them: Setup must accept a task it no longer has.
 $both = Join-Path $temp 'both'
 Install-Wasync $both '!addtopath,installaxi'
 if ((& (Join-Path $both 'wasync-axi.exe') --version).Trim() -ne $Version) { throw 'Installed wasync-axi is not the release version' }
 if ((& (Join-Path $both 'whatsapp-axi.exe') --version 2>$null).Trim() -ne $Version) { throw 'The old name whatsapp-axi.exe is not the release wasync-axi' }
 if (((& (Join-Path $both 'wasync.exe') version) | Out-String) -notmatch [regex]::Escape("wasync $Version ")) { throw 'Installed wasync reports the wrong version' }
 foreach ($folder in $agentFolders) { if (Test-Path $folder) { throw "Setup changed agent settings: $folder" } }

 # 2a. Setup upgrades an earlier install in place, so the agent install script must report the folder a person chose.
 $releases = Join-Path $temp 'releases.json'
 '[{"tag_name": "v0.1.0-preview.9", "draft": false, "assets": [{"name": "wasync_0.1.0-preview.9_windows_amd64-setup.exe", "browser_download_url": "https://example.invalid/9.exe"}, {"name": "checksums.txt", "browser_download_url": "https://example.invalid/9.txt"}]}]' | Set-Content -Path $releases -Encoding utf8
 $plan = & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'install-windows.ps1') -DryRun -ReleasesJson $releases | Out-String
 if ($LASTEXITCODE -ne 0) { throw "install-windows.ps1 dry run failed: $plan" }
 if ($plan -notmatch ('Folder:\s+' + [regex]::Escape($both) + '\s')) { throw "install-windows.ps1 did not report the install in ${both}: $plan" }

 # 2b. The Codex plugin launches `wasync-axi mcp`. Speak the tool protocol to the installed program
 # the way Codex does, through Windows pipes: line-delimited JSON-RPC, answers on standard output only.
 $requests = @(
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}',
  '{"jsonrpc":"2.0","method":"notifications/initialized"}',
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}',
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whatsapp_status","arguments":{}}}'
 )
 $replies = @($requests | & (Join-Path $both 'wasync-axi.exe') mcp)
 if ($LASTEXITCODE -ne 0) { throw "wasync-axi mcp exited with code $LASTEXITCODE" }
 if ($replies.Count -ne 3) { throw "wasync-axi mcp answered $($replies.Count) times, expected 3: $replies" }
 $answers = $replies | ForEach-Object { $_ | ConvertFrom-Json }
 if ($answers[0].result.serverInfo.name -ne 'wasync-axi') { throw 'wasync-axi mcp did not identify itself' }
 $names = @($answers[1].result.tools | ForEach-Object { $_.name })
 foreach ($want in 'whatsapp_status', 'whatsapp_read_messages', 'whatsapp_request_access', 'whatsapp_start_setup') { if ($names -notcontains $want) { throw "Tool list is missing $want" } }
 foreach ($name in $names) { if ($name -match 'approve|grant|revoke|credential|secret|token') { throw "Tool server offers a forbidden tool: $name" } }
 if ($answers[2].result.content[0].text -notmatch '\S') { throw 'whatsapp_status returned no text' }

 # 2c. wasync starts, answers its health check, and keeps its approval and setup pages closed to a visitor
 # with no one-time link. This is the owner process the setup page and the tool server talk to.
 $data = Join-Path $temp 'serve-data'
 $port = 18787
 $owner = Start-Process -FilePath (Join-Path $both 'wasync.exe') -PassThru -WindowStyle Hidden -ArgumentList '--data-dir', $data, '--api-addr', "127.0.0.1:$port", 'serve', '--offline'
 try {
  $up = $false
  for ($i = 0; $i -lt 60 -and -not $up; $i++) {
   try { $up = (Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:$port/health").StatusCode -eq 200 } catch { Start-Sleep -Milliseconds 500 }
  }
  if (-not $up) { throw 'wasync serve did not answer its health check' }
  foreach ($page in '/ui/', '/ui/setup') {
   $code = 0
   try { $code = (Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:$port$page").StatusCode } catch { $code = [int]$_.Exception.Response.StatusCode }
   if ($code -ne 401) { throw "$page answered $code without a sign-in link; expected 401" }
  }
 } finally {
  if (-not $owner.HasExited) { Stop-Process -Id $owner.Id -Force }
 }

 # 2d. The wasync app starts as it does at login (no window): it starts wasync for its data
 # directory, refuses a second copy, and wasync stops when the app goes away.
 $appData = Join-Path $temp 'app-data'
 $appPort = 18788
 $app = Start-Process -FilePath (Join-Path $both 'wasync-desktop.exe') -PassThru -ArgumentList '--background', '--data-dir', $appData, '--api-addr', "127.0.0.1:$appPort"
 try {
  $up = $false
  for ($i = 0; $i -lt 120 -and -not $up; $i++) {
   try { $up = (Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:$appPort/health").StatusCode -eq 200 } catch { Start-Sleep -Milliseconds 500 }
  }
  if (-not $up) { throw "The wasync app did not start wasync: $(Get-Content -Raw (Join-Path $appData 'desktop.log') -ErrorAction SilentlyContinue)" }
  foreach ($page in '/ui/chats', '/ui/setup', '/ui/desktop') {
   $code = 0
   try { $code = (Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:$appPort$page").StatusCode } catch { $code = [int]$_.Exception.Response.StatusCode }
   if ($code -ne 401) { throw "$page answered $code without a sign-in link; expected 401" }
  }
  if (((& (Join-Path $both 'wasync.exe') --data-dir $appData status) | Out-String) -notmatch 'started by the wasync app') { throw 'wasync does not say the app started it' }
  $second = Start-Process -FilePath (Join-Path $both 'wasync-desktop.exe') -PassThru -ArgumentList '--background', '--data-dir', $appData, '--api-addr', "127.0.0.1:$appPort"
  if (-not $second.WaitForExit(15000)) { Stop-Process -Id $second.Id -Force; throw 'A second copy of the wasync app kept running' }
 } finally {
  if (-not $app.HasExited) { Stop-Process -Id $app.Id -Force }
 }
 $down = $false
 for ($i = 0; $i -lt 60 -and -not $down; $i++) {
  try { Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:$appPort/health" | Out-Null; Start-Sleep -Milliseconds 500 } catch { $down = $true }
 }
 if (-not $down) { throw 'wasync kept running after the wasync app went away' }

 Uninstall-Wasync $both
 foreach ($name in 'wasync.exe', 'wasync-desktop.exe', 'wasync-axi.exe', 'whatsapp-axi.exe') { if (Test-Path (Join-Path $both $name)) { throw "Uninstall left $name behind" } }

 # 3. The portable ZIP carries both executables; extracting it installs and configures nothing.
 $extracted = Join-Path $temp 'zip'
 Expand-Archive $zip $extracted
 foreach ($name in 'wasync.exe', 'wasync-desktop.exe', 'wasync-axi.exe', 'whatsapp-axi.exe', 'LICENSE', 'THIRD_PARTY_LICENSES') { if (-not (Test-Path (Join-Path $extracted $name))) { throw "ZIP is missing $name" } }
 if ((& (Join-Path $extracted 'wasync-axi.exe') --version).Trim() -ne $Version) { throw 'ZIP wasync-axi is not the release version' }
 foreach ($folder in $agentFolders) { if (Test-Path $folder) { throw "Extracting changed agent settings: $folder" } }
 Write-Host 'Windows Setup: the Start menu opens the wasync app, which starts wasync and stops it when it goes away; AXI for agents is always installed at the release version and changes no agent settings; the installed tool server speaks MCP and offers no approving tool; wasync serve starts with its pages closed to visitors; the ZIP carries both tools.'
} finally {
 Remove-Item -Recurse -Force $temp -ErrorAction SilentlyContinue
}
