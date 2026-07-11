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
	// Column is rg's --column, already resolved by the caller (rg
	// defaults it to Vimgrep when neither --column nor --no-column was
	// given -- see cmd/gg's Config.Column doc; Standard itself has no
	// opinion on that default). Shows the 1-based byte column of the
	// first match on a line (or, under Vimgrep, of each occurrence).
	// Context lines never carry a column, matching rg exactly.
	Column bool
	// Vimgrep is rg's --vimgrep: prints one row per match OCCURRENCE
	// rather than one row per matched line (a line with N occurrences
	// becomes N rows, each with that occurrence's own column/byte-offset
	// and, when Color is on, only that occurrence highlighted). Context
	// lines are unaffected -- they still print once. See Matched's doc.
	Vimgrep bool
	// ByteOffset is rg's -b/--byte-offset: prints the absolute byte
	// offset of each line (of each match occurrence's own start, under
	// Vimgrep) immediately before the text, after any column field.
	ByteOffset bool

	buf  []byte
	path []byte

	headingDone bool
	haveLast    bool
	lastLine    int64
	lastOffset  int64
	lastLen     int

	// spanScratch is findSpans' pooled result buffer, reused across
	// Matched/Context calls on this Standard -- see findSpans' doc.
	spanScratch []matchSpan
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

// Matched implements search.Sink. Per search's documented line-boundary
// convention (search's lineStep: "Line terminators are considered part
// of the line they terminate"), m.Line includes its trailing '\n' when
// one exists in the source -- trimLineTerminator strips it once here so
// writeLine's own unconditional '\n' doesn't double up, and so the
// gap-detection byte-offset math in writeSeparatorIfGap (which already
// accounts for one terminator byte via its own "+1") sees the same
// terminator-free length convention it was written against.
//
// Match spans (needed for Column, Vimgrep, and match coloring alike) are
// located here via findSpans -- the SAME re-scan of the line's bytes
// through Matcher that computed the search layer's own match, done again
// because the Sink interface (deliberately, see search.Match's doc)
// never carries match bounds through from the search layer: gg's search
// package finds candidate LINES, not exact spans, on its fast path (see
// search/core.go's findByLineFast), and only the printer ever needs exact
// spans, so recomputing them here -- exactly like pre-existing
// color-highlighting already did -- keeps that cost off the no-flags hot
// path entirely (findSpans is skipped unless Column, Vimgrep, or Color
// is on) rather than paying it on every match regardless of whether any
// caller wants it.
//
// A Matcher re-confirming zero spans on the line handed to it (found's
// doc: this happens exactly when the underlying search was inverted --
// see search.Searcher.Invert -- since the Sink has no other way to learn
// that; an inverted "match" is a line the pattern does NOT match) is not
// an error: rg's own real behavior for `--column -v` and `--vimgrep -v`
// is to print the line with no column at all, which is exactly what
// falls out here for free once no span is found.
func (p *Standard) Matched(m *search.Match) (bool, error) {
	line := trimLineTerminator(m.Line)
	p.writeSeparatorIfGap(m.LineNumber, m.HasLineNumber, m.Offset, len(line))

	if p.Vimgrep {
		return p.matchedVimgrep(m, line)
	}

	col := -1
	var spans []matchSpan
	if p.Column || (p.Color && p.Matcher != nil) {
		spans = p.findSpans(line)
	}
	if p.Column && len(spans) > 0 {
		col = spans[0].s + 1
	}
	p.writeLine(line, m.LineNumber, m.HasLineNumber, ':', col, m.Offset, spans)
	return true, nil
}

// matchedVimgrep implements --vimgrep's "one row per match occurrence"
// format: every span findSpans locates on the line becomes its own row,
// each with that occurrence's own column (span start + 1) and byte
// offset (m.Offset + span start -- verified against the real rg binary:
// `-b --vimgrep` reports the OCCURRENCE's offset, not the line's, unlike
// plain -b), and -- when Color is on -- only that occurrence highlighted
// (rg's sink_slow per_match branch colors `&[m]`, not every span on the
// line). A line with zero spans (the Invert case; see Matched's doc)
// still prints exactly one row, with no column, matching `--vimgrep -v`.
func (p *Standard) matchedVimgrep(m *search.Match, line []byte) (bool, error) {
	spans := p.findSpans(line)
	if len(spans) == 0 {
		p.writeLine(line, m.LineNumber, m.HasLineNumber, ':', -1, m.Offset, nil)
		return true, nil
	}
	for _, sp := range spans {
		col := -1
		if p.Column {
			col = sp.s + 1
		}
		p.writeLine(line, m.LineNumber, m.HasLineNumber, ':', col, m.Offset+int64(sp.s), []matchSpan{sp})
	}
	return true, nil
}

// Context implements search.Sink. See Matched's doc for why the line is
// trimmed before use, and for why spans are (re)located here rather than
// carried from the search layer. Context lines never carry a column
// (rg's write_prelude always passes None for context; verified against
// the real binary) or a per-occurrence row split (Vimgrep only affects
// Matched) -- but DO get colored like a normal line when Color is on,
// matching rg's own context sink, which shares the same match-coloring
// path as a matched line.
func (p *Standard) Context(c *search.Ctx) (bool, error) {
	line := trimLineTerminator(c.Line)
	p.writeSeparatorIfGap(c.LineNumber, c.HasLineNumber, c.Offset, len(line))
	var spans []matchSpan
	if p.Color && p.Matcher != nil {
		spans = p.findSpans(line)
	}
	p.writeLine(line, c.LineNumber, c.HasLineNumber, '-', -1, c.Offset, spans)
	return true, nil
}

// trimLineTerminator strips a single trailing '\n' from line, if
// present. A CRLF file's '\r' is left untouched -- it is ordinary line
// content per PLAN.md's CRLF edge case ("\r stays in the line bytes"),
// matching rg exactly: only the '\n' search's line-boundary scan treats
// as a terminator is ever removed.
func trimLineTerminator(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		return line[:n-1]
	}
	return line
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
// buffer: path, line number, column, byte offset, then text, in exactly
// that order -- matching rg's own PreludeWriter field sequence
// (write_path/write_line_number/write_column_number/write_byte_offset)
// byte for byte, including which separator byte sits between which
// fields. sep is ':' for a match, '-' for context, and is reused as the
// separator for every prelude field (column, byte offset included), same
// as rg.
//
// column < 0 omits the column field entirely -- used for context lines
// (which never carry one) and for a matched line on which no span was
// found (the Invert case; see Matched's doc). colorSpans is the set of
// spans to highlight when Color is on; nil/empty means no highlighting
// (Matcher absent, Color off, or -- again -- Invert).
func (p *Standard) writeLine(line []byte, lineNumber int64, hasLineNumber bool, sep byte, column int, offset int64, colorSpans []matchSpan) {
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
	if column >= 0 {
		p.appendPlainNumber(int64(column))
		p.buf = append(p.buf, sep)
	}
	if p.ByteOffset {
		p.appendPlainNumber(offset)
		p.buf = append(p.buf, sep)
	}
	if p.Color && len(colorSpans) > 0 {
		p.appendColoredSpans(line, colorSpans)
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

// appendPlainNumber appends a --column or -b field. rg colors both with
// its "column" color spec (crates/printer/src/standard.rs's
// write_column_number/write_byte_offset both call
// self.config().colors.column()), which has no default entry in rg's own
// default_color_specs() -- unlike the path/line/match specs, which do --
// so by default this only ever emits the mandatory reset wrapping, never
// an actual color code (verified against the real rg binary under
// --color=always: `\x1b[0m5\x1b[0m`, no color escape in between). gg has
// no --colors flag yet (v1 scope), so that's the only case this needs to
// handle.
func (p *Standard) appendPlainNumber(n int64) {
	if p.Color {
		p.buf = append(p.buf, ansiReset...)
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
// locating the 2nd+ match span on a line; see findSpans' subslice
// fallback for why the naive Find(line[pos:]) loop gets those pattern
// classes wrong. This is deliberately not part of the frozen
// match.Matcher interface (some Matcher implementations may not need or
// support resuming mid-line); Standard degrades gracefully when absent.
type findAtMatcher interface {
	FindAt(line []byte, start int) (s, e int, ok bool)
}

// matchSpan is one match occurrence's byte bounds [s, e) within a line,
// as located by findSpans.
type matchSpan struct{ s, e int }

// findSpans locates every match span in line and returns them in
// left-to-right, non-overlapping order, using p.spanScratch as backing
// storage (reused across calls on this Standard -- copy out anything
// that must outlive the next findSpans/Matched/Context call). This is
// the ONE span-finding implementation shared by match coloring,
// --column, and --vimgrep (previously coloring had its own inline
// FindAt/subslice loop, separate from column/vimgrep's -- unifying them
// means all three necessarily agree on what "the match spans in this
// line" means, which matters most for --color=always --vimgrep: it must
// highlight the exact same span each row's column/byte-offset fields are
// computed from).
//
// Prefers Matcher.(findAtMatcher) when available (exact for every
// pattern class, including anchors and -w); falls back to repeated
// Matcher.Find on the remaining suffix otherwise, which is exact for
// literal patterns but can miscolor or miss a 2nd+ occurrence of an
// anchored/word-boundary pattern (see findAtMatcher's doc) -- an existing
// documented limitation, unchanged by this refactor. Returns an empty
// slice (not an error) when Matcher is nil or the line has no span at
// all — the latter is exactly what happens when the underlying search
// was inverted (see Matched's doc), since Matcher genuinely does not
// match an inverted "matched" line.
//
// An empty match starting exactly where the PREVIOUSLY REPORTED match
// ended is skipped, not reported as its own span -- mirroring Go's own
// regexp.FindAllIndex (verified directly against it) and rg's Rust regex
// engine (verified against the real rg binary), both of which suppress
// exactly this case in their all-matches iteration. Without this rule, a
// pattern like `a?` over "aaa" would report a redundant empty match
// glued to the end of the real "a" match right before it -- three real
// single-char matches plus a phantom empty one after each -- which
// neither engine actually produces when asked to find "all matches" in
// one call; findSpans must reproduce that even though it locates spans
// one FindAt/Find call at a time; see findSpans_test.go's mixed
// empty/non-empty regression case.
func (p *Standard) findSpans(line []byte) []matchSpan {
	p.spanScratch = p.spanScratch[:0]
	if p.Matcher == nil {
		return p.spanScratch
	}
	fa, hasFindAt := p.Matcher.(findAtMatcher)
	lastEnd := -1
	for pos := 0; pos <= len(line); {
		var s, e int
		var ok bool
		if hasFindAt {
			s, e, ok = fa.FindAt(line, pos)
		} else {
			s, e, ok = p.Matcher.Find(line[pos:])
			if ok {
				s += pos
				e += pos
			}
		}
		if !ok {
			break
		}
		if s == e && s == lastEnd {
			pos = s + 1
			continue
		}
		p.spanScratch = append(p.spanScratch, matchSpan{s, e})
		lastEnd = e
		if e == s {
			pos = e + 1
			continue
		}
		pos = e
	}
	return p.spanScratch
}

// appendColoredSpans appends line with every span in spans (assumed
// sorted, non-overlapping, per findSpans' contract) wrapped in
// ansiMatch. A zero-width span (e == s) contributes no colored bytes —
// there is nothing to color — but still marks its position: the
// uncolored byte at that position, if any, is emitted as part of the
// next segment (or the final trailing append), never duplicated or
// dropped.
func (p *Standard) appendColoredSpans(line []byte, spans []matchSpan) {
	pos := 0
	for _, sp := range spans {
		p.buf = append(p.buf, line[pos:sp.s]...)
		if sp.e > sp.s {
			p.buf = appendColoredBytes(p.buf, ansiMatch, line[sp.s:sp.e])
		}
		pos = sp.e
	}
	p.buf = append(p.buf, line[pos:]...)
}
