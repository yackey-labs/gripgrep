package main

import (
	"fmt"
	"io"
	"os"
)

// usageLine is printed alongside any flag-parsing error, matching rg's
// convention of a short usage reminder on exit 2.
const usageLine = "Usage: gg [OPTIONS] PATTERN [PATH ...]"

// run contains all of main's logic, factored out so it can be exercised
// directly by an in-process test (see flags_test.go's TestRun*) in
// addition to the black-box subprocess tests that build and run the
// real binary. It writes diagnostics to stderr and returns the process
// exit code; main just wires it to os.Args/os.Stderr/os.Exit.
//
// TODO(M2): wire the parsed Config into walk -> match -> search ->
// printer per PLAN.md's "v1 CLI scope", and set runtime tuning
// (GOMEMLIMIT, debug.SetGCPercent(400)) here before first allocation
// (library packages leave GC alone).
//
// Flag parsing itself (the call to ParseArgs) is real as of task #13: a
// bad flag now genuinely exits 2 with the parser's error and a usage
// line, rather than exiting 2 by coincidence via the stub below. A
// successfully parsed Config still can't execute a real search yet --
// that wiring is M2's job -- so it falls through to the same "not yet
// implemented" exit 2 as before.
func run(args []string, stderr io.Writer) int {
	cfg, err := ParseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "gg: %s\n%s\n", err, usageLine)
		return 2
	}
	_ = cfg // TODO(M2): wire into walk/match/search/printer

	fmt.Fprintln(stderr, "gg: not yet implemented (M0 scaffold only)")
	return 2
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}
