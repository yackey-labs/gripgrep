package engine

import (
	"sync/atomic"
	"time"
)

// StatsAccumulator aggregates the counters rg's --stats block reports,
// summed across every file the walk searches and every parallel worker
// that searches them. A single accumulator is shared by every per-file
// matchTracker (see Run), which is why every field is an atomic: workers
// running concurrently all add into the same instance, exactly like rg's
// own Mutex<Stats> in its parallel search path.
//
// The accumulator is created only when --stats is requested; when it is
// nil, matchTracker skips every stats update entirely, so the feature is
// zero-cost on the default (no --stats) path -- no counting, no per-file
// timing, no per-line occurrence re-scan.
//
// The counters mirror grep-printer's Stats exactly (see the rg source at
// crates/printer/src/stats.rs):
//
//   - matches: total pattern OCCURRENCES (multiple per line possible; an
//     inverted -v line contributes 0, since the pattern does not occur on
//     it, which is why -v reports "0 matches" but a nonzero "matched
//     lines" -- see AddMatchedLine).
//   - matchedLines: total emitted result lines (one per Matched call).
//   - filesWithMatch: files that produced at least one match ("files
//     contained matches").
//   - filesSearched: files actually searched (one per Finish).
//   - bytesSearched: total file content bytes scanned.
//   - searchNanos: summed per-file search wall time, reported as the
//     "seconds spent searching" line (nondeterministic; e2e tests
//     normalize it).
type StatsAccumulator struct {
	matches        atomic.Int64
	matchedLines   atomic.Int64
	filesWithMatch atomic.Int64
	filesSearched  atomic.Int64
	bytesSearched  atomic.Int64
	searchNanos    atomic.Int64
}

// NewStatsAccumulator returns a fresh, all-zero accumulator.
func NewStatsAccumulator() *StatsAccumulator {
	return &StatsAccumulator{}
}

// AddMatchedLine records one emitted matched result line carrying the given
// number of pattern occurrences (occurrences may be 0 for an inverted line
// -- see the type doc). It is the counterpart of grep-printer's
// add_matches + add_matched_lines, called once per Sink.Matched.
func (a *StatsAccumulator) AddMatchedLine(occurrences int) {
	a.matches.Add(int64(occurrences))
	a.matchedLines.Add(1)
}

// AddFile records one searched file: its content byte count, whether it
// matched, and how long searching it took. Called once per Sink.Finish.
func (a *StatsAccumulator) AddFile(bytesSearched int64, matched bool, elapsed time.Duration) {
	a.filesSearched.Add(1)
	a.bytesSearched.Add(bytesSearched)
	if matched {
		a.filesWithMatch.Add(1)
	}
	a.searchNanos.Add(int64(elapsed))
}

// StatsSnapshot is a consistent-enough read of an accumulator's counters
// for rendering the final --stats block, taken after the walk has fully
// completed (so no worker is still writing). The two timing lines rg
// prints are supplied by the caller, not carried here: SearchTime is this
// snapshot's summed per-file time, while the "seconds total" line is the
// caller's own wall-clock measurement of the whole search phase.
type StatsSnapshot struct {
	Matches        int64
	MatchedLines   int64
	FilesWithMatch int64
	FilesSearched  int64
	BytesSearched  int64
	SearchTime     time.Duration
}

// Snapshot reads every counter. Callers must only invoke it once the walk
// has returned, when no worker goroutine can still be adding.
func (a *StatsAccumulator) Snapshot() StatsSnapshot {
	return StatsSnapshot{
		Matches:        a.matches.Load(),
		MatchedLines:   a.matchedLines.Load(),
		FilesWithMatch: a.filesWithMatch.Load(),
		FilesSearched:  a.filesSearched.Load(),
		BytesSearched:  a.bytesSearched.Load(),
		SearchTime:     time.Duration(a.searchNanos.Load()),
	}
}
