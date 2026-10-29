#!/usr/bin/env bash
# Run on the signing Mac. The private keys and notary credentials stay there.
set -euo pipefail
version="${1:?Usage: scripts/sign-release-macos.sh VERSION ASSET_DIRECTORY}"
assets="${2:?Usage: scripts/sign-release-macos.sh VERSION ASSET_DIRECTORY}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$ ]]; then
  echo 'Invalid release version' >&2; exit 1
fi
scripts_dir="$(cd "$(dirname "$0")" && pwd)"
assets="$(cd "$assets" && pwd)"
: "${WASYNC_CODESIGN_IDENTITY:?Set the Developer ID Application identity}"
: "${WASYNC_INSTALLER_IDENTITY:?Set the Developer ID Installer identity}"
: "${WASYNC_NOTARY_PROFILE:?Set the notarytool profile}"
: "${WASYNC_SIGNING_KEYCHAIN:?Set the signing Keychain path}"
work="$(mktemp -d)"
trap 'result=$?; if [[ $result == 0 ]]; then rm -rf "$work"; else printf "Signing stopped; diagnostic files retained in %s\n" "$work" >&2; fi' EXIT

# Unlock and use credentials in this same SSH session. Password input is handled
# by security's terminal prompt, never an argument or a script variable.
if [[ "${WASYNC_SIGNING_UNATTENDED:-0}" != 1 ]]; then
  security unlock-keychain "$WASYNC_SIGNING_KEYCHAIN"
fi
xcrun notarytool history --keychain-profile "$WASYNC_NOTARY_PROFILE" \
  --keychain "$WASYNC_SIGNING_KEYCHAIN" --output-format json > "$work/history.json"
for architecture in arm64 amd64; do
  name="wasync_${version}_darwin_${architecture}"
  axi_name="whatsapp-axi_${version}_darwin_${architecture}"
  archive="$assets/$name.tar.gz"
  package="$assets/$name.pkg"
  mkdir "$work/$architecture" "$work/axi-$architecture"
  tar -xzf "$archive" -C "$work/$architecture"
  tar -xzf "$assets/$axi_name.tar.gz" -C "$work/axi-$architecture"
  binary="$work/$architecture/wasync"
  axi_binary="$work/axi-$architecture/whatsapp-axi"
  expected_cpu="$architecture"
  if [[ "$architecture" == amd64 ]]; then expected_cpu=x86_64; fi
  if [[ "$(lipo -archs "$binary")" != "$expected_cpu" ]]; then
    echo "Unexpected executable architecture: $name" >&2; exit 1
  fi
  [[ "$(lipo -archs "$axi_binary")" == "$expected_cpu" ]]
  # The installer is a product archive with two components: wasync (always
  # installed) and whatsapp-axi (a choice that starts unticked).
  expanded="$work/$architecture-pkg"
  pkgutil --expand-full "$package" "$expanded"
  payload="$expanded/wasync.pkg/Payload"
  axi_payload="$expanded/whatsapp-axi.pkg/Payload"
  # Confirm archive and installer contain identical original wasync executables.
  cmp "$binary" "$payload/usr/local/bin/wasync"
  # The installer's AXI was built beside the standalone one but not necessarily
  # byte for byte; require the same CPU and release version, then ship the one
  # signed standalone executable in both places so the two cannot differ.
  [[ "$(lipo -archs "$axi_payload/usr/local/bin/whatsapp-axi")" == "$expected_cpu" ]]
  grep -qF "$version" "$axi_payload/usr/local/bin/whatsapp-axi"
  grep -qF "$version" "$axi_binary"
  codesign --force --options runtime --timestamp \
    --keychain "$WASYNC_SIGNING_KEYCHAIN" \
    --identifier com.gehariharan.wasynctuicli \
    --sign "$WASYNC_CODESIGN_IDENTITY" "$binary"
  codesign --force --options runtime --timestamp \
    --keychain "$WASYNC_SIGNING_KEYCHAIN" \
    --identifier com.gehariharan.wasynctuicli.axi \
    --sign "$WASYNC_CODESIGN_IDENTITY" "$axi_binary"
  install -m 755 "$binary" "$payload/usr/local/bin/wasync"
  install -m 755 "$axi_binary" "$axi_payload/usr/local/bin/whatsapp-axi"
  components="$work/$architecture-components"
  mkdir "$components"
  pkgbuild --root "$payload" --scripts "$expanded/wasync.pkg/Scripts" \
    --identifier com.gehariharan.wasynctuicli --version "${version%%-*}" \
    --install-location / --ownership recommended "$components/wasync.pkg"
  pkgbuild --root "$axi_payload" --scripts "$expanded/whatsapp-axi.pkg/Scripts" \
    --identifier com.gehariharan.wasynctuicli.axi --version "${version%%-*}" \
    --install-location / --ownership recommended "$components/whatsapp-axi.pkg"
  productbuild --distribution "$expanded/Distribution" --resources "$expanded/Resources" \
    --package-path "$components" \
    --keychain "$WASYNC_SIGNING_KEYCHAIN" \
    --sign "$WASYNC_INSTALLER_IDENTITY" --timestamp "$work/$name.pkg"
  bash "$scripts_dir/notarize.sh" "$WASYNC_NOTARY_PROFILE" "$binary" "$work/$name.pkg"
  # spctl's execute assessment expects an app bundle and rejects standalone
  # Mach-O tools even when notarized. Require Apple's ticket for the CLI itself.
  codesign --verify --strict --verbose=2 -R='notarized' --check-notarization "$binary"
  archive_files=(wasync README.md THIRD_PARTY_LICENSES)
  if [[ -f "$work/$architecture/LICENSE" ]]; then archive_files+=(LICENSE); fi
  tar -czf "$work/$name.tar.gz" -C "$work/$architecture" "${archive_files[@]}"

  # The standalone AXI executable is notarized by its own submission; the same
  # bytes inside the installer are covered by the installer's submission.
  bash "$scripts_dir/notarize.sh" "$WASYNC_NOTARY_PROFILE" "$axi_binary"
  codesign --verify --strict --verbose=2 -R='notarized' --check-notarization "$axi_binary"
  tar -czf "$work/$axi_name.tar.gz" -C "$work/axi-$architecture" whatsapp-axi README.md LICENSE
done
# Replace inputs only after both architectures pass signing and notarization.
for architecture in arm64 amd64; do
  name="wasync_${version}_darwin_${architecture}"
  cp "$work/$name.tar.gz" "$work/$name.pkg" "$assets/"
  cp "$work/whatsapp-axi_${version}_darwin_${architecture}.tar.gz" "$assets/"
  (cd "$assets"; shasum -a 256 "$name.tar.gz" > "$name.tar.gz.sha256"; shasum -a 256 "$name.pkg" > "$name.pkg.sha256")
done
printf 'Signed and notarized both macOS architectures in %s\n' "$assets"
