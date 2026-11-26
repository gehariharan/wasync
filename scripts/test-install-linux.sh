#!/usr/bin/env bash
# Tests packaging/linux/install.sh, the script shipped in the Linux archive.
#
#   scripts/test-install-linux.sh                    stub programs, any platform
#   scripts/test-install-linux.sh ARCHIVE VERSION    also the real archive's binaries
#
# Everything runs under a temporary HOME, so no real agent settings are touched.
set -euo pipefail
project_dir="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fail() { echo "test-install-linux: FAIL: $*" >&2; exit 1; }
pass() { echo "test-install-linux: ok - $*"; }

# A fake extracted archive: stub programs that log any `setup` request.
stage="$work/archive"
mkdir -p "$stage"
cp "$project_dir/packaging/linux/install.sh" "$stage/install.sh"
cat > "$stage/wasync" <<'STUB'
#!/bin/sh
echo "wasync 0.0.0-stub linux/amd64"
STUB
cat > "$stage/wasync-axi" <<'STUB'
#!/bin/sh
if [ "${1:-}" = setup ]; then echo "$*" >> "$STUB_LOG"; exit 0; fi
echo 0.0.0-stub
STUB
chmod 755 "$stage/install.sh" "$stage/wasync" "$stage/wasync-axi"

# run NAME ARGS...: fresh HOME and non-interactive stdin; sets $bin and $log.
run() {
  name="$1"; shift
  home="$work/$name-home"; mkdir -p "$home"
  bin="$home/bin"; log="$work/$name.log"; : > "$log"
  STUB_LOG="$log" HOME="$home" "$stage/install.sh" --bin-dir "$bin" "$@" < /dev/null > "$work/$name.out" 2>&1
}

run default
[[ -x "$bin/wasync" && -x "$bin/wasync-axi" ]] || fail "a run without flags did not install both tools"
[[ ! -s "$log" ]] || fail "installing changed agent settings"
grep -q 'Connect an agent' "$work/default.out" || fail "output does not say agents are connected later from wasync"
pass "a run without flags installs wasync and wasync-axi and changes no agent settings"

[[ -L "$bin/whatsapp-axi" && "$(readlink "$bin/whatsapp-axi")" == wasync-axi ]] || fail "install.sh did not link the old name whatsapp-axi"
pass "the old name whatsapp-axi links to wasync-axi"

home="$work/oldname-home"; mkdir -p "$home/bin"
printf 'old build of github.com/gehariharan/wasync/axi\n' > "$home/bin/whatsapp-axi"
HOME="$home" "$stage/install.sh" --bin-dir "$home/bin" < /dev/null > /dev/null 2>&1
[[ -L "$home/bin/whatsapp-axi" ]] || fail "an old wasync whatsapp-axi was not replaced by the link"
home="$work/foreign-home"; mkdir -p "$home/bin"
printf '#!/bin/sh\necho another tool\n' > "$home/bin/whatsapp-axi"
HOME="$home" "$stage/install.sh" --bin-dir "$home/bin" < /dev/null > "$work/foreign.out" 2>&1
[[ ! -L "$home/bin/whatsapp-axi" ]] && grep -q 'another tool' "$home/bin/whatsapp-axi" || fail "another program named whatsapp-axi was replaced"
grep -q 'Left .*whatsapp-axi alone' "$work/foreign.out" || fail "install.sh did not say it left another whatsapp-axi alone"
pass "an old wasync whatsapp-axi becomes the link; another program of that name is kept"

home="$work/brewlink-home"; mkdir -p "$home/bin" "$home/Cellar"
printf 'brew build of github.com/gehariharan/wasync/axi\n' > "$home/Cellar/whatsapp-axi"
ln -s ../Cellar/whatsapp-axi "$home/bin/whatsapp-axi"
HOME="$home" "$stage/install.sh" --bin-dir "$home/bin" < /dev/null > "$work/brewlink.out" 2>&1
[[ "$(readlink "$home/bin/whatsapp-axi")" == ../Cellar/whatsapp-axi ]] || fail "a package manager's whatsapp-axi link was replaced"
grep -q 'Left .*whatsapp-axi alone' "$work/brewlink.out" || fail "install.sh did not say it left a foreign whatsapp-axi link alone"
HOME="$work/default-home" "$stage/install.sh" --bin-dir "$work/default-home/bin" < /dev/null > /dev/null 2>&1
[[ "$(readlink "$work/default-home/bin/whatsapp-axi")" == wasync-axi ]] || fail "re-installing lost the whatsapp-axi link"
pass "a whatsapp-axi link owned by another installer is kept; our own link survives a re-install"

# An archive without wasync-axi still installs wasync.
noaxi="$work/noaxi-archive"; mkdir -p "$noaxi"
cp "$stage/install.sh" "$stage/wasync" "$noaxi/"
home="$work/noaxi-home"; mkdir -p "$home"
HOME="$home" "$noaxi/install.sh" --bin-dir "$home/bin" < /dev/null > /dev/null 2>&1 || fail "an archive without wasync-axi did not install"
[[ -x "$home/bin/wasync" && ! -e "$home/bin/wasync-axi" && ! -e "$home/bin/whatsapp-axi" ]] || fail "an archive without wasync-axi installed the wrong files"
pass "an archive without wasync-axi installs wasync alone"

home="$work/defaultdir-home"; mkdir -p "$home"
HOME="$home" "$stage/install.sh" < /dev/null > /dev/null 2>&1
[[ -x "$home/.local/bin/wasync" && -x "$home/.local/bin/wasync-axi" ]] || fail "default directory is not ~/.local/bin"
pass "default install directory is ~/.local/bin"

# The removed AXI options are refused, so no old flag can wire wasync-axi into an agent.
for option in --nonsense --with-axi --without-axi --axi-skill --axi-hooks; do
  if run "refused$option" "$option" 2> /dev/null; then fail "option $option should be refused"; fi
  [[ ! -s "$log" ]] || fail "option $option changed agent settings"
done
pass "unknown and removed options are refused"

# In a real terminal the script must not ask anything or change agent settings.
python3 - "$stage/install.sh" "$work/tty-home" "$work/tty.log" <<'PY2' || fail "interactive run failed"
import os, pty, select, subprocess, sys
script, home, log = sys.argv[1:]
os.makedirs(home, exist_ok=True)
open(log, "w").close()
master, slave = pty.openpty()
proc = subprocess.Popen([script, "--bin-dir", home + "/bin"], stdin=slave, stdout=slave, stderr=slave,
                        env=dict(os.environ, HOME=home, STUB_LOG=log), close_fds=True)
os.close(slave)
seen = b""
while True:
    ready, _, _ = select.select([master], [], [], 15)
    if not ready:
        proc.kill()
        sys.exit("timed out; the installer may be waiting for an answer; saw: " + seen.decode(errors="replace"))
    try:
        chunk = os.read(master, 4096)
    except OSError:
        break
    if not chunk:
        break
    seen += chunk
code = proc.wait()
if b"[y/N]" in seen:
    sys.exit("the installer asked a question: " + seen.decode(errors="replace"))
sys.exit(code)
PY2
[[ -x "$work/tty-home/bin/wasync" && -x "$work/tty-home/bin/wasync-axi" ]] || fail "interactive run did not install both tools"
[[ ! -s "$work/tty.log" ]] || fail "interactive run changed agent settings"
pass "in a terminal it asks nothing, installs both tools and changes no agent settings"

if [[ $# -ge 2 ]]; then
  archive="$1"; version="$2"
  real="$work/real"; mkdir -p "$real"
  tar -xzf "$archive" -C "$real"
  for file in wasync wasync-axi install.sh README.md LICENSE THIRD_PARTY_LICENSES; do
    [[ -e "$real/$file" ]] || fail "archive is missing $file"
  done
  home="$work/real-home"; mkdir -p "$home"
  HOME="$home" "$real/install.sh" --bin-dir "$home/bin" < /dev/null > /dev/null
  [[ "$("$home/bin/wasync-axi" --version)" == "$version" ]] || fail "installed wasync-axi is not version $version"
  [[ "$("$home/bin/whatsapp-axi" --version 2>/dev/null)" == "$version" ]] || fail "the old name whatsapp-axi does not run wasync-axi $version"
  "$home/bin/wasync" version | grep -qF "wasync $version " || fail "installed wasync is not version $version"
  [[ -z "$(find "$home" -mindepth 1 -maxdepth 1 ! -name bin)" ]] || fail "installing wrote outside the bin directory"
  pass "real archive $version: installs both tools at one version and writes nothing else"
fi
echo "test-install-linux: all checks passed"
