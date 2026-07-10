package gripgrep

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yackey-labs/gripgrep/search"
)

// matchCollector is a search.Sink that groups matched lines with their
// context into Match values and hands each one to emit as soon as it's
// complete, copying every byte it retains (path and line bytes are only
// valid for the duration of the Searcher's call -- see the package doc's
// memory-safety note). One instance is used per pooled engine.Worker
// (see internal/engine.Run's doc), so its per-file state (path, pending
// before/after context) never has to be synchronized against other
// files being searched concurrently -- only emit's delivery to the
// caller's callback is (via mu/stopped, shared across every worker).
type matchCollector struct {
	before, after int // Options.Before/After (post resolveContext), 0 = no context requested

	path              string
	pendingBefore     []string
	pending           *Match
	pendingAfterCount int

	emit    func(Match) bool
	mu      *sync.Mutex
	stopped *atomic.Bool
}

var _ search.Sink = (*matchCollector)(nil)

// trimLineTerminator strips a trailing "\n" or "\r\n" from line, copying
// it into a fresh string in the same step (see the package doc's
// memory-safety note -- line is only valid for the duration of the
// Searcher's call). search.Match.Line/search.Ctx.Line include the
// terminator that was in the file (search's line-splitting keeps it,
// exactly like bufio.Scanner's underlying token before ScanLines trims
// it) -- Match.Line's own doc promises "no trailing newline," matching
// what the CLI itself prints per line (its own trailing '\n' is the
// terminal write, not file content), so the trim happens once, here, for
// every line this package ever returns.
func trimLineTerminator(line []byte) string {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
	}
	return string(line) // copies
}

func (c *matchCollector) Begin(path string) (bool, error) {
	if c.stopped.Load() {
		return false, nil
	}
	c.path = path
	c.pendingBefore = nil
	c.pending = nil
	c.pendingAfterCount = 0
	return true, nil
}

// flushPending emits c.pending, if any, and reports whether the caller
// should keep going.
func (c *matchCollector) flushPending() bool {
	if c.pending == nil {
		return !c.stopped.Load()
	}
	m := *c.pending
	c.pending = nil
	c.pendingAfterCount = 0
	return c.deliver(m)
}

// deliver serializes emit calls across every concurrently-active worker
// (matches may arrive from many files at once -- see matchCollector's
// doc) and latches c.stopped the first time emit returns false, so every
// worker's next Begin/Matched/Context call observes the stop request.
func (c *matchCollector) deliver(m Match) bool {
	c.mu.Lock()
	ok := !c.stopped.Load() && c.emit(m)
	c.mu.Unlock()
	if !ok {
		c.stopped.Store(true)
	}
	return !c.stopped.Load()
}

func (c *matchCollector) Context(ctx *search.Ctx) (bool, error) {
	if c.stopped.Load() {
		return false, nil
	}
	line := trimLineTerminator(ctx.Line)
	if ctx.After {
		if c.pending != nil {
			c.pending.After = append(c.pending.After, line)
			c.pendingAfterCount++
			if c.after > 0 && c.pendingAfterCount >= c.after {
				return c.flushPending(), nil
			}
		}
		return true, nil
	}
	// Leading (-B) context for the NEXT match: any previous match's
	// after-context block is now known to be complete (a new before-
	// context line only starts once the searcher has moved past the
	// previous match's trailing window), so flush it before starting a
	// new pending-before buffer.
	if !c.flushPending() {
		return false, nil
	}
	c.pendingBefore = append(c.pendingBefore, line)
	if c.before > 0 && len(c.pendingBefore) > c.before {
		c.pendingBefore = c.pendingBefore[len(c.pendingBefore)-c.before:]
	}
	return true, nil
}

func (c *matchCollector) Matched(m *search.Match) (bool, error) {
	if c.stopped.Load() {
		return false, nil
	}
	if !c.flushPending() {
		return false, nil
	}
	lineNumber := 0
	if m.HasLineNumber {
		lineNumber = int(m.LineNumber)
	}
	match := Match{
		Path:       strings.Clone(c.path),
		LineNumber: lineNumber,
		Line:       trimLineTerminator(m.Line),
		Before:     c.pendingBefore,
	}
	c.pendingBefore = nil
	if c.after == 0 {
		return c.deliver(match), nil
	}
	c.pending = &match
	c.pendingAfterCount = 0
	return true, nil
}

func (c *matchCollector) Finish(path string, stats *search.Stats) error {
	c.flushPending()
	return nil
}

// countingSink is a search.Sink that tallies Matched calls per file and
// commits the count into a shared map at Finish -- mirroring
// printer.Count's own "tally from Matched calls, not Stats.MatchCount"
// contract, so it inherits engine's binary-suppression semantics exactly
// (a BinaryQuit walk-discovered file with matches is dropped entirely
// when Finish is never called on it -- see internal/engine's matchTracker
// doc -- matching rg's own `-c` on such a file).
type countingSink struct {
	path  string
	count int

	mu  *sync.Mutex
	out map[string]int
}

var _ search.Sink = (*countingSink)(nil)

func (c *countingSink) Begin(path string) (bool, error) {
	c.path = path
	c.count = 0
	return true, nil
}

func (c *countingSink) Matched(m *search.Match) (bool, error) {
	c.count++
	return true, nil
}

func (c *countingSink) Context(ctx *search.Ctx) (bool, error) {
	return true, nil
}

func (c *countingSink) Finish(path string, stats *search.Stats) error {
	if c.count == 0 {
		return nil
	}
	c.mu.Lock()
	c.out[strings.Clone(c.path)] = c.count
	c.mu.Unlock()
	return nil
}

// pathListSink is a search.Sink that records whether a file matched at
// all, stopping that file's own search at the first match (mirroring
// printer.FilesWithMatches) and appending the path to a shared list at
// Finish.
type pathListSink struct {
	path    string
	matched bool

	mu  *sync.Mutex
	out *[]string
}

var _ search.Sink = (*pathListSink)(nil)

func (p *pathListSink) Begin(path string) (bool, error) {
	p.path = path
	p.matched = false
	return true, nil
}

func (p *pathListSink) Matched(m *search.Match) (bool, error) {
	p.matched = true
	return false, nil
}

func (p *pathListSink) Context(ctx *search.Ctx) (bool, error) {
	return true, nil
}

func (p *pathListSink) Finish(path string, stats *search.Stats) error {
	if !p.matched {
		return nil
	}
	p.mu.Lock()
	*p.out = append(*p.out, strings.Clone(p.path))
	p.mu.Unlock()
	return nil
}
