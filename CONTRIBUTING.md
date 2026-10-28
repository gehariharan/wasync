# Contributing to wasync

Develop both Go modules through the root `go.work` workspace. Use Go 1.27.2
and a C compiler; `mise exec go@1.27.2 --` can run the commands below without
changing your global Go installation.

Run `bash scripts/check.sh` for both modules' tests, vet checks, packaging helper
checks, a real API/offline integration check using a disposable synthetic
archive, and a terminal UI smoke check against the same kind of archive. It never reads a user's WhatsApp data or connects to WhatsApp.

Follow the [process and authorization boundary](docs/architecture/ADR-001-repository-boundaries.md)
and [AXI contribution guide](axi/CONTRIBUTING.md) when changing either module.

For release procedures and Homebrew publication safeguards, see
[releasing](docs/releasing.md). The [consolidation decision](docs/architecture/ADR-002-monorepo.md)
owns repository and release ownership.
