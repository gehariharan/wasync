// Package bench compares what an agent reads through wasync-axi with
// what it reads from raw `wasync query --v1 --json` for typical tasks, on a
// synthetic corpus served by the fake wasync of internal/testutil. It holds
// no real data. See docs/benchmark.md for the method and the results; run
//
//	go test ./internal/bench -run TestBenchmarkReport -v
//
// to print the results table, and `go test ./internal/bench -bench .` for
// timings.
package bench
