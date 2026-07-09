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

var _ io.Writer = discard{}

// linesPerCycle bounds the simulated file size in the benchmarks below.
// It must stay well under maxPooledCap (64KB) once formatted (~60
// bytes/line here, so ~12KB total) so the per-file buffer's capacity
// stabilizes after the first couple of cycles instead of oscillating
// near the pool's release threshold — see resetBuf. Go's per-op
// allocation counts are integer-divided (MemAllocs/N), so an
// unstabilized buffer that reallocates once every ~1000 ops still
// rounds down to "0 allocs/op" while quietly reporting nonzero B/op;
// keeping the cycle small enough to stabilize is what makes a genuine
// 0 allocs/op *and* 0 B/op result trustworthy.
const linesPerCycle = 200

var benchStats = &search.Stats{Matched: true}

// BenchmarkStandard_Matched_Piped measures the steady-state cost of the
// binding piped/no-color path: Begin once, then repeated Matched calls
// reusing the same *search.Match value (as a real Searcher would, per
// Match's pooling contract), with periodic Finish/Begin to simulate
// file boundaries. It must show 0 allocs/op AND 0 B/op in steady state.
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
		m.LineNumber = int64(i%linesPerCycle + 1)
		if _, err := p.Matched(m); err != nil {
			b.Fatal(err)
		}
		// Simulate a new file every linesPerCycle matches so the
		// buffer's flush path is exercised too, without dominating the
		// per-op cost or destabilizing the buffer's steady-state size.
		if i%linesPerCycle == linesPerCycle-1 {
			p.Finish("bench/file.txt", benchStats)
			p.Begin("bench/file.txt")
		}
	}
}

// BenchmarkStandard_Matched_Color measures the color path for
// comparison; it appends extra ANSI escape bytes into the same reused
// buffer and calls Matcher.Find, but still performs no heap allocation
// once the buffer has stabilized.
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
		m.LineNumber = int64(i%linesPerCycle + 1)
		if _, err := p.Matched(m); err != nil {
			b.Fatal(err)
		}
		if i%linesPerCycle == linesPerCycle-1 {
			p.Finish("bench/file.txt", benchStats)
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
		m.LineNumber = int64(i%linesPerCycle + 1)
		if _, err := p.Matched(m); err != nil {
			b.Fatal(err)
		}
		if i%linesPerCycle == linesPerCycle-1 {
			p.Finish("bench/file.txt", benchStats)
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
	p.Finish("bench/file.txt", benchStats)
}
