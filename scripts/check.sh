#!/usr/bin/env bash
# Local checks for the complete product. All runtime data is synthetic.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
go test -race -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
(cd axi && go run ./cmd/generate-skill --check && go test -race ./... && go vet ./...)
bash axi/scripts/test-homebrew-formula.sh
bash scripts/test-publish-tap.sh
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
go build -tags sqlite_fts5 -o "$work/wasync" ./cmd/wasync
(cd axi && CGO_ENABLED=0 go build -o "$work/whatsapp-axi" ./cmd/whatsapp-axi)
python3 scripts/smoke-pair.py "$work/wasync" "$work/whatsapp-axi"
python3 scripts/smoke-installed.py "$work/wasync" dev "$(go env GOARCH)"
