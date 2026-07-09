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
	// BeforeContext / AfterContext are line counts for -B/-A; -C sets
	// both to the same value.
	BeforeContext int
	AfterContext  int

	// Pooled resources, amortized across Search/SearchBytes calls on this
	// Searcher. Not part of the public config; New allocates them. Per the
	// Match/Ctx docs, statsScratch is likewise reused: the *Stats handed to
	// Sink.Finish is only valid for the duration of that call.
	lb           *lineBuffer
	matchScratch Match
	ctxScratch   Ctx
	statsScratch Stats

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
}

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
	s.lb.reset(s.BinaryMode)

	for {
		oldBuf := s.lb.buffer()
		oldLen := len(oldBuf)
		consumed := s.rollConsume(oldBuf)
		s.lb.consume(consumed)

		more, ferr := s.lb.fill(r)
		if ferr != nil {
			return ferr
		}
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
		Binary:        s.lb.hasBinaryOffset,
		BinaryOffset:  s.lb.binaryOffset,
	}
	return sink.Finish(path, &s.statsScratch)
}

// SearchBytes searches data directly, with no rolling buffer or read
// syscalls: the same core line-scanning logic as Search (see core.go)
// runs over the whole slice in one pass. Intended for the small number of
// explicitly-named files that may be mmap'd, and for M3's intra-file
// parallel chunking (each chunk becomes one SearchBytes-style call over a
// sub-slice).
//
// data must remain valid and unmodified for the duration of the call, and
// is never written to: unlike Search's owned rolling buffer, a
// caller-owned slice can't safely be mutated in place. Consequently, under
// BinaryConvert, detection still fires (Stats.Binary/BinaryOffset) but NUL
// bytes are left as ordinary bytes in the searched content rather than
// being turned into line breaks — this matches rg's own read-only mmap
// path (grep-searcher's SliceByLine), which detects but never rewrites.
// Under BinaryQuit, a NUL in the first DefaultBufferSize bytes skips the
// file entirely (also matching SliceByLine, which is stricter here than
// the incremental Search path: a slice is the whole file up front, so
// there's no partial "searched up to the NUL" result to offer).
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
	hasBinary := false
	var binaryOffset int64
	if s.BinaryMode != BinaryNone {
		upto := len(data)
		if upto > DefaultBufferSize {
			upto = DefaultBufferSize
		}
		if i := bytes.IndexByte(data[:upto], 0); i >= 0 {
			hasBinary = true
			binaryOffset = int64(i)
			if s.BinaryMode == BinaryQuit {
				searchData = nil
			}
		}
	}

	if len(searchData) > 0 {
		if _, err := s.matchByLine(searchData, sink); err != nil {
			return err
		}
	}

	s.statsScratch = Stats{
		Matched:       s.hasMatched,
		MatchCount:    s.matchCount,
		BytesSearched: int64(len(searchData)),
		Binary:        hasBinary,
		BinaryOffset:  binaryOffset,
	}
	return sink.Finish(path, &s.statsScratch)
}
