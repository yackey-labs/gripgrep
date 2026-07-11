package engine

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yackey-labs/gripgrep/printer"
	"github.com/yackey-labs/gripgrep/search"
)

// collectSink is a minimal search.Sink that records nothing but its call
// counts -- enough to stand in for a real printer sink while the
// matchTracker above it does the stats accounting under test. more reports
// what Matched should return, so a test can simulate an early-abort sink
// (-l/-q) and confirm --stats forces the search to completion anyway.
type collectSink struct {
	more    bool
	matched int
}

func (s *collectSink) Begin(string) (bool, error) { return true, nil }
func (s *collectSink) Matched(*search.Match) (bool, error) {
	s.matched++
	return s.more, nil
}
func (s *collectSink) Context(*search.Ctx) (bool, error)  { return true, nil }
func (s *collectSink) Finish(string, *search.Stats) error { return nil }

// runStatsSearch drives one file's worth of data through a real
// search.Searcher wrapped in the production matchTracker with a live
// StatsAccumulator, returning the resulting snapshot. It's the same code
// path Run uses per file, minus the walk.
func runStatsSearch(t *testing.T, sr *search.Searcher, sink search.Sink, standard bool, path string, data []byte) statsResult {
	t.Helper()
	acc := NewStatsAccumulator()
	var matched atomic.Bool
	tr := &matchTracker{
		Sink:       sink,
		matched:    &matched,
		standard:   standard,
		binMode:    sr.BinaryMode,
		searcher:   sr,
		stats:      acc,
		statsStart: time.Now(),
		// A binary-convert file's Finish writes the "binary file matches"
		// message through dest; a discard dest keeps that off the test's
		// output while still exercising the real path.
		dest: printer.NewDest(io.Discard),
	}
	if err := sr.SearchBytes(path, data, tr); err != nil {
		t.Fatal(err)
	}
	return statsResult{acc.Snapshot(), matched.Load()}
}

type statsResult struct {
	snap    StatsSnapshot
	matched bool
}

// TestStatsAccumulator_OccurrencesVsLines pins the two counters rg
// distinguishes: "matches" counts pattern OCCURRENCES (multiple per line),
// "matched lines" counts emitted result lines. The fixture has a line with
// three occurrences, so a correct accounting reports 4 matches / 2 matched
// lines, never 2/2.
func TestStatsAccumulator_OccurrencesVsLines(t *testing.T) {
	m := newTestMatcher(t)
	sr := search.New(search.Searcher{Matcher: m})
	data := []byte("needle one\nneedle needle needle\nhay\n")

	got := runStatsSearch(t, sr, &collectSink{more: true}, true, "a.txt", data)
	if got.snap.Matches != 4 {
		t.Errorf("matches = %d, want 4 (1 + 3 occurrences)", got.snap.Matches)
	}
	if got.snap.MatchedLines != 2 {
		t.Errorf("matched lines = %d, want 2", got.snap.MatchedLines)
	}
	if got.snap.FilesSearched != 1 {
		t.Errorf("files searched = %d, want 1", got.snap.FilesSearched)
	}
	if got.snap.FilesWithMatch != 1 {
		t.Errorf("files with match = %d, want 1", got.snap.FilesWithMatch)
	}
	if got.snap.BytesSearched != int64(len(data)) {
		t.Errorf("bytes searched = %d, want %d", got.snap.BytesSearched, len(data))
	}
}

// TestStatsAccumulator_InvertZeroMatches pins the -v discriminator: an
// inverted (non-matching) line is delivered as a result, so it counts as a
// matched line, but the pattern does not occur on it -- so matches stays 0.
// This is exactly why rg reports "0 matches / N matched lines" under -v.
func TestStatsAccumulator_InvertZeroMatches(t *testing.T) {
	m := newTestMatcher(t)
	sr := search.New(search.Searcher{Matcher: m, Invert: true})
	data := []byte("needle one\nhay\nno match here\n")

	got := runStatsSearch(t, sr, &collectSink{more: true}, true, "a.txt", data)
	if got.snap.Matches != 0 {
		t.Errorf("matches = %d, want 0 (inverted lines carry no occurrences)", got.snap.Matches)
	}
	if got.snap.MatchedLines != 2 {
		t.Errorf("matched lines = %d, want 2 (the two non-'needle' lines)", got.snap.MatchedLines)
	}
	if !got.matched {
		t.Error("expected the file to register as matched (inverted output is a match signal)")
	}
}

// TestStatsAccumulator_ForcesFullSearchOnEarlyAbort is the regression guard
// for the -l/-q/--stats interaction: a sink that returns more=false after
// its first match (like FilesWithMatches) would, without the tracker's
// keepSearching override, truncate the counts to that first match. Under
// --stats the whole file must still be counted.
func TestStatsAccumulator_ForcesFullSearchOnEarlyAbort(t *testing.T) {
	m := newTestMatcher(t)
	sr := search.New(search.Searcher{Matcher: m})
	data := []byte("needle one\nneedle two\nneedle three\nhay\n")

	sink := &collectSink{more: false} // aborts after the first match
	got := runStatsSearch(t, sr, sink, false, "a.txt", data)
	if got.snap.Matches != 3 || got.snap.MatchedLines != 3 {
		t.Errorf("got %d matches / %d matched lines, want 3/3 -- early-abort sink truncated stats",
			got.snap.Matches, got.snap.MatchedLines)
	}
	if got.snap.BytesSearched != int64(len(data)) {
		t.Errorf("bytes searched = %d, want %d (must search the whole file, not stop at the first match)",
			got.snap.BytesSearched, len(data))
	}
}

// TestStatsAccumulator_BinaryTruncatesCount pins the fix for an explicitly
// named binary file (convert mode) with a match on each side of the NUL: rg
// stops counting at the binary-detection point, so only the pre-NUL match
// contributes, even though gg's convert path reads to EOF and delivers
// both. Expect 1 match / 1 matched line, not 2/2.
func TestStatsAccumulator_BinaryTruncatesCount(t *testing.T) {
	m := newTestMatcher(t)
	sr := search.New(search.Searcher{Matcher: m, BinaryMode: search.BinaryConvert})
	data := []byte("needle here\n\x00binary tail with needle\n")

	got := runStatsSearch(t, sr, &collectSink{more: true}, true, "bin.dat", data)
	if got.snap.Matches != 1 || got.snap.MatchedLines != 1 {
		t.Errorf("got %d matches / %d matched lines, want 1/1 (counting must stop at the NUL)",
			got.snap.Matches, got.snap.MatchedLines)
	}
}

// TestStatsAccumulator_ParallelMatchesSerial confirms the intra-file
// parallel replay path (searchBytesParallel) accumulates exactly the same
// counts as a serial scan of the same data -- the property the shared
// atomic accumulator and the replay loop's force-more interaction exist to
// guarantee.
func TestStatsAccumulator_ParallelMatchesSerial(t *testing.T) {
	m := newTestMatcher(t)
	// Build a multi-line fixture with a known occurrence total.
	var data []byte
	wantMatches := 0
	wantLines := 0
	for i := 0; i < 500; i++ {
		switch i % 3 {
		case 0:
			data = append(data, "needle needle\n"...)
			wantMatches += 2
			wantLines++
		case 1:
			data = append(data, "needle\n"...)
			wantMatches++
			wantLines++
		default:
			data = append(data, "no match here\n"...)
		}
	}

	serial := search.New(search.Searcher{Matcher: m})
	gotSerial := runStatsSearch(t, serial, &collectSink{more: true}, true, "a.txt", data)

	// Force intra-file chunking: several workers, tiny minimum size.
	parallel := search.New(search.Searcher{Matcher: m, ParallelWorkers: 4, ParallelMinBytes: 1})
	gotParallel := runStatsSearch(t, parallel, &collectSink{more: true}, true, "a.txt", data)

	if gotSerial.snap.Matches != int64(wantMatches) || gotSerial.snap.MatchedLines != int64(wantLines) {
		t.Fatalf("serial: got %d matches / %d lines, want %d/%d",
			gotSerial.snap.Matches, gotSerial.snap.MatchedLines, wantMatches, wantLines)
	}
	if gotParallel.snap.Matches != gotSerial.snap.Matches ||
		gotParallel.snap.MatchedLines != gotSerial.snap.MatchedLines ||
		gotParallel.snap.BytesSearched != gotSerial.snap.BytesSearched ||
		gotParallel.snap.FilesWithMatch != gotSerial.snap.FilesWithMatch {
		t.Errorf("parallel stats diverge from serial:\nserial=%+v\nparallel=%+v", gotSerial.snap, gotParallel.snap)
	}
}

// TestStatsAccumulator_ConcurrentAddsAreAtomic drives many goroutines into
// one accumulator (as cross-file workers do) and checks the totals are
// exact -- guarding the atomic counters against lost updates.
func TestStatsAccumulator_ConcurrentAddsAreAtomic(t *testing.T) {
	acc := NewStatsAccumulator()
	const workers, perWorker = 8, 1000
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				acc.AddMatchedLine(2)
				acc.AddFile(10, true, time.Millisecond)
			}
		}()
	}
	wg.Wait()

	snap := acc.Snapshot()
	if snap.Matches != int64(workers*perWorker*2) {
		t.Errorf("matches = %d, want %d", snap.Matches, workers*perWorker*2)
	}
	if snap.MatchedLines != int64(workers*perWorker) {
		t.Errorf("matched lines = %d, want %d", snap.MatchedLines, workers*perWorker)
	}
	if snap.FilesSearched != int64(workers*perWorker) {
		t.Errorf("files searched = %d, want %d", snap.FilesSearched, workers*perWorker)
	}
	if snap.FilesWithMatch != int64(workers*perWorker) {
		t.Errorf("files with match = %d, want %d", snap.FilesWithMatch, workers*perWorker)
	}
	if snap.BytesSearched != int64(workers*perWorker*10) {
		t.Errorf("bytes searched = %d, want %d", snap.BytesSearched, workers*perWorker*10)
	}
}

// TestStatsAccumulator_NilIsZeroCost documents the zero-cost-when-off
// mechanism: a matchTracker with a nil accumulator does no stats work and
// leaves the sink's own more value untouched (no forced full search), so
// the default no-stats path pays nothing.
func TestStatsAccumulator_NilIsZeroCost(t *testing.T) {
	m := newTestMatcher(t)
	sr := search.New(search.Searcher{Matcher: m})
	data := []byte("needle one\nneedle two\n")

	sink := &collectSink{more: false} // early abort must be honored when stats off
	var matched atomic.Bool
	tr := &matchTracker{Sink: sink, matched: &matched, standard: true, searcher: sr, stats: nil}
	if err := sr.SearchBytes("a.txt", data, tr); err != nil {
		t.Fatal(err)
	}
	if sink.matched != 1 {
		t.Errorf("sink saw %d matches, want 1 -- nil accumulator must not force a full search", sink.matched)
	}
}
