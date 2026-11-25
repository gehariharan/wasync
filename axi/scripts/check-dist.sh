#!/usr/bin/env bash
# Check the output of scripts/package.sh before anything is released:
#
#   scripts/check-dist.sh VERSION [DIRECTORY]   (DIRECTORY defaults to dist/VERSION)
#
# - exactly the six expected archives plus SHA256SUMS, nothing else
# - SHA256SUMS lists every archive once and every checksum matches
# - each archive holds exactly the binary, its old name whatsapp-axi (a
#   symlink to it, or an identical copy on Windows), README.md and LICENSE,
#   with an executable binary, and README.md/LICENSE identical to the
#   repository's
#
# Needs: bash, tar, unzip, sha256sum or shasum. Changes nothing.
set -euo pipefail

version="${1:?Usage: scripts/check-dist.sh VERSION [DIRECTORY]}"
project_dir="$(cd "$(dirname "$0")/.." && pwd)"
dir="${2:-$project_dir/dist/$version}"
dir="$(cd "$dir" && pwd)"

fail() { echo "check-dist: FAIL: $*" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then sha256() { sha256sum "$@"; }
else sha256() { shasum -a 256 "$@"; }
fi

expected=()
for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  expected+=("wasync-axi_${version}_${target}.tar.gz")
done
for target in windows_amd64 windows_arm64; do
  expected+=("wasync-axi_${version}_${target}.zip")
done

actual="$(cd "$dir" && LC_ALL=C ls -1A)"
want="$(printf '%s\n' "${expected[@]}" SHA256SUMS | LC_ALL=C sort)"
[[ "$actual" == "$want" ]] || fail "unexpected files in $dir:
$(diff <(echo "$want") <(echo "$actual") || true)"

listed="$(awk '{print $2}' "$dir/SHA256SUMS" | LC_ALL=C sort)"
[[ "$listed" == "$(printf '%s\n' "${expected[@]}" | LC_ALL=C sort)" ]] || fail "SHA256SUMS does not list exactly the six archives"
awk 'NF != 2 || $1 !~ /^[0-9a-f]{64}$/ { exit 1 }' "$dir/SHA256SUMS" || fail "SHA256SUMS has a malformed line"
(cd "$dir" && sha256 -c SHA256SUMS >/dev/null) || fail "a checksum does not match"
echo "check-dist: ok - SHA256SUMS matches all ${#expected[@]} archives"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
for archive in "${expected[@]}"; do
  x="$work/${archive%.*}"
  mkdir -p "$x"
  if [[ "$archive" == *.zip ]]; then
    exe=wasync-axi.exe
    alias=whatsapp-axi.exe
    entries="$(unzip -Z1 "$dir/$archive" | LC_ALL=C sort)"
    unzip -q "$dir/$archive" -d "$x"
    cmp -s "$x/$exe" "$x/$alias" || fail "$archive: $alias is not a copy of $exe"
  else
    exe=wasync-axi
    alias=whatsapp-axi
    entries="$(tar -tzf "$dir/$archive" | LC_ALL=C sort)"
    tar -xzf "$dir/$archive" -C "$x"
    [[ -x "$x/$exe" ]] || fail "$archive: $exe is not executable"
    [[ -L "$x/$alias" && "$(readlink "$x/$alias")" == "$exe" ]] || fail "$archive: $alias is not a symlink to $exe"
  fi
  [[ "$entries" == "$(printf '%s\n' LICENSE README.md "$exe" "$alias" | LC_ALL=C sort)" ]] || fail "$archive holds:
$entries"
  [[ -s "$x/$exe" ]] || fail "$archive: $exe is empty"
  cmp -s "$x/README.md" "$project_dir/README.md" || fail "$archive: README.md differs from the repository"
  cmp -s "$x/LICENSE" "$project_dir/LICENSE" || fail "$archive: LICENSE differs from the repository"
  # The version string is embedded by -ldflags; check it without running a
  # foreign-architecture binary.
  grep -qF "$version" "$x/$exe" || fail "$archive: $exe does not embed version $version"
  echo "check-dist: ok - $archive ($exe, $alias, README.md, LICENSE)"
done
echo "check-dist: all release files for $version look right"
