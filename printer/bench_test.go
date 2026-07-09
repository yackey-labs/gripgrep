package printer

import (
	"io"
	"testing"

	"github.com/yackey-labs/gripgrep/search"
)

// discard is an io.Writer that never grows, so Dest's locked Write
// never triggers an allocation of its own that would pollute the
// per-Matched-call allocation count being measured here.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// BenchmarkStandard_Matched_Piped measures the steady-state cost of the
// binding piped/no-color path: Begin once, then repeated Matched calls
// reusing the same *search.Match value (as a real Searcher would, per
// Match's pooling contract), with periodic Finish/Begin to simulate
// file boundaries. It must show 0 allocs/op in steady state.
func BenchmarkStandard_Matched_Piped(b *testing.B) {
	dest := NewDest(discard{})
	p := NewStandard(dest)
	p.Begin("bench/file.txt")

	m := &search.Match{
		Line:          []byte("the quick brown fox jumps over the lazy dog"),
		LineNumber:    1,
		HasLineNumber: true,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.LineNumber = int64(i%1000 + 1)
		if _, err := p.Matched(m); err != nil {
			b.Fatal(err)
		}
		// Simulate a new file every 1000 matches so the buffer's flush
		// path is exercised too, without dominating the per-op cost.
		if i%1000 == 999 {
			p.Finish("bench/file.txt", &search.Stats{Matched: true})
			p.Begin("bench/file.txt")
		}
	}
}

// BenchmarkStandard_Matched_Color measures the color path for
// comparison; it is expected to allocate (color escape bytes are
// appended, and Find is called), unlike the piped path above.
func BenchmarkStandard_Matched_Color(b *testing.B) {
	dest := NewDest(discard{})
	p := NewStandard(dest)
	p.Color = true
	p.Matcher = &literalMatcher{lit: []byte("fox")}
	p.Begin("bench/file.txt")

	m := &search.Match{
		Line:          []byte("the quick brown fox jumps over the lazy dog"),
		LineNumber:    1,
		HasLineNumber: true,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.LineNumber = int64(i%1000 + 1)
		if _, err := p.Matched(m); err != nil {
			b.Fatal(err)
		}
		if i%1000 == 999 {
			p.Finish("bench/file.txt", &search.Stats{Matched: true})
			p.Begin("bench/file.txt")
		}
	}
}

// BenchmarkStandard_Matched_Context measures the ContextEnabled gap-
// tracking overhead on the otherwise-piped path.
func BenchmarkStandard_Matched_Context(b *testing.B) {
	dest := NewDest(discard{})
	p := NewStandard(dest)
	p.ContextEnabled = true
	p.Begin("bench/file.txt")

	m := &search.Match{
		Line:          []byte("the quick brown fox jumps over the lazy dog"),
		HasLineNumber: true,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.LineNumber = int64(i%1000 + 1)
		if _, err := p.Matched(m); err != nil {
			b.Fatal(err)
		}
		if i%1000 == 999 {
			p.Finish("bench/file.txt", &search.Stats{Matched: true})
			p.Begin("bench/file.txt")
		}
	}
}

// BenchmarkCount_Matched measures -c's tally-only hot path.
func BenchmarkCount_Matched(b *testing.B) {
	dest := NewDest(discard{})
	p := NewCount(dest)
	p.Begin("bench/file.txt")

	m := &search.Match{Line: []byte("the quick brown fox jumps over the lazy dog")}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Matched(m); err != nil {
			b.Fatal(err)
		}
	}
	p.Finish("bench/file.txt", &search.Stats{Matched: true})
}

var _ io.Writer = discard{}
