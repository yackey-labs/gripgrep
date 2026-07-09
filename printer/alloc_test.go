package printer

import (
	"testing"

	"github.com/yackey-labs/gripgrep/search"
)

// TestStandard_Matched_PipedZeroAllocs is the lead-mandated regular-test
// (not merely benchmark) allocation assertion for printer's hot path:
// PLAN.md's "Test coverage requirements" lists "printer Matched (piped
// path)" among the hot paths that must get a testing.AllocsPerRun == 0
// assertion in `go test`, so an alloc regression fails CI rather than
// only showing up if someone happens to eyeball a benchmark.
//
// As with the benchmark (see bench_test.go's linesPerCycle comment),
// the file-cycle length must stay well under the buffer pool's
// maxPooledCap or a periodic reallocation gets rounded away by
// AllocsPerRun's own averaging just like it would in go test -bench;
// this test warms the buffer up to steady-state capacity before
// measuring, and keeps every measured cycle the same small size.
func TestStandard_Matched_PipedZeroAllocs(t *testing.T) {
	dest := NewDest(discard{})
	p := NewStandard(dest)
	p.Begin("bench/file.txt")

	m := &search.Match{
		Line:          []byte("the quick brown fox jumps over the lazy dog"),
		LineNumber:    1,
		HasLineNumber: true,
	}

	cycle := func(n int) {
		for i := 0; i < n; i++ {
			m.LineNumber = int64(i%linesPerCycle + 1)
			if _, err := p.Matched(m); err != nil {
				t.Fatal(err)
			}
			if i%linesPerCycle == linesPerCycle-1 {
				p.Finish("bench/file.txt", benchStats)
				p.Begin("bench/file.txt")
			}
		}
	}

	// Warm up: let the buffer grow to its steady-state capacity and
	// flush several times before the measured region starts.
	cycle(linesPerCycle * 5)

	i := 0
	avg := testing.AllocsPerRun(1000, func() {
		m.LineNumber = int64(i%linesPerCycle + 1)
		if _, err := p.Matched(m); err != nil {
			t.Fatal(err)
		}
		i++
		if i%linesPerCycle == 0 {
			p.Finish("bench/file.txt", benchStats)
			p.Begin("bench/file.txt")
		}
	})
	if avg != 0 {
		t.Errorf("Standard.Matched (piped path): %v allocs/op on average, want 0", avg)
	}
}

// TestCount_Matched_ZeroAllocs covers -c's tally-only hot path the same
// way; it has no buffer-growth concerns at all (never formats a line),
// so no warm-up cycling is needed.
func TestCount_Matched_ZeroAllocs(t *testing.T) {
	dest := NewDest(discard{})
	p := NewCount(dest)
	p.Begin("bench/file.txt")

	m := &search.Match{Line: []byte("the quick brown fox jumps over the lazy dog")}

	avg := testing.AllocsPerRun(1000, func() {
		if _, err := p.Matched(m); err != nil {
			t.Fatal(err)
		}
	})
	if avg != 0 {
		t.Errorf("Count.Matched: %v allocs/op on average, want 0", avg)
	}
}
