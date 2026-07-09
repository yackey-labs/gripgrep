//go:build race

package glob

// raceDetectorEnabled is true when built with -race. testing.AllocsPerRun
// counts genuine allocations, but the race detector's own instrumentation
// (shadow memory bookkeeping, disabled inlining/escape-analysis
// optimizations) adds allocations of its own that are not present in a
// normal build — see race_off.go and TestMatchZeroAllocs.
const raceDetectorEnabled = true
