package search

import (
	"bytes"
	"sync"
)

// defaultParallelMinBytes is the minimum input length SearchBytes will
// consider parallelizing when ParallelMinBytes is left at zero, per
// PLAN.md's intra-file parallelism row (">~64MB").
const defaultParallelMinBytes = 64 << 20

// parallelEligible reports whether SearchBytes should split n bytes of
// input into concurrent chunks rather than scan it in one pass.
//
// v1 (M3 task #18) only covers the no-context, non-invert case:
// BeforeContext/AfterContext both zero and Invert false. With no context
// requested, a match's output is exactly its own line -- never anything
// from a neighboring line -- so line-aligned chunks are strictly
// non-overlapping and need no boundary reconciliation at all. Context
// support requires each chunk to borrow a few lines from its neighbors
// (to correctly compute before/after context for a match near a chunk
// boundary) plus a merge-time dedup pass to avoid double-emitting a
// shared boundary line -- a real, sound design, but enough surface area
// that it's deliberately deferred to a follow-up rather than landed in
// the same change as the base parallel path. Invert is deferred
// separately: its "gap between matches" bookkeeping (matchByLineFastInvert)
// interacts with an artificial chunk boundary in ways that need their own
// dedicated reasoning, not an extension of the context design.
func (s *Searcher) parallelEligible(n int) bool {
	if s.ParallelWorkers <= 1 {
		return false
	}
	if s.Invert || s.BeforeContext > 0 || s.AfterContext > 0 {
		return false
	}
	minBytes := s.ParallelMinBytes
	if minBytes <= 0 {
		minBytes = defaultParallelMinBytes
	}
	return int64(n) >= minBytes
}

// chunkRange is a half-open byte range [start, end) within the slice
// being split.
type chunkRange struct{ start, end int }

// splitChunks divides data into up to n line-aligned, non-overlapping
// chunkRanges covering the whole slice. Every boundary other than 0 and
// len(data) is snapped forward to just past the next newline at or after
// its target position, so no chunk ever splits a line in two. Degenerates
// gracefully to fewer than n chunks (down to a single chunkRange covering
// everything) when data has too few newlines to support n even divisions
// -- callers must not assume len(result) == n.
func splitChunks(data []byte, n int) []chunkRange {
	if n < 1 {
		n = 1
	}
	if len(data) == 0 || n == 1 {
		return []chunkRange{{0, len(data)}}
	}

	target := len(data) / n
	if target == 0 {
		target = 1
	}

	var bounds []int
	pos := 0
	for i := 1; i < n; i++ {
		want := i * target
		if want <= pos || want >= len(data) {
			continue
		}
		idx := bytes.IndexByte(data[want:], lineTerm)
		if idx < 0 {
			// No newline at or after want: everything from pos onward
			// becomes the final chunk, so stop looking for more splits.
			break
		}
		at := want + idx + 1
		if at <= pos || at >= len(data) {
			continue
		}
		bounds = append(bounds, at)
		pos = at
	}

	ranges := make([]chunkRange, 0, len(bounds)+1)
	prev := 0
	for _, b := range bounds {
		ranges = append(ranges, chunkRange{prev, b})
		prev = b
	}
	ranges = append(ranges, chunkRange{prev, len(data)})
	return ranges
}

// recordedKind distinguishes a matched line from a context line in a
// chunkRecorder's captured events.
type recordedKind uint8

const (
	recordedMatch recordedKind = iota
	recordedContext
)

// recordedEvent is an owned-copy snapshot of one Matched or Context call,
// captured by chunkRecorder for later replay through the caller's real
// Sink. Match/Ctx's own Line field is only valid for the duration of the
// call (see their doc), so it must be copied here, not referenced.
type recordedEvent struct {
	kind          recordedKind
	line          []byte
	offset        int64
	lineNumber    int64
	hasLineNumber bool
	after         bool // meaningful only when kind == recordedContext
}

// chunkRecorder implements Sink by capturing every Matched/Context call
// as a recordedEvent instead of acting on it immediately. Each intra-file
// parallel chunk's private Searcher writes into its own chunkRecorder;
// the parallel orchestrator (searchBytesParallel) then replays every
// chunk's recorded events, in chunk order, through the caller's real Sink
// -- reproducing the exact Begin/Matched/Context/Finish stream order a
// serial scan would have produced, while the expensive matching work
// itself ran concurrently.
//
// chunkRecorder always reports more=true, deliberately never honoring an
// early-stop request (-q/-m): a chunk has no way to know whether an
// earlier chunk's replay will end the search first, so every chunk always
// runs to completion. Early-stop semantics are applied only during
// replay, which is what actually observes the caller's real Sink
// returning more=false and byte-for-byte reproduces where serial would
// have stopped -- see searchBytesParallel. The tradeoff is some wasted
// work in chunks whose results end up discarded; it is never a
// correctness problem.
//
// v1 records every event uninspected, including a full copy of Line, for
// every mode (Standard, Count, List, ...) alike -- package search has no
// visibility into which downstream mode actually needs the bytes (that
// distinction lives in cmd/gg's printer layer, which search intentionally
// doesn't import). This is known to cost more memory than necessary for
// a count-only invocation (e.g. `-c` of a common token across a 1GB file
// buffers every matched line's bytes across every chunk before replay,
// where a real Count sink would have discarded them immediately); left
// as a documented v1 limitation rather than plumbing a mode hint through
// package search, which would leak a cmd/gg-layer concern into a
// library package.
type chunkRecorder struct {
	events     []recordedEvent
	matched    bool
	matchCount int64
}

func (r *chunkRecorder) Begin(string) (bool, error) { return true, nil }

func (r *chunkRecorder) Matched(m *Match) (bool, error) {
	r.events = append(r.events, recordedEvent{
		kind:          recordedMatch,
		line:          append([]byte(nil), m.Line...),
		offset:        m.Offset,
		lineNumber:    m.LineNumber,
		hasLineNumber: m.HasLineNumber,
	})
	r.matched = true
	r.matchCount++
	return true, nil
}

func (r *chunkRecorder) Context(c *Ctx) (bool, error) {
	r.events = append(r.events, recordedEvent{
		kind:          recordedContext,
		line:          append([]byte(nil), c.Line...),
		offset:        c.Offset,
		lineNumber:    c.LineNumber,
		hasLineNumber: c.HasLineNumber,
		after:         c.After,
	})
	return true, nil
}

func (r *chunkRecorder) Finish(string, *Stats) error { return nil }

// searchBytesParallel implements the parallel side of SearchBytes for
// eligible inputs (see parallelEligible): it splits data into line-aligned
// chunks, searches each with its own private Searcher concurrently, then
// replays every chunk's recorded events through sink in chunk order.
//
// It leaves s.hasMatched/s.matchCount set to the merged (post-replay)
// results, mirroring what runChunk leaves them as for the serial path --
// SearchBytes builds its final Stats from those fields uniformly,
// regardless of which path ran.
//
// Binary detection is untouched here: SearchBytes already performed its
// one upfront bounded-prefix check before deciding whether to
// parallelize, and each per-chunk child below is constructed with
// BinaryMode=BinaryNone specifically so it never redoes or interacts with
// that state.
//
// Line numbers (-n) do NOT get a dedicated up-front bytes.Count pass over
// every chunk to seed absolute starting line bases -- an earlier version
// did exactly that, and profiling showed it cost MORE CPU time than the
// actual literal search itself (bytes.Count touches every byte in the
// file with no way to skip ahead, unlike a rare-byte literal scan), while
// also duplicating work: each child's own matchByLine already lazily
// counts newlines up to every match it reports (core.go's countLines).
// Instead, each child scans using RELATIVE line numbers (as if it were
// its own whole file, lineBase always 1), and after its scan finishes,
// one cheap catch-up countLines call to its own chunk's end yields that
// chunk's total line count -- the exact same total lazy counting would
// have reached anyway, just finished off in one call instead of stopping
// at the last match. Only after every chunk's total is known (all of
// them computed concurrently, not gated behind a separate serial or
// parallel pre-pass) do the true absolute starting line numbers -- a
// trivial O(workers) prefix sum -- get computed, and applied as a flat
// per-chunk offset added to each recorded event's relative line number
// during replay.
func (s *Searcher) searchBytesParallel(data []byte, sink Sink) error {
	chunks := splitChunks(data, s.ParallelWorkers)
	if len(chunks) <= 1 {
		return s.runChunk(data, sink, 0, 1)
	}

	recs := make([]*chunkRecorder, len(chunks))
	chunkLines := make([]int64, len(chunks)) // total newline count per chunk; only meaningful when s.LineNumbers
	errs := make([]error, len(chunks))
	var wg sync.WaitGroup
	for i, c := range chunks {
		wg.Add(1)
		go func(i int, c chunkRange) {
			defer wg.Done()
			child := &Searcher{
				Matcher:     s.Matcher,
				LineNumbers: s.LineNumbers,
				BinaryMode:  BinaryNone,
			}
			rec := &chunkRecorder{}
			recs[i] = rec
			chunkData := data[c.start:c.end]
			// lineBase is always 1 here (relative to this chunk, not the
			// whole file) -- see the doc above for why, and how the
			// correct absolute value gets applied later, at replay time.
			errs[i] = child.runChunk(chunkData, rec, int64(c.start), 1)
			if s.LineNumbers && errs[i] == nil {
				child.countLines(chunkData, len(chunkData))
				chunkLines[i] = child.lineNumber - 1
			}
		}(i, c)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	lineBases := make([]int64, len(chunks))
	if s.LineNumbers {
		base := int64(1)
		for i, n := range chunkLines {
			lineBases[i] = base
			base += n
		}
	}

	s.hasMatched = false
	s.matchCount = 0
	for _, rec := range recs {
		if rec.matched {
			s.hasMatched = true
		}
		s.matchCount += rec.matchCount
	}

	for i, rec := range recs {
		offset := lineBases[i] - 1
		for _, ev := range rec.events {
			if ev.hasLineNumber {
				ev.lineNumber += offset
			}
			more, err := s.replay(sink, ev)
			if err != nil {
				return err
			}
			if !more {
				return nil
			}
		}
	}
	return nil
}

// replay delivers one recorded event to sink via s's own scratch Match/Ctx
// structs, mirroring sinkMatched/sinkContext's contract (core.go) but
// sourcing every field from a recordedEvent instead of a live buffer
// position.
func (s *Searcher) replay(sink Sink, ev recordedEvent) (bool, error) {
	if ev.kind == recordedMatch {
		m := &s.matchScratch
		m.Line = ev.line
		m.Offset = ev.offset
		m.LineNumber = ev.lineNumber
		m.HasLineNumber = ev.hasLineNumber
		return sink.Matched(m)
	}
	c := &s.ctxScratch
	c.Line = ev.line
	c.Offset = ev.offset
	c.LineNumber = ev.lineNumber
	c.HasLineNumber = ev.hasLineNumber
	c.After = ev.after
	return sink.Context(c)
}
