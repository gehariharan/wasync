// Package buildinfo holds the wasync-axi version. It is a leaf package
// (no imports) so the --version fast path stays cheap. Release builds
// (scripts/package.sh) set all three values with -ldflags, for example
// "-X github.com/gehariharan/wasync/axi/internal/buildinfo.Version=...".
package buildinfo

// Version is the wasync-axi version.
var Version = "0.0.0-dev"

// Commit is the source commit of a release build ("unknown" otherwise).
var Commit = "unknown"

// Date is the release build's source date (the commit time, RFC 3339 UTC),
// so rebuilding the same commit gives the same binary ("unknown" otherwise).
var Date = "unknown"
