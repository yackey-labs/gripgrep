package printer

import (
	"strconv"

	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/search"
)

// Preallocated separators passed to Dest.WriteBlock by
// interFileSeparator, so choosing one never allocates.
var (
	sepHeading      = []byte("\n")
	sepContextBreak = []byte("--\n")
)

// Standard formats matched and context lines as rg's default output:
// "path:line:text" per match ("path:text" when line numbers are off),
// "path-line-text" for context lines, with "--" between discontiguous
// runs of output when Context is enabled. In Heading mode it instead
// prints the path once above each file's lines.
//
// One Standard should be constructed per worker goroutine (NewStandard)
// and reused across every file that worker searches: Begin resets its
// private buffer lazily and Finish flushes the whole file's output to
// the shared Dest as one locked write. A Standard's buffer is not safe
// for concurrent use — do not share one across goroutines.
type Standard struct {
	dest *Dest

	// Color enables ANSI coloring of the path, line number, and match
	// spans (rg's default TTY colors). When false, Matcher.Find is never
	// called — the entire color-formatting path is elided at the call
	// site, not merely skipped after being reached.
	Color bool
	// Matcher locates match spans for coloring; required when Color is
	// true (color is silently skipped if nil), unused otherwise.
	Matcher match.Matcher
	// Heading switches to TTY heading mode: the path is printed once
	// above each file's matches instead of prefixing every line.
	Heading bool
	// ShowPath controls whether the path appears at all. Callers should
	// set this false for the single-explicit-file case (rg suppresses
	// the path when there's exactly one named file and -H wasn't
	// forced); Standard has no way to know how many files are being
	// searched, so this must be driven from outside.
	ShowPath bool
	// ContextEnabled turns on "--" separators between discontiguous runs
	// of matched/context lines within one file. Callers should set this
	// true iff BeforeContext or AfterContext is non-zero on the
	// search.Searcher — Standard has no visibility into search
	// configuration itself (mirrors rg's own printer-level
	// separator_context config, which is likewise independent of the
	// CLI flag parsing that sets it). Named distinctly from the Context
	// method (which handles one context line) to avoid a field/method
	// name collision.
	ContextEnabled bool

	buf  []byte
	path []byte

	headingDone bool
	haveLast    bool
	lastLine    int64
	lastOffset  int64
	lastLen     int
}

// NewStandard returns a Standard flushing completed files to dest, with
// ShowPath defaulting to true (the common multi-file case).
func NewStandard(dest *Dest) *Standard {
	return &Standard{dest: dest, buf: getBuf(), ShowPath: true}
}

var _ search.Sink = (*Standard)(nil)

// Begin implements search.Sink: resets the per-file buffer and
// per-file gap/heading tracking state, and converts path to []byte
// once for reuse across every Matched/Context call in this file.
func (p *Standard) Begin(path string) (bool, error) {
	p.buf = resetBuf(p.buf)
	p.path = append(p.path[:0], path...)
	p.headingDone = false
	p.haveLast = false
	return true, nil
}

// Matched implements search.Sink.
func (p *Standard) Matched(m *search.Match) (bool, error) {
	p.writeSeparatorIfGap(m.LineNumber, m.HasLineNumber, m.Offset, len(m.Line))
	p.writeLine(m.Line, m.LineNumber, m.HasLineNumber, ':')
	return true, nil
}

// Context implements search.Sink.
func (p *Standard) Context(c *search.Ctx) (bool, error) {
	p.writeSeparatorIfGap(c.LineNumber, c.HasLineNumber, c.Offset, len(c.Line))
	p.writeLine(c.Line, c.LineNumber, c.HasLineNumber, '-')
	return true, nil
}

// Finish implements search.Sink: flushes the accumulated buffer to Dest
// as one block (WriteBlock), preceded by a between-file separator when
// one applies — "--\n" in --no-heading context mode, "\n" in heading
// mode, matching rg's own inter-file separator placement exactly (see
// interFileSeparator). Files with zero matches (stats.Matched false,
// buffer still empty) produce no output at all.
func (p *Standard) Finish(path string, stats *search.Stats) error {
	return p.dest.WriteBlock(p.buf, p.interFileSeparator())
}

// interFileSeparator returns the separator Dest.WriteBlock should
// prepend before this file's block if it isn't the first block written
// to that Dest (see Dest.WriteBlock and its hasPrinted tracking).
// Heading mode always separates blocks with a blank line, regardless of
// context; --no-heading mode only separates with "--" when
// ContextEnabled (verified against real rg: two widely-separated
// matches in plain --no-heading, non-context mode get no separator at
// all, even across files).
func (p *Standard) interFileSeparator() []byte {
	switch {
	case p.Heading:
		return sepHeading
	case p.ContextEnabled:
		return sepContextBreak
	default:
		return nil
	}
}

// writeSeparatorIfGap appends a "--\n" separator line when Context is
// enabled and this line is not contiguous with the previously emitted
// line, within the current file. Contiguity is determined by line
// number when available (exact, and covers every case this package's
// tests exercise); when line numbers are disabled it falls back to byte
// offsets, since Offset is always populated regardless of numbering.
// Between-file separators are Finish's job (see interFileSeparator);
// this only ever fires on gaps within one Begin/Finish sequence.
func (p *Standard) writeSeparatorIfGap(lineNumber int64, hasLineNumber bool, offset int64, lineLen int) {
	if !p.ContextEnabled {
		return
	}
	if p.haveLast {
		var gap bool
		if hasLineNumber {
			gap = lineNumber != p.lastLine+1
		} else {
			gap = offset != p.lastOffset+int64(p.lastLen)+1
		}
		if gap {
			p.buf = append(p.buf, '-', '-', '\n')
		}
	}
	p.haveLast = true
	p.lastLine = lineNumber
	p.lastOffset = offset
	p.lastLen = lineLen
}

// writeLine appends one formatted line (match or context) to the
// buffer. sep is ':' for a match, '-' for context, matching rg's field
// separator convention for both the path prefix and the line-number
// prefix.
func (p *Standard) writeLine(line []byte, lineNumber int64, hasLineNumber bool, sep byte) {
	if p.Heading {
		if !p.headingDone {
			if p.ShowPath {
				p.appendPath()
				p.buf = append(p.buf, '\n')
			}
			p.headingDone = true
		}
	} else if p.ShowPath {
		p.appendPath()
		p.buf = append(p.buf, sep)
	}
	if hasLineNumber {
		p.appendLineNumber(lineNumber)
		p.buf = append(p.buf, sep)
	}
	if p.Color && p.Matcher != nil {
		p.appendColoredLine(line)
	} else {
		p.buf = append(p.buf, line...)
	}
	p.buf = append(p.buf, '\n')
}

func (p *Standard) appendPath() {
	if p.Color {
		p.buf = appendColoredBytes(p.buf, ansiPath, p.path)
	} else {
		p.buf = append(p.buf, p.path...)
	}
}

func (p *Standard) appendLineNumber(n int64) {
	if p.Color {
		p.buf = append(p.buf, ansiReset...)
		p.buf = append(p.buf, ansiLine...)
		p.buf = strconv.AppendInt(p.buf, n, 10)
		p.buf = append(p.buf, ansiReset...)
	} else {
		p.buf = strconv.AppendInt(p.buf, n, 10)
	}
}

// findAtMatcher is implemented by Matchers that can resume a search
// from an arbitrary byte offset within the ORIGINAL line — evaluating
// anchors (^) and word boundaries (-w) relative to the whole line, not
// a subslice of it. Standard prefers this via type assertion when
// locating the 2nd+ match span on a line; see appendColoredLineSubslice
// for why the naive Find(line[pos:]) loop gets those pattern classes
// wrong. This is deliberately not part of the frozen match.Matcher
// interface (some Matcher implementations may not need or support
// resuming mid-line); Standard degrades gracefully when absent.
type findAtMatcher interface {
	FindAt(line []byte, start int) (s, e int, ok bool)
}

// appendColoredLine appends line with every match span wrapped in
// ansiMatch, preferring Matcher.(findAtMatcher) when available (exact
// for every pattern class) and falling back to the subslice-Find loop
// otherwise (exact for literals; see appendColoredLineSubslice's
// caveat).
func (p *Standard) appendColoredLine(line []byte) {
	if fa, ok := p.Matcher.(findAtMatcher); ok {
		p.appendColoredLineFindAt(line, fa)
		return
	}
	p.appendColoredLineSubslice(line)
}

// appendColoredLineFindAt colors every match span using FindAt, which
// evaluates the pattern against the whole line at each resumed offset
// — correct for anchored (^) and word-boundary (-w) patterns as well as
// literals.
func (p *Standard) appendColoredLineFindAt(line []byte, fa findAtMatcher) {
	pos := 0
	for pos <= len(line) {
		s, e, ok := fa.FindAt(line, pos)
		if !ok {
			break
		}
		p.buf = append(p.buf, line[pos:s]...)
		p.buf = appendColoredBytes(p.buf, ansiMatch, line[s:e])
		if e == s {
			// Zero-width match: emit the byte at e (if any) uncolored
			// and advance by one to guarantee forward progress.
			if e < len(line) {
				p.buf = append(p.buf, line[e])
			}
			pos = e + 1
			continue
		}
		pos = e
	}
	p.buf = append(p.buf, line[pos:]...)
}

// appendColoredLineSubslice is the fallback used when Matcher doesn't
// implement findAtMatcher: it repeatedly calls Find on the remaining
// suffix of line to color every occurrence (Find itself only reports
// the leftmost match), guarding against a zero-width match looping
// forever. This is exact for literal patterns, but Find's "leftmost
// match within the given []byte" contract means a subslice shifts what
// "start of line" or a word boundary means — so an anchored (^) or
// word-boundary (-w) pattern's 2nd+ occurrence on a line can be colored
// at the wrong span, or missed, under this fallback.
func (p *Standard) appendColoredLineSubslice(line []byte) {
	pos := 0
	for pos <= len(line) {
		s, e, ok := p.Matcher.Find(line[pos:])
		if !ok {
			break
		}
		s += pos
		e += pos
		p.buf = append(p.buf, line[pos:s]...)
		p.buf = appendColoredBytes(p.buf, ansiMatch, line[s:e])
		if e == s {
			// Zero-width match: emit the byte at e (if any) uncolored
			// and advance by one to guarantee forward progress.
			if e < len(line) {
				p.buf = append(p.buf, line[e])
			}
			pos = e + 1
			continue
		}
		pos = e
	}
	p.buf = append(p.buf, line[pos:]...)
}
