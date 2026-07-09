// Package bench documents this directory's role: it holds the
// benchmarking harness scripts (setup.sh, bench.sh), not Go
// benchmark code — per-package Go benchmarks (go test -bench) live
// alongside the packages they measure. See setup.sh to provision the
// corpus in /dev/shm and bench.sh for the hyperfine-driven dev loop, per
// docs/research/benchmarking.md §4.
package bench
