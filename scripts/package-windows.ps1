param([Parameter(Mandatory=$true)][string]$Version)
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^\d+\.\d+\.\d+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$') { throw 'Invalid package version' }
Set-Location (Join-Path $PSScriptRoot '..')
if ((go env GOHOSTOS) -ne 'windows' -or (go env GOHOSTARCH) -ne 'amd64') { throw 'Use a native Windows x64 builder' }
function Check-Exit { if ($LASTEXITCODE -ne 0) { throw "Command failed with exit code $LASTEXITCODE" } }
$out = Join-Path (Get-Location) "dist/$Version/windows_amd64"
$temporaryRoot = $env:RUNNER_TEMP
if (-not $temporaryRoot) { $temporaryRoot = [IO.Path]::GetTempPath() }
$stage = Join-Path $temporaryRoot "wasync-package-$([guid]::NewGuid())"
New-Item -ItemType Directory -Force $out, $stage | Out-Null
try {
 $commit = (git rev-parse --short=12 HEAD).Trim(); Check-Exit
 $built = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
 # whatsapp-axi (AXI for agents) ships in the ZIP and is an opt-in task in Setup.
 # It is pure Go, so build it without the C toolchain, at the same version.
 $env:CGO_ENABLED = '0'
 $axiBuildinfo = 'github.com/gehariharan/wasync/axi/internal/buildinfo'
 Push-Location axi
 try {
  go build -trimpath -ldflags "-s -w -X $axiBuildinfo.Version=$Version -X $axiBuildinfo.Commit=$commit -X $axiBuildinfo.Date=$built" -o "$stage/whatsapp-axi.exe" ./cmd/whatsapp-axi
  Check-Exit
 } finally { Pop-Location }
 if ((& "$stage/whatsapp-axi.exe" --version).Trim() -ne $Version) { throw "whatsapp-axi does not report version $Version" }
 $env:CGO_ENABLED = '1'
 # Bundle the MinGW runtime so the download needs no compiler/runtime install.
 $env:CGO_LDFLAGS = '-static'
 go build -trimpath -tags sqlite_fts5 -ldflags "-s -w -X github.com/gehariharan/wasync/internal/buildinfo.Version=$Version -X github.com/gehariharan/wasync/internal/buildinfo.Commit=$commit -X github.com/gehariharan/wasync/internal/buildinfo.BuiltAt=$built" -o "$stage/wasync.exe" ./cmd/wasync
 Check-Exit
 & "$stage/wasync.exe" version; Check-Exit
 Copy-Item README.md, LICENSE $stage
 python scripts/collect-licenses.py "$stage/THIRD_PARTY_LICENSES"; Check-Exit
 $name = "wasync_${Version}_windows_amd64"
 Compress-Archive -Path "$stage/*" -DestinationPath "$out/$name.zip" -Force
 $iscc = Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6/ISCC.exe'
 if (-not (Test-Path $iscc)) { throw 'Install Inno Setup 6 before packaging' }
 & $iscc "/DAppVersion=$Version" "/DSourceDir=$stage" "/DOutputDir=$out" "/DOutputName=$name-setup" packaging/windows/wasync.iss
 Check-Exit
 Get-ChildItem $out -File | Where-Object Extension -in '.zip','.exe' | ForEach-Object {
  $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
  "$hash  $($_.Name)" | Set-Content -Encoding ascii "$($_.FullName).sha256"
 }
} finally { Remove-Item -Recurse -Force $stage }
