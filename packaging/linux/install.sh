#!/bin/sh
# Installs wasync from this extracted archive. whatsapp-axi (AXI for agents) is
# optional: it is installed only when you say yes. Needs no administrator access.
#
#   ./install.sh [--bin-dir DIR] [--with-axi | --without-axi]
#                [--axi-skill] [--axi-hooks]
#
# Without --with-axi or --without-axi the script asks when run in a terminal and
# otherwise installs wasync only. --axi-skill and --axi-hooks run
# `whatsapp-axi setup skill` / `setup hooks` after installing, which write to your
# agents' configuration; they imply --with-axi. Without them, a terminal run asks
# about each (default no) and a non-interactive run changes no agent settings.
set -eu

bin_dir="${HOME:-}/.local/bin"
axi=ask
skill=ask
hooks=ask

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --bin-dir)
      [ $# -ge 2 ] || { echo "install.sh: --bin-dir needs a directory" >&2; exit 2; }
      bin_dir="$2"; shift ;;
    --with-axi) axi=yes ;;
    --without-axi) axi=no ;;
    --axi-skill) skill=yes; [ "$axi" = no ] || axi=yes ;;
    --axi-hooks) hooks=yes; [ "$axi" = no ] || axi=yes ;;
    -h|--help) usage; exit 0 ;;
    *) echo "install.sh: unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done
if [ "$axi" = no ] && { [ "$skill" = yes ] || [ "$hooks" = yes ]; }; then
  echo "install.sh: --axi-skill and --axi-hooks cannot be combined with --without-axi" >&2
  exit 2
fi
if [ -z "$bin_dir" ] || [ "$bin_dir" = "/.local/bin" ]; then
  echo "install.sh: cannot determine where to install; pass --bin-dir DIR" >&2
  exit 2
fi

here="$(cd "$(dirname "$0")" && pwd)"
[ -f "$here/wasync" ] || { echo "install.sh: wasync is not next to this script" >&2; exit 1; }

# Questions are asked only when a person can answer them.
interactive=no
if [ -t 0 ] && [ -t 1 ]; then interactive=yes; fi
ask() {
  # ask QUESTION: succeeds only for an explicit yes; anything else means no.
  [ "$interactive" = yes ] || return 1
  printf '%s [y/N] ' "$1"
  answer=""
  read -r answer || answer=""
  case "$answer" in y|Y|yes|YES|Yes) return 0 ;; *) return 1 ;; esac
}

if [ "$axi" = ask ]; then
  if [ -f "$here/whatsapp-axi" ] && ask "Also install whatsapp-axi (AXI for agents, used by AI agents such as Claude Code, Codex and OpenCode)?"; then
    axi=yes
  else
    axi=no
  fi
fi
if [ "$axi" = yes ] && [ ! -f "$here/whatsapp-axi" ]; then
  echo "install.sh: whatsapp-axi is not in this archive" >&2
  exit 1
fi

mkdir -p "$bin_dir"
install -m 755 "$here/wasync" "$bin_dir/wasync"
echo "Installed wasync to $bin_dir/wasync"
if [ "$axi" = yes ]; then
  install -m 755 "$here/whatsapp-axi" "$bin_dir/whatsapp-axi"
  echo "Installed whatsapp-axi to $bin_dir/whatsapp-axi"
else
  echo "Skipped whatsapp-axi. Re-run with --with-axi to add it later."
fi

if [ "$axi" = yes ]; then
  if [ "$skill" = ask ]; then
    if ask "Run 'whatsapp-axi setup skill' to add the whatsapp-axi skill to your agents?"; then skill=yes; else skill=no; fi
  fi
  if [ "$hooks" = ask ]; then
    if ask "Run 'whatsapp-axi setup hooks' to add whatsapp-axi's session hooks to your agents?"; then hooks=yes; else hooks=no; fi
  fi
  if [ "$skill" = yes ]; then "$bin_dir/whatsapp-axi" setup skill; fi
  if [ "$hooks" = yes ]; then "$bin_dir/whatsapp-axi" setup hooks; fi
  if [ "$skill" != yes ]; then echo "Agent skill not installed. Later: whatsapp-axi setup skill (add --dry-run to preview)."; fi
  if [ "$hooks" != yes ]; then echo "Agent session hooks not installed. Later: whatsapp-axi setup hooks (add --dry-run to preview)."; fi
fi

case ":${PATH:-}:" in
  *":$bin_dir:"*) ;;
  *) echo "Add $bin_dir to your PATH, for example: export PATH=\"$bin_dir:\$PATH\"" ;;
esac
"$bin_dir/wasync" version
