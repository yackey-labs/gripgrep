package search

import (
	"bytes"
	"io"

	"github.com/yackey-labs/gripgrep/match"
)

// BinaryMode controls how a Searcher reacts to NUL bytes in a file.
type BinaryMode uint8

const (
	// BinaryQuit stops searching at the first NUL byte, treating the
	// rest of the file as absent. Default for walk-discovered files.
	BinaryQuit BinaryMode = iota
	// BinaryConvert keeps searching past NUL bytes (replacing them).
	// Used for explicitly-named files and --binary.
	BinaryConvert
	// BinaryNone disables NUL detection entirely (--text).
	BinaryNone
)

// Match describes one matching line, delivered to Sink.Matched.
//
// The *Match pointer itself, and Line (and any other []byte field), are
// valid ONLY for the duration of the Matched call. A Searcher
// implementation may reuse/pool a single Match value across many calls
// (mutating it in place before each Matched call) rather than allocate
// one per match — this is the expected zero-allocation implementation,
// not merely a possibility callers must defend against. A Sink that
// needs to retain the bytes, or any field, after Matched returns MUST
// copy them; retaining the pointer or the Line slice itself is a bug
// that will read corrupted or unrelated data on the next call.
//
// LineNumber is populated only when the Searcher has line numbering
// enabled (Searcher.LineNumbers); HasLineNumber reports whether it is
// valid. Numbering is computed lazily by the Searcher — it counts
// newlines only up to the position of this match, never precomputes
// line numbers for the whole file — so a Sink must not defer counting
// itself via a closure (that would reintroduce the per-match allocation
// this design avoids).
type Match struct {
	Line          []byte
	LineNumber    int64
	HasLineNumber bool
	// Offset is the absolute byte offset of Line's first byte within
	// the searched stream.
	Offset int64
}

// Ctx describes one context line (-A/-B/-C), delivered to Sink.Context.
// Same validity and reuse/pooling contract as Match: the *Ctx pointer
// and Line are valid, and may be mutated by the Searcher for the next
// call, only for the duration of the current Context call.
type Ctx struct {
	Line          []byte
	LineNumber    int64
	HasLineNumber bool
	Offset        int64
	// After is true for trailing (-A) context, false for leading (-B).
	After bool
}

// Stats summarizes one file's search, passed to Sink.Finish.
type Stats struct {
	Matched       bool
	MatchCount    int64
	BytesSearched int64
	// Binary is true when NUL-byte detection fired (BinaryQuit or
	// BinaryConvert). BinaryOffset is the absolute byte offset of the
	// first NUL seen, valid only when Binary is true.
	Binary       bool
	BinaryOffset int64
}

// Sink receives search results. A Searcher drives exactly one
// Begin/{Matched,Context}*/Finish sequence per path.
type Sink interface {
	// Begin is called once per path before searching starts. If search
	// is false, or err is non-nil, the Searcher skips the file and
	// still calls Finish. err aborts the whole walk when non-nil.
	Begin(path string) (search bool, err error)

	// Matched is called once per matching line, in stream order. Per
	// Match's doc comment, m (and its Line field) may be a reused/pooled
	// value the Searcher mutates before every call — valid only until
	// Matched returns; copy anything that must outlive the call.
	// Returning more=false aborts the rest of that file's search (used
	// by -q/-l/-m early exit); it does not abort the overall walk.
	Matched(m *Match) (more bool, err error)

	// Context is called once per context line (-A/-B/-C), in stream
	// order, interleaved with Matched calls as they occur in the file.
	Context(c *Ctx) (more bool, err error)

	// Finish is called once per path after searching completes or is
	// aborted, even when Begin returned search=false.
	Finish(path string, stats *Stats) error
}

// Searcher holds configuration shared across searches. Once constructed,
// a Searcher's config fields are read-only; callers should allocate one
// Searcher per worker goroutine and reuse it across files rather than
// reconstruct it per file, so its pooled buffers amortize.
//
// A Searcher is single-goroutine: its pooled rolling buffer and scratch
// Match/Ctx values are mutated in place across calls. Workers searching in
// parallel must each own their own Searcher (constructed via New); do not
// share one across goroutines.
type Searcher struct {
	Matcher     match.Matcher
	BinaryMode  BinaryMode
	Invert      bool
	LineNumbers bool
	// CRLF is rg's --crlf: lines are still split on '\n', but a trailing
	// '\r' is stripped from the MATCH window (see withoutTerminator) so
	// `$`/`-x`/`.` behave as if the '\r' weren't there, while the printer
	// still emits the original bytes. Never combined with NullData (the
	// CLI resolves them mutually exclusively -- see cmd/gg's flags).
	CRLF bool
	// NullData is rg's --null-data: records are delimited by '\x00' instead
	// of '\n' (the terminator threads through the searcher's line splitting,
	// the rolling buffer, and the printer). Binary detection is disabled
	// under this mode (a NUL cannot be both a record terminator and a
	// binary marker); the caller sets BinaryMode=BinaryNone accordingly.
	NullData bool
	// BeforeContext / AfterContext are line counts for -B/-A; -C sets
	// both to the same value.
	BeforeContext int
	AfterContext  int
	// PassThru is rg's --passthru: a DEDICATED mode, not merely "very
	// large BeforeContext/AfterContext" (verified against the real rg
	// searcher source, crates/searcher/src/searcher/mod.rs: passthru
	// forces before_context/after_context to 0 and is checked as its own
	// bool everywhere). Every line not otherwise sunk as a match or
	// owed after-context is sunk as context too (see matchByLineSlow),
	// so the whole file streams through with no gaps -- callers must
	// leave BeforeContext/AfterContext at 0 when this is set (cmd/gg's
	// flags.go already guarantees --passthru and -A/-B/-C are mutually
	// exclusive at the CLI layer, mirroring rg's own ContextMode enum).
	// Forces the slow line-by-line scan path (see isFastPath) and is
	// deliberately excluded from intra-file parallel eligibility (see
	// parallelEligible in parallel.go).
	PassThru bool

	// MaxCount is -m/--max-count: the maximum number of matched LINES
	// (never context lines, never total match occurrences within a
	// line) sunk per file. nil means unlimited. A non-nil value of 0 is
	// a legitimate, real limit (rg parity, verified against the real
	// binary: `rg -m 0 pat file` searches nothing and reports no
	// match) -- this is why the field is a pointer rather than plain
	// int with a 0-means-unlimited convention like BeforeContext/
	// AfterContext above. Trailing after-context for the final counted
	// match still prints once the limit is hit; no FURTHER match is
	// ever found or sunk afterward (see core.go's matchLimitReached).
	MaxCount *int

	// ParallelWorkers, when > 1, lets SearchBytes split an eligible input
	// into line-aligned chunks searched concurrently, then replayed
	// through sink in file order (M3 task #18 -- see searchBytesParallel's
	// doc for the eligibility rule and why context/invert aren't
	// supported yet). 0 or 1 means always serial. Search's io.Reader path
	// never parallelizes -- PLAN.md's intra-file chunking is defined over
	// an in-memory slice, where chunk boundaries can be computed up
	// front; a stream has no such fixed extent to divide.
	ParallelWorkers int
	// ParallelMinBytes is the minimum input length SearchBytes will
	// consider parallelizing; below it, per-goroutine overhead likely
	// costs more than it saves. Zero means defaultParallelMinBytes.
	// Tests lower this (and pair it with a small ParallelWorkers count)
	// to force parallel chunking on tiny fixtures, so the invariance
	// harness can stress chunk boundaries without huge inputs.
	ParallelMinBytes int64

	// Pooled resources, amortized across Search/SearchBytes calls on this
	// Searcher. Not part of the public config; New allocates them. Per the
	// Match/Ctx docs, statsScratch is likewise reused: the *Stats handed to
	// Sink.Finish is only valid for the duration of that call.
	lb           *lineBuffer
	matchScratch Match
	ctxScratch   Ctx
	statsScratch Stats

	// lineTerm is the resolved record/line terminator byte for this scan
	// ('\x00' under NullData, otherwise '\n' -- including under CRLF, which
	// still splits on '\n'). Set by resetRun/runChunk from NullData so the
	// zero value of a Searcher stays the default '\n' path.
	lineTerm byte

	// Per-call scan state, reset by resetRun at the start of every
	// Search/SearchBytes call.
	pos              int
	absOffsetBase    int64
	lineNumber       int64
	lastLineCounted  int
	lastLineVisited  int
	afterContextLeft int
	hasMatched       bool
	matchCount       int64
	// matchLimitReached is MaxCount's per-call scan state: once true, no
	// FURTHER match is found or sunk for the rest of this file, though
	// trailing after-context already owed to the last counted match
	// still drains normally (see core.go's matchByLineFast/Slow). Reset
	// by resetRun/runChunk at the start of every top-level scan --
	// initialized to true immediately when MaxCount points at 0, since
	// in that case the limit is already reached before any match can
	// ever be found.
	matchLimitReached bool
	// matchLimitReachedAtStart snapshots matchLimitReached's value
	// immediately after it's computed by resetRun/runChunk -- i.e. it is
	// true if and only if MaxCount pointed at <=0, so the limit was
	// ALREADY exceeded before this file's scan ever processed a single
	// line. This distinguishes that case from "the limit was reached
	// mid-scan, after some real matches" for PassThru's purposes:
	// verified against the real rg binary, `--passthru -m 0` prints
	// NOTHING at all (rg's own matches_possible() check skips searching
	// entirely for MaxCount==Some(0), for every mode including passthru
	// -- crates/core/flags/hiargs.rs), unlike hitting the limit mid-file,
	// where passthru keeps printing every remaining line as context (see
	// matchByLineSlow's PassThru branch). gg has no equivalent top-level
	// skip (see resetRun's doc), so this flag reproduces the same
	// observable zero-output result locally, from within the ordinary
	// per-file loop.
	matchLimitReachedAtStart bool
	// hasBinaryOffset/binaryOffset mirror the eventual Stats.Binary/
	// BinaryOffset values, but are kept live (updated as detection
	// happens, not just at Finish) so HasBinaryOffset/BinaryOffset can be
	// queried mid-scan -- see those methods' doc for why a caller needs
	// that.
	hasBinaryOffset bool
	binaryOffset    int64
}

// HasBinaryOffset reports whether a binary byte (NUL) has been detected
// so far in the file currently being searched. Unlike Stats.Binary
// (which Sink.Finish only receives once the whole file has been
// processed), this is live: it may already be true partway through a
// Search/SearchBytes call, queryable from within a Sink's Matched/
// Context callback. cmd/gg's matchTracker uses this to implement rg's
// explicit-file (BinaryConvert) printer-suppression rule, which depends
// on knowing binary state as each match streams in, not after the fact.
func (s *Searcher) HasBinaryOffset() bool { return s.hasBinaryOffset }

// BinaryOffset returns the absolute byte offset of the first detected
// binary byte for the file currently being searched. Valid only when
// HasBinaryOffset returns true.
func (s *Searcher) BinaryOffset() int64 { return s.binaryOffset }

// New constructs a Searcher from cfg, allocating its pooled rolling read
// buffer (DefaultBufferSize) up front.
func New(cfg Searcher) *Searcher {
	s := cfg
	s.lb = newLineBuffer(DefaultBufferSize)
	return &s
}

// Search reads path's content from r and drives sink's
// Begin/Matched/Context/Finish sequence.
//
// Search owns a pooled rolling line buffer (see linebuffer.go): it reads
// r in DefaultBufferSize chunks, always presenting complete lines to the
// scanner, doubling its buffer only for a single line that exceeds the
// current capacity. It shares its core line-scanning logic (core.go) with
// SearchBytes, so a future mmap/[]byte entrypoint or M3's intra-file
// parallel chunking can reuse it without any interface change.
func (s *Searcher) Search(path string, r io.Reader, sink Sink) error {
	ok, err := sink.Begin(path)
	if err != nil {
		return err
	}
	if !ok {
		s.statsScratch = Stats{}
		return sink.Finish(path, &s.statsScratch)
	}

	s.resetRun()
	if s.lb == nil {
		s.lb = newLineBuffer(DefaultBufferSize)
	}
	s.lb.reset(s.BinaryMode, s.lineTerm)

	for {
		oldBuf := s.lb.buffer()
		oldLen := len(oldBuf)
		consumed := s.rollConsume(oldBuf)
		s.lb.consume(consumed)

		more, ferr := s.lb.fill(r)
		if ferr != nil {
			return ferr
		}
		// Sync live binary-detection state from the line buffer before
		// matchByLine runs on this window, so HasBinaryOffset/
		// BinaryOffset are already current for every Matched/Context
		// call this iteration produces (see those methods' doc).
		s.hasBinaryOffset = s.lb.hasBinaryOffset
		s.binaryOffset = s.lb.binaryOffset
		if !more {
			break
		}
		if consumed == 0 && oldLen == len(s.lb.buffer()) {
			// No progress possible: everything left in the buffer is
			// context that will never be needed again. Force EOF.
			s.lb.consume(oldLen)
			break
		}

		buf := s.lb.buffer()
		outcome, serr := s.matchByLine(buf, sink)
		if serr != nil {
			return serr
		}
		if outcome == scanStop {
			s.lb.consume(s.pos)
			break
		}
	}

	s.statsScratch = Stats{
		Matched:       s.hasMatched,
		MatchCount:    s.matchCount,
		BytesSearched: s.lb.absoluteOffset,
		Binary:        s.hasBinaryOffset,
		BinaryOffset:  s.binaryOffset,
	}
	return sink.Finish(path, &s.statsScratch)
}

// SearchBytes searches data directly, with no rolling buffer or read
// syscalls: the same core line-scanning logic as Search (see core.go)
// runs over the slice (in one pass, or split into concurrent chunks --
// see parallelEligible/searchBytesParallel) rather than a stream read in
// DefaultBufferSize increments. Intended for the small number of
// explicitly-named files that may be mmap'd.
//
// data must remain valid and unmodified for the duration of the call, and
// is never written to: unlike Search's owned rolling buffer, a
// caller-owned slice can't safely be mutated in place. Consequently, under
// BinaryConvert, detection still fires (Stats.Binary/BinaryOffset) but NUL
// bytes are left as ordinary bytes in the searched content rather than
// being turned into line breaks — this matches rg's own read-only mmap
// path (grep-searcher's SliceByLine), which detects but never rewrites.
// Under BinaryQuit, any NUL anywhere in data discards the whole slice
// (searchData is never truncated to just before the NUL): SearchBytes
// treats the entire slice as a single "read", matching the same
// whole-chunk-discard rule Search's incremental path applies to
// whichever single chunk a NUL falls in (see linebuffer.go's fill).
//
// Detection only scans the first DefaultBufferSize bytes up front — this
// is deliberate, not a leftover limitation: empirically, real rg's own
// SliceByLine does the same bounded check and does NOT scan the rest of
// the file for a NUL that no matched/context line ever touches (verified
// against the installed rg binary: a NUL placed well past 64KB, with a
// match before it and none after, produces no "binary file matches"
// message at all under --mmap, unlike --no-mmap's streaming path, which
// does report it because it scans every byte it reads regardless of
// matches). An earlier version of this method scanned the whole slice to
// "fix" the past-64KB gap below, but that both regressed throughput (a
// full memchr pass over the entire file on every search) and made gg
// diverge from rg by over-detecting NULs rg's own mmap path never notices.
//
// The real gap this bounded prefix leaves — a NUL that DOES fall within a
// line some Sink actually visits (a matched or context line), just past
// the first DefaultBufferSize bytes — is closed one layer up: cmd/gg's
// matchTracker inspects each delivered Match/Ctx line's own bytes for a
// NUL (see its noteLineNUL), which gives the exact same coverage rg's
// mmap path has at negligible cost (one short memchr per match, not one
// over the whole file) without this method needing to know anything about
// line delivery.
func (s *Searcher) SearchBytes(path string, data []byte, sink Sink) error {
	ok, err := sink.Begin(path)
	if err != nil {
		return err
	}
	if !ok {
		s.statsScratch = Stats{}
		return sink.Finish(path, &s.statsScratch)
	}

	s.resetRun()

	searchData := data
	if s.BinaryMode != BinaryNone {
		prefix := data
		if len(prefix) > DefaultBufferSize {
			prefix = prefix[:DefaultBufferSize]
		}
		if i := bytes.IndexByte(prefix, 0); i >= 0 {
			s.hasBinaryOffset = true
			s.binaryOffset = int64(i)
			if s.BinaryMode == BinaryQuit {
				searchData = nil
			}
		}
	}

	if s.parallelEligible(len(searchData)) {
		if err := s.searchBytesParallel(searchData, sink); err != nil {
			return err
		}
	} else if err := s.runChunk(searchData, sink, 0, 1); err != nil {
		return err
	}

	s.statsScratch = Stats{
		Matched:       s.hasMatched,
		MatchCount:    s.matchCount,
		BytesSearched: int64(len(searchData)),
		Binary:        s.hasBinaryOffset,
		BinaryOffset:  s.binaryOffset,
	}
	return sink.Finish(path, &s.statsScratch)
}

// runChunk drives matchByLine over data, treating base as the absolute
// byte offset of data[0] and lineBase as the line number of data's first
// line (only meaningful when LineNumbers is set) -- the shared primitive
// behind SearchBytes's own whole-file scan (base=0, lineBase=1) and each
// intra-file parallel chunk's scan (base/lineBase reflecting where that
// chunk sits in the file). It does not call sink.Begin/Finish -- callers
// own that -- and it does not touch binary-detection state: SearchBytes
// already performs its one upfront bounded check before any chunking
// decision, and per-chunk children (searchBytesParallel) are constructed
// with BinaryMode=BinaryNone specifically so they never redo or interact
// with it.
func (s *Searcher) runChunk(data []byte, sink Sink, base, lineBase int64) error {
	s.lineTerm = resolveLineTerm(s.NullData)
	s.pos = 0
	s.absOffsetBase = base
	if s.LineNumbers {
		s.lineNumber = lineBase
	} else {
		s.lineNumber = 0
	}
	s.lastLineCounted = 0
	s.lastLineVisited = 0
	s.afterContextLeft = 0
	s.hasMatched = false
	s.matchCount = 0
	s.matchLimitReached = s.MaxCount != nil && *s.MaxCount <= 0
	s.matchLimitReachedAtStart = s.matchLimitReached
	if len(data) == 0 {
		return nil
	}
	_, err := s.matchByLine(data, sink)
	return err
}
