#!/usr/bin/env bash
# Signs wasync.app from the inside out: the wasync and wasync-axi commands in
# it, then the app itself (its main program, wasync-desktop, takes the app's
# bundle identifier). With an identity the signatures are Developer ID with the
# hardened runtime and a timestamp, ready for notarization; without one they are
# ad hoc, for builds that are not released.
#
#   scripts/sign-app-macos.sh PATH/wasync.app [DEVELOPER_ID_APPLICATION_IDENTITY]
#
# WASYNC_SIGNING_KEYCHAIN, when set, is the Keychain that holds the identity.
set -euo pipefail
app="${1:?Usage: scripts/sign-app-macos.sh PATH/wasync.app [IDENTITY]}"
identity="${2:-}"
[[ "$(basename "$app")" == wasync.app && -x "$app/Contents/MacOS/wasync-desktop" ]] || { echo "Not a wasync.app: $app" >&2; exit 1; }
args=(--force)
if [[ -n "$identity" ]]; then
  args+=(--options runtime --timestamp --sign "$identity")
  if [[ -n "${WASYNC_SIGNING_KEYCHAIN:-}" ]]; then args+=(--keychain "$WASYNC_SIGNING_KEYCHAIN"); fi
else
  args+=(--sign -)
fi
# The same identifiers as the standalone commands, so the Keychain trusts the
# app, its wasync and a wasync installed elsewhere for one approval credential.
codesign "${args[@]}" --identifier com.gehariharan.wasynctuicli "$app/Contents/MacOS/wasync"
codesign "${args[@]}" --identifier com.gehariharan.wasynctuicli.axi "$app/Contents/MacOS/wasync-axi"
codesign "${args[@]}" "$app"
codesign --verify --strict --deep --verbose=1 "$app"
