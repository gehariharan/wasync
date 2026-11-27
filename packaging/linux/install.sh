#!/bin/sh
# Installs wasync from this extracted archive, together with wasync-axi (AXI for
# agents) when the archive has it. Needs no administrator access.
#
#   ./install.sh [--bin-dir DIR] [--axi-skill] [--axi-hooks]
#
# wasync-axi is always installed (--with-axi is accepted and changes nothing)
# and stays inactive until you connect an agent from wasync's Connect an agent
# step. Without --axi-skill or --axi-hooks it asks nothing and changes no agent
# settings; with them it runs `wasync-axi setup skill` / `setup hooks` after
# installing, which write to your agents' configuration.
set -eu

bin_dir="${HOME:-}/.local/bin"
skill=no
hooks=no

usage() {
  awk 'NR > 1 && !/^#/ { exit } NR > 1 { sub(/^# ?/, ""); print }' "$0"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --bin-dir)
      [ $# -ge 2 ] || { echo "install.sh: --bin-dir needs a directory" >&2; exit 2; }
      bin_dir="$2"; shift ;;
    --with-axi) ;;
    --without-axi)
      echo "install.sh: --without-axi is no longer supported: wasync-axi is always installed and stays inactive until you connect an agent from wasync" >&2
      exit 2 ;;
    --axi-skill) skill=yes ;;
    --axi-hooks) hooks=yes ;;
    -h|--help) usage; exit 0 ;;
    *) echo "install.sh: unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done
if [ -z "$bin_dir" ] || [ "$bin_dir" = "/.local/bin" ]; then
  echo "install.sh: cannot determine where to install; pass --bin-dir DIR" >&2
  exit 2
fi

here="$(cd "$(dirname "$0")" && pwd)"
[ -f "$here/wasync" ] || { echo "install.sh: wasync is not next to this script" >&2; exit 1; }
if { [ "$skill" = yes ] || [ "$hooks" = yes ]; } && [ ! -f "$here/wasync-axi" ]; then
  echo "install.sh: wasync-axi is not in this archive" >&2
  exit 1
fi

mkdir -p "$bin_dir"
install -m 755 "$here/wasync" "$bin_dir/wasync"
echo "Installed wasync to $bin_dir/wasync"
if [ -f "$here/wasync-axi" ]; then
  install -m 755 "$here/wasync-axi" "$bin_dir/wasync-axi"
  echo "Installed wasync-axi to $bin_dir/wasync-axi"
  # Keep the old name, whatsapp-axi, as a link unless another program owns it.
  old="$bin_dir/whatsapp-axi"
  if [ -L "$old" ]; then
    [ "$(readlink "$old")" = wasync-axi ] && own=yes || own=no
  elif [ ! -e "$old" ] || LC_ALL=C grep -aq 'gehariharan/wasync/axi' "$old" 2>/dev/null; then
    own=yes
  else
    own=no
  fi
  if [ "$own" = yes ]; then
    ln -sf wasync-axi "$old"
  else
    echo "Left $old alone: it is another program."
  fi
  if [ "$skill" = yes ]; then "$bin_dir/wasync-axi" setup skill; fi
  if [ "$hooks" = yes ]; then "$bin_dir/wasync-axi" setup hooks; fi
  if [ "$skill" != yes ] && [ "$hooks" != yes ]; then
    echo "No agent settings were changed. To let an AI agent use wasync, use wasync's Connect an agent step."
  fi
fi

case ":${PATH:-}:" in
  *":$bin_dir:"*) ;;
  *) echo "Add $bin_dir to your PATH, for example: export PATH=\"$bin_dir:\$PATH\"" ;;
esac
"$bin_dir/wasync" version
