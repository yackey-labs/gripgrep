package search

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

var (
	benchDataOnce sync.Once
	benchData     []byte
)

// getBenchData lazily builds a ~100MB, mostly-non-matching synthetic
// corpus (one rare "needle" line per 1000), matching the PLAN.md
// benchmarking target for fast vs. slow path throughput.
//
// Filler lines deliberately avoid 'n' (needle's leading byte): fakeMatcher
// is a bare bytes.Index, which itself falls back to scanning for the
// needle's first byte as a candidate and verifying each hit. A real
// Matcher (M1-match) picks literals via a rarity table specifically to
// avoid ever prefiltering on a common byte like 'n' in English prose; an
// English-prose filler corpus with a naive bytes.Index stand-in defeats
// that rarity selection and measures the fake matcher's pathology instead
// of the searcher's dispatch logic, which is what this benchmark is for.
func getBenchData() []byte {
	benchDataOnce.Do(func() {
		const target = 100 << 20
		const line = "1234567890 the quick brow fox jumps over the lazy dog abcdefghijklm\n"
		const needleLine = "1234567890 this line has the rare needle token buried in it here\n"
		var buf bytes.Buffer
		buf.Grow(target + len(needleLine))
		for i := 0; buf.Len() < target; i++ {
			if i%1000 == 999 {
				buf.WriteString(needleLine)
			} else {
				buf.WriteString(line)
			}
		}
		benchData = buf.Bytes()
	})
	return benchData
}

type discardSink struct{}

func (discardSink) Begin(string) (bool, error)   { return true, nil }
func (discardSink) Matched(*Match) (bool, error) { return true, nil }
func (discardSink) Context(*Ctx) (bool, error)   { return true, nil }
func (discardSink) Finish(string, *Stats) error  { return nil }

func benchmarkSearch(b *testing.B, fast bool) {
	data := getBenchData()
	m := literalMatcher("needle", fast)
	s := New(Searcher{Matcher: m})
	r := bytes.NewReader(nil)

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Reset(data)
		if err := s.Search("bench", r, discardSink{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearchFastPath measures the whole-buffer candidate scan path
// (NonMatchingLineTerm true), which should stay close to raw bytes.Index
// throughput regardless of file size.
func BenchmarkSearchFastPath(b *testing.B) { benchmarkSearch(b, true) }

// BenchmarkSearchSlowPath measures the per-line Verify path (every line
// gets a Matcher.Verify call, matched or not) — the baseline the fast
// path exists to beat.
func BenchmarkSearchSlowPath(b *testing.B) { benchmarkSearch(b, false) }

func BenchmarkSearchBytesFastPath(b *testing.B) {
	data := getBenchData()
	m := literalMatcher("needle", true)
	s := New(Searcher{Matcher: m})

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.SearchBytes("bench", data, discardSink{}); err != nil {
			b.Fatal(err)
		}
	}
}

// TestSearchZeroAllocSteadyState asserts the hot Search loop performs zero
// heap allocations once warmed up, for a Sink that itself never allocates.
// This is the concrete, checked form of the "alloc discipline" design
// requirement in PLAN.md, rather than something only visible by eyeballing
// -benchmem output.
func TestSearchZeroAllocSteadyState(t *testing.T) {
	var buf strings.Builder
	for i := 0; i < 2000; i++ {
		if i%97 == 96 {
			buf.WriteString("line with needle token\n")
		} else {
			buf.WriteString("an entirely unremarkable line of text\n")
		}
	}
	data := []byte(buf.String())

	m := literalMatcher("needle", true)
	s := New(Searcher{Matcher: m, LineNumbers: true})
	r := bytes.NewReader(nil)
	sink := discardSink{}

	allocs := testing.AllocsPerRun(20, func() {
		r.Reset(data)
		if err := s.Search("f", r, sink); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 0 {
		t.Fatalf("Search allocated %.2f allocs/op in steady state, want 0", allocs)
	}
}
