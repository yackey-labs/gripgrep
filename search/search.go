package search

import (
	"errors"
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
// Line and any other []byte field are views into the Searcher's internal
// rolling buffer and are valid ONLY for the duration of the Matched
// call. A Sink that needs to retain the bytes (or the Match itself)
// after returning must copy them.
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
// Same validity contract as Match: Line is a buffer view valid only for
// the duration of the call.
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
}

// Sink receives search results. A Searcher drives exactly one
// Begin/{Matched,Context}*/Finish sequence per path.
type Sink interface {
	// Begin is called once per path before searching starts. If search
	// is false, or err is non-nil, the Searcher skips the file and
	// still calls Finish. err aborts the whole walk when non-nil.
	Begin(path string) (search bool, err error)

	// Matched is called once per matching line, in stream order.
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

// ErrNotImplemented is returned by the M0 stub Search. It will be
// removed once M1-search lands a real implementation.
var ErrNotImplemented = errors.New("search: not implemented (TODO M1-search)")

// Searcher holds configuration shared across searches. Once constructed,
// a Searcher's config fields are read-only; callers should allocate one
// Searcher per worker goroutine and reuse it across files rather than
// reconstruct it per file, so its pooled buffers (TODO M1-search) amortize.
type Searcher struct {
	Matcher     match.Matcher
	BinaryMode  BinaryMode
	Invert      bool
	LineNumbers bool
	// BeforeContext / AfterContext are line counts for -B/-A; -C sets
	// both to the same value.
	BeforeContext int
	AfterContext  int
}

// New constructs a Searcher from cfg.
//
// TODO(M1-search): validate cfg, allocate the pooled 64KB rolling read
// buffer and any context ring buffer.
func New(cfg Searcher) *Searcher {
	s := cfg
	return &s
}

// Search reads path's content from r and drives sink's
// Begin/Matched/Context/Finish sequence.
//
// TODO(M1-search): rolling line buffer (fill/roll/ensure-capacity per
// docs/research/ripgrep-internals.md §1), fast whole-buffer candidate
// path gated on s.Matcher.NonMatchingLineTerm(), slow per-line fallback,
// NUL-based binary detection per s.BinaryMode, lazy line counting, invert,
// context tracking. The M0 stub always returns ErrNotImplemented.
func (s *Searcher) Search(path string, r io.Reader, sink Sink) error {
	return ErrNotImplemented
}
