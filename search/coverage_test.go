package search

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yackey-labs/gripgrep/match"
)

// invarianceBufSizes are the four buffer sizes the lead's binding test
// coverage mandate requires every scenario to agree on byte-for-byte:
// "the same input searched with buffer sizes 7, 64, 4096, 65536 must
// produce byte-identical sink event streams." This one property catches
// nearly every rolling-buffer bug class, so it's checked as its own
// helper rather than folded ad hoc into individual tests.
var invarianceBufSizes = []int{7, 64, 4096, 65536}

func eventsEqual(a, b []event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// runAtAllBufferSizes runs content through Search once per
// invarianceBufSizes and fails the test if any size's event stream
// differs from the first. It returns that (shared) event stream so the
// caller can make further assertions against it.
func runAtAllBufferSizes(t *testing.T, m match.Matcher, cfg Searcher, content []byte) []event {
	t.Helper()
	var reference []event
	var refSize int
	for i, bufSize := range invarianceBufSizes {
		s := newTestSearcher(m, bufSize, cfg)
		sink := newRecordingSink()
		if err := s.Search("f", bytes.NewReader(content), sink); err != nil {
			t.Fatalf("bufSize=%d: Search error: %v", bufSize, err)
		}
		if i == 0 {
			reference = sink.events
			refSize = bufSize
			continue
		}
		if !eventsEqual(sink.events, reference) {
			t.Fatalf("buffer-size invariance violated: bufSize=%d differs from bufSize=%d\nbufSize=%d: %+v\nbufSize=%d: %+v",
				bufSize, refSize, bufSize, sink.events, refSize, reference)
		}
	}
	return reference
}

// TestSearchBufferSizeInvarianceMatrix runs a representative cross-section
// of search's scenarios (plain matches, overlapping context, invert,
// invert+context, long lines, CRLF, no-trailing-newline, only-newlines)
// through runAtAllBufferSizes, on both the fast and slow path.
func TestSearchBufferSizeInvarianceMatrix(t *testing.T) {
	scenarios := []struct {
		name    string
		content string
		cfg     Searcher
		matcher func(fast bool) match.Matcher
	}{
		{
			name:    "basic_match",
			content: "one\ntwo needle\nthree\nneedle four\nfive\n",
			cfg:     Searcher{LineNumbers: true},
			matcher: func(fast bool) match.Matcher { return literalMatcher("needle", fast) },
		},
		{
			name:    "context_overlap",
			content: "L1\nMATCH2\nL3\nL4\nMATCH5\nL6\nL7\n",
			cfg:     Searcher{LineNumbers: true, BeforeContext: 2, AfterContext: 2},
			matcher: func(fast bool) match.Matcher { return literalMatcher("MATCH", fast) },
		},
		{
			name:    "invert",
			content: "keep1\nskip this\nkeep2\nskip too\nkeep3\n",
			cfg:     Searcher{Invert: true},
			matcher: func(fast bool) match.Matcher { return literalMatcher("skip", fast) },
		},
		{
			name:    "invert_with_context",
			content: "skip1\nKEEP-A\nskip2\nskip3\nKEEP-B\nskip4\n",
			cfg:     Searcher{Invert: true, BeforeContext: 1, AfterContext: 1, LineNumbers: true},
			matcher: func(fast bool) match.Matcher { return literalMatcher("skip", fast) },
		},
		{
			name:    "no_trailing_newline",
			content: "first\nlast line no newline",
			cfg:     Searcher{},
			matcher: func(fast bool) match.Matcher { return literalMatcher("last", fast) },
		},
		{
			name:    "only_newlines",
			content: "\n\n\n\n\n",
			cfg:     Searcher{LineNumbers: true},
			matcher: func(fast bool) match.Matcher { return &alwaysMatcher{nonMatchingTerm: fast} },
		},
		{
			name:    "crlf",
			content: "one\r\nneedle\r\nthree\r\n",
			cfg:     Searcher{},
			matcher: func(fast bool) match.Matcher { return literalMatcher("needle", fast) },
		},
		{
			name:    "long_line",
			content: "pre\n" + strings.Repeat("x", 500) + "END\nafter\n",
			cfg:     Searcher{},
			matcher: func(fast bool) match.Matcher { return literalMatcher("END", fast) },
		},
		{
			name:    "context_at_start_and_eof",
			content: "MATCH-first\nmid1\nmid2\nMATCH-last\n",
			cfg:     Searcher{BeforeContext: 2, AfterContext: 2},
			matcher: func(fast bool) match.Matcher { return literalMatcher("MATCH", fast) },
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			for _, fast := range []bool{true, false} {
				t.Run(pathName(fast, false), func(t *testing.T) {
					m := sc.matcher(fast)
					runAtAllBufferSizes(t, m, sc.cfg, []byte(sc.content))
				})
			}
		})
	}
}

// TestSearchEmptyMatchPatternTerminates covers the classic grep-implementation
// bug: a pattern that can match the empty string at every position (`^`,
// `$`, `^$`, `a*`, `()`) must not hang the scan loop. alwaysMatcher
// simulates the worst case (every position is a hit); search's line-
// granularity advancement (always jump to the end of the whole enclosing
// line, never just past the match) must guarantee termination regardless.
func TestSearchEmptyMatchPatternTerminates(t *testing.T) {
	for _, fast := range []bool{true, false} {
		t.Run(pathName(fast, false), func(t *testing.T) {
			content := []byte(strings.Repeat("line\n", 2000))
			m := &alwaysMatcher{nonMatchingTerm: fast}
			s := newTestSearcher(m, 64, Searcher{LineNumbers: true})
			sink := newRecordingSink()

			done := make(chan error, 1)
			go func() {
				done <- s.Search("f", bytes.NewReader(content), sink)
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Search error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Search did not terminate within 5s: likely an empty-match infinite loop")
			}

			want := strings.Count(string(content), "\n")
			if got := sink.matchCount(); got != want {
				t.Fatalf("matchCount = %d, want %d (one match per line)", got, want)
			}
		})
	}
}

func TestSearchOnlyNewlines(t *testing.T) {
	content := []byte("\n\n\n\n\n")
	for _, fast := range []bool{true, false} {
		// No literal ever matches a blank line.
		m := literalMatcher("x", fast)
		s := newTestSearcher(m, 64, Searcher{})
		sink := newRecordingSink()
		if err := s.Search("f", bytes.NewReader(content), sink); err != nil {
			t.Fatal(err)
		}
		if len(sink.events) != 0 {
			t.Fatalf("fast=%v: expected no matches on all-blank file, got %v", fast, sink.events)
		}

		// A pattern that matches everywhere matches every blank line.
		am := &alwaysMatcher{nonMatchingTerm: fast}
		s2 := newTestSearcher(am, 64, Searcher{LineNumbers: true})
		sink2 := newRecordingSink()
		if err := s2.Search("f", bytes.NewReader(content), sink2); err != nil {
			t.Fatal(err)
		}
		want := []string{"\n", "\n", "\n", "\n", "\n"}
		if got := sink2.matchLines(); !equalStrings(got, want) {
			t.Fatalf("fast=%v: matches = %q, want %q", fast, got, want)
		}
		wantLineNo := []int64{1, 2, 3, 4, 5}
		var gotLineNo []int64
		for _, e := range sink2.events {
			gotLineNo = append(gotLineNo, e.lineNumber)
		}
		if !equalInt64s(gotLineNo, wantLineNo) {
			t.Fatalf("fast=%v: line numbers = %v, want %v", fast, gotLineNo, wantLineNo)
		}
	}
}

func TestSearchOnlyNULs(t *testing.T) {
	content := []byte("\x00\x00\x00\x00\x00")

	t.Run("BinaryQuit", func(t *testing.T) {
		m := literalMatcher("x", true)
		s := newTestSearcher(m, 64, Searcher{BinaryMode: BinaryQuit})
		sink := newRecordingSink()
		if err := s.Search("f", bytes.NewReader(content), sink); err != nil {
			t.Fatal(err)
		}
		if len(sink.events) != 0 || !sink.finishStats.Binary || sink.finishStats.BinaryOffset != 0 {
			t.Fatalf("events=%v stats=%+v", sink.events, sink.finishStats)
		}
	})

	t.Run("BinaryNone", func(t *testing.T) {
		// No real '\n' anywhere: the whole file is one final,
		// unterminated "line" of raw NUL bytes, passed through untouched.
		m := literalMatcher("\x00\x00", true)
		s := newTestSearcher(m, 64, Searcher{BinaryMode: BinaryNone})
		sink := newRecordingSink()
		if err := s.Search("f", bytes.NewReader(content), sink); err != nil {
			t.Fatal(err)
		}
		want := []string{string(content)}
		if got := sink.matchLines(); !equalStrings(got, want) {
			t.Fatalf("matches = %q, want %q", got, want)
		}
		if sink.finishStats.Binary {
			t.Fatal("expected Stats.Binary = false")
		}
	})

	t.Run("BinaryConvert", func(t *testing.T) {
		// Every NUL becomes '\n' in the owned rolling buffer, splitting
		// the content into 5 single-byte lines.
		m := &alwaysMatcher{nonMatchingTerm: true}
		s := newTestSearcher(m, 64, Searcher{BinaryMode: BinaryConvert})
		sink := newRecordingSink()
		if err := s.Search("f", bytes.NewReader(content), sink); err != nil {
			t.Fatal(err)
		}
		want := []string{"\n", "\n", "\n", "\n", "\n"}
		if got := sink.matchLines(); !equalStrings(got, want) {
			t.Fatalf("matches = %q, want %q", got, want)
		}
		if !sink.finishStats.Binary || sink.finishStats.BinaryOffset != 0 {
			t.Fatalf("stats = %+v", sink.finishStats)
		}
	})
}

// TestSearchInvertWithContext's expected event sequence was verified
// against the real `rg -v -B1 -A1 -n "skip" <file>` (rg reported
// "1-skip1 / 2:KEEP-A / 3-skip2 / 4-skip3 / 5:KEEP-B / 6-skip4", i.e.
// every line shown, alternating before/match/after with no gap since the
// context windows are exactly adjacent).
func TestSearchInvertWithContext(t *testing.T) {
	content := "skip1\nKEEP-A\nskip2\nskip3\nKEEP-B\nskip4\n"
	wantKinds := []string{"before", "match", "after", "before", "match", "after"}
	wantLines := []string{"skip1\n", "KEEP-A\n", "skip2\n", "skip3\n", "KEEP-B\n", "skip4\n"}
	wantLineNo := []int64{1, 2, 3, 4, 5, 6}

	for _, fast := range []bool{true, false} {
		t.Run(pathName(fast, false), func(t *testing.T) {
			m := literalMatcher("skip", fast)
			cfg := Searcher{Invert: true, BeforeContext: 1, AfterContext: 1, LineNumbers: true}
			events := runAtAllBufferSizes(t, m, cfg, []byte(content))
			if len(events) != len(wantKinds) {
				t.Fatalf("got %d events, want %d: %+v", len(events), len(wantKinds), events)
			}
			for i, e := range events {
				if e.kind != wantKinds[i] || e.line != wantLines[i] || e.lineNumber != wantLineNo[i] {
					t.Fatalf("event[%d] = %+v, want kind=%s line=%q lineNo=%d",
						i, e, wantKinds[i], wantLines[i], wantLineNo[i])
				}
			}
		})
	}
}

// TestSearchAfterContextCrossesBufferRoll forces a match's after-context
// lines to arrive across more than one fill()/roll() cycle (tiny buffers,
// tiny reads) and asserts the context is still delivered in full and in
// order, with no duplication or loss at the roll boundary.
func TestSearchAfterContextCrossesBufferRoll(t *testing.T) {
	content := "MATCH1\nc1\nc2\nc3\nMATCH2\nc4\nc5\nc6\n"
	wantKinds := []string{"match", "after", "after", "after", "match", "after", "after", "after"}
	wantLines := []string{"MATCH1\n", "c1\n", "c2\n", "c3\n", "MATCH2\n", "c4\n", "c5\n", "c6\n"}

	for _, fast := range []bool{true, false} {
		for _, bufSize := range []int{4, 5, 6, 7, 8, 10, 16} {
			for _, chunk := range []int{1, 2, 3} {
				name := fmt.Sprintf("%s/buf=%d/chunk=%d", pathName(fast, false), bufSize, chunk)
				t.Run(name, func(t *testing.T) {
					m := literalMatcher("MATCH", fast)
					s := newTestSearcher(m, bufSize, Searcher{AfterContext: 3})
					sink := newRecordingSink()
					if err := s.Search("f", &chunkReader{data: []byte(content), chunk: chunk}, sink); err != nil {
						t.Fatalf("bufSize=%d chunk=%d: %v", bufSize, chunk, err)
					}
					if len(sink.events) != len(wantKinds) {
						t.Fatalf("bufSize=%d chunk=%d: got %d events, want %d: %+v",
							bufSize, chunk, len(sink.events), len(wantKinds), sink.events)
					}
					for i, e := range sink.events {
						if e.kind != wantKinds[i] || e.line != wantLines[i] {
							t.Fatalf("bufSize=%d chunk=%d: event[%d] = %+v, want kind=%s line=%q",
								bufSize, chunk, i, e, wantKinds[i], wantLines[i])
						}
					}
				})
			}
		}
	}
}

// TestSearchLineNumberPropertyAcrossBufferSizes is the lead-mandated
// property test: reported lineno must equal a naive line count at every
// match, checked at all four invariance buffer sizes.
func TestSearchLineNumberPropertyAcrossBufferSizes(t *testing.T) {
	var buf strings.Builder
	for i := 0; i < 500; i++ {
		if i%7 == 3 {
			buf.WriteString("this has needle in it\n")
		} else {
			buf.WriteString("filler filler filler\n")
		}
	}
	content := []byte(buf.String())
	want := naiveSearch(content, []byte("needle"))
	if len(want) == 0 {
		t.Fatal("test setup bug: oracle found no matches")
	}

	for _, fast := range []bool{true, false} {
		t.Run(pathName(fast, false), func(t *testing.T) {
			m := literalMatcher("needle", fast)
			events := runAtAllBufferSizes(t, m, Searcher{LineNumbers: true}, content)
			if len(events) != len(want) {
				t.Fatalf("got %d matches, want %d", len(events), len(want))
			}
			for i, e := range events {
				w := want[i]
				if e.lineNumber != w.lineNumber || e.offset != w.offset || e.line != w.line {
					t.Fatalf("event[%d] lineNumber=%d offset=%d line=%q, want lineNumber=%d offset=%d line=%q",
						i, e.lineNumber, e.offset, e.line, w.lineNumber, w.offset, w.line)
				}
			}
		})
	}
}

// TestSearchInvalidUTF8MidLine: search is byte-oriented and must never
// validate, reject, or mangle invalid UTF-8 — it matches and returns the
// raw bytes as-is.
func TestSearchInvalidUTF8MidLine(t *testing.T) {
	content := []byte("before\nbad \xff\xfe byte needle here\nafter\n")
	for _, fast := range []bool{true, false} {
		m := literalMatcher("needle", fast)
		s := newTestSearcher(m, 64, Searcher{})
		sink := newRecordingSink()
		if err := s.Search("f", bytes.NewReader(content), sink); err != nil {
			t.Fatal(err)
		}
		want := []string{"bad \xff\xfe byte needle here\n"}
		if got := sink.matchLines(); !equalStrings(got, want) {
			t.Fatalf("fast=%v: matches = %q, want %q", fast, got, want)
		}
	}
}
