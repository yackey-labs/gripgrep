package search

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/yackey-labs/gripgrep/match"
)

// event is a copied-out (per the Match/Ctx validity contract) record of one
// Matched or Context call.
type event struct {
	kind          string // "match", "before", "after"
	line          string
	lineNumber    int64
	hasLineNumber bool
	offset        int64
}

// recordingSink implements Sink, copying every Match/Ctx it sees (since the
// originals are only valid for the duration of the call) so tests can
// assert on the full sequence afterward. stopAfter, if > 0, makes Matched
// return more=false once that many matches have been recorded, simulating
// -q/-l early exit.
type recordingSink struct {
	events      []event
	beginPath   string
	beginCalled bool
	finishStats *Stats
	finishPath  string
	stopAfter   int
	beginResult bool
	beginErr    error
}

func newRecordingSink() *recordingSink {
	return &recordingSink{beginResult: true}
}

func (s *recordingSink) Begin(path string) (bool, error) {
	s.beginCalled = true
	s.beginPath = path
	return s.beginResult, s.beginErr
}

func (s *recordingSink) Matched(m *Match) (bool, error) {
	s.events = append(s.events, event{
		kind: "match", line: string(m.Line), lineNumber: m.LineNumber,
		hasLineNumber: m.HasLineNumber, offset: m.Offset,
	})
	if s.stopAfter > 0 && s.matchCount() >= s.stopAfter {
		return false, nil
	}
	return true, nil
}

func (s *recordingSink) matchCount() int {
	n := 0
	for _, e := range s.events {
		if e.kind == "match" {
			n++
		}
	}
	return n
}

func (s *recordingSink) Context(c *Ctx) (bool, error) {
	kind := "before"
	if c.After {
		kind = "after"
	}
	s.events = append(s.events, event{
		kind: kind, line: string(c.Line), lineNumber: c.LineNumber,
		hasLineNumber: c.HasLineNumber, offset: c.Offset,
	})
	return true, nil
}

func (s *recordingSink) Finish(path string, stats *Stats) error {
	s.finishPath = path
	s.finishStats = stats
	return nil
}

func (s *recordingSink) matchLines() []string {
	var out []string
	for _, e := range s.events {
		if e.kind == "match" {
			out = append(out, e.line)
		}
	}
	return out
}

// chunkReader forces io.Reader to hand back small, fixed-size reads
// regardless of the caller's buffer size, to exercise the rolling buffer's
// multi-read fill loop and roll() boundary logic instead of letting a
// single Read satisfy everything at once.
type chunkReader struct {
	data  []byte
	chunk int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.chunk
	if n <= 0 {
		n = 1
	}
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	n = copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

// newTestSearcher builds a Searcher with an overridden (typically tiny)
// buffer size, so boundary/rolling behavior can be exercised without
// needing >64KB test fixtures.
func newTestSearcher(m match.Matcher, bufSize int, cfg Searcher) *Searcher {
	cfg.Matcher = m
	s := New(cfg)
	s.lb = newLineBuffer(bufSize)
	return s
}

// errReader always fails, to test Search's I/O error propagation.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestSearchBasicMatch(t *testing.T) {
	for _, fast := range []bool{true, false} {
		for _, candidate := range []bool{false, true} {
			if !fast && candidate {
				continue // candidate mode only meaningful on fast path
			}
			t.Run(pathName(fast, candidate), func(t *testing.T) {
				m := literalMatcher("needle", fast)
				m.candidate = candidate
				s := newTestSearcher(m, DefaultBufferSize, Searcher{LineNumbers: true})
				sink := newRecordingSink()
				content := "one\ntwo needle\nthree\nneedle four\nfive\n"
				if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
					t.Fatalf("Search: %v", err)
				}
				want := []string{"two needle\n", "needle four\n"}
				if got := sink.matchLines(); !equalStrings(got, want) {
					t.Fatalf("matches = %q, want %q", got, want)
				}
				wantLineNo := []int64{2, 4}
				var gotLineNo []int64
				for _, e := range sink.events {
					gotLineNo = append(gotLineNo, e.lineNumber)
				}
				if !equalInt64s(gotLineNo, wantLineNo) {
					t.Fatalf("line numbers = %v, want %v", gotLineNo, wantLineNo)
				}
				if sink.finishStats == nil || !sink.finishStats.Matched || sink.finishStats.MatchCount != 2 {
					t.Fatalf("stats = %+v", sink.finishStats)
				}
				if sink.finishStats.BytesSearched != int64(len(content)) {
					t.Fatalf("BytesSearched = %d, want %d", sink.finishStats.BytesSearched, len(content))
				}
			})
		}
	}
}

func pathName(fast, candidate bool) string {
	switch {
	case fast && candidate:
		return "fast/candidate"
	case fast:
		return "fast/confirmed"
	default:
		return "slow"
	}
}

func TestSearchNoMatchNoLineNumberCost(t *testing.T) {
	m := literalMatcher("nope", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{LineNumbers: true})
	sink := newRecordingSink()
	content := "a\nb\nc\n"
	if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("expected no events, got %v", sink.events)
	}
	if sink.finishStats.Matched {
		t.Fatal("expected Matched=false")
	}
}

func TestSearchEmptyFile(t *testing.T) {
	for _, fast := range []bool{true, false} {
		m := literalMatcher("x", fast)
		s := newTestSearcher(m, DefaultBufferSize, Searcher{})
		sink := newRecordingSink()
		if err := s.Search("f", bytes.NewReader(nil), sink); err != nil {
			t.Fatal(err)
		}
		if len(sink.events) != 0 || sink.finishStats.Matched || sink.finishStats.BytesSearched != 0 {
			t.Fatalf("fast=%v: unexpected result: events=%v stats=%+v", fast, sink.events, sink.finishStats)
		}
		if !sink.beginCalled {
			t.Fatal("Begin was not called")
		}
	}
}

func TestSearchNoTrailingNewline(t *testing.T) {
	for _, fast := range []bool{true, false} {
		m := literalMatcher("last", fast)
		s := newTestSearcher(m, DefaultBufferSize, Searcher{})
		sink := newRecordingSink()
		content := "first\nlast line no newline"
		if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
			t.Fatal(err)
		}
		want := []string{"last line no newline"}
		if got := sink.matchLines(); !equalStrings(got, want) {
			t.Fatalf("fast=%v: matches = %q, want %q", fast, got, want)
		}
	}
}

func TestSearchCRLF(t *testing.T) {
	// Terminator is '\n' only; '\r' stays part of the line bytes.
	for _, fast := range []bool{true, false} {
		m := literalMatcher("needle", fast)
		s := newTestSearcher(m, DefaultBufferSize, Searcher{})
		sink := newRecordingSink()
		content := "one\r\nneedle\r\nthree\r\n"
		if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
			t.Fatal(err)
		}
		want := []string{"needle\r\n"}
		if got := sink.matchLines(); !equalStrings(got, want) {
			t.Fatalf("fast=%v: matches = %q, want %q", fast, got, want)
		}
	}
}

func TestSearchInvert(t *testing.T) {
	for _, fast := range []bool{true, false} {
		m := literalMatcher("skip", fast)
		s := newTestSearcher(m, DefaultBufferSize, Searcher{Invert: true})
		sink := newRecordingSink()
		content := "keep1\nskip this\nkeep2\nskip too\nkeep3\n"
		if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
			t.Fatal(err)
		}
		want := []string{"keep1\n", "keep2\n", "keep3\n"}
		if got := sink.matchLines(); !equalStrings(got, want) {
			t.Fatalf("fast=%v: matches = %q, want %q", fast, got, want)
		}
	}
}

func TestSearchInvertNoMatchAtAll(t *testing.T) {
	// No candidate line ever matches, so every line is inverted-in.
	for _, fast := range []bool{true, false} {
		m := literalMatcher("neverhere", fast)
		s := newTestSearcher(m, DefaultBufferSize, Searcher{Invert: true})
		sink := newRecordingSink()
		content := "a\nb\nc\n"
		if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
			t.Fatal(err)
		}
		want := []string{"a\n", "b\n", "c\n"}
		if got := sink.matchLines(); !equalStrings(got, want) {
			t.Fatalf("fast=%v: matches = %q, want %q", fast, got, want)
		}
	}
}

func TestSearchContextOverlapDedup(t *testing.T) {
	// Matches on line 2 and line 5 (1-based) with Before=2/After=2: the
	// after-context of the first match (lines 3,4) is exactly the
	// before-context window of the second (lines 3,4) — must not repeat.
	for _, fast := range []bool{true, false} {
		m := literalMatcher("MATCH", fast)
		s := newTestSearcher(m, DefaultBufferSize, Searcher{
			LineNumbers: true, BeforeContext: 2, AfterContext: 2,
		})
		sink := newRecordingSink()
		content := "L1\nMATCH2\nL3\nL4\nMATCH5\nL6\nL7\n"
		if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
			t.Fatal(err)
		}
		wantKinds := []string{"before", "match", "after", "after", "match", "after", "after"}
		wantLines := []string{"L1\n", "MATCH2\n", "L3\n", "L4\n", "MATCH5\n", "L6\n", "L7\n"}
		if len(sink.events) != len(wantKinds) {
			t.Fatalf("fast=%v: got %d events, want %d: %+v", fast, len(sink.events), len(wantKinds), sink.events)
		}
		for i, e := range sink.events {
			if e.kind != wantKinds[i] || e.line != wantLines[i] {
				t.Fatalf("fast=%v: event[%d] = %+v, want kind=%s line=%q", fast, i, e, wantKinds[i], wantLines[i])
			}
		}
	}
}

func TestSearchContextNoOverlapGap(t *testing.T) {
	// Matches far apart: before-context of the second match must not reach
	// back into the first match's line.
	for _, fast := range []bool{true, false} {
		m := literalMatcher("MATCH", fast)
		s := newTestSearcher(m, DefaultBufferSize, Searcher{BeforeContext: 1, AfterContext: 1})
		sink := newRecordingSink()
		content := "MATCH1\nfiller1\nfiller2\nfiller3\nMATCH2\n"
		if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
			t.Fatal(err)
		}
		wantKinds := []string{"match", "after", "before", "match"}
		wantLines := []string{"MATCH1\n", "filler1\n", "filler3\n", "MATCH2\n"}
		if len(sink.events) != len(wantKinds) {
			t.Fatalf("fast=%v: got %d events, want %d: %+v", fast, len(sink.events), len(wantKinds), sink.events)
		}
		for i, e := range sink.events {
			if e.kind != wantKinds[i] || e.line != wantLines[i] {
				t.Fatalf("fast=%v: event[%d] = %+v, want kind=%s line=%q", fast, i, e, wantKinds[i], wantLines[i])
			}
		}
	}
}

func TestSearchStopEarly(t *testing.T) {
	// Simulates -q/-l: Sink stops after the first match.
	for _, fast := range []bool{true, false} {
		m := literalMatcher("needle", fast)
		s := newTestSearcher(m, DefaultBufferSize, Searcher{})
		sink := newRecordingSink()
		sink.stopAfter = 1
		content := "needle one\nneedle two\nneedle three\n"
		if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
			t.Fatal(err)
		}
		if got := sink.matchCount(); got != 1 {
			t.Fatalf("fast=%v: matchCount = %d, want 1", fast, got)
		}
		if sink.finishStats == nil {
			t.Fatal("Finish was not called")
		}
	}
}

func TestSearchBeginSkip(t *testing.T) {
	m := literalMatcher("x", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{})
	sink := newRecordingSink()
	sink.beginResult = false
	if err := s.Search("f", bytes.NewReader([]byte("x\n")), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("expected no events when Begin returns false, got %v", sink.events)
	}
	if sink.finishStats == nil {
		t.Fatal("Finish must still be called when Begin returns search=false")
	}
}

func TestSearchIOError(t *testing.T) {
	m := literalMatcher("x", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{})
	sink := newRecordingSink()
	wantErr := errors.New("boom")
	if err := s.Search("f", errReader{wantErr}, sink); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestSearchLongLineDoublesBuffer(t *testing.T) {
	// A single line far longer than the initial buffer capacity must still
	// be read whole via ensureCapacity's doubling.
	for _, fast := range []bool{true, false} {
		m := literalMatcher("END", fast)
		s := newTestSearcher(m, 8, Searcher{})
		sink := newRecordingSink()
		longLine := bytes.Repeat([]byte("x"), 500)
		content := append(append([]byte("pre\n"), longLine...), []byte("END\nafter\n")...)
		if err := s.Search("f", &chunkReader{data: content, chunk: 3}, sink); err != nil {
			t.Fatal(err)
		}
		want := []string{string(longLine) + "END\n"}
		if got := sink.matchLines(); !equalStrings(got, want) {
			t.Fatalf("fast=%v: matches len=%v, want len=%v", fast, len(got), len(want))
		}
	}
}

func TestSearchBoundaryAtExactBufferSize(t *testing.T) {
	// Line lengths chosen to land exactly on fill/roll boundaries for a
	// small buffer, forced through multiple small reads.
	for _, bufSize := range []int{4, 8, 16, 32, 64} {
		for _, chunk := range []int{1, 2, 3, 7} {
			m := literalMatcher("HIT", true)
			s := newTestSearcher(m, bufSize, Searcher{LineNumbers: true})
			sink := newRecordingSink()
			content := "aaaa\nbbbb\nHIT\ncccccccc\ndddddddddddddddd\nHIT\nz\n"
			if err := s.Search("f", &chunkReader{data: []byte(content), chunk: chunk}, sink); err != nil {
				t.Fatalf("bufSize=%d chunk=%d: %v", bufSize, chunk, err)
			}
			want := []string{"HIT\n", "HIT\n"}
			if got := sink.matchLines(); !equalStrings(got, want) {
				t.Fatalf("bufSize=%d chunk=%d: matches = %q, want %q", bufSize, chunk, got, want)
			}
			wantLineNo := []int64{3, 6}
			var gotLineNo []int64
			for _, e := range sink.events {
				gotLineNo = append(gotLineNo, e.lineNumber)
			}
			if !equalInt64s(gotLineNo, wantLineNo) {
				t.Fatalf("bufSize=%d chunk=%d: line numbers = %v, want %v", bufSize, chunk, gotLineNo, wantLineNo)
			}
		}
	}
}

// TestSearchBinaryQuit pins the corrected (rg-parity) semantics: the NUL
// and the "needle before" match both arrive in the SAME single read (the
// whole 36-byte content fits in one bytes.Reader.Read call well within
// DefaultBufferSize), so the entire chunk containing the NUL is discarded
// -- including "needle before", even though it textually precedes the
// NUL -- and zero matches are reported. This mirrors the real rg binary
// exactly (verified: a tiny file with "needle before NUL byte\n" then a
// NUL reports zero matches, not the pre-NUL one) and ripgrep's own
// upstream searcher tests (binary2 in
// ../ripgrep/crates/searcher/src/searcher/glue.rs: "a\x00" reports byte
// count 0, not a match on the leading "a"). See
// TestSearchBinaryQuit_MatchInEarlierChunkIsReported for the case where a
// match in an earlier, separate (NUL-free) read IS reported.
func TestSearchBinaryQuit(t *testing.T) {
	m := literalMatcher("needle", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{BinaryMode: BinaryQuit})
	sink := newRecordingSink()
	content := "needle before\nfoo\x00bar\nneedle after\n"
	if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
		t.Fatal(err)
	}
	if got := sink.matchLines(); len(got) != 0 {
		t.Fatalf("matches = %q, want none (whole chunk containing the NUL is discarded, including same-chunk content before it)", got)
	}
	if !sink.finishStats.Binary {
		t.Fatal("expected Stats.Binary = true")
	}
	if sink.finishStats.Matched {
		t.Fatal("expected Stats.Matched = false")
	}
	wantOffset := int64(bytes.IndexByte([]byte(content), 0))
	if sink.finishStats.BinaryOffset != wantOffset {
		t.Fatalf("BinaryOffset = %d, want %d", sink.finishStats.BinaryOffset, wantOffset)
	}
}

// TestSearchBinaryQuit_MatchInEarlierChunkIsReported is the counterpart
// to TestSearchBinaryQuit: a match found in an earlier read that
// contained no NUL is already searched and sunk before the later read
// containing the NUL is ever seen, so it must survive -- only the
// same-chunk-as-the-NUL content is discarded. This is the mechanism that
// makes real rg print partial results (then a warning) for a large
// walk-discovered binary file whose matches are spread across many
// reads before the NUL, e.g. ../ripgrep/tests/data/sherlock-nul.txt
// (verified against the real rg binary: matches up through line 1565
// are printed, then "WARNING: stopped searching..."; two more textual
// matches between there and the NUL, landing in the same final read as
// the NUL, are silently dropped).
//
// chunkReader with chunk=len(line1)=len(line2) forces each line into its
// own single Read call, so "needle one!" and its NUL-adjacent sibling
// never share a read with each other.
func TestSearchBinaryQuit_MatchInEarlierChunkIsReported(t *testing.T) {
	m := literalMatcher("needle", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{BinaryMode: BinaryQuit})
	sink := newRecordingSink()

	line1 := "needle one!\n"    // 12 bytes, no NUL: a clean, earlier read.
	line2 := "needle two\x00\n" // 12 bytes, NUL at index 10: discarded whole.
	content := line1 + line2

	if err := s.Search("f", &chunkReader{data: []byte(content), chunk: len(line1)}, sink); err != nil {
		t.Fatal(err)
	}
	want := []string{line1}
	if got := sink.matchLines(); !equalStrings(got, want) {
		t.Fatalf("matches = %q, want %q (the earlier chunk's match must survive; the NUL-chunk's match must not)", got, want)
	}
	if !sink.finishStats.Binary {
		t.Fatal("expected Stats.Binary = true")
	}
	if !sink.finishStats.Matched {
		t.Fatal("expected Stats.Matched = true (from the earlier, NUL-free chunk)")
	}
	wantOffset := int64(len(line1) + bytes.IndexByte([]byte(line2), 0))
	if sink.finishStats.BinaryOffset != wantOffset {
		t.Fatalf("BinaryOffset = %d, want %d", sink.finishStats.BinaryOffset, wantOffset)
	}
}

func TestSearchBinaryConvert(t *testing.T) {
	m := literalMatcher("bar", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{BinaryMode: BinaryConvert})
	sink := newRecordingSink()
	// The NUL splits what looks like one line into two once converted.
	content := "needle before\nfoo\x00bar\nafter\n"
	if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
		t.Fatal(err)
	}
	want := []string{"bar\n"}
	if got := sink.matchLines(); !equalStrings(got, want) {
		t.Fatalf("matches = %q, want %q", got, want)
	}
	if !sink.finishStats.Binary {
		t.Fatal("expected Stats.Binary = true")
	}
}

func TestSearchBinaryNoneLeavesNULAlone(t *testing.T) {
	m := literalMatcher("bar", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{BinaryMode: BinaryNone})
	sink := newRecordingSink()
	content := "foo\x00bar\n"
	if err := s.Search("f", bytes.NewReader([]byte(content)), sink); err != nil {
		t.Fatal(err)
	}
	want := []string{"foo\x00bar\n"}
	if got := sink.matchLines(); !equalStrings(got, want) {
		t.Fatalf("matches = %q, want %q", got, want)
	}
	if sink.finishStats.Binary {
		t.Fatal("expected Stats.Binary = false under BinaryNone")
	}
}

func TestSearchBytesBasic(t *testing.T) {
	for _, fast := range []bool{true, false} {
		m := literalMatcher("needle", fast)
		s := newTestSearcher(m, DefaultBufferSize, Searcher{LineNumbers: true, BeforeContext: 1, AfterContext: 1})
		sink := newRecordingSink()
		data := []byte("a\nneedle\nb\nc\n")
		if err := s.SearchBytes("f", data, sink); err != nil {
			t.Fatal(err)
		}
		wantKinds := []string{"before", "match", "after"}
		if len(sink.events) != len(wantKinds) {
			t.Fatalf("fast=%v: events = %+v", fast, sink.events)
		}
		for i, e := range sink.events {
			if e.kind != wantKinds[i] {
				t.Fatalf("fast=%v: event[%d].kind = %s, want %s", fast, i, e.kind, wantKinds[i])
			}
		}
		if sink.finishStats.BytesSearched != int64(len(data)) {
			t.Fatalf("BytesSearched = %d, want %d", sink.finishStats.BytesSearched, len(data))
		}
	}
}

func TestSearchBytesBinaryConvertDetectsButDoesNotMutateInput(t *testing.T) {
	// Unlike Search's owned rolling buffer, SearchBytes must never write to
	// a caller-owned slice. Under BinaryConvert this means detection still
	// fires, but the NUL is left as an ordinary byte (no line-split at that
	// point) — matching rg's read-only mmap path.
	m := literalMatcher("bar", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{BinaryMode: BinaryConvert})
	sink := newRecordingSink()
	data := []byte("foo\x00bar\n")
	orig := append([]byte(nil), data...)
	if err := s.SearchBytes("f", data, sink); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, orig) {
		t.Fatalf("SearchBytes mutated caller data: got %q, want %q", data, orig)
	}
	want := []string{"foo\x00bar\n"}
	if got := sink.matchLines(); !equalStrings(got, want) {
		t.Fatalf("matches = %q, want %q", got, want)
	}
	if !sink.finishStats.Binary {
		t.Fatal("expected Stats.Binary = true")
	}
}

func TestSearchBytesBinaryQuitSkipsWholeFile(t *testing.T) {
	m := literalMatcher("after", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{BinaryMode: BinaryQuit})
	sink := newRecordingSink()
	data := []byte("before\nfoo\x00bar\nafter\n")
	if err := s.SearchBytes("f", data, sink); err != nil {
		t.Fatal(err)
	}
	if got := sink.matchLines(); len(got) != 0 {
		t.Fatalf("expected no matches (whole file skipped once binary detected), got %q", got)
	}
	if !sink.finishStats.Binary {
		t.Fatal("expected Stats.Binary = true")
	}
}

// TestSearchBytesDoesNotScanPastDefaultBufferSizeForNUL covers the
// deliberate boundary of SearchBytes's NUL detection (see its doc): a NUL
// placed past the first DefaultBufferSize bytes, in a stretch no matched
// line's own bytes ever cover, must NOT be detected at this layer --
// Stats.Binary stays false. This mirrors real rg's own SliceByLine
// (verified against the installed rg binary), which leaves exactly this
// gap for its mmap path; cmd/gg's matchTracker closes it one layer up by
// inspecting delivered match/context lines directly (see wire.go's
// noteLineNUL), not by making this method scan the whole slice.
func TestSearchBytesDoesNotScanPastDefaultBufferSizeForNUL(t *testing.T) {
	m := literalMatcher("needle", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{BinaryMode: BinaryConvert})
	sink := newRecordingSink()

	var data []byte
	data = append(data, "needle one\n"...)
	filler := []byte("filler filler filler filler filler filler filler filler\n")
	for len(data) < DefaultBufferSize+4096 {
		data = append(data, filler...)
	}
	data = append(data, 0)
	data = append(data, "filler filler filler filler filler filler filler\n"...)

	if err := s.SearchBytes("f", data, sink); err != nil {
		t.Fatal(err)
	}
	if sink.finishStats.Binary {
		t.Fatal("expected Stats.Binary = false: the NUL falls past DefaultBufferSize and no matched line covers it")
	}
	// BinaryConvert never truncates the search itself -- the sole match
	// (before the NUL) is still found and counted.
	if sink.finishStats.MatchCount != 1 {
		t.Errorf("MatchCount = %d, want 1", sink.finishStats.MatchCount)
	}
}

// TestSearcher_HasBinaryOffsetLiveDuringScan covers the live (mid-scan)
// query surface HasBinaryOffset/BinaryOffset exist for: cmd/gg's
// matchTracker needs to know binary state as of the most recently
// delivered Matched/Context call, not only once Finish runs.
func TestSearcher_HasBinaryOffsetLiveDuringScan(t *testing.T) {
	m := literalMatcher("needle", true)
	s := newTestSearcher(m, DefaultBufferSize, Searcher{BinaryMode: BinaryConvert})

	if s.HasBinaryOffset() {
		t.Fatal("HasBinaryOffset should be false before any search runs")
	}

	data := []byte("needle one\n\x00needle two\n")
	sink := newRecordingSink()
	if err := s.SearchBytes("f", data, sink); err != nil {
		t.Fatal(err)
	}
	if !s.HasBinaryOffset() {
		t.Fatal("HasBinaryOffset should be true immediately after a SearchBytes call that found a NUL")
	}
	if want := int64(11); s.BinaryOffset() != want {
		t.Errorf("BinaryOffset() = %d, want %d", s.BinaryOffset(), want)
	}

	// A subsequent, NUL-free file must reset the live state, not leak
	// the previous file's binary detection.
	sink2 := newRecordingSink()
	if err := s.SearchBytes("g", []byte("needle three\n"), sink2); err != nil {
		t.Fatal(err)
	}
	if s.HasBinaryOffset() {
		t.Error("HasBinaryOffset should reset to false for a clean file")
	}
}

func equalStrings(a, b []string) bool {
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

func equalInt64s(a, b []int64) bool {
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
