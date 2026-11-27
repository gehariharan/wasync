#!/usr/bin/env bash
# Signatures must already exist. Credentials stay in the signing Mac's Keychain.
#
#   scripts/notarize.sh KEYCHAIN_PROFILE SIGNED_ITEM [SIGNED_ITEM...]
#
# Each item is notarized by its own submission, according to what it is:
#   a command-line program  - submitted as a ZIP (it cannot hold a ticket)
#   an .app bundle          - submitted as a ZIP, then its ticket is stapled to it
#   a .pkg installer        - submitted, stapled and assessed by Gatekeeper
#   a .dmg disk image       - submitted, stapled and assessed by Gatekeeper
set -euo pipefail
if [[ $# -lt 2 ]]; then
  echo 'Usage: scripts/notarize.sh KEYCHAIN_PROFILE SIGNED_ITEM [SIGNED_ITEM...]' >&2
  exit 1
fi
profile="$1"
shift
notary_args=(--keychain-profile "$profile")
if [[ -n "${WASYNC_SIGNING_KEYCHAIN:-}" ]]; then
  notary_args+=(--keychain "$WASYNC_SIGNING_KEYCHAIN")
fi
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
developer_id='anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.13] exists'

submit() {
  local input="$1"
  local report="$2"
  xcrun notarytool submit "$input" "${notary_args[@]}" \
    --wait --timeout 20m --output-format json > "$report"
  python3 - "$report" <<'PY'
import json,sys
from pathlib import Path
report=json.loads(Path(sys.argv[1]).read_text())
if report.get('status') != 'Accepted':
    raise SystemExit('Apple notarization was not accepted. Submission: '+str(report.get('id','unknown')))
print('Apple notarization accepted:',report['id'])
PY
}

# Reject development and ad hoc signatures before contacting Apple's service.
n=0
for item in "$@"; do
  n=$((n + 1))
  case "$item" in
    *.pkg)
      pkgutil --check-signature "$item" > "$work/package-signature.txt"
      if ! /usr/bin/grep -q 'Developer ID Installer:' "$work/package-signature.txt"; then
        echo 'The package needs a Developer ID Installer signature.' >&2
        exit 1
      fi
      ;;
    *)
      codesign --verify --strict -R="$developer_id" "$item"
      ;;
  esac
done

for item in "$@"; do
  n=$((n + 1))
  case "$item" in
    *.pkg)
      submit "$item" "$work/$n.json"
      xcrun stapler staple "$item"
      xcrun stapler validate "$item"
      spctl --assess --type install --verbose=2 "$item"
      ;;
    *.dmg)
      submit "$item" "$work/$n.json"
      xcrun stapler staple "$item"
      xcrun stapler validate "$item"
      spctl --assess --type open --context context:primary-signature --verbose=2 "$item"
      ;;
    *.app)
      ditto -c -k --keepParent "$item" "$work/$n.zip"
      submit "$work/$n.zip" "$work/$n.json"
      xcrun stapler staple "$item"
      xcrun stapler validate "$item"
      spctl --assess --type execute --verbose=2 "$item"
      ;;
    *)
      # Mach-O executables and tarballs cannot hold stapled tickets. Submit a ZIP of
      # the exact signed executable so Apple's online lookup recognizes its code hash.
      ditto -c -k --keepParent "$item" "$work/$n.zip"
      submit "$work/$n.zip" "$work/$n.json"
      ;;
  esac
done
echo 'Notarization accepted. Generate release checksums after this step.'
