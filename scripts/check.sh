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
# Stamp both tools with one version, as a release does, so the smoke check can
# require them to match.
version=0.0.0-check
go build -tags sqlite_fts5 -ldflags "-X github.com/gehariharan/wasync/internal/buildinfo.Version=$version" -o "$work/wasync" ./cmd/wasync
(cd axi && CGO_ENABLED=0 go build -ldflags "-X github.com/gehariharan/wasync/axi/internal/buildinfo.Version=$version" -o "$work/whatsapp-axi" ./cmd/whatsapp-axi)
python3 scripts/smoke-pair.py "$work/wasync" "$work/whatsapp-axi"
python3 scripts/smoke-installed.py "$work/wasync" "$version" "$(go env GOARCH)" "$work/whatsapp-axi"
bash scripts/test-install-linux.sh
