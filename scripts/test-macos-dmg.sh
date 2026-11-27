#!/usr/bin/env bash
# Checks the wasync app's disk image.
#
#   scripts/test-macos-dmg.sh DMG VERSION ARCHITECTURE [SIGNED_AXI_BINARY]
#
# Always: the image opens read-only and shows wasync.app beside a link to
# Applications on the background picture; the app carries wasync-desktop,
# wasync and wasync-axi at VERSION for this CPU, with a valid signature. With
# SIGNED_AXI_BINARY, the app's wasync-axi must be byte-identical to it.
#
# On a disposable GitHub-hosted runner, or with WASYNC_TEST_APP_LAUNCH=1, it
# also starts the app from the image as it starts at login (no window), with
# a new data directory and a free port: the app must start wasync, keep its
# pages closed to visitors, refuse a second copy, and stop wasync when it quits.
# It never links a phone.
set -euo pipefail
image="${1:?Usage: scripts/test-macos-dmg.sh DMG VERSION ARCHITECTURE [SIGNED_AXI_BINARY]}"
version="${2:?}"
architecture="${3:?}"
signed_axi="${4:-}"
case "$architecture" in arm64) cpu=arm64;; amd64) cpu=x86_64;; *) echo "unknown architecture $architecture" >&2; exit 1;; esac
work="$(mktemp -d)"
mount="$work/mount"
desktop_pid=""
data=""
cleanup() {
  if [[ -n "$desktop_pid" ]] && kill -0 "$desktop_pid" 2>/dev/null; then kill -9 "$desktop_pid" 2>/dev/null || true; fi
  if [[ -n "$data" ]]; then
    # The approval credential wasync made for this test's data directory.
    account="$(python3 -c 'import hashlib,os,sys; print("approve:"+hashlib.sha256(os.path.abspath(sys.argv[1]).encode()).hexdigest()[:16])' "$data")"
    security delete-generic-password -s wasync -a "$account" >/dev/null 2>&1 || true
  fi
  if [[ -d "$mount" ]]; then hdiutil detach "$mount" -quiet -force >/dev/null 2>&1 || true; fi
  rm -rf "$work"
}
trap cleanup EXIT
fail() { echo "test-macos-dmg: FAIL: $*" >&2; exit 1; }
pass() { echo "test-macos-dmg: ok - $*"; }

mkdir "$mount"
hdiutil attach "$image" -readonly -nobrowse -noautoopen -mountpoint "$mount" -quiet
app="$mount/wasync.app"
[[ -d "$app" ]] || fail "wasync.app is not in the disk image"
[[ "$(readlink "$mount/Applications")" == /Applications ]] || fail "the disk image has no link to Applications"
[[ -f "$mount/.background.tiff" && -f "$mount/.DS_Store" ]] || fail "the disk image window has no background picture or layout"
pass "the disk image holds wasync.app, a link to Applications and its window layout"

plist="$app/Contents/Info.plist"
[[ "$(plutil -extract CFBundleIdentifier raw "$plist")" == com.gehariharan.wasynctuicli ]] || fail "unexpected bundle identifier"
[[ "$(plutil -extract CFBundleExecutable raw "$plist")" == wasync-desktop ]] || fail "unexpected main program"
[[ "$(plutil -extract CFBundleShortVersionString raw "$plist")" == "$version" ]] || fail "the app is not version $version"
for program in wasync-desktop wasync wasync-axi; do
  [[ -x "$app/Contents/MacOS/$program" ]] || fail "the app is missing $program"
  [[ "$(lipo -archs "$app/Contents/MacOS/$program")" == "$cpu" ]] || fail "$program is not built for $cpu"
done
"$app/Contents/MacOS/wasync-desktop" --version | grep -qF "wasync $version " || fail "wasync-desktop is not version $version"
"$app/Contents/MacOS/wasync" version | grep -qF "wasync $version " || fail "the app's wasync is not version $version"
[[ "$("$app/Contents/MacOS/wasync-axi" --version)" == "$version" ]] || fail "the app's wasync-axi is not version $version"
codesign --verify --strict --deep "$app" || fail "the app's signature is not valid"
[[ -f "$app/Contents/Resources/wasync.icns" ]] || fail "the app has no icon"
pass "wasync.app carries wasync-desktop, wasync and wasync-axi at $version for $cpu, validly signed"

if [[ -n "$signed_axi" ]]; then
  cmp "$signed_axi" "$app/Contents/MacOS/wasync-axi" || fail "the app's wasync-axi differs from the standalone signed one"
  pass "the app's wasync-axi is byte-identical to the standalone one"
fi

if [[ ( "${GITHUB_ACTIONS:-}" == true && "${RUNNER_ENVIRONMENT:-}" == github-hosted ) || "${WASYNC_TEST_APP_LAUNCH:-}" == 1 ]]; then
  data="$work/data"
  port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  base="http://127.0.0.1:$port"
  "$app/Contents/MacOS/wasync-desktop" --background --data-dir "$data" --api-addr "127.0.0.1:$port" &
  desktop_pid=$!
  up=0
  for _ in $(seq 120); do
    if curl -fsS "$base/health" >/dev/null 2>&1; then up=1; break; fi
    kill -0 "$desktop_pid" 2>/dev/null || break
    sleep 0.5
  done
  [[ "$up" == 1 ]] || { cat "$data/desktop.log" >&2 2>/dev/null || true; fail "the app did not start wasync"; }
  for page in /ui/chats /ui/setup /ui/desktop; do
    code="$(curl -s -o /dev/null -w '%{http_code}' "$base$page")"
    [[ "$code" == 401 ]] || fail "$page answered $code without a sign-in link; expected 401"
  done
  # Read the whole status before matching: grep -q leaves at the first line
  # that matches, and status, still writing its later lines, would then die
  # of a broken pipe and fail the check under pipefail.
  status="$("$app/Contents/MacOS/wasync" --data-dir "$data" status)" || fail "wasync status failed"
  grep -qF 'started by the wasync app' <<<"$status" || { echo "$status" >&2; fail "wasync does not say the app started it"; }
  pass "the app started wasync for its data directory, with its pages closed to visitors"

  # A second copy hands over to the first and leaves.
  "$app/Contents/MacOS/wasync-desktop" --background --data-dir "$data" --api-addr "127.0.0.1:$port" &
  second=$!
  for _ in $(seq 40); do kill -0 "$second" 2>/dev/null || break; sleep 0.25; done
  if kill -0 "$second" 2>/dev/null; then kill -9 "$second"; fail "a second copy of the app kept running"; fi
  pass "a second copy of the app leaves the first one running"

  kill -TERM "$desktop_pid"
  for _ in $(seq 120); do kill -0 "$desktop_pid" 2>/dev/null || break; sleep 0.25; done
  if kill -0 "$desktop_pid" 2>/dev/null; then fail "the app did not quit"; fi
  desktop_pid=""
  for _ in $(seq 40); do curl -fsS "$base/health" >/dev/null 2>&1 || break; sleep 0.25; done
  if curl -fsS "$base/health" >/dev/null 2>&1; then fail "wasync kept running after the app quit"; fi
  grep -q 'wasync app quit' "$data/desktop.log" || fail "the app's log does not record a clean quit"
  pass "quitting the app stops the wasync it started"
else
  echo "test-macos-dmg: skipped starting the app (only on disposable GitHub-hosted runners, or with WASYNC_TEST_APP_LAUNCH=1)"
fi
