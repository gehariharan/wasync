# Verifies Authenticode signatures on Windows release packages and every shipped .exe inside them.
#   ./scripts/verify-windows-signatures.ps1 -Version 0.1.0-preview.1 [-DistDir path] [-ExpectedPublisher 'Hariharan Ragunathan']
param(
 [Parameter(Mandatory=$true)][string]$Version,
 [string]$DistDir = '',
 [string]$ExpectedPublisher = 'Hariharan Ragunathan'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($Version -notmatch '^\d+\.\d+\.\d+(-[A-Za-z0-9][A-Za-z0-9.-]*)?\z') { throw 'Invalid package version' }
Set-Location (Join-Path $PSScriptRoot '..')
$dist = if ($DistDir) { (Resolve-Path $DistDir).Path } else { Join-Path (Get-Location) "dist/$Version/windows_amd64" }
if (-not (Test-Path $dist)) { throw "Distribution directory does not exist: $dist" }

function Find-SignTool {
 $cmd = Get-Command signtool.exe -ErrorAction SilentlyContinue
 if ($cmd) { return $cmd.Source }
 $kitsRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits/10/bin'
 if (Test-Path $kitsRoot) {
  $sdk = Get-ChildItem $kitsRoot -Directory |
   Where-Object { $_.Name -match '^10\.0\.\d+\.0\z' -and (Test-Path (Join-Path $_.FullName 'x64/signtool.exe')) } |
   Sort-Object { [version]$_.Name } -Descending | Select-Object -First 1
  if ($sdk) { return (Join-Path $sdk.FullName 'x64/signtool.exe') }
 }
 $asRoot = Join-Path $env:LOCALAPPDATA 'ArtifactSigning/Microsoft.Windows.SDK.BuildTools'
 if (Test-Path $asRoot) {
  $found = Get-ChildItem $asRoot -Recurse -File -Filter 'signtool.exe' -ErrorAction SilentlyContinue |
   Where-Object { $_.DirectoryName -match '[\\/]x64$' } | Select-Object -First 1
  if ($found) { return $found.FullName }
 }
 throw 'signtool.exe was not found on this runner'
}

$signtool = Find-SignTool

function Assert-SignedExecutable([string]$Path, [string]$Label) {
 if (-not (Test-Path -LiteralPath $Path)) { throw "Missing executable: $Label ($Path)" }
 $sig = Get-AuthenticodeSignature -LiteralPath $Path
 if ($sig.Status -ne 'Valid') {
  throw "$Label is not validly signed: Status=$($sig.Status) ($($sig.StatusMessage))"
 }
 if ($null -eq $sig.SignerCertificate) {
  throw "$Label has no signer certificate"
 }
 if ($null -eq $sig.TimeStamperCertificate) {
  throw "$Label is missing an RFC3161 timestamp"
 }
 if ($ExpectedPublisher) {
  $pattern = '(^|,\s*)CN="?' + [regex]::Escape($ExpectedPublisher) + '"?(,|$)'
  if ($sig.SignerCertificate.Subject -notmatch $pattern) {
   throw "$Label signer subject '$($sig.SignerCertificate.Subject)' does not match expected publisher '$ExpectedPublisher'"
  }
 }
 $verifyOut = & $signtool verify /pa $Path 2>&1 | Out-String
 if ($LASTEXITCODE -ne 0) {
  throw "signtool verify /pa failed for $Label with exit code ${LASTEXITCODE}: $verifyOut"
 }
 Write-Host "Signed OK: $Label | Signer: $($sig.SignerCertificate.Subject) | Timestamp: $($sig.TimeStamperCertificate.Subject)"
}

$setupName = "wasync_${Version}_windows_amd64-setup.exe"
$zipName = "wasync_${Version}_windows_amd64.zip"
$setup = Join-Path $dist $setupName
$zip = Join-Path $dist $zipName
$axiAmd64Zip = Join-Path $dist "wasync-axi_${Version}_windows_amd64.zip"
$axiArm64Zip = Join-Path $dist "wasync-axi_${Version}_windows_arm64.zip"

# Verify Setup installer and any loose .exe files in $dist first.
Assert-SignedExecutable $setup $setupName
foreach ($exe in @(Get-ChildItem -Path $dist -File -Filter '*.exe')) {
 if ($exe.Name -ne $setupName) {
  Assert-SignedExecutable $exe.FullName $exe.Name
 }
}

$temporaryRoot = [Environment]::GetEnvironmentVariable('RUNNER_TEMP')
if (-not $temporaryRoot) { $temporaryRoot = [IO.Path]::GetTempPath() }
$work = Join-Path $temporaryRoot "wasync-sig-verify-$([guid]::NewGuid())"
New-Item -ItemType Directory -Force $work | Out-Null
try {
 # Verify portable wasync ZIP and every .exe inside it.
 if (-not (Test-Path -LiteralPath $zip)) { throw "Missing portable ZIP: $zipName" }
 $coreDir = Join-Path $work 'core'
 Expand-Archive -LiteralPath $zip -DestinationPath $coreDir
 foreach ($req in 'wasync.exe', 'wasync-desktop.exe', 'wasync-axi.exe', 'whatsapp-axi.exe') {
  if (-not (Test-Path -LiteralPath (Join-Path $coreDir $req))) { throw "$zipName is missing $req" }
 }
 $coreExes = @(Get-ChildItem -Path $coreDir -Recurse -File -Filter '*.exe')
 if ($coreExes.Count -eq 0) { throw "$zipName contains no .exe files" }
 foreach ($exe in $coreExes) {
  $rel = $exe.FullName.Substring($coreDir.Length).TrimStart('\', '/')
  Assert-SignedExecutable $exe.FullName "$zipName/$rel"
 }
 if ((Get-FileHash (Join-Path $coreDir 'wasync-axi.exe') -Algorithm SHA256).Hash -ne (Get-FileHash (Join-Path $coreDir 'whatsapp-axi.exe') -Algorithm SHA256).Hash) {
  throw "$zipName whatsapp-axi.exe is not identical to wasync-axi.exe"
 }

 # Verify standalone wasync-axi Windows ZIPs (amd64 and arm64) and every .exe inside them.
 foreach ($arch in 'amd64', 'arm64') {
  $axiZipName = "wasync-axi_${Version}_windows_${arch}.zip"
  $axiZip = if ($arch -eq 'amd64') { $axiAmd64Zip } else { $axiArm64Zip }
  if (-not (Test-Path -LiteralPath $axiZip)) { throw "Missing standalone AXI ZIP: $axiZipName" }
  $axiDir = Join-Path $work "axi-$arch"
  Expand-Archive -LiteralPath $axiZip -DestinationPath $axiDir
  foreach ($req in 'wasync-axi.exe', 'whatsapp-axi.exe', 'README.md', 'LICENSE') {
   if (-not (Test-Path -LiteralPath (Join-Path $axiDir $req))) { throw "$axiZipName is missing $req" }
  }
  $axiExes = @(Get-ChildItem -Path $axiDir -Recurse -File -Filter '*.exe')
  if ($axiExes.Count -eq 0) { throw "$axiZipName contains no .exe files" }
  foreach ($exe in $axiExes) {
   $rel = $exe.FullName.Substring($axiDir.Length).TrimStart('\', '/')
   Assert-SignedExecutable $exe.FullName "$axiZipName/$rel"
  }
  if ((Get-FileHash (Join-Path $axiDir 'wasync-axi.exe') -Algorithm SHA256).Hash -ne (Get-FileHash (Join-Path $axiDir 'whatsapp-axi.exe') -Algorithm SHA256).Hash) {
   throw "$axiZipName whatsapp-axi.exe is not identical to wasync-axi.exe"
  }
  if ($arch -eq 'amd64') {
   $reported = ((& (Join-Path $axiDir 'wasync-axi.exe') --version) | Out-String).Trim()
   if ($LASTEXITCODE -ne 0 -or $reported -ne $Version) {
    throw "$axiZipName/wasync-axi.exe reported version '$reported', expected '$Version'"
   }
  }
 }
} finally {
 Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
Write-Host "All Windows release executables and installer for $Version have valid, timestamped Authenticode signatures."
