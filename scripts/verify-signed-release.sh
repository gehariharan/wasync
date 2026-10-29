#!/usr/bin/env bash
set -euo pipefail
version="${1:?Usage: scripts/verify-signed-release.sh VERSION ARCHITECTURE}"
architecture="${2:?Usage: scripts/verify-signed-release.sh VERSION ARCHITECTURE}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$ ]]; then
  echo 'Invalid version' >&2; exit 1
fi
case "$architecture" in arm64) cpu=arm64;; amd64) cpu=x86_64;; *) exit 1;; esac
project_dir="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
# Apple's ticket service can drop connections. Retry checks, retaining failure
# when all attempts fail; never skip a failed signature or ticket requirement.
retry_check() {
  local attempt result=1
  for attempt in 1 2 3; do
    if "$@"; then return 0; else result=$?; fi
    if [[ "$attempt" != 3 ]]; then sleep 3; fi
  done
  return "$result"
}
if [[ "$(gh release view "v$version" --json isDraft --jq .isDraft)" != true ]]; then
  echo 'Only draft releases may be published by this workflow.' >&2; exit 1
fi
name="wasync_${version}_darwin_${architecture}"
axi_name="whatsapp-axi_${version}_darwin_${architecture}"
gh release download "v$version" --pattern "$name.*" --pattern "$axi_name.tar.gz" --pattern checksums.txt --dir "$work"
cd "$work"
# Require the aggregate checksum manifest to cover both assets.
python3 - "$name" "$axi_name" <<'PY'
import hashlib, sys
from pathlib import Path
lines = Path('checksums.txt').read_text().splitlines()
entries = {line.split()[1].removeprefix('./'): line.split()[0] for line in lines}
for name in (sys.argv[1] + '.tar.gz', sys.argv[1] + '.pkg', sys.argv[2] + '.tar.gz'):
    if entries.get(name) != hashlib.sha256(Path(name).read_bytes()).hexdigest():
        raise SystemExit('Missing or invalid checksum: ' + name)
PY
mkdir archive
tar -xzf "$name.tar.gz" -C archive
[[ "$(lipo -archs archive/wasync)" == "$cpu" ]]
codesign --verify --strict \
  -R='anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = "57V379V642"' archive/wasync
pkgutil --check-signature "$name.pkg" > package-signature.txt
grep -Fq 'Developer ID Installer: Xaurum Fintech Private Limited (57V379V642)' package-signature.txt
retry_check xcrun stapler validate "$name.pkg"
retry_check spctl --assess --type install --verbose=2 "$name.pkg"
retry_check codesign --verify --strict --verbose=2 -R='notarized' --check-notarization archive/wasync
pkgutil --expand-full "$name.pkg" expanded
# The installer has two components; wasync is always installed, AXI for agents is a choice.
cmp archive/wasync expanded/wasync.pkg/Payload/usr/local/bin/wasync
mkdir axi
tar -xzf "$axi_name.tar.gz" -C axi
[[ "$(lipo -archs axi/whatsapp-axi)" == "$cpu" ]]
codesign --verify --strict \
  -R='anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = "57V379V642"' axi/whatsapp-axi
retry_check codesign --verify --strict --verbose=2 -R='notarized' --check-notarization axi/whatsapp-axi
# The installer's AXI must be the very executable signed and notarized above.
cmp axi/whatsapp-axi expanded/whatsapp-axi.pkg/Payload/usr/local/bin/whatsapp-axi
# Exercise the exact signed executables with a quarantine attribute intact.
xattr -w com.apple.quarantine "0083;$(printf '%x' "$(date +%s)");wasync-release-check;" archive/wasync
xattr -w com.apple.quarantine "0083;$(printf '%x' "$(date +%s)");wasync-release-check;" axi/whatsapp-axi
[[ "$(axi/whatsapp-axi --version)" == "$version" ]]
python3 "$project_dir/scripts/smoke-installed.py" "$work/archive/wasync" "$version" "$architecture" "$work/axi/whatsapp-axi"
# The installer offers AXI for agents unticked and, on a hosted runner, installs both ways.
bash "$project_dir/scripts/test-macos-pkg.sh" "$work/$name.pkg" "$version" "$architecture" "$work/axi/whatsapp-axi"
