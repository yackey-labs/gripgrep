package match

import (
	"strings"
	"testing"

	grafanaregexp "github.com/grafana/regexp"
	stdregexp "regexp"
)

// mkHaystack builds a ~10MB haystack of repeated English-ish text with
// a few real occurrences of the needle words scattered through it, so
// throughput benchmarks reflect realistic candidate density rather than
// either "matches everywhere" or "matches nowhere".
func mkHaystack(size int) []byte {
	line := "the quick brown fox jumps over the lazy dog while pm_suspend runs quietly in the background\n"
	needle := "PM_RESUME called from the kernel at boot time\n"
	var b strings.Builder
	b.Grow(size + len(needle))
	i := 0
	for b.Len() < size {
		if i%97 == 0 {
			b.WriteString(needle)
		} else {
			b.WriteString(line)
		}
		i++
	}
	return []byte(b.String())
}

const tenMB = 10 * 1024 * 1024

func reportThroughput(b *testing.B, nBytes int) {
	b.SetBytes(int64(nBytes))
}

// BenchmarkThroughputLiteral: Strategy 1, single literal.
func BenchmarkThroughputLiteral(b *testing.B) {
	buf := mkHaystack(tenMB)
	m, err := New(cs("PM_RESUME"))
	if err != nil {
		b.Fatal(err)
	}
	reportThroughput(b, len(buf))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for {
			off, _, ok := m.FindCandidate(buf, pos)
			if !ok {
				break
			}
			pos = off + 1
		}
	}
}

// BenchmarkThroughputLiteralCI: Strategy 1, case-insensitive literal
// (dedicated ASCII anchor scan).
func BenchmarkThroughputLiteralCI(b *testing.B) {
	buf := mkHaystack(tenMB)
	m, err := New(ci("pm_resume"))
	if err != nil {
		b.Fatal(err)
	}
	reportThroughput(b, len(buf))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for {
			off, _, ok := m.FindCandidate(buf, pos)
			if !ok {
				break
			}
			pos = off + 1
		}
	}
}

// BenchmarkThroughputPrefilteredRegex: Strategy 2, literal-prefiltered
// regex (candidate scan only -- Verify is intentionally not called here
// since this benchmark isolates the whole-buffer prefilter scan, which
// is the throughput-critical piece; per-candidate Verify cost is
// dominated by line length, not haystack size).
func BenchmarkThroughputPrefilteredRegex(b *testing.B) {
	buf := mkHaystack(tenMB)
	m, err := New(cs(`[A-Z]+_RESUME`))
	if err != nil {
		b.Fatal(err)
	}
	reportThroughput(b, len(buf))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for {
			off, _, ok := m.FindCandidate(buf, pos)
			if !ok {
				break
			}
			pos = off + 1
		}
	}
}

// BenchmarkThroughputEngineOnly: Strategy 3, no usable literal prefilter
// -- the regex engine runs directly over the whole buffer.
func BenchmarkThroughputEngineOnly(b *testing.B) {
	buf := mkHaystack(tenMB)
	m, err := New(cs(`\w{3}\s+\w{3}`))
	if err != nil {
		b.Fatal(err)
	}
	reportThroughput(b, len(buf))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos := 0
		for {
			off, _, ok := m.FindCandidate(buf, pos)
			if !ok {
				break
			}
			pos = off + 1
		}
	}
}

// --- allocation-free steady state ------------------------------------

func BenchmarkFindCandidateLiteral(b *testing.B) {
	line := []byte("the quick brown fox jumps over the lazy dog PM_RESUME here")
	m, err := New(cs("PM_RESUME"))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.FindCandidate(line, 0)
	}
}

func BenchmarkVerifyLiteral(b *testing.B) {
	line := []byte("the quick brown fox jumps over the lazy dog PM_RESUME here")
	m, err := New(cs("PM_RESUME"))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Verify(line)
	}
}

func BenchmarkVerifyPrefilteredRegex(b *testing.B) {
	line := []byte("the quick brown fox jumps over the lazy dog XY_RESUME here")
	m, err := New(cs(`[A-Z]+_RESUME`))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Verify(line)
	}
}

// --- grafana/regexp vs stdlib regexp: the required engine bench ------
//
// Task brief: "check its default branch -- if optimizations live on a
// `speedup` branch, pin that; verify with a quick benchmark vs stdlib
// and note the result." grafana/regexp's speedup branch is pinned in
// go.mod (github.com/grafana/regexp @ the speedup-branch commit). These
// benchmarks compare it against stdlib regexp on the same patterns/
// haystacks used elsewhere in this file; see the completion report for
// the numbers and interpretation.

func BenchmarkEngineStdlibSimpleLiteral(b *testing.B) {
	re := stdregexp.MustCompile(`PM_RESUME`)
	buf := mkHaystack(1024 * 1024)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		re.FindAllIndex(buf, -1)
	}
}

func BenchmarkEngineGrafanaSimpleLiteral(b *testing.B) {
	re := grafanaregexp.MustCompile(`PM_RESUME`)
	buf := mkHaystack(1024 * 1024)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		re.FindAllIndex(buf, -1)
	}
}

func BenchmarkEngineStdlibLiteralWithClass(b *testing.B) {
	re := stdregexp.MustCompile(`[A-Z]+_RESUME`)
	buf := mkHaystack(1024 * 1024)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		re.FindAllIndex(buf, -1)
	}
}

func BenchmarkEngineGrafanaLiteralWithClass(b *testing.B) {
	re := grafanaregexp.MustCompile(`[A-Z]+_RESUME`)
	buf := mkHaystack(1024 * 1024)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		re.FindAllIndex(buf, -1)
	}
}

func BenchmarkEngineStdlibNoLiteral(b *testing.B) {
	re := stdregexp.MustCompile(`\w{3}\s+\w{3}`)
	buf := mkHaystack(1024 * 1024)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		re.FindAllIndex(buf, -1)
	}
}

func BenchmarkEngineGrafanaNoLiteral(b *testing.B) {
	re := grafanaregexp.MustCompile(`\w{3}\s+\w{3}`)
	buf := mkHaystack(1024 * 1024)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		re.FindAllIndex(buf, -1)
	}
}
