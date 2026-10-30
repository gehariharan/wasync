# Installs wasync and whatsapp-axi for the current Windows user, without a browser or administrator rights.
# It is meant to be run by a person, or by an agent such as Codex on their behalf:
#   powershell -ExecutionPolicy Bypass -File scripts/install-windows.ps1
# What it does: finds the newest published release (previews included), downloads the Setup
# program, checks its SHA-256 against the release's checksums.txt, then runs Setup silently
# for your account with the PATH entry and whatsapp-axi. It changes no agent settings.
#   -Version 0.1.0-preview.N   install that release instead of the newest
#   -NoAxi                     install wasync only (the Codex plugin needs whatsapp-axi)
#   -DryRun                    say what would be installed; download and install nothing
param(
 [string]$Version = '',
 [switch]$NoAxi,
 [switch]$DryRun
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
# Windows PowerShell 5.1 does not offer TLS 1.2 to GitHub unless asked.
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }
if ($env:OS -ne 'Windows_NT') { throw 'This script installs the Windows build of wasync.' }
if (-not [Environment]::Is64BitOperatingSystem) { throw 'wasync for Windows needs a 64-bit (x64) version of Windows.' }

$repo = 'gehariharan/wasync'
$headers = @{ 'User-Agent' = 'wasync-install-script'; 'Accept' = 'application/vnd.github+json' }
if ($Version) {
 $release = Invoke-RestMethod -Headers $headers -Uri "https://api.github.com/repos/$repo/releases/tags/v$Version"
} else {
 # /releases/latest skips pre-releases, and every current release is one, so list instead.
 $release = @(Invoke-RestMethod -Headers $headers -Uri "https://api.github.com/repos/$repo/releases?per_page=10" | Where-Object { -not $_.draft })[0]
}
if (-not $release) { throw 'No published wasync release was found.' }
$assets = @($release.assets)
$setup = @($assets | Where-Object { $_.name -like 'wasync_*_windows_amd64-setup.exe' })[0]
$sums = @($assets | Where-Object { $_.name -eq 'checksums.txt' })[0]
if (-not $setup) { throw "Release $($release.tag_name) has no Windows Setup program." }
if (-not $sums) { throw "Release $($release.tag_name) has no checksums.txt, so the download cannot be verified." }

$tasks = if ($NoAxi) { 'addtopath' } else { 'addtopath,installaxi' }
Write-Host "Release:  $($release.tag_name)"
Write-Host "Setup:    $($setup.name)"
Write-Host "Install:  for your account only, tasks: $tasks (no administrator rights)"
if ($DryRun) { Write-Host 'Dry run: nothing was downloaded or installed.'; return }

$work = Join-Path ([IO.Path]::GetTempPath()) ("wasync-install-" + [guid]::NewGuid())
New-Item -ItemType Directory $work | Out-Null
try {
 $file = Join-Path $work $setup.name
 Invoke-WebRequest -UseBasicParsing -Headers @{ 'User-Agent' = 'wasync-install-script' } -Uri $setup.browser_download_url -OutFile $file
 $text = (Invoke-WebRequest -UseBasicParsing -Headers @{ 'User-Agent' = 'wasync-install-script' } -Uri $sums.browser_download_url).Content
 if ($text -is [byte[]]) { $text = [Text.Encoding]::UTF8.GetString($text) }
 $line = @($text -split "`n" | Where-Object { $_.Trim().EndsWith($setup.name) })[0]
 if (-not $line) { throw "checksums.txt does not list $($setup.name); refusing to install an unverified file." }
 $expected = ($line.Trim() -split '\s+')[0].ToLowerInvariant()
 $actual = (Get-FileHash -Algorithm SHA256 -Path $file).Hash.ToLowerInvariant()
 if ($actual -ne $expected) { throw "The download does not match its published checksum (expected $expected, got $actual). Nothing was installed." }
 Write-Host 'Checksum verified.'
 # Setup shows nothing and never restarts the computer. Quit wasync before upgrading.
 $process = Start-Process -FilePath $file -Wait -PassThru -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/MERGETASKS=`"$tasks`""
 if ($process.ExitCode -ne 0) { throw "Setup failed with exit code $($process.ExitCode)." }
} finally {
 Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
$dir = Join-Path $env:LOCALAPPDATA 'Programs\wasync'
Write-Host "Installed to $dir."
Write-Host 'Fully quit and reopen the Codex app (or any terminal) so it sees the new PATH, then continue with the setup.'
