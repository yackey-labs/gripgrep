package search

import (
	"bytes"
	"math/rand"
	"testing"
)

// naiveLines splits content into lines the same way rg / this package
// defines a line (terminator included, non-empty, final unterminated
// partial line kept) but via stdlib bytes.SplitAfter rather than any code
// path shared with the production line-stepping logic, so it's a genuine
// independent check.
func naiveLines(content []byte) [][]byte {
	parts := bytes.SplitAfter(content, []byte{'\n'})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// naiveMatch describes one expected match, used as the fuzz oracle.
type naiveMatch struct {
	line       string
	lineNumber int64
	offset     int64
}

// naiveSearch is a from-scratch (SplitAfter-based, no shared line-stepping
// code) reference implementation of plain matching (no context, no
// invert): every line containing literal is a match.
func naiveSearch(content, literal []byte) []naiveMatch {
	var want []naiveMatch
	offset := int64(0)
	for i, line := range naiveLines(content) {
		if bytes.Contains(line, literal) {
			want = append(want, naiveMatch{
				line:       string(line),
				lineNumber: int64(i + 1),
				offset:     offset,
			})
		}
		offset += int64(len(line))
	}
	return want
}

func FuzzSearchMatchesNaiveOracle(f *testing.F) {
	f.Add([]byte("hello\nneedle world\nfoo\n"), int64(1), 5, 3)
	f.Add([]byte(""), int64(2), 8, 1)
	f.Add([]byte("no newline at all needle"), int64(3), 16, 4)
	f.Add([]byte("needle\n\n\nneedle\n"), int64(4), 2, 2)

	f.Fuzz(func(t *testing.T, content []byte, seed int64, rawBufSize, rawChunk int) {
		if len(content) > 1<<20 {
			t.Skip("cap corpus size for fuzz speed")
		}
		rnd := rand.New(rand.NewSource(seed))

		bufSize := rawBufSize % 64
		if bufSize < 0 {
			bufSize = -bufSize
		}
		bufSize++ // 1..64
		chunk := rawChunk % 32
		if chunk < 0 {
			chunk = -chunk
		}
		chunk++ // 1..32

		// Pick a literal that actually occurs sometimes, biased toward
		// short common substrings of the content so both matching and
		// non-matching lines show up.
		literal := []byte("needle")
		if len(content) > 3 && rnd.Intn(2) == 0 {
			start := rnd.Intn(len(content) - 2)
			n := 1 + rnd.Intn(min(3, len(content)-start))
			if cand := content[start : start+n]; !bytes.Contains(cand, []byte{'\n'}) {
				// Excludes candidates spanning a '\n': a pattern
				// containing a literal terminator byte can never match
				// within a single (terminator-stripped) line by
				// construction, in either the searcher or the oracle, so
				// it isn't a meaningful case to fuzz here.
				literal = append([]byte(nil), cand...)
			}
		}
		if len(literal) == 0 {
			literal = []byte("x")
		}

		want := naiveSearch(content, literal)

		for _, fast := range []bool{true, false} {
			for _, candidate := range []bool{false, true} {
				if !fast && candidate {
					continue
				}
				m := literalMatcher(string(literal), fast)
				m.candidate = candidate
				// BinaryNone: the naive oracle has no model of NUL-byte
				// detection (that's covered separately by the dedicated
				// TestSearchBinary* table tests), so a fuzzed NUL byte
				// must not truncate/alter the search here.
				s := newTestSearcher(m, bufSize, Searcher{LineNumbers: true, BinaryMode: BinaryNone})
				sink := newRecordingSink()
				if err := s.Search("f", &chunkReader{data: append([]byte(nil), content...), chunk: chunk}, sink); err != nil {
					t.Fatalf("fast=%v candidate=%v bufSize=%d chunk=%d: Search error: %v", fast, candidate, bufSize, chunk, err)
				}
				if len(sink.events) != len(want) {
					t.Fatalf("fast=%v candidate=%v bufSize=%d chunk=%d literal=%q content=%q: got %d matches, want %d\ngot=%+v\nwant=%+v",
						fast, candidate, bufSize, chunk, literal, content, len(sink.events), len(want), sink.events, want)
				}
				for i, e := range sink.events {
					w := want[i]
					if e.kind != "match" || e.line != w.line || e.lineNumber != w.lineNumber || e.offset != w.offset {
						t.Fatalf("fast=%v candidate=%v bufSize=%d chunk=%d literal=%q: event[%d] = %+v, want match line=%q lineNumber=%d offset=%d",
							fast, candidate, bufSize, chunk, literal, i, e, w.line, w.lineNumber, w.offset)
					}
				}
			}
		}
	})
}
