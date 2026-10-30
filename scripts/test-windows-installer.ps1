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
# Agent settings that whatsapp-axi setup would create. None may appear from installing alone.
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
 # 1. Default choices: wasync only. AXI for agents must be off unless asked for.
 $plain = Join-Path $temp 'plain'
 Install-Wasync $plain '!addtopath'
 if (-not (Test-Path (Join-Path $plain 'wasync.exe'))) { throw 'Setup did not install wasync.exe' }
 if (Test-Path (Join-Path $plain 'whatsapp-axi.exe')) { throw 'Setup installed whatsapp-axi without being asked' }
 if (((& (Join-Path $plain 'wasync.exe') version) | Out-String) -notmatch [regex]::Escape("wasync $Version ")) { throw 'Installed wasync reports the wrong version' }
 Uninstall-Wasync $plain
 if (Test-Path (Join-Path $plain 'wasync.exe')) { throw 'Uninstall left wasync.exe behind' }

 # 2. Opt in: both tools at one version, and still no agent settings changed.
 $both = Join-Path $temp 'both'
 Install-Wasync $both '!addtopath,installaxi'
 if ((& (Join-Path $both 'whatsapp-axi.exe') --version).Trim() -ne $Version) { throw 'Installed whatsapp-axi is not the release version' }
 if (((& (Join-Path $both 'wasync.exe') version) | Out-String) -notmatch [regex]::Escape("wasync $Version ")) { throw 'Installed wasync reports the wrong version' }
 foreach ($folder in $agentFolders) { if (Test-Path $folder) { throw "Setup changed agent settings: $folder" } }

 # 2a. Setup upgrades an earlier install in place, so the agent install script must report the folder a person chose.
 $releases = Join-Path $temp 'releases.json'
 '[{"tag_name": "v0.1.0-preview.9", "draft": false, "assets": [{"name": "wasync_0.1.0-preview.9_windows_amd64-setup.exe", "browser_download_url": "https://example.invalid/9.exe"}, {"name": "checksums.txt", "browser_download_url": "https://example.invalid/9.txt"}]}]' | Set-Content -Path $releases -Encoding utf8
 $plan = & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'install-windows.ps1') -DryRun -ReleasesJson $releases | Out-String
 if ($LASTEXITCODE -ne 0) { throw "install-windows.ps1 dry run failed: $plan" }
 if ($plan -notmatch ('Folder:\s+' + [regex]::Escape($both) + '\s')) { throw "install-windows.ps1 did not report the install in ${both}: $plan" }

 # 2b. The Codex plugin launches `whatsapp-axi mcp`. Speak the tool protocol to the installed program
 # the way Codex does, through Windows pipes: line-delimited JSON-RPC, answers on standard output only.
 $requests = @(
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}',
  '{"jsonrpc":"2.0","method":"notifications/initialized"}',
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}',
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whatsapp_status","arguments":{}}}'
 )
 $replies = @($requests | & (Join-Path $both 'whatsapp-axi.exe') mcp)
 if ($LASTEXITCODE -ne 0) { throw "whatsapp-axi mcp exited with code $LASTEXITCODE" }
 if ($replies.Count -ne 3) { throw "whatsapp-axi mcp answered $($replies.Count) times, expected 3: $replies" }
 $answers = $replies | ForEach-Object { $_ | ConvertFrom-Json }
 if ($answers[0].result.serverInfo.name -ne 'whatsapp-axi') { throw 'whatsapp-axi mcp did not identify itself' }
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
 Uninstall-Wasync $both
 foreach ($name in 'wasync.exe', 'whatsapp-axi.exe') { if (Test-Path (Join-Path $both $name)) { throw "Uninstall left $name behind" } }

 # 3. The portable ZIP carries both executables; extracting it installs and configures nothing.
 $extracted = Join-Path $temp 'zip'
 Expand-Archive $zip $extracted
 foreach ($name in 'wasync.exe', 'whatsapp-axi.exe', 'LICENSE', 'THIRD_PARTY_LICENSES') { if (-not (Test-Path (Join-Path $extracted $name))) { throw "ZIP is missing $name" } }
 if ((& (Join-Path $extracted 'whatsapp-axi.exe') --version).Trim() -ne $Version) { throw 'ZIP whatsapp-axi is not the release version' }
 foreach ($folder in $agentFolders) { if (Test-Path $folder) { throw "Extracting changed agent settings: $folder" } }
 Write-Host 'Windows Setup: AXI for agents is off by default, installs on request at the release version, and changes no agent settings; the installed tool server speaks MCP and offers no approving tool; wasync serve starts with its pages closed to visitors; the ZIP carries both tools.'
} finally {
 Remove-Item -Recurse -Force $temp -ErrorAction SilentlyContinue
}
