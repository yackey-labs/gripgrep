package printer

import (
	"strconv"
	"sync/atomic"

	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/search"
)

// Count implements -c: writes "path:count\n" for every file with at
// least one match. The count comes from Count's own tally of Matched
// calls, never from stats.MatchCount, and no matched line is ever
// formatted.
type Count struct {
	dest *Dest
	// Color enables coloring the path (matching rg's -c --color=always
	// behavior, which colors only the path, not the count).
	Color bool
	// ShowPath controls whether "path:" is prepended at all. Like
	// Standard.ShowPath, callers should set this false for the
	// single-explicit-file case (rg prints a bare count with no path
	// prefix there) -- Count has no way to know how many files are being
	// searched, so this must be driven from outside. Unlike Standard,
	// there is no Heading concept for -c: this is the only path-display
	// switch Count has.
	ShowPath bool
	// OnlyMatching is rg's -o/--only-matching's documented effect on -c:
	// "when --count is combined with --only-matching, ripgrep behaves as
	// if --count-matches was given" -- i.e. count OCCURRENCES, not
	// matched LINES. Matcher must also be set for this to take effect
	// (mirrors Standard.Matcher's doc: required to locate spans at all).
	//
	// Verified against the real rg binary that this carve-out does NOT
	// extend to an inverted search (`rg -o -c -v` prints the LINE count,
	// same as plain `-c -v`, not 0): Matched still fires once per
	// genuinely non-matching inverted line, on which Matcher legitimately
	// finds zero spans (same "Invert" case Standard.Matched's doc
	// describes), so counting max(1, spanCount) rather than raw
	// spanCount reproduces that fallback for free, without Count needing
	// to know whether the underlying search was inverted at all.
	OnlyMatching bool
	// Matcher locates match spans for OnlyMatching's occurrence count;
	// unused when OnlyMatching is false.
	Matcher match.Matcher
	// IncludeZero is rg's --include-zero: when true, Finish writes
	// "path:0\n" even for a file with no matches, instead of skipping it
	// entirely. Verified against the real rg binary: this never changes
	// the process exit code (still 1 when nothing matched anywhere,
	// even though "path:0" lines were printed) -- it's purely a display
	// change, so ONLY Finish's early-return guard below is affected.
	IncludeZero bool
	// Null is rg's -0/--null: the ':' between path and count becomes a
	// NUL byte instead (the trailing terminator after the count is
	// unaffected) -- see printer.Standard.Null's doc for the general rule
	// this is one instance of.
	Null bool
	// CRLF/NullData select the trailing terminator after the count, matching
	// the searcher's line terminator (rg's -c honors --crlf/--null-data:
	// "path:1\r\n" / "path:1\x00" -- verified against the real rg binary).
	// The ':' separator itself is unaffected (it is Null's concern). See
	// summaryTerm.
	CRLF     bool
	NullData bool

	buf         []byte
	path        []byte
	count       int64
	spanScratch []matchSpan
}

// summaryTerm returns the trailing terminator bytes for a summary-mode row
// (rg's LineTerminator::as_bytes): '\r\n' under CRLF, '\x00' under
// NullData, '\n' otherwise. Shared by Count and the -l/--files-without-match
// printers, whose path/count rows all follow the searcher's terminator.
func summaryTerm(crlf, nullData bool) []byte {
	switch {
	case nullData:
		return termNUL
	case crlf:
		return termCRLF
	default:
		return termLF
	}
}

// NewCount returns a Count printer flushing completed files to dest, with
// ShowPath defaulting to true (the common multi-file case).
func NewCount(dest *Dest) *Count {
	return &Count{dest: dest, buf: getBuf(), ShowPath: true}
}

var _ search.Sink = (*Count)(nil)

// Begin implements search.Sink.
func (p *Count) Begin(path string) (bool, error) {
	p.buf = resetBuf(p.buf)
	p.path = append(p.path[:0], path...)
	p.count = 0
	return true, nil
}

// Matched implements search.Sink: tallies only, never formats the line.
// Under OnlyMatching (see its doc), tallies the number of match spans on
// the line instead of 1 -- falling back to 1 when Matcher finds none,
// which is exactly the Invert case (see Standard.Matched's doc): there is
// no meaningful "occurrence count" for a line reported because it did
// NOT match, so this counts it as one line, same as without -o.
func (p *Count) Matched(m *search.Match) (bool, error) {
	if !p.OnlyMatching || p.Matcher == nil {
		p.count++
		return true, nil
	}
	line := trimRecordTerminator(m.Line, p.CRLF, p.NullData)
	p.spanScratch = findMatchSpans(p.spanScratch[:0], p.Matcher, line)
	if n := len(p.spanScratch); n > 0 {
		p.count += int64(n)
	} else {
		p.count++
	}
	return true, nil
}

// Context implements search.Sink. Count mode never requests context,
// but the method must exist to satisfy search.Sink.
func (p *Count) Context(c *search.Ctx) (bool, error) {
	return true, nil
}

// Finish implements search.Sink: writes "path:count\n" if any match was
// tallied, using strconv.AppendInt (no fmt) -- or, under IncludeZero,
// unconditionally (see its doc).
func (p *Count) Finish(path string, stats *search.Stats) error {
	if p.count == 0 && !p.IncludeZero {
		return nil
	}
	if p.ShowPath {
		if p.Color {
			p.buf = appendColoredBytes(p.buf, ansiPath, p.path)
		} else {
			p.buf = append(p.buf, p.path...)
		}
		if p.Null {
			p.buf = append(p.buf, 0)
		} else {
			p.buf = append(p.buf, ':')
		}
	}
	p.buf = strconv.AppendInt(p.buf, p.count, 10)
	p.buf = append(p.buf, summaryTerm(p.CRLF, p.NullData)...)
	return p.dest.Write(p.buf)
}

// FilesWithMatches implements -l: writes "path\n" once per matching
// file, with no count and no formatted lines. It aborts the file's
// search after the first match (Matched returns more=false), since
// nothing past the first match changes the outcome.
type FilesWithMatches struct {
	dest *Dest
	// Color enables coloring the path, matching rg's -l --color=always.
	Color bool
	// Null is rg's -0/--null: the trailing terminator becomes a NUL byte
	// instead -- since path is the only field -l ever prints, this IS
	// the path's own terminator (see printer.Standard.Null's doc). Null
	// wins over CRLF/NullData for this single field.
	Null bool
	// CRLF/NullData select the path terminator when Null is not set, matching
	// the searcher's line terminator (rg's -l honors --null-data:
	// "path\x00" -- verified against the real rg binary; -0 gives the same
	// '\x00' independently). See summaryTerm.
	CRLF     bool
	NullData bool

	buf     []byte
	path    []byte
	matched bool
}

// NewFilesWithMatches returns a FilesWithMatches printer flushing
// completed files to dest.
func NewFilesWithMatches(dest *Dest) *FilesWithMatches {
	return &FilesWithMatches{dest: dest, buf: getBuf()}
}

var _ search.Sink = (*FilesWithMatches)(nil)

// Begin implements search.Sink.
func (p *FilesWithMatches) Begin(path string) (bool, error) {
	p.buf = resetBuf(p.buf)
	p.path = append(p.path[:0], path...)
	p.matched = false
	return true, nil
}

// Matched implements search.Sink: records the match and returns
// more=false to abort the rest of this file's search early.
func (p *FilesWithMatches) Matched(m *search.Match) (bool, error) {
	p.matched = true
	return false, nil
}

// Context implements search.Sink. FilesWithMatches never requests
// context (it aborts on the first match, before any context would be
// gathered), but the method must exist to satisfy search.Sink.
func (p *FilesWithMatches) Context(c *search.Ctx) (bool, error) {
	return true, nil
}

// Finish implements search.Sink: writes "path\n" if the file matched.
func (p *FilesWithMatches) Finish(path string, stats *search.Stats) error {
	if !p.matched {
		return nil
	}
	if p.Color {
		p.buf = appendColoredBytes(p.buf, ansiPath, p.path)
	} else {
		p.buf = append(p.buf, p.path...)
	}
	if p.Null {
		p.buf = append(p.buf, 0)
	} else {
		p.buf = append(p.buf, summaryTerm(p.CRLF, p.NullData)...)
	}
	return p.dest.Write(p.buf)
}

// FilesWithoutMatch implements --files-without-match: the exact complement
// of FilesWithMatches -- writes "path\n" once per file with ZERO matches,
// instead of at least one. Like FilesWithMatches, it aborts the file's
// search after the first match (Matched returns more=false): once any
// match is found, this file can never end up in the "without match" set,
// so nothing past the first match changes the outcome.
//
// Its exit-code contribution is the exact complement too -- see
// ModeFilesWithoutMatch's doc in cmd/gg/flags.go and internal/engine's
// matchTracker, which has to know about this mode specifically to flip
// its match-signal aggregation (a file with zero Matched calls is the
// "found" case here, not the "nothing happened" case every other mode
// treats it as).
type FilesWithoutMatch struct {
	dest *Dest
	// Color enables coloring the path, matching rg's --files-without-match
	// --color=always.
	Color bool
	// Null is rg's -0/--null -- see FilesWithMatches.Null's doc.
	Null bool
	// CRLF/NullData select the path terminator when Null is not set -- see
	// FilesWithMatches.CRLF's doc.
	CRLF     bool
	NullData bool

	buf     []byte
	path    []byte
	matched bool
}

// NewFilesWithoutMatch returns a FilesWithoutMatch printer flushing
// completed files to dest.
func NewFilesWithoutMatch(dest *Dest) *FilesWithoutMatch {
	return &FilesWithoutMatch{dest: dest, buf: getBuf()}
}

var _ search.Sink = (*FilesWithoutMatch)(nil)

// Begin implements search.Sink.
func (p *FilesWithoutMatch) Begin(path string) (bool, error) {
	p.buf = resetBuf(p.buf)
	p.path = append(p.path[:0], path...)
	p.matched = false
	return true, nil
}

// Matched implements search.Sink: records the match and returns
// more=false to abort the rest of this file's search early -- see the
// type doc.
func (p *FilesWithoutMatch) Matched(m *search.Match) (bool, error) {
	p.matched = true
	return false, nil
}

// Context implements search.Sink. FilesWithoutMatch never requests
// context (it aborts on the first match, before any context would be
// gathered), but the method must exist to satisfy search.Sink.
func (p *FilesWithoutMatch) Context(c *search.Ctx) (bool, error) {
	return true, nil
}

// Finish implements search.Sink: writes "path\n" if the file did NOT
// match -- the inverse condition of FilesWithMatches.Finish.
func (p *FilesWithoutMatch) Finish(path string, stats *search.Stats) error {
	if p.matched {
		return nil
	}
	if p.Color {
		p.buf = appendColoredBytes(p.buf, ansiPath, p.path)
	} else {
		p.buf = append(p.buf, p.path...)
	}
	if p.Null {
		p.buf = append(p.buf, 0)
	} else {
		p.buf = append(p.buf, summaryTerm(p.CRLF, p.NullData)...)
	}
	return p.dest.Write(p.buf)
}

// Quiet implements -q: it writes nothing at all, just records whether
// any match was found anywhere and aborts that file's search
// immediately on the first hit. Unlike Standard/Count/FilesWithMatches,
// a single Quiet is meant to be shared across every worker goroutine
// (there is no per-file output to keep separate), so Found and the
// internal flag are safe for concurrent use: the walk/search
// coordinator polls Found to decide when to stop dispatching further
// work (rg's "abort the entire parallel walk" semantics), which is
// outside this package's scope.
type Quiet struct {
	found atomic.Bool
}

// NewQuiet returns a Quiet sink. It has no output destination: -q never
// writes anything.
func NewQuiet() *Quiet {
	return &Quiet{}
}

var _ search.Sink = (*Quiet)(nil)

// Begin implements search.Sink: once a match has been found anywhere,
// declines to search further files too (search=false), though a
// walk/search coordinator racing across goroutines may still start a
// few more files before observing this.
func (p *Quiet) Begin(path string) (bool, error) {
	return !p.found.Load(), nil
}

// Matched implements search.Sink: records the match and aborts this
// file's search immediately.
func (p *Quiet) Matched(m *search.Match) (bool, error) {
	p.found.Store(true)
	return false, nil
}

// Context implements search.Sink. Quiet never requests context, but the
// method must exist to satisfy search.Sink.
func (p *Quiet) Context(c *search.Ctx) (bool, error) {
	return true, nil
}

// Finish implements search.Sink: a no-op, since Quiet writes nothing.
func (p *Quiet) Finish(path string, stats *search.Stats) error {
	return nil
}

// Found reports whether any match has been recorded yet by any
// goroutine sharing this Quiet.
func (p *Quiet) Found() bool {
	return p.found.Load()
}
