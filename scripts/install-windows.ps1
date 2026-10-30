# Installs wasync and whatsapp-axi for the current Windows user, without a browser or administrator rights.
# It is meant to be run by a person, or by an agent such as Codex on their behalf:
#   powershell -ExecutionPolicy Bypass -File scripts/install-windows.ps1
# What it does: finds the newest published release (previews included), downloads the Setup
# program, checks its SHA-256 against the release's checksums.txt, then runs Setup silently
# for your account with the PATH entry and whatsapp-axi. It changes no agent settings.
#   -DryRun                    say what would be installed; download and install nothing
#   -ReleasesJson FILE         tests only, with -DryRun: read the release list from a saved JSON array
param(
 [switch]$DryRun,
 [string]$ReleasesJson = ''
)
$ErrorActionPreference = 'Stop'
# Windows PowerShell 5.1 downloads many times slower while it draws a progress bar.
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version Latest
# Windows PowerShell 5.1 does not offer TLS 1.2 to GitHub unless asked.
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }
if ($ReleasesJson -and -not $DryRun) { throw '-ReleasesJson is for tests and needs -DryRun.' }
if ($env:OS -ne 'Windows_NT') { throw 'This script installs the Windows build of wasync.' }
if (-not [Environment]::Is64BitOperatingSystem) { throw 'wasync for Windows needs a 64-bit (x64) version of Windows.' }

$repo = 'gehariharan/wasync'
# Setup reuses the folder of an earlier install, so ask its uninstall record where that is.
function Get-WasyncFolder {
 $entry = Get-ItemProperty -LiteralPath 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\{A3D912C0-B5BF-46F8-85F4-78E1FEF01FCA}_is1' -Name InstallLocation -ErrorAction SilentlyContinue
 if ($entry -and $entry.InstallLocation) { return $entry.InstallLocation.TrimEnd('\') }
 Join-Path $env:LOCALAPPDATA 'Programs\wasync'
}
$headers = @{ 'User-Agent' = 'wasync-install-script'; 'Accept' = 'application/vnd.github+json' }
# /releases/latest skips pre-releases, and every current release is one, so list instead.
# Windows PowerShell 5.1 passes a JSON array down a pipeline as one item, so walk it with foreach.
if ($ReleasesJson) { $list = Get-Content -Raw -Path $ReleasesJson | ConvertFrom-Json }
else { $list = Invoke-RestMethod -Headers $headers -Uri "https://api.github.com/repos/$repo/releases?per_page=10" }
$release = $null
foreach ($candidate in $list) { if (-not $candidate.draft) { $release = $candidate; break } }
if (-not $release) { throw 'No published wasync release was found.' }
$assets = @($release.assets)
$setup = $assets | Where-Object { $_.name -like 'wasync_*_windows_amd64-setup.exe' } | Select-Object -First 1
$sums = $assets | Where-Object { $_.name -eq 'checksums.txt' } | Select-Object -First 1
if (-not $setup) { throw "Release $($release.tag_name) has no Windows Setup program." }
if (-not $sums) { throw "Release $($release.tag_name) has no checksums.txt, so the download cannot be verified." }

$tasks = 'addtopath,installaxi'
Write-Host "Release:  $($release.tag_name)"
Write-Host "Setup:    $($setup.name)"
Write-Host "Install:  for your account only, tasks: $tasks (no administrator rights)"
Write-Host "Folder:   $(Get-WasyncFolder)"
if ($DryRun) { Write-Host 'Dry run: nothing was downloaded or installed.'; return }

$work = Join-Path ([IO.Path]::GetTempPath()) ("wasync-install-" + [guid]::NewGuid())
New-Item -ItemType Directory $work | Out-Null
try {
 $file = Join-Path $work $setup.name
 Invoke-WebRequest -UseBasicParsing -Headers @{ 'User-Agent' = 'wasync-install-script' } -Uri $setup.browser_download_url -OutFile $file
 $text = (Invoke-WebRequest -UseBasicParsing -Headers @{ 'User-Agent' = 'wasync-install-script' } -Uri $sums.browser_download_url).Content
 if ($text -is [byte[]]) { $text = [Text.Encoding]::UTF8.GetString($text) }
 $line = $text -split "`n" | Where-Object { $_.Trim().EndsWith($setup.name) } | Select-Object -First 1
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
$dir = Get-WasyncFolder
Write-Host "Installed to: $dir"
Write-Host "Start the setup page with its full path: $(Join-Path $dir 'wasync.exe') setup"
Write-Host 'The Codex app and terminals see the new PATH only after they are fully quit and reopened.'
