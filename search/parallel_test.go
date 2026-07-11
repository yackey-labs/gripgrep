package search

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/yackey-labs/gripgrep/match"
)

// TestParallelEligiblePassThruDisqualifies covers --passthru:
// it must fall back to the serial path exactly like Invert/context
// already do, since chunkRecorder's per-chunk run-to-completion-then-
// replay design can't reproduce PassThru's -m interaction (a single
// Searcher's matchLimitReached evolving continuously in scan order --
// see parallelEligible's doc). An otherwise-eligible input (large enough,
// ParallelWorkers>1, no invert/context) must still refuse to parallelize
// once PassThru is set.
func TestParallelEligiblePassThruDisqualifies(t *testing.T) {
	base := Searcher{ParallelWorkers: 4, ParallelMinBytes: 1}
	eligible := New(base)
	if !eligible.parallelEligible(1024) {
		t.Fatal("sanity: base config (no PassThru) should be parallel-eligible")
	}

	withPassThru := base
	withPassThru.PassThru = true
	s := New(withPassThru)
	if s.parallelEligible(1024) {
		t.Fatal("parallelEligible(PassThru=true) = true, want false")
	}
}

// TestSplitChunksInvariants checks splitChunks's structural contract
// directly, independent of any Searcher: ranges must cover [0, len(data))
// contiguously with no gaps or overlaps, and no range boundary (other
// than the final len(data)) may fall mid-line.
func TestSplitChunksInvariants(t *testing.T) {
	mkData := func(lines int) []byte {
		var b []byte
		for i := 0; i < lines; i++ {
			b = append(b, []byte(fmt.Sprintf("line-%d-filler-filler\n", i))...)
		}
		return b
	}

	cases := []struct {
		name string
		data []byte
		n    int
	}{
		{"empty", nil, 4},
		{"n=1", mkData(20), 1},
		{"n=0_clamped_to_1", mkData(20), 0},
		{"more_workers_than_lines", mkData(3), 16},
		{"exact_line_count", mkData(64), 8},
		{"single_line_no_terminator", []byte("no newline at all"), 4},
		{"single_line_with_terminator", []byte("just one line\n"), 4},
		{"many_short_lines", mkData(5000), 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ranges := splitChunks(c.data, c.n)
			if len(ranges) == 0 {
				t.Fatal("splitChunks returned no ranges")
			}
			if ranges[0].start != 0 {
				t.Errorf("first range must start at 0, got %d", ranges[0].start)
			}
			if got := ranges[len(ranges)-1].end; got != len(c.data) {
				t.Errorf("last range must end at len(data)=%d, got %d", len(c.data), got)
			}
			for i, r := range ranges {
				if r.start > r.end {
					t.Fatalf("range %d: start %d > end %d", i, r.start, r.end)
				}
				if i > 0 && r.start != ranges[i-1].end {
					t.Fatalf("range %d starts at %d, want %d (previous range's end, contiguous)", i, r.start, ranges[i-1].end)
				}
				// Every boundary except the very last must land just after
				// a newline (or at 0), i.e. never split a line in two.
				if r.end != len(c.data) && r.end > 0 && c.data[r.end-1] != defaultLineTerm {
					t.Errorf("range %d ends at %d, which is not just after a newline (mid-line split)", i, r.end)
				}
			}
		})
	}
}

// parallelInvariance is the shared serial-vs-parallel comparison used by
// both the table-driven test and the fuzz target below: it runs data
// through a serial Searcher (ParallelWorkers=0) and a parallel one
// (ParallelWorkers=workers, ParallelMinBytes=1 so even tiny fixtures
// force real chunking), both with LineNumbers on, and fails t if the
// recorded event streams or final Stats differ in any way visible to a
// Sink -- the correctness gate task #18 was assigned under: "serial vs
// N-worker event streams byte-identical."
func parallelInvariance(t *testing.T, data []byte, pattern string, workers int) {
	t.Helper()

	// literalMatcher's FindCandidate always reports Confirmed (it has no
	// candidate/verify distinction to fall back on), which is only a
	// faithful stand-in for a real match.Matcher's NonMatchingLineTerm()
	// guarantee when pattern itself cannot straddle a line terminator --
	// a real Matcher never reports NonMatchingLineTerm=true for a pattern
	// that can match across '\n' (that's exactly what the fast path's
	// "trust a Confirmed hit without checking its enclosing line"
	// contract depends on). A fuzzer will happily generate patterns
	// containing a literal '\n' byte; treating those as fastPath=true
	// here would fabricate a divergence that isn't a real bug in either
	// engine, just an unfaithful test double.
	fastPath := !strings.Contains(pattern, "\n")

	serial := New(Searcher{
		Matcher:     literalMatcher(pattern, fastPath),
		LineNumbers: true,
	})
	serialSink := newRecordingSink()
	if err := serial.SearchBytes("f", data, serialSink); err != nil {
		t.Fatalf("serial SearchBytes: %v", err)
	}

	parallel := New(Searcher{
		Matcher:          literalMatcher(pattern, fastPath),
		LineNumbers:      true,
		ParallelWorkers:  workers,
		ParallelMinBytes: 1,
	})
	parallelSink := newRecordingSink()
	if err := parallel.SearchBytes("f", data, parallelSink); err != nil {
		t.Fatalf("parallel(%d) SearchBytes: %v", workers, err)
	}

	if len(serialSink.events) != len(parallelSink.events) {
		t.Fatalf("workers=%d: event count mismatch: serial=%d parallel=%d\nserial: %+v\nparallel: %+v",
			workers, len(serialSink.events), len(parallelSink.events), serialSink.events, parallelSink.events)
	}
	for i := range serialSink.events {
		se, pe := serialSink.events[i], parallelSink.events[i]
		if se != pe {
			t.Fatalf("workers=%d: event %d mismatch:\nserial:   %+v\nparallel: %+v", workers, i, se, pe)
		}
	}

	if *serialSink.finishStats != *parallelSink.finishStats {
		t.Fatalf("workers=%d: Stats mismatch:\nserial:   %+v\nparallel: %+v", workers, *serialSink.finishStats, *parallelSink.finishStats)
	}
}

// TestSearchBytesParallelMatchesSerial is task #18's correctness gate:
// serial and parallel (at several worker counts) must produce identical
// event streams and Stats across a range of shapes -- no matches, every
// line matching, matches landing exactly on likely chunk boundaries,
// single-line/empty inputs, and a larger realistic mix.
func TestSearchBytesParallelMatchesSerial(t *testing.T) {
	mkLines := func(n int, matchEvery int) []byte {
		var b strings.Builder
		for i := 0; i < n; i++ {
			if matchEvery > 0 && i%matchEvery == 0 {
				fmt.Fprintf(&b, "needle line %d filler filler filler\n", i)
			} else {
				fmt.Fprintf(&b, "filler filler filler filler %d\n", i)
			}
		}
		return []byte(b.String())
	}

	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"single_line_no_match", []byte("just filler, no hits\n")},
		{"single_line_match", []byte("needle right here\n")},
		{"no_trailing_newline", []byte("needle here with no newline at eof")},
		{"no_matches_at_all", mkLines(500, 0)},
		{"every_line_matches", mkLines(500, 1)},
		{"sparse_matches", mkLines(2000, 37)},
		{"dense_matches", mkLines(2000, 3)},
		{"tiny_two_lines", []byte("needle\nfiller\n")},
	}
	workerCounts := []int{2, 4, 8}

	for _, c := range cases {
		for _, w := range workerCounts {
			t.Run(fmt.Sprintf("%s/workers=%d", c.name, w), func(t *testing.T) {
				parallelInvariance(t, c.data, "needle", w)
			})
		}
	}
}

// TestSearchBytesParallelByteOffsetAcrossChunks is the dedicated
// gate for -b/--byte-offset's parallel-path correctness: Match.Offset
// was already an existing field before this round (populated via
// s.absOffsetBase, which searchBytesParallel already seeds from each
// chunk's own absolute start -- see runChunk's callers in
// searchBytesParallel: `child.runChunk(chunkData, rec, int64(c.start),
// 1)`), so parallelInvariance's existing whole-event equality check
// (which includes offset) already covers serial-vs-parallel agreement.
// This test adds a STRONGER, independent check that doesn't just compare
// the two paths to each other (which would pass even if both were wrong
// the same way): every recorded match's Offset, from BOTH the serial and
// the parallel run, must point at the exact byte in the original data
// slice where that match's own line actually starts -- i.e. Offset is
// verified against ground truth, not just cross-checked between paths.
//
// Uses enough data and a low ParallelMinBytes to guarantee several
// worker chunks, so at least one matched line's absolute offset can only
// be correct if the chunk-relative offset was properly rebased by that
// chunk's own start position (a bug here would show up as offsets that
// are systematically low by one or more chunks' worth of bytes for
// every match after the first chunk).
func TestSearchBytesParallelByteOffsetAcrossChunks(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		if i%17 == 0 {
			fmt.Fprintf(&b, "needle line %d filler filler filler filler filler\n", i)
		} else {
			fmt.Fprintf(&b, "filler filler filler filler filler filler %d\n", i)
		}
	}
	data := []byte(b.String())

	checkOffsets := func(t *testing.T, sink *recordingSink) {
		t.Helper()
		matches := 0
		for _, e := range sink.events {
			if e.kind != "match" {
				continue
			}
			matches++
			if e.offset < 0 || e.offset+int64(len(e.line)) > int64(len(data)) {
				t.Fatalf("offset %d + len %d out of range for data of length %d", e.offset, len(e.line), len(data))
			}
			got := string(data[e.offset : e.offset+int64(len(e.line))])
			if got != e.line {
				t.Fatalf("data[offset:offset+len(line)] = %q, want %q (offset=%d)", got, e.line, e.offset)
			}
		}
		if matches == 0 {
			t.Fatal("expected at least one match to actually verify offsets against")
		}
	}

	for _, w := range []int{2, 4, 8} {
		t.Run(fmt.Sprintf("workers=%d", w), func(t *testing.T) {
			serial := New(Searcher{Matcher: literalMatcher("needle", true), LineNumbers: true})
			serialSink := newRecordingSink()
			if err := serial.SearchBytes("f", data, serialSink); err != nil {
				t.Fatalf("serial SearchBytes: %v", err)
			}
			checkOffsets(t, serialSink)

			parallel := New(Searcher{
				Matcher:          literalMatcher("needle", true),
				LineNumbers:      true,
				ParallelWorkers:  w,
				ParallelMinBytes: 1,
			})
			parallelSink := newRecordingSink()
			if err := parallel.SearchBytes("f", data, parallelSink); err != nil {
				t.Fatalf("parallel(%d) SearchBytes: %v", w, err)
			}
			checkOffsets(t, parallelSink)

			// Belt and suspenders: the two paths must also agree with each
			// other exactly, per parallelInvariance's usual contract.
			parallelInvariance(t, data, "needle", w)
		})
	}
}

// TestSearchBytesParallelEarlyStopMatchesSerial covers -q/-m-style early
// exit (Sink.Matched returning more=false): even though every chunk
// always runs to completion internally (chunkRecorder never honors
// early-stop -- see its doc), the REPLAYED event sequence delivered to
// the real sink must stop at exactly the same point serial would have,
// since replay is what actually observes more=false.
func TestSearchBytesParallelEarlyStopMatchesSerial(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		if i%5 == 0 {
			fmt.Fprintf(&b, "needle line %d\n", i)
		} else {
			fmt.Fprintf(&b, "filler %d\n", i)
		}
	}
	data := []byte(b.String())

	for _, w := range []int{2, 4, 8} {
		t.Run(fmt.Sprintf("workers=%d", w), func(t *testing.T) {
			serial := New(Searcher{Matcher: literalMatcher("needle", true), LineNumbers: true})
			serialSink := newRecordingSink()
			serialSink.stopAfter = 3
			if err := serial.SearchBytes("f", data, serialSink); err != nil {
				t.Fatal(err)
			}

			parallel := New(Searcher{
				Matcher:          literalMatcher("needle", true),
				LineNumbers:      true,
				ParallelWorkers:  w,
				ParallelMinBytes: 1,
			})
			parallelSink := newRecordingSink()
			parallelSink.stopAfter = 3
			if err := parallel.SearchBytes("f", data, parallelSink); err != nil {
				t.Fatal(err)
			}

			if len(serialSink.events) != len(parallelSink.events) {
				t.Fatalf("event count mismatch: serial=%d parallel=%d", len(serialSink.events), len(parallelSink.events))
			}
			for i := range serialSink.events {
				if serialSink.events[i] != parallelSink.events[i] {
					t.Fatalf("event %d mismatch:\nserial:   %+v\nparallel: %+v", i, serialSink.events[i], parallelSink.events[i])
				}
			}
			if serialSink.matchCount() != 3 {
				t.Fatalf("test setup: expected serial to stop after 3 matches, got %d", serialSink.matchCount())
			}
			// Round-31 fix: matchCount/hasMatched used to be derived from
			// the RAW per-chunk totals (every match any child ever
			// recorded, regardless of where replay actually stopped),
			// which diverged from serial's own Stats here (serial's
			// matchCount only ever counts up to wherever Matched last
			// returned more=true) -- this was a real, previously
			// untested latent inconsistency between the two paths.
			if *serialSink.finishStats != *parallelSink.finishStats {
				t.Fatalf("workers=%d: Stats mismatch:\nserial:   %+v\nparallel: %+v", w, *serialSink.finishStats, *parallelSink.finishStats)
			}
		})
	}
}

// TestSearchBytesParallelMaxCountMatchesSerial is round 31's extension of
// the serial-vs-parallel correctness gate to cover -m/--max-count: the
// cap is applied entirely at REPLAY time (every child searches its own
// chunk to completion, unbounded -- see searchBytesParallel's doc), so
// this specifically exercises that replay-time cutoff against the
// serial path's matchLimitReached machinery, including event streams
// AND final Stats (MatchCount/Matched must reflect the CAPPED count,
// not the raw per-chunk total).
func TestSearchBytesParallelMaxCountMatchesSerial(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		if i%3 == 0 {
			fmt.Fprintf(&b, "needle line %d\n", i)
		} else {
			fmt.Fprintf(&b, "filler %d\n", i)
		}
	}
	data := []byte(b.String())

	for _, maxCount := range []int{0, 1, 5, 50, 1_000_000} {
		for _, w := range []int{2, 4, 8} {
			t.Run(fmt.Sprintf("m=%d/workers=%d", maxCount, w), func(t *testing.T) {
				mc := maxCount
				serial := New(Searcher{
					Matcher:     literalMatcher("needle", true),
					LineNumbers: true,
					MaxCount:    &mc,
				})
				serialSink := newRecordingSink()
				if err := serial.SearchBytes("f", data, serialSink); err != nil {
					t.Fatal(err)
				}

				parallel := New(Searcher{
					Matcher:          literalMatcher("needle", true),
					LineNumbers:      true,
					MaxCount:         &mc,
					ParallelWorkers:  w,
					ParallelMinBytes: 1,
				})
				parallelSink := newRecordingSink()
				if err := parallel.SearchBytes("f", data, parallelSink); err != nil {
					t.Fatal(err)
				}

				if len(serialSink.events) != len(parallelSink.events) {
					t.Fatalf("event count mismatch: serial=%d parallel=%d\nserial: %+v\nparallel: %+v",
						len(serialSink.events), len(parallelSink.events), serialSink.events, parallelSink.events)
				}
				for i := range serialSink.events {
					if serialSink.events[i] != parallelSink.events[i] {
						t.Fatalf("event %d mismatch:\nserial:   %+v\nparallel: %+v", i, serialSink.events[i], parallelSink.events[i])
					}
				}
				if got, want := serialSink.matchCount(), min(maxCount, 667); got != want {
					t.Fatalf("test setup: serial matchCount = %d, want %d", got, want)
				}
				if *serialSink.finishStats != *parallelSink.finishStats {
					t.Fatalf("workers=%d: Stats mismatch:\nserial:   %+v\nparallel: %+v", w, *serialSink.finishStats, *parallelSink.finishStats)
				}
			})
		}
	}
}

// FuzzSearchBytesParallelMatchesSerial is the fuzz-driven half of the
// correctness gate: random line-oriented data and a random short pattern,
// checked against the same serial oracle at worker counts 2/4/8. Small
// ParallelMinBytes (set inside parallelInvariance) means even the tiny
// inputs a fuzzer naturally generates force real chunking, maximizing
// boundary crossings relative to input size.
func FuzzSearchBytesParallelMatchesSerial(f *testing.F) {
	f.Add([]byte("needle\nfiller\nneedle needle\n\nneedle\n"), "needle")
	f.Add([]byte("no matches here at all\njust filler\n"), "needle")
	f.Add([]byte(""), "needle")
	f.Add([]byte("needle"), "needle")
	f.Add([]byte("aaaaaaaaaaaaaaaaaaaa\n"), "a")

	f.Fuzz(func(t *testing.T, data []byte, pattern string) {
		if pattern == "" {
			t.Skip()
		}
		for _, w := range []int{2, 4, 8} {
			parallelInvariance(t, data, pattern, w)
		}
	})
}

// TestSearchBytesParallelRealMultiLiteralMatcherIsRaceSafe exercises the
// production match.Matcher implementations (not the search-package-local
// literalMatcher fake) through the parallel path under -race, at both the
// small (rareByteMultiScanner) and large (ahoCorasickScanner) multi-literal
// thresholds. This is the specific check requested when reviewing
// searchBytesParallel's design: every chunk's private child Searcher
// shares the SAME outer Matcher instance (see searchBytesParallel's
// `Matcher: s.Matcher`), so if either scanner kept any of its scan state
// as a struct field (rather than call-local, as both actually do --
// rareByteMultiScanner.find's monotonic-sweep `next` array is a local
// variable, and ahoCorasickScanner wraps a third-party AhoCorasick value
// whose IterByte has a value receiver and allocates a fresh iterator per
// call), concurrent chunks calling FindCandidate/Verify on the one shared
// Matcher would race and could corrupt results. -race is the right tool
// to confirm this empirically, not just by reading the source.
func TestSearchBytesParallelRealMultiLiteralMatcherIsRaceSafe(t *testing.T) {
	mkText := func(words []string, lines int) []byte {
		var b strings.Builder
		for i := 0; i < lines; i++ {
			fmt.Fprintf(&b, "line %d: %s filler filler filler\n", i, words[i%len(words)])
		}
		return []byte(b.String())
	}

	cases := []struct {
		name     string
		patterns []string
		words    []string
	}{
		// <=8 literals: routes through rareByteMultiScanner.
		{"small_set", []string{"Sherlock", "Watson"}, []string{"Sherlock Holmes", "Doctor Watson", "neither name here", "just filler text"}},
		// >8 literals: routes through ahoCorasickScanner.
		{"large_set", []string{"aa", "bb", "cc", "dd", "ee", "ff", "gg", "hh", "ii", "jj"},
			[]string{"has an aa in it", "has a bb here", "cc and dd both", "no match at all", "ee ff gg hh ii jj all here"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := mkText(c.words, 4000)

			m, err := match.New(match.Config{Patterns: c.patterns, Fixed: true})
			if err != nil {
				t.Fatalf("match.New: %v", err)
			}

			// Same Matcher instance used to build BOTH the serial and
			// parallel Searchers, mirroring production exactly
			// (searchBytesParallel shares s.Matcher across every child).
			serial := New(Searcher{Matcher: m, LineNumbers: true})
			serialSink := newRecordingSink()
			if err := serial.SearchBytes("f", data, serialSink); err != nil {
				t.Fatalf("serial: %v", err)
			}

			parallel := New(Searcher{
				Matcher:          m,
				LineNumbers:      true,
				ParallelWorkers:  8,
				ParallelMinBytes: 1,
			})
			parallelSink := newRecordingSink()
			if err := parallel.SearchBytes("f", data, parallelSink); err != nil {
				t.Fatalf("parallel: %v", err)
			}

			if len(serialSink.events) != len(parallelSink.events) {
				t.Fatalf("event count mismatch: serial=%d parallel=%d", len(serialSink.events), len(parallelSink.events))
			}
			for i := range serialSink.events {
				if serialSink.events[i] != parallelSink.events[i] {
					t.Fatalf("event %d mismatch:\nserial:   %+v\nparallel: %+v", i, serialSink.events[i], parallelSink.events[i])
				}
			}
			if serialSink.matchCount() == 0 {
				t.Fatal("test setup: expected at least one match")
			}
		})
	}
}

// TestSearchBytesParallelRandomizedShapes complements the fuzz target
// with a seeded, repeatable randomized sweep across many shapes and all
// three worker counts in one deterministic run (useful in -race CI
// without relying on fuzz corpus state).
func TestSearchBytesParallelRandomizedShapes(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	words := []string{"needle", "filler", "x", "aaa"}
	for trial := 0; trial < 200; trial++ {
		var b strings.Builder
		lines := rng.Intn(50)
		for i := 0; i < lines; i++ {
			wordsInLine := rng.Intn(4)
			for j := 0; j < wordsInLine; j++ {
				if j > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(words[rng.Intn(len(words))])
			}
			b.WriteByte('\n')
		}
		data := []byte(b.String())
		for _, w := range []int{2, 4, 8} {
			parallelInvariance(t, data, "needle", w)
		}
	}
}
