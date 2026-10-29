#!/usr/bin/env bash
# Checks the macOS installer's two components and their choices.
#
#   scripts/test-macos-pkg.sh PKG VERSION ARCHITECTURE [SIGNED_AXI_BINARY]
#
# Always (no administrator access, installs nothing): the installer holds wasync
# and whatsapp-axi as separate components; wasync is selected and locked, and AXI
# for agents is offered but not selected. With SIGNED_AXI_BINARY, the installer's
# whatsapp-axi must be byte-identical to it.
#
# On a disposable GitHub-hosted runner only, it also installs the package twice
# with `installer`: once with the default choices (wasync only) and once with AXI
# for agents ticked (both at VERSION), and removes everything it installed.
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
    sudo rm -f /usr/local/bin/wasync /usr/local/bin/whatsapp-axi
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
[[ -x "$work/expanded/whatsapp-axi.pkg/Payload/usr/local/bin/whatsapp-axi" ]] || fail "whatsapp-axi is not in its own component"
[[ ! -e "$work/expanded/wasync.pkg/Payload/usr/local/bin/whatsapp-axi" ]] || fail "whatsapp-axi leaked into the always-installed component"
grep -q 'customize="always"' "$work/expanded/Distribution" || fail "the installer does not show the component list"
pass "wasync and whatsapp-axi are separate components"

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
assert axi["choiceIsSelected"] == 0 and axi["choiceIsEnabled"], "AXI for agents must be offered but not selected"
assert "AXI for agents" in axi["choiceTitle"], axi["choiceTitle"]
PY
pass "Installer preselects wasync (locked) and leaves AXI for agents unticked"

if [[ -n "$signed_axi" ]]; then
  cmp "$signed_axi" "$work/expanded/whatsapp-axi.pkg/Payload/usr/local/bin/whatsapp-axi" || fail "installer whatsapp-axi differs from the standalone signed one"
  pass "installer whatsapp-axi is byte-identical to the standalone one"
fi

if [[ "${GITHUB_ACTIONS:-}" == true && "${RUNNER_ENVIRONMENT:-}" == github-hosted ]]; then
  if [[ -e /usr/local/bin/wasync || -e /usr/local/bin/whatsapp-axi ]]; then fail "refusing to install over an existing wasync"; fi
  installed=1
  run_installer
  [[ -x /usr/local/bin/wasync ]] || fail "default install did not provide wasync"
  [[ ! -e /usr/local/bin/whatsapp-axi ]] || fail "default install added whatsapp-axi"
  pass "default install provides wasync only"
  cat > "$work/choose-axi.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><array><dict>
<key>attributeSetting</key><integer>1</integer>
<key>choiceAttribute</key><string>selected</string>
<key>choiceIdentifier</key><string>axi</string>
</dict></array></plist>
PLIST
  run_installer -applyChoiceChangesXML "$work/choose-axi.plist"
  [[ "$(/usr/local/bin/whatsapp-axi --version)" == "$version" ]] || fail "installed whatsapp-axi is not version $version"
  /usr/local/bin/wasync version | grep -qF "wasync $version " || fail "installed wasync is not version $version"
  python3 "$project_dir/scripts/smoke-installed.py" /usr/local/bin/wasync "$version" "$architecture" /usr/local/bin/whatsapp-axi
  pass "choosing AXI for agents installs both at $version, and they work together"
else
  echo "test-macos-pkg: skipped the real install (only run on disposable GitHub-hosted runners)"
fi
