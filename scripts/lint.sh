#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
formatted="$(gofmt -l cmd internal axi/cmd axi/internal)"
if [[ -n "$formatted" ]]; then
  printf 'Run gofmt on:\n%s\n' "$formatted" >&2
  exit 1
fi
go vet -tags sqlite_fts5 ./...
(cd axi && go vet ./...)
bash -n scripts/*.sh axi/scripts/*.sh
for script in packaging/linux/install.sh packaging/macos/scripts/axi-preinstall; do sh -n "$script"; done
python3 -m compileall -q scripts axi/scripts
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= -pyflakes=
