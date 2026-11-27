#!/usr/bin/env bash
# Checks the macOS installer's two components and their choices.
#
#   scripts/test-macos-pkg.sh PKG VERSION ARCHITECTURE [SIGNED_AXI_BINARY]
#
# Always (no administrator access, installs nothing): the installer holds wasync
# and wasync-axi as separate components, and both are selected and locked. With
# SIGNED_AXI_BINARY, the installer's wasync-axi must be byte-identical to it.
#
# On a disposable GitHub-hosted runner only, it also installs the package with
# `installer` and its default choices (both at VERSION), installs it again over
# a wasync-axi symlink owned by another installation method (which it must keep,
# also after an interrupted install), and removes everything it installed.
set -euo pipefail
package="${1:?Usage: scripts/test-macos-pkg.sh PKG VERSION ARCHITECTURE [SIGNED_AXI_BINARY]}"
version="${2:?}"
architecture="${3:?}"
signed_axi="${4:-}"
project_dir="$(cd "$(dirname "$0")/.." && pwd)"
package="$(cd "$(dirname "$package")" && pwd)/$(basename "$package")"
work="$(mktemp -d)"
installed=0
cleanup() {
  if [[ "$installed" == 1 ]]; then
    sudo rm -f /usr/local/bin/wasync /usr/local/bin/wasync-axi /usr/local/bin/whatsapp-axi /usr/local/bin/.wasync-axi.kept
    sudo rm -rf /usr/local/share/wasync
    for id in com.gehariharan.wasynctuicli com.gehariharan.wasynctuicli.axi; do sudo pkgutil --forget "$id" >/dev/null 2>&1 || true; done
  fi
  rm -rf "$work"
}
trap cleanup EXIT
fail() { echo "test-macos-pkg: FAIL: $*" >&2; exit 1; }
pass() { echo "test-macos-pkg: ok - $*"; }
# Run Installer with its output visible; if it fails, show why from its own log.
run_installer() {
  sudo installer -pkg "$package" -target / -verboseR "$@" || {
    status=$?
    sudo tail -n 60 /var/log/install.log >&2 || true
    fail "installer exited with status $status"
  }
}

pkgutil --expand-full "$package" "$work/expanded"
[[ -x "$work/expanded/wasync.pkg/Payload/usr/local/bin/wasync" ]] || fail "wasync is not in the wasync component"
[[ -x "$work/expanded/wasync-axi.pkg/Payload/usr/local/bin/wasync-axi" ]] || fail "wasync-axi is not in its own component"
[[ ! -e "$work/expanded/wasync.pkg/Payload/usr/local/bin/wasync-axi" ]] || fail "wasync-axi leaked into the always-installed component"
grep -q 'customize="always"' "$work/expanded/Distribution" || fail "the installer does not show the component list"
pass "wasync and wasync-axi are separate components"

# Ask Installer itself what it would preselect; this installs nothing.
installer -pkg "$package" -target / -showChoicesXML > "$work/choices.plist"
python3 - "$work/choices.plist" <<'PY'
import plistlib, sys
def walk(items):
    for item in items:
        yield item
        yield from walk(item.get("childItems", []))
with open(sys.argv[1], "rb") as handle:
    choices = {c["choiceIdentifier"]: c for c in walk(plistlib.load(handle))}
wasync, axi = choices["wasync"], choices["axi"]
assert wasync["choiceIsSelected"] == 1 and not wasync["choiceIsEnabled"], "wasync must be selected and not removable"
assert axi["choiceIsSelected"] == 1 and not axi["choiceIsEnabled"], "AXI for agents must be selected and not removable"
assert "AXI for agents" in axi["choiceTitle"], axi["choiceTitle"]
PY
pass "Installer preselects wasync and AXI for agents, both locked"

if [[ -n "$signed_axi" ]]; then
  cmp "$signed_axi" "$work/expanded/wasync-axi.pkg/Payload/usr/local/bin/wasync-axi" || fail "installer wasync-axi differs from the standalone signed one"
  pass "installer wasync-axi is byte-identical to the standalone one"
fi

if [[ "${GITHUB_ACTIONS:-}" == true && "${RUNNER_ENVIRONMENT:-}" == github-hosted ]]; then
  if [[ -e /usr/local/bin/wasync || -e /usr/local/bin/wasync-axi || -e /usr/local/bin/whatsapp-axi ]]; then fail "refusing to install over an existing wasync"; fi
  installed=1
  run_installer
  [[ "$(/usr/local/bin/wasync-axi --version)" == "$version" ]] || fail "installed wasync-axi is not version $version"
  [[ "$(readlink /usr/local/bin/whatsapp-axi)" == wasync-axi ]] || fail "the old name whatsapp-axi is not a link to wasync-axi"
  /usr/local/bin/wasync version | grep -qF "wasync $version " || fail "installed wasync is not version $version"
  python3 "$project_dir/scripts/smoke-installed.py" /usr/local/bin/wasync "$version" "$architecture" /usr/local/bin/wasync-axi
  pass "default install provides both tools at $version, and they work together"

  other_axi="$work/other/wasync-axi"
  mkdir -p "$work/other"
  printf '#!/bin/sh\necho other-wasync-axi\n' > "$other_axi"
  chmod 755 "$other_axi"
  sudo rm -f /usr/local/bin/wasync-axi
  sudo ln -s "$other_axi" /usr/local/bin/wasync-axi
  run_installer
  [[ "$(readlink /usr/local/bin/wasync-axi)" == "$other_axi" ]] || fail "the install replaced another installation's wasync-axi symlink"
  [[ "$(/usr/local/bin/wasync-axi --version)" == other-wasync-axi ]] || fail "the install wrote through another installation's wasync-axi symlink"
  [[ ! -e /usr/local/bin/.wasync-axi.kept && ! -L /usr/local/bin/.wasync-axi.kept ]] || fail "the install left the set-aside wasync-axi link behind"
  /usr/local/bin/wasync version | grep -qF "wasync $version " || fail "wasync is not installed when another installation owns wasync-axi"
  pass "install over another installation's wasync-axi symlink keeps it and still installs wasync"

  # An install that stopped after preinstall set the link aside leaves it at
  # .wasync-axi.kept with nothing at wasync-axi; the next install restores it.
  sudo mv /usr/local/bin/wasync-axi /usr/local/bin/.wasync-axi.kept
  run_installer
  [[ "$(readlink /usr/local/bin/wasync-axi)" == "$other_axi" ]] || fail "an install after an interrupted one lost another installation's wasync-axi symlink"
  [[ ! -e /usr/local/bin/.wasync-axi.kept && ! -L /usr/local/bin/.wasync-axi.kept ]] || fail "the install left the set-aside wasync-axi link behind"
  pass "an install after an interrupted one puts another installation's wasync-axi symlink back"
else
  echo "test-macos-pkg: skipped the real install (only run on disposable GitHub-hosted runners)"
fi
