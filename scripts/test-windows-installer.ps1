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
 Uninstall-Wasync $both
 foreach ($name in 'wasync.exe', 'whatsapp-axi.exe') { if (Test-Path (Join-Path $both $name)) { throw "Uninstall left $name behind" } }

 # 3. The portable ZIP carries both executables; extracting it installs and configures nothing.
 $extracted = Join-Path $temp 'zip'
 Expand-Archive $zip $extracted
 foreach ($name in 'wasync.exe', 'whatsapp-axi.exe', 'LICENSE', 'THIRD_PARTY_LICENSES') { if (-not (Test-Path (Join-Path $extracted $name))) { throw "ZIP is missing $name" } }
 if ((& (Join-Path $extracted 'whatsapp-axi.exe') --version).Trim() -ne $Version) { throw 'ZIP whatsapp-axi is not the release version' }
 foreach ($folder in $agentFolders) { if (Test-Path $folder) { throw "Extracting changed agent settings: $folder" } }
 Write-Host 'Windows Setup: AXI for agents is off by default, installs on request at the release version, and changes no agent settings; the ZIP carries both tools.'
} finally {
 Remove-Item -Recurse -Force $temp -ErrorAction SilentlyContinue
}
