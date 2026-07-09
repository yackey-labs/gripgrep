package walk

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkWalkRipgrepTree walks the ripgrep checkout (a real,
// moderately-sized tree with nested .gitignore files) with default
// ignore-aware options — the mode cmd/gg's default search will actually
// use. Run with -benchmem: allocations here should track directories
// created (one *dirTask + one ignoreNode per directory) and essentially
// nothing per file, per PLAN.md's "near-zero allocs per entry visited"
// target.
func BenchmarkWalkRipgrepTree(b *testing.B) {
	dir := ripgrepCheckoutDir(b)

	b.ReportAllocs()
	b.ResetTimer()
	total := 0
	for i := 0; i < b.N; i++ {
		n := 0
		err := Walk([]string{dir}, Options{}, func(e *Entry) WalkState {
			n++
			return Continue
		})
		if err != nil {
			b.Fatal(err)
		}
		total += n
	}
	b.StopTimer()
	b.ReportMetric(float64(total)/b.Elapsed().Seconds(), "entries/sec")
}

// BenchmarkWalkRipgrepTreeNoIgnore isolates walker mechanics from ignore-
// stack cost (no glob compilation/matching at all).
func BenchmarkWalkRipgrepTreeNoIgnore(b *testing.B) {
	dir := ripgrepCheckoutDir(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := Walk([]string{dir}, Options{NoIgnore: true}, func(e *Entry) WalkState {
			return Continue
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func ripgrepCheckoutDir(b *testing.B) string {
	b.Helper()
	wd, err := filepath.Abs(".")
	if err != nil {
		b.Fatal(err)
	}
	// walk/ -> repo root -> sibling ripgrep checkout, mirroring
	// oracle_test.go's layout assumption.
	dir := filepath.Join(filepath.Dir(filepath.Dir(wd)), "ripgrep")
	if _, err := os.Stat(dir); err != nil {
		b.Skipf("%s not present, skipping: %v", dir, err)
	}
	return dir
}
