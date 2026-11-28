param(
 [Parameter(Mandatory=$true)][string]$Version,
 [string]$StageDir = '',
 [switch]$BuildOnly,
 [switch]$PackageOnly
)
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^\d+\.\d+\.\d+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$') { throw 'Invalid package version' }
if ($BuildOnly -and $PackageOnly) { throw 'Specify at most one of -BuildOnly or -PackageOnly' }
if (($BuildOnly -or $PackageOnly) -and [string]::IsNullOrWhiteSpace($StageDir)) { throw '-StageDir is required with -BuildOnly or -PackageOnly' }
Set-Location (Join-Path $PSScriptRoot '..')
if ((go env GOHOSTOS) -ne 'windows' -or (go env GOHOSTARCH) -ne 'amd64') { throw 'Use a native Windows x64 builder' }
function Check-Exit { if ($LASTEXITCODE -ne 0) { throw "Command failed with exit code $LASTEXITCODE" } }
$out = Join-Path (Get-Location) "dist/$Version/windows_amd64"
$temporaryRoot = [Environment]::GetEnvironmentVariable('RUNNER_TEMP')
if (-not $temporaryRoot) { $temporaryRoot = [IO.Path]::GetTempPath() }
$doBuild = -not $PackageOnly
$doPackage = -not $BuildOnly
if ($StageDir) {
 $stage = $StageDir
 $ownsStage = $false
 if ($doBuild) {
  if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
  New-Item -ItemType Directory -Force $stage | Out-Null
 } elseif (-not (Test-Path $stage)) {
  throw "Stage directory does not exist: $stage"
 }
} else {
 $stage = Join-Path $temporaryRoot "wasync-package-$([guid]::NewGuid())"
 $ownsStage = $true
 New-Item -ItemType Directory -Force $stage | Out-Null
}
$licensePage = $null
try {
 if ($doBuild) {
  $commit = (git rev-parse --short=12 HEAD).Trim(); Check-Exit
  $built = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
  # wasync-axi (AXI for agents) ships in the ZIP and Setup always installs it.
  # It is pure Go, so build it without the C toolchain, at the same version.
  $env:CGO_ENABLED = '0'
  $axiBuildinfo = 'github.com/gehariharan/wasync/axi/internal/buildinfo'
  Push-Location axi
  try {
   go build -trimpath -ldflags "-s -w -X $axiBuildinfo.Version=$Version -X $axiBuildinfo.Commit=$commit -X $axiBuildinfo.Date=$built" -o "$stage/wasync-axi.exe" ./cmd/wasync-axi
   Check-Exit
  } finally { Pop-Location }
  if ((& "$stage/wasync-axi.exe" --version).Trim() -ne $Version) { throw "wasync-axi does not report version $Version" }
  # The wasync app: a windowed program (no console) that starts wasync.exe beside it.
  # It needs no C compiler; its icon and manifest come from cmd/wasync-desktop/rsrc_windows_amd64.syso.
  go build -trimpath -ldflags "-s -w -H windowsgui -X github.com/gehariharan/wasync/internal/buildinfo.Version=$Version -X github.com/gehariharan/wasync/internal/buildinfo.Commit=$commit -X github.com/gehariharan/wasync/internal/buildinfo.BuiltAt=$built" -o "$stage/wasync-desktop.exe" ./cmd/wasync-desktop
  Check-Exit
  $env:CGO_ENABLED = '1'
  # Bundle the MinGW runtime so the download needs no compiler/runtime install.
  $env:CGO_LDFLAGS = '-static'
  go build -trimpath -tags sqlite_fts5 -ldflags "-s -w -X github.com/gehariharan/wasync/internal/buildinfo.Version=$Version -X github.com/gehariharan/wasync/internal/buildinfo.Commit=$commit -X github.com/gehariharan/wasync/internal/buildinfo.BuiltAt=$built" -o "$stage/wasync.exe" ./cmd/wasync
  Check-Exit
  & "$stage/wasync.exe" version; Check-Exit
  Copy-Item README.md, LICENSE $stage
  python scripts/collect-licenses.py "$stage/THIRD_PARTY_LICENSES"; Check-Exit
 }
 if ($doPackage) {
  foreach ($required in 'wasync.exe', 'wasync-desktop.exe', 'wasync-axi.exe', 'README.md', 'LICENSE', 'THIRD_PARTY_LICENSES') {
   if (-not (Test-Path (Join-Path $stage $required))) { throw "Missing staged file: $required" }
  }
  # The old command name keeps working as an identical copy (preserving any signature on wasync-axi.exe).
  Copy-Item -Force "$stage/wasync-axi.exe" "$stage/whatsapp-axi.exe"
  New-Item -ItemType Directory -Force $out | Out-Null
  $name = "wasync_${Version}_windows_amd64"
  Compress-Archive -Path "$stage/*" -DestinationPath "$out/$name.zip" -Force
  $iscc = Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6/ISCC.exe'
  if (-not (Test-Path $iscc)) { throw 'Install Inno Setup 6 before packaging' }
  # Setup's licence page: the note that wasync is not WhatsApp, then the MIT licence.
  $licensePage = Join-Path $temporaryRoot "wasync-license-page-$([guid]::NewGuid()).txt"
  (Get-Content -Raw packaging/windows/notice.txt) + (Get-Content -Raw LICENSE) | Set-Content -Encoding ascii -NoNewline $licensePage
  $packaging = Join-Path (Get-Location) 'packaging/windows'
  & $iscc "/DAppVersion=$Version" "/DSourceDir=$stage" "/DOutputDir=$out" "/DOutputName=$name-setup" "/DLicensePage=$licensePage" "/DPackagingDir=$packaging" packaging/windows/wasync.iss
  Check-Exit
  foreach ($artifact in "$out/$name.zip", "$out/$name-setup.exe") {
   $hash = (Get-FileHash $artifact -Algorithm SHA256).Hash.ToLower()
   "$hash  $(Split-Path -Leaf $artifact)" | Set-Content -Encoding ascii "$artifact.sha256"
  }
 }
} finally {
 if ($ownsStage) { Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue }
 if ($licensePage) { Remove-Item -Force $licensePage -ErrorAction SilentlyContinue }
}
