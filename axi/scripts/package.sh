#!/usr/bin/env bash
# Maintainer and CI tool: cross-build every release archive and SHA256SUMS.
#
#   scripts/package.sh [--dry-run] VERSION
#
# Builds wasync-axi with CGO_ENABLED=0 for darwin, linux and windows on amd64
# and arm64 into dist/VERSION/:
#   wasync-axi_VERSION_{darwin,linux}_{amd64,arm64}.tar.gz
#   wasync-axi_VERSION_windows_{amd64,arm64}.zip
#   SHA256SUMS
# Each archive holds the binary, its old name whatsapp-axi (a symlink, or a
# copy on Windows), README.md and LICENSE at its top level.
#
# The script never publishes, tags or uploads anything. Without --dry-run it
# refuses a dirty work tree or a HEAD that is not tagged vVERSION, so a release
# archive always matches a tag. --dry-run (CI, local checks) skips both checks.
#
# Archives are reproducible for a given commit and Go toolchain: files are
# added in a fixed order with fixed owner and mtime (SOURCE_DATE_EPOCH, by
# default the commit time) and gzip stores no name or timestamp.
#
# Needs: go, git, GNU tar (gtar on macOS), gzip, zip, sha256sum or shasum.
set -euo pipefail

dry_run=0
if [[ "${1:-}" == --dry-run ]]; then dry_run=1; shift; fi
version="${1:-}"
if [[ $# -ne 1 || ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$ ]]; then
  echo "Usage: scripts/package.sh [--dry-run] VERSION (example: 0.1.0 or 0.1.0-rc.1, no leading v)" >&2
  exit 2
fi

project_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$project_dir"

if tar --version 2>/dev/null | grep -q 'GNU tar'; then gnutar=tar
elif command -v gtar >/dev/null 2>&1; then gnutar=gtar
else echo "package.sh: GNU tar is required (on macOS: brew install gnu-tar)" >&2; exit 1
fi
for tool in go git gzip zip; do
  command -v "$tool" >/dev/null 2>&1 || { echo "package.sh: $tool is required" >&2; exit 1; }
done
if command -v sha256sum >/dev/null 2>&1; then sha256() { sha256sum "$@"; }
else sha256() { shasum -a 256 "$@"; }
fi

commit="$(git rev-parse --short=12 HEAD)"
dirty=0
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then dirty=1; fi
if (( ! dry_run )); then
  if (( dirty )); then
    echo "package.sh: the work tree has uncommitted changes; commit them or use --dry-run" >&2
    exit 1
  fi
  if ! git tag --points-at HEAD | grep -qx "v$version"; then
    echo "package.sh: HEAD is not tagged v$version; tag it first or use --dry-run" >&2
    exit 1
  fi
fi
if (( dirty )); then commit="$commit-dirty"; fi

# The build date is the commit time, so rebuilding a commit is reproducible.
epoch="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct HEAD)}"
if [[ ! "$epoch" =~ ^[0-9]+$ ]]; then echo "package.sh: SOURCE_DATE_EPOCH must be an integer" >&2; exit 1; fi
build_date="$(TZ=UTC git log -1 --date=format-local:%Y-%m-%dT%H:%M:%SZ --format=%cd HEAD)"
if [[ -n "${SOURCE_DATE_EPOCH:-}" ]]; then
  build_date="$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$epoch" +%Y-%m-%dT%H:%M:%SZ)"
fi
# touch -t wants local time; run it in UTC so zip and tar see the same instant.
touch_stamp="$(date -u -d "@$epoch" +%Y%m%d%H%M.%S 2>/dev/null || date -u -r "$epoch" +%Y%m%d%H%M.%S)"

out="$project_dir/dist/$version"
rm -rf "$out"
mkdir -p "$out"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

pkg=github.com/gehariharan/wasync/axi/internal/buildinfo
ldflags="-s -w -X $pkg.Version=$version -X $pkg.Commit=$commit -X $pkg.Date=$build_date"
host="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
targets=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64)

for target in "${targets[@]}"; do
  goos="${target%/*}"
  goarch="${target#*/}"
  name="wasync-axi_${version}_${goos}_${goarch}"
  exe=wasync-axi
  alias=whatsapp-axi
  if [[ "$goos" == windows ]]; then exe=wasync-axi.exe; alias=whatsapp-axi.exe; fi
  dir="$stage/$name"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "$dir/$exe" ./cmd/wasync-axi
  cp README.md LICENSE "$dir/"
  chmod 755 "$dir/$exe"
  # The old command name keeps working: a symlink, or a copy on Windows.
  if [[ "$goos" == windows ]]; then cp "$dir/$exe" "$dir/$alias"; else ln -s "$exe" "$dir/$alias"; fi
  chmod 644 "$dir/README.md" "$dir/LICENSE"

  if [[ "$target" == "$host" ]]; then
    reported="$("$dir/$exe" --version)"
    if [[ "$reported" != "$version" ]]; then
      echo "package.sh: $target binary reports '$reported', expected '$version'" >&2
      exit 1
    fi
    echo "package.sh: $target binary runs: --version = $reported"
  fi

  (cd "$dir" && TZ=UTC touch -h -t "$touch_stamp" "$exe" "$alias" README.md LICENSE)
  if [[ "$goos" == windows ]]; then
    # -X drops extra attributes (uid/gid, extended timestamps); -D adds no
    # directory entries; files are listed in a fixed order.
    (cd "$dir" && TZ=UTC zip -q -X -D -9 "$out/$name.zip" "$exe" "$alias" README.md LICENSE)
  else
    "$gnutar" --format=ustar --owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
      -C "$dir" -cf - "$exe" "$alias" README.md LICENSE | gzip -n -9 > "$out/$name.tar.gz"
  fi
done

(
  cd "$out"
  sha256 -- *.tar.gz *.zip | LC_ALL=C sort -k2 > SHA256SUMS
)
printf 'package.sh: wasync-axi %s (commit %s, date %s)\n' "$version" "$commit" "$build_date"
printf 'package.sh: release files in %s\n' "$out"
(cd "$out" && ls -l)
