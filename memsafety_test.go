package gripgrep

import (
	"sort"
	"strings"
	"testing"
)

// TestSearch_MatchesOutliveCallbacks is round #30's memory-safety test:
// the engine's Match/Ctx types are unsafe.String-backed views valid only
// during the delivering Sink callback (see internal/engine's doc and
// search.Match's own doc comment) -- this project has been burned before
// by a retention path that skipped the copy and silently corrupted map
// keys once a pooled buffer was reused. The facade's whole job is to
// copy every byte it hands back (see sink.go's trimLineTerminator and
// matchCollector's doc), so this test is designed to fail loudly if that
// copy is ever dropped, rather than passing by accident because no
// buffer happened to get reused before the assertions ran.
//
// "static" on benchmark-data/linux is a common C keyword: it produces
// well over 10k matches across thousands of files, spread across every
// parallel worker's pooled Searcher buffer many times over -- enough
// reuse pressure that a broken (view-retaining) implementation would
// almost certainly show corrupted Line/Path content by the time
// collection finishes, not just "probably".
func TestSearch_MatchesOutliveCallbacks(t *testing.T) {
	if testing.Short() {
		t.Skip("walks the full benchmark-data/linux tree; skipped in -short")
	}
	if _, err := os.Stat("benchmark-data/linux"); err != nil {
		// benchmark-data/ is gitignored and only exists on boxes
		// provisioned for benchmarking; CI runners don't have it.
		t.Skipf("corpus not present: %v", err)
	}

	parallel, err := Search("static", "benchmark-data/linux")
	if err != nil {
		t.Fatalf("parallel Search: %v", err)
	}
	if len(parallel) < 10000 {
		t.Fatalf("only %d matches -- test needs 10k+ to meaningfully exercise buffer reuse; pick a more common pattern", len(parallel))
	}

	// Oracle: the same search, but Workers: 1 forces every file through
	// one Searcher instance sequentially -- still pooled/reused buffers
	// (so it's not immune to the same bug class), but a second,
	// independently-run pass whose results must exactly match the
	// parallel run if neither corrupted anything.
	serial, err := Options{Workers: 1}.Search("static", "benchmark-data/linux")
	if err != nil {
		t.Fatalf("serial Search: %v", err)
	}
	if len(serial) != len(parallel) {
		t.Fatalf("match count mismatch: parallel=%d serial=%d", len(parallel), len(serial))
	}

	sortMatches(parallel)
	sortMatches(serial)
	for i := range parallel {
		p, s := parallel[i], serial[i]
		if p.Path != s.Path || p.LineNumber != s.LineNumber || p.Line != s.Line {
			t.Fatalf("match %d differs between parallel and serial runs (buffer-reuse corruption):\nparallel: %+v\nserial:   %+v", i, p, s)
		}
	}

	// Direct tripwire, independent of the cross-run comparison above: if
	// Line were an unsafe view into a reused Searcher buffer, a LATER
	// match's Line would silently overwrite an EARLIER one's backing
	// array by the time collection finishes -- every retained Match
	// must still contain the literal pattern it was matched for.
	for i, m := range parallel {
		if !strings.Contains(m.Line, "static") {
			t.Fatalf("match %d (%s:%d) no longer contains %q -- Line was not copied and got overwritten by buffer reuse: %q", i, m.Path, m.LineNumber, "static", m.Line)
		}
		if m.Path == "" {
			t.Fatalf("match %d has empty Path", i)
		}
	}
}

func sortMatches(m []Match) {
	sort.Slice(m, func(i, j int) bool {
		if m[i].Path != m[j].Path {
			return m[i].Path < m[j].Path
		}
		if m[i].LineNumber != m[j].LineNumber {
			return m[i].LineNumber < m[j].LineNumber
		}
		return m[i].Line < m[j].Line
	})
}
