package main

import (
	"fmt"
	"os"
)

// TODO(M2): full flag parsing (regex/-F/-i/-S/-w/-e, filtering flags,
// output flags, perf flags) per PLAN.md's "v1 CLI scope", wiring
// walk -> match -> search -> printer. Runtime tuning (GOMEMLIMIT,
// debug.SetGCPercent(400)) belongs here, not in the library packages.
//
// Exit code 2 signals "not yet implemented" so internal/bench/bench.sh
// can distinguish this from a real search failure while M1 is in flight.
func main() {
	fmt.Fprintln(os.Stderr, "gg: not yet implemented (M0 scaffold only)")
	os.Exit(2)
}
