#!/usr/bin/env bash
# Maintainer tool: build the native wasync and whatsapp-axi binaries, the archive,
# and the macOS installer. Both binaries carry the same VERSION. The macOS
# installer lets the user choose whether to add whatsapp-axi (off by default).
set -euo pipefail

version="${1:-}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$ ]]; then
  echo "Usage: scripts/package.sh VERSION (example: 0.1.0-preview.1)" >&2
  exit 1
fi
project_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$project_dir"
platform="$(go env GOHOSTOS)"
architecture="$(go env GOHOSTARCH)"
case "$platform/$architecture" in
  darwin/arm64|darwin/amd64|linux/arm64|linux/amd64) ;;
  *) echo "Unsupported native release target: $platform/$architecture" >&2; exit 1 ;;
esac
out="$project_dir/dist/$version/${platform}_${architecture}"
mkdir -p "$out"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
commit="$(git rev-parse --short=12 HEAD)"
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then commit="$commit-dirty"; fi
built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
if [[ "$platform" == darwin ]]; then
  export MACOSX_DEPLOYMENT_TARGET=15.0
  if [[ -n "${WASYNC_NOTARY_PROFILE:-}" && ( -z "${WASYNC_CODESIGN_IDENTITY:-}" || -z "${WASYNC_INSTALLER_IDENTITY:-}" ) ]]; then
    echo 'Notarized packages require Developer ID Application and Developer ID Installer identities.' >&2
    exit 1
  fi
fi
CGO_ENABLED=1 GOOS="$platform" GOARCH="$architecture" go build -trimpath -tags sqlite_fts5 \
  -ldflags "-s -w -X github.com/gehariharan/wasync/internal/buildinfo.Version=$version -X github.com/gehariharan/wasync/internal/buildinfo.Commit=$commit -X github.com/gehariharan/wasync/internal/buildinfo.BuiltAt=$built_at" \
  -o "$stage/wasync" ./cmd/wasync
axi_buildinfo=github.com/gehariharan/wasync/axi/internal/buildinfo
(cd axi && CGO_ENABLED=0 GOOS="$platform" GOARCH="$architecture" go build -trimpath \
  -ldflags "-s -w -X $axi_buildinfo.Version=$version -X $axi_buildinfo.Commit=$commit -X $axi_buildinfo.Date=$built_at" \
  -o "$stage/whatsapp-axi" ./cmd/whatsapp-axi)
if [[ "$platform" == darwin ]]; then
  if [[ -n "${WASYNC_CODESIGN_IDENTITY:-}" ]]; then
    codesign --force --options runtime --timestamp --identifier com.gehariharan.wasynctuicli --sign "$WASYNC_CODESIGN_IDENTITY" "$stage/wasync"
    codesign --force --options runtime --timestamp --identifier com.gehariharan.wasynctuicli.axi --sign "$WASYNC_CODESIGN_IDENTITY" "$stage/whatsapp-axi"
  else
    codesign --force --sign - "$stage/wasync"
    codesign --force --sign - "$stage/whatsapp-axi"
  fi
fi
"$stage/wasync" version
if [[ "$("$stage/whatsapp-axi" --version)" != "$version" ]]; then
  echo "whatsapp-axi does not report version $version; both tools must ship from one release." >&2
  exit 1
fi
cp README.md "$stage/README.md"
cp LICENSE "$stage/LICENSE"
python3 scripts/collect-licenses.py "$stage/THIRD_PARTY_LICENSES"
name="wasync_${version}_${platform}_${architecture}"
if [[ "$platform" == darwin ]]; then
  # The signing step and the Homebrew cask expect wasync alone in the Mac archive.
  tar -czf "$out/$name.tar.gz" -C "$stage" wasync README.md LICENSE THIRD_PARTY_LICENSES
else
  # Linux: install.sh asks before installing whatsapp-axi; extracting installs nothing.
  install -m 755 packaging/linux/install.sh "$stage/install.sh"
  tar -czf "$out/$name.tar.gz" -C "$stage" wasync whatsapp-axi install.sh README.md LICENSE THIRD_PARTY_LICENSES
fi

if [[ "$platform" == darwin ]]; then
  # Two component packages joined by packaging/macos/distribution.xml: wasync is
  # always installed; whatsapp-axi is a choice that starts unticked.
  mkdir -p "$stage/payload/usr/local/bin" "$stage/payload/usr/local/share/wasync" "$stage/hooks" \
    "$stage/axi-payload/usr/local/bin" "$stage/axi-hooks" "$stage/components"
  cp -R "$stage/THIRD_PARTY_LICENSES" "$stage/payload/usr/local/share/wasync/"
  cp LICENSE "$stage/payload/usr/local/share/wasync/LICENSE"
  install -m 755 "$stage/wasync" "$stage/payload/usr/local/bin/wasync"
  install -m 755 "$stage/whatsapp-axi" "$stage/axi-payload/usr/local/bin/whatsapp-axi"
  install -m 755 packaging/macos/scripts/axi-preinstall "$stage/axi-hooks/preinstall"
  # Refuse installation on the wrong CPU rather than installing an unusable CLI.
  if [[ "$architecture" == arm64 ]]; then expected_cpu=arm64; else expected_cpu=x86_64; fi
  cat > "$stage/hooks/preinstall" <<HOOK
#!/bin/sh
set -eu
if [ "\$(uname -m)" != "$expected_cpu" ]; then
  echo "Download the wasync installer matching this Mac's CPU ($expected_cpu required)." >&2
  exit 1
fi
major="\$(sw_vers -productVersion | cut -d. -f1)"
if [ "\$major" -lt 15 ]; then
  echo "This wasync package requires macOS 15 or later." >&2
  exit 1
fi
if [ -L /usr/local/bin/wasync ]; then
  echo "A symlink already owns /usr/local/bin/wasync. Uninstall that installation before using this package." >&2
  exit 1
fi
HOOK
  chmod 755 "$stage/hooks/preinstall"
  component_args=(--install-location / --ownership recommended --version "${version%%-*}")
  pkgbuild --root "$stage/payload" --scripts "$stage/hooks" --identifier com.gehariharan.wasynctuicli \
    "${component_args[@]}" "$stage/components/wasync.pkg"
  pkgbuild --root "$stage/axi-payload" --scripts "$stage/axi-hooks" --identifier com.gehariharan.wasynctuicli.axi \
    "${component_args[@]}" "$stage/components/whatsapp-axi.pkg"
  sed "s/HOST_ARCHITECTURE/$expected_cpu/" packaging/macos/distribution.xml > "$stage/distribution.xml"
  product_args=(--distribution "$stage/distribution.xml" --resources packaging/macos/resources
    --package-path "$stage/components")
  if [[ -n "${WASYNC_INSTALLER_IDENTITY:-}" ]]; then product_args+=(--sign "$WASYNC_INSTALLER_IDENTITY" --timestamp); fi
  productbuild "${product_args[@]}" "$out/$name.pkg"
  if [[ -n "${WASYNC_NOTARY_PROFILE:-}" ]]; then
    bash scripts/notarize.sh "$WASYNC_NOTARY_PROFILE" "$stage/wasync" "$out/$name.pkg"
  fi
fi
(
  cd "$out"
  shasum -a 256 "$name.tar.gz" > "$name.tar.gz.sha256"
  if [[ -f "$name.pkg" ]]; then shasum -a 256 "$name.pkg" > "$name.pkg.sha256"; fi
)
printf 'Release packages: %s\n' "$out"
