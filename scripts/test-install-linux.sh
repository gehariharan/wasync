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
[[ -x "$bin/wasync" ]] || fail "wasync was not installed by default"
[[ ! -e "$bin/wasync-axi" ]] || fail "wasync-axi was installed without being asked"
[[ ! -s "$log" ]] || fail "agent setup ran without consent"
pass "non-interactive run without flags installs wasync only (default no)"

run without --without-axi
[[ -x "$bin/wasync" && ! -e "$bin/wasync-axi" ]] || fail "--without-axi installed wasync-axi"
pass "--without-axi installs wasync only"

run with --with-axi
[[ -x "$bin/wasync" && -x "$bin/wasync-axi" ]] || fail "--with-axi did not install both"
[[ ! -s "$log" ]] || fail "--with-axi alone changed agent settings"
grep -q 'setup skill' "$work/with.out" || fail "output does not tell how to set up the skill later"
pass "--with-axi installs both and changes no agent settings"

[[ -L "$bin/whatsapp-axi" && "$(readlink "$bin/whatsapp-axi")" == wasync-axi ]] || fail "--with-axi did not link the old name whatsapp-axi"
pass "the old name whatsapp-axi links to wasync-axi"

home="$work/oldname-home"; mkdir -p "$home/bin"
printf 'old build of github.com/gehariharan/wasync/axi\n' > "$home/bin/whatsapp-axi"
HOME="$home" "$stage/install.sh" --bin-dir "$home/bin" --with-axi < /dev/null > /dev/null 2>&1
[[ -L "$home/bin/whatsapp-axi" ]] || fail "an old wasync whatsapp-axi was not replaced by the link"
home="$work/foreign-home"; mkdir -p "$home/bin"
printf '#!/bin/sh\necho another tool\n' > "$home/bin/whatsapp-axi"
HOME="$home" "$stage/install.sh" --bin-dir "$home/bin" --with-axi < /dev/null > "$work/foreign.out" 2>&1
[[ ! -L "$home/bin/whatsapp-axi" ]] && grep -q 'another tool' "$home/bin/whatsapp-axi" || fail "another program named whatsapp-axi was replaced"
grep -q 'Left .*whatsapp-axi alone' "$work/foreign.out" || fail "install.sh did not say it left another whatsapp-axi alone"
pass "an old wasync whatsapp-axi becomes the link; another program of that name is kept"

home="$work/brewlink-home"; mkdir -p "$home/bin" "$home/Cellar"
printf 'brew build of github.com/gehariharan/wasync/axi\n' > "$home/Cellar/whatsapp-axi"
ln -s ../Cellar/whatsapp-axi "$home/bin/whatsapp-axi"
HOME="$home" "$stage/install.sh" --bin-dir "$home/bin" --with-axi < /dev/null > "$work/brewlink.out" 2>&1
[[ "$(readlink "$home/bin/whatsapp-axi")" == ../Cellar/whatsapp-axi ]] || fail "a package manager's whatsapp-axi link was replaced"
grep -q 'Left .*whatsapp-axi alone' "$work/brewlink.out" || fail "install.sh did not say it left a foreign whatsapp-axi link alone"
HOME="$work/with-home" "$stage/install.sh" --bin-dir "$work/with-home/bin" --with-axi < /dev/null > /dev/null 2>&1
[[ "$(readlink "$work/with-home/bin/whatsapp-axi")" == wasync-axi ]] || fail "re-installing lost the whatsapp-axi link"
pass "a whatsapp-axi link owned by another installer is kept; our own link survives a re-install"

run consent --with-axi --axi-skill --axi-hooks
[[ "$(sort "$log" | tr '\n' ';')" == 'setup hooks;setup skill;' ]] || fail "unexpected setup calls: $(cat "$log")"
pass "--axi-skill and --axi-hooks run only those two setup commands"

run skillonly --axi-skill
[[ -x "$bin/wasync-axi" ]] || fail "--axi-skill should imply --with-axi"
[[ "$(cat "$log")" == 'setup skill' ]] || fail "unexpected setup calls: $(cat "$log")"
pass "--axi-skill implies --with-axi and runs nothing else"

home="$work/defaultdir-home"; mkdir -p "$home"
HOME="$home" "$stage/install.sh" --with-axi < /dev/null > /dev/null 2>&1
[[ -x "$home/.local/bin/wasync" && -x "$home/.local/bin/wasync-axi" ]] || fail "default directory is not ~/.local/bin"
pass "default install directory is ~/.local/bin"

if run conflict --without-axi --axi-hooks 2> /dev/null; then fail "--without-axi with --axi-hooks should be refused"; fi
if run unknown --nonsense 2> /dev/null; then fail "an unknown option should be refused"; fi
pass "contradictory and unknown options are refused"

# Interactive prompts need a real terminal; drive them through a pseudo-terminal.
# ANSWERS is a comma-separated list, one per question asked; "-" means just Enter.
drive() {
  python3 - "$stage/install.sh" "$1" "$2" "$3" <<'PY'
import os, pty, select, subprocess, sys
script, home, log, answers = sys.argv[1:]
master, slave = pty.openpty()
proc = subprocess.Popen([script, "--bin-dir", home + "/bin"], stdin=slave, stdout=slave, stderr=slave,
                        env=dict(os.environ, HOME=home, STUB_LOG=log), close_fds=True)
os.close(slave)
pending = answers.split(",")
seen = b""
while True:
    ready, _, _ = select.select([master], [], [], 15)
    if not ready:
        proc.kill()
        sys.exit("timed out waiting for the installer; saw: " + seen.decode(errors="replace"))
    try:
        chunk = os.read(master, 4096)
    except OSError:
        break
    if not chunk:
        break
    seen += chunk
    if seen.rstrip().endswith(b"[y/N]"):
        if not pending:
            proc.kill()
            sys.exit("asked more questions than expected: " + seen.decode(errors="replace"))
        answer = pending.pop(0)
        os.write(master, (b"" if answer == "-" else answer.encode()) + b"\n")
        seen = b""
code = proc.wait()
if pending:
    sys.exit("expected more questions; unanswered: %r" % pending)
sys.exit(code)
PY
}
for case in "enter:-" "no:n" "yes-skill-no-hooks:y,y,n"; do
  name="${case%%:*}"; answers="${case#*:}"
  home="$work/tty-$name-home"; mkdir -p "$home"; log="$work/tty-$name.log"; : > "$log"
  drive "$home" "$log" "$answers" || fail "interactive run '$name' failed"
  case "$name" in
    enter|no)
      [[ -x "$home/bin/wasync" && ! -e "$home/bin/wasync-axi" ]] || fail "$name: wasync-axi was installed after answering no"
      [[ ! -s "$log" ]] || fail "$name: setup ran after answering no" ;;
    yes-skill-no-hooks)
      [[ -x "$home/bin/wasync-axi" ]] || fail "$name: wasync-axi missing after yes"
      [[ "$(cat "$log")" == 'setup skill' ]] || fail "$name: unexpected setup calls: $(cat "$log")" ;;
  esac
done
pass "interactive prompts: Enter and n mean no; each agent change is asked separately"

if [[ $# -ge 2 ]]; then
  archive="$1"; version="$2"
  real="$work/real"; mkdir -p "$real"
  tar -xzf "$archive" -C "$real"
  for file in wasync wasync-axi install.sh README.md LICENSE THIRD_PARTY_LICENSES; do
    [[ -e "$real/$file" ]] || fail "archive is missing $file"
  done
  home="$work/real-home"; mkdir -p "$home"
  HOME="$home" "$real/install.sh" --bin-dir "$home/bin" < /dev/null > /dev/null
  [[ ! -e "$home/bin/wasync-axi" ]] || fail "real archive installed wasync-axi without being asked"
  HOME="$home" "$real/install.sh" --bin-dir "$home/bin" --with-axi < /dev/null > /dev/null
  [[ "$("$home/bin/wasync-axi" --version)" == "$version" ]] || fail "installed wasync-axi is not version $version"
  [[ "$("$home/bin/whatsapp-axi" --version 2>/dev/null)" == "$version" ]] || fail "the old name whatsapp-axi does not run wasync-axi $version"
  "$home/bin/wasync" version | grep -qF "wasync $version " || fail "installed wasync is not version $version"
  [[ -z "$(find "$home" -mindepth 1 -maxdepth 1 ! -name bin)" ]] || fail "installing wrote outside the bin directory"
  pass "real archive $version: opt-in installs both tools at one version and writes nothing else"
fi
echo "test-install-linux: all checks passed"
