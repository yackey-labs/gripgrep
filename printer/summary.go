package printer

import (
	"strconv"
	"sync/atomic"

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

	buf   []byte
	path  []byte
	count int64
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
func (p *Count) Matched(m *search.Match) (bool, error) {
	p.count++
	return true, nil
}

// Context implements search.Sink. Count mode never requests context,
// but the method must exist to satisfy search.Sink.
func (p *Count) Context(c *search.Ctx) (bool, error) {
	return true, nil
}

// Finish implements search.Sink: writes "path:count\n" if any match was
// tallied, using strconv.AppendInt (no fmt).
func (p *Count) Finish(path string, stats *search.Stats) error {
	if p.count == 0 {
		return nil
	}
	if p.ShowPath {
		if p.Color {
			p.buf = appendColoredBytes(p.buf, ansiPath, p.path)
		} else {
			p.buf = append(p.buf, p.path...)
		}
		p.buf = append(p.buf, ':')
	}
	p.buf = strconv.AppendInt(p.buf, p.count, 10)
	p.buf = append(p.buf, '\n')
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
	p.buf = append(p.buf, '\n')
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
