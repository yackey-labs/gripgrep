package printer

import (
	"sort"
	"strings"
	"testing"
)

// TestPathPrinter_WritesAllPaths mirrors `rg --files`: every path sent
// on Paths() is written as "path\n", with no matcher/searcher
// involvement at all. Order is not asserted (matches the walk's
// inherent nondeterminism), only the resulting set of lines.
func TestPathPrinter_WritesAllPaths(t *testing.T) {
	dest, out := newTestDest()
	pp := NewPathPrinter(dest, false)

	want := []string{"a.txt", "b/c.txt", "d/e/f.txt"}
	for _, p := range want {
		pp.Paths() <- p
	}
	close(pp.Paths())
	pp.Wait()

	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("got %d lines %v, want %d lines %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestPathPrinter_EmptyProducesNoOutput verifies closing Paths with
// nothing sent writes nothing at all.
func TestPathPrinter_EmptyProducesNoOutput(t *testing.T) {
	dest, out := newTestDest()
	pp := NewPathPrinter(dest, false)
	close(pp.Paths())
	pp.Wait()

	if out.Len() != 0 {
		t.Errorf("expected no output, got %q", out.String())
	}
}

// TestPathPrinter_Color verifies --files --color=always's path coloring
// (verified against the real rg binary: `rg --files --color=always` does
// colorize its output, wrapping each path reset-magenta-text-reset, the
// same ansiPath scheme Count/FilesWithMatches use for their own paths).
func TestPathPrinter_Color(t *testing.T) {
	dest, out := newTestDest()
	pp := NewPathPrinter(dest, true)

	pp.Paths() <- "a.txt"
	close(pp.Paths())
	pp.Wait()

	want := "\x1b[0m\x1b[35ma.txt\x1b[0m\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestPathPrinter_ManyPathsFlushInBatches exercises the flush-on-batch-
// size path (more than pathBatchSize paths sent) to make sure batching
// doesn't drop or duplicate anything.
func TestPathPrinter_ManyPathsFlushInBatches(t *testing.T) {
	dest, out := newTestDest()
	pp := NewPathPrinter(dest, false)

	n := pathBatchSize*3 + 7
	for i := 0; i < n; i++ {
		pp.Paths() <- "p"
	}
	close(pp.Paths())
	pp.Wait()

	got := strings.Count(out.String(), "p\n")
	if got != n {
		t.Errorf("got %d lines, want %d", got, n)
	}
}
