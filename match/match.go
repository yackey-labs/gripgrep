package match

import "errors"

// CandidateKind distinguishes a genuine match from a literal-prefilter
// hit that still needs full-regex confirmation on its enclosing line.
type CandidateKind uint8

const (
	// Confirmed means FindCandidate ran the real pattern directly (no
	// separate prefilter regex exists, e.g. a pure-literal pattern); the
	// hit is a genuine match and needs no further verification.
	Confirmed CandidateKind = iota
	// Candidate means FindCandidate matched only a literal prefilter;
	// the caller must locate the enclosing line and call Verify on it
	// before treating this as a real match.
	Candidate
)

// CaseMode selects how case is handled while matching.
type CaseMode uint8

const (
	// CaseSensitive matches patterns exactly as written.
	CaseSensitive CaseMode = iota
	// CaseInsensitive folds case on both pattern and haystack.
	CaseInsensitive
	// CaseSmart is case-insensitive unless the pattern contains an
	// uppercase literal character, in which case it is case-sensitive.
	CaseSmart
)

// Config describes how to compile one or more patterns into a Matcher.
// It is the sole construction-time input to New; Matcher implementations
// expose no runtime setters for case/word/fixed-string behavior, only
// query methods (see NonMatchingLineTerm).
type Config struct {
	// Patterns are combined as an alternation (like ripgrep's -e).
	Patterns []string
	CaseMode CaseMode
	// Word wraps each pattern in word-boundary looks (rg's -w).
	Word bool
	// Fixed treats Patterns as literal strings rather than regexes (-F).
	Fixed bool
}

// ErrNotImplemented is returned by the M0 stub constructor. It will be
// removed once M1-match lands a real implementation.
var ErrNotImplemented = errors.New("match: not implemented (TODO M1-match)")

// Matcher is a compiled pattern ready to search []byte haystacks. Every
// method operates on []byte only — implementations and callers must
// never convert to string on a hot path. A Matcher's compiled state is
// read-only after construction, so a single Matcher may be shared and
// called concurrently by multiple goroutines; any per-call scratch space
// an implementation needs must not be stored on the Matcher itself
// (pool it in the caller, e.g. per search.Searcher worker).
type Matcher interface {
	// FindCandidate scans buf starting at byte offset start for the next
	// possible match and reports its offset plus whether it is Confirmed
	// or merely a Candidate. ok is false once no further candidates
	// exist in buf[start:]. Implementations must not allocate in steady
	// state — this is the whole-buffer hot-path scan (rg's
	// find_candidate_line).
	FindCandidate(buf []byte, start int) (off int, kind CandidateKind, ok bool)

	// Verify reports whether the full pattern matches anywhere within
	// line. Used to confirm a Candidate hit against exactly the one
	// line that contains it.
	Verify(line []byte) bool

	// Find returns the leftmost match's byte bounds [s, e) within line.
	// Callers that only need a yes/no + line (the common "path:line:text"
	// case) should prefer Verify and skip Find entirely to avoid the
	// extra work of locating exact bounds.
	Find(line []byte) (s, e int, ok bool)

	// NonMatchingLineTerm reports whether the compiled pattern is
	// provably unable to match across a line-terminator byte ('\n').
	// When true, a search.Searcher may use the fast whole-buffer
	// candidate path (FindCandidate over the whole buffer, then expand
	// to line boundaries); when false it must fall back to scanning
	// line-by-line. This is the only capability a Searcher queries on a
	// Matcher at runtime — all other behavior (case, word, fixed) is
	// baked in at construction via Config.
	NonMatchingLineTerm() bool
}

// New compiles cfg into a Matcher.
//
// TODO(M1-match): literal extraction from regexp/syntax, rarity-ranked
// prefilters, smart case, word wrapping, -F/multi-pattern alternation,
// grafana/regexp fallback engine. The M0 stub always returns
// ErrNotImplemented.
func New(cfg Config) (Matcher, error) {
	return nil, ErrNotImplemented
}
