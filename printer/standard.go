package printer

import (
	"strconv"

	"github.com/rivo/uniseg"
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
	// OnlyMatching is rg's -o/--only-matching: renders one row per match
	// OCCURRENCE, like Vimgrep, but with the row's text narrowed to just
	// the matched substring (empty for a zero-width match -- still its
	// own row, never suppressed) instead of the whole line. Has no
	// effect when Vimgrep is also set (checked first in Matched -- see
	// cmd/gg's Config.OnlyMatching doc, verified against the real rg
	// binary: Vimgrep wins outright regardless of flag order) and no
	// effect on a line reported via Invert (Matched finds zero spans
	// there exactly as the non-OnlyMatching path does, so the whole
	// line prints -- matches the real rg binary's `-o -v`, which is not
	// an error).
	OnlyMatching bool
	// MaxColumns is rg's -M/--max-columns: 0 means unlimited (the
	// omission check in writeLine is skipped entirely). See cmd/gg's
	// Config.MaxColumns doc for why 0 is a safe unlimited sentinel.
	MaxColumns int
	// MaxColumnsPreview is rg's --max-columns-preview: see cmd/gg's
	// Config.MaxColumnsPreview doc. No effect unless MaxColumns > 0.
	MaxColumnsPreview bool
	// Trim is rg's --trim: strips leading ASCII whitespace from every
	// printed line (matched, context, and -- since a row's "line" IS
	// the matched substring there -- OnlyMatching alike), applied before
	// the MaxColumns length check but never changing an already-computed
	// Column/ByteOffset field. See cmd/gg's Config.Trim doc.
	Trim bool
	// Null is rg's -0/--null: the path's own terminator -- the '\n' after
	// a heading path line, or the prelude separator (sep, below) right
	// after an inline path -- becomes a NUL byte instead. Every OTHER
	// separator in the row (line-number/column/byte-offset fields, and
	// the field-match/field-context separator between the last prelude
	// field and the text) is completely unaffected, including when
	// those are themselves customized -- verified against the real rg
	// binary: `--field-match-separator='|' --null -H -n` renders
	// "path\x00N|text", NUL for the path only, '|' everywhere else. See
	// writeLine.
	Null bool
	// MatchFieldSep is rg's --field-match-separator: replaces EVERY ':'
	// field separator on a matched line -- path, line number, column,
	// byte offset -- with this instead. Callers must always set this
	// (there is no nil-means-default fallback here; NewStandard does NOT
	// set it -- see wire.go's buildCLISink, which resolves rg's own ":"
	// default before constructing Standard). Overridden for the path
	// field specifically by Null, which wins when both are set (verified
	// against the real rg binary: `--field-match-separator='|' --null`
	// renders "path\x00N|text" -- NUL for the path, '|' everywhere else).
	MatchFieldSep []byte
	// ContextFieldSep is rg's --field-context-separator: the same idea as
	// MatchFieldSep, but for context lines' '-' separator. Defaults to
	// "-", resolved the same way (see MatchFieldSep's doc).
	ContextFieldSep []byte
	// GapSeparator is rg's --context-separator: used both for the
	// intra-file "--" line between discontiguous matched/context runs
	// (writeSeparatorIfGap) and as the inter-file separator in
	// non-heading context mode (interFileSeparator) -- verified against
	// the real rg binary, both share the exact same string. nil means
	// --no-context-separator: NO separator line at all, in either
	// position (verified: files just run together with no blank line
	// and no gap marker). A non-nil, possibly EMPTY slice is the
	// separator's own content (without a trailing '\n' -- one is always
	// appended after it regardless of content, even when empty, per
	// rg's own doc: "an empty string still inserts a line break").
	// Callers must always set this explicitly (there is no nil-means-
	// default fallback here, unlike a normal Go zero value -- nil
	// specifically means DISABLED; wire.go resolves rg's own "--"
	// default before constructing Standard when --context-separator was
	// never given at all).
	GapSeparator []byte

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
// ShowPath defaulting to true (the common multi-file case) and
// MatchFieldSep/ContextFieldSep/GapSeparator defaulting to rg's own
// defaults (":", "-", "--") -- callers only need to touch those fields
// at all when a caller-facing flag (--field-match-separator/--field-
// context-separator/--context-separator/--no-context-separator)
// actually overrides them; every other Standard behaves exactly as it
// did before those fields existed.
func NewStandard(dest *Dest) *Standard {
	return &Standard{
		dest:            dest,
		buf:             getBuf(),
		ShowPath:        true,
		MatchFieldSep:   []byte(":"),
		ContextFieldSep: []byte("-"),
		GapSeparator:    []byte("--"),
	}
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

	// OnlyMatching is checked BEFORE Vimgrep, not after -- verified
	// against the real rg binary, which surprised this round's probes:
	// `-o --vimgrep` does NOT print the whole line like plain --vimgrep
	// does, it prints just the matched text on each row, same as plain
	// -o. rg's own sink_slow has the same priority: only_matching and
	// per_match (vimgrep) are alternatives in one if/else-if chain with
	// only_matching checked first, so when both are set, only_matching's
	// branch wins outright -- it already iterates one row per match
	// exactly like vimgrep's own branch would have, so nothing about
	// Vimgrep's row-splitting is lost, only its "print the whole line"
	// content choice is overridden. Vimgrep's OTHER effects (implied
	// --column/-H defaults, forced heading off) are resolved in cmd/gg's
	// wire.go independently of this branch, so they still apply.
	if p.OnlyMatching {
		return p.matchedOnlyMatching(m, line)
	}
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
	text, cut := p.trimPrefix(line)
	spans = shiftSpansAfterTrim(spans, cut)
	p.writeLine(text, m.LineNumber, m.HasLineNumber, false, col, m.Offset, spans, spans, 1)
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
	text, cut := p.trimPrefix(line)
	if len(spans) == 0 {
		p.writeLine(text, m.LineNumber, m.HasLineNumber, false, -1, m.Offset, nil, nil, 1)
		return true, nil
	}
	// allSpans (every match on this line) drives -M's omitted-line
	// "N matches" count and preview wording; each row below still only
	// highlights its OWN occurrence (rg's sink_slow per_match branch
	// passes just `[m]` to the shared colored-line writer, but reports
	// the full match count in the omission message regardless -- see
	// writeLine's doc).
	allSpans := shiftSpansAfterTrim(spans, cut)
	for _, sp := range spans {
		col := -1
		if p.Column {
			col = sp.s + 1
		}
		row := shiftSpansAfterTrim([]matchSpan{sp}, cut)
		p.writeLine(text, m.LineNumber, m.HasLineNumber, false, col, m.Offset+int64(sp.s), row, allSpans, 1)
	}
	return true, nil
}

// matchedOnlyMatching implements -o/--only-matching's format: every span
// findSpans locates becomes its own row, like matchedVimgrep, but the
// row's TEXT is narrowed to just that occurrence's matched bytes (an
// empty row for a zero-width match, never suppressed -- verified against
// the real rg binary: `-o 'x*'` prints one blank line per empty-match
// position). Column/byte-offset are still computed from the occurrence's
// position in the ORIGINAL line (unaffected by narrowing the printed
// text down to the match itself), matching Vimgrep's own fields exactly.
// A line with zero spans (the Invert case; see Matched's doc) still
// prints exactly one row with the WHOLE line and no column, matching
// `-o -v`.
func (p *Standard) matchedOnlyMatching(m *search.Match, line []byte) (bool, error) {
	spans := p.findSpans(line)
	if len(spans) == 0 {
		text, _ := p.trimPrefix(line)
		p.writeLine(text, m.LineNumber, m.HasLineNumber, false, -1, m.Offset, nil, nil, 1)
		return true, nil
	}
	for _, sp := range spans {
		col := -1
		if p.Column {
			col = sp.s + 1
		}
		matchText := line[sp.s:sp.e]
		text, cut := p.trimPrefix(matchText)
		// The row's own span always covers the WHOLE (possibly trimmed)
		// text by construction -- there is nothing else on an
		// only-matching row to highlight or to count as a "sibling"
		// match, so the same single span serves as both colorSpans and
		// allSpans (rg's own sink_slow only_matching branch: `write_
		// colored_line(&[Match::new(0, buf.len())], buf)`).
		only := shiftSpansAfterTrim([]matchSpan{{0, len(matchText)}}, cut)
		// termPad is 0 here, not 1: an -o row's text is a bare match
		// with no trailing line terminator to account for, unlike every
		// other row shape -- verified against the real rg binary's
		// --max-columns boundary: `-M N -o` omits only when the match
		// itself is STRICTLY longer than N, one byte later than the
		// `>=` boundary every other row uses (see writeLine's doc).
		p.writeLine(text, m.LineNumber, m.HasLineNumber, false, col, m.Offset+int64(sp.s), only, only, 0)
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
	text, cut := p.trimPrefix(line)
	spans = shiftSpansAfterTrim(spans, cut)
	p.writeLine(text, c.LineNumber, c.HasLineNumber, true, -1, c.Offset, spans, spans, 1)
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
// one applies — GapSeparator+"\n" in --no-heading context mode, "\n" in
// heading mode, matching rg's own inter-file separator placement exactly
// (see interFileSeparator). Files with zero matches (stats.Matched
// false, buffer still empty) produce no output at all.
func (p *Standard) Finish(path string, stats *search.Stats) error {
	return p.dest.WriteBlock(p.buf, p.interFileSeparator())
}

// interFileSeparator returns the separator Dest.WriteBlock should
// prepend before this file's block if it isn't the first block written
// to that Dest (see Dest.WriteBlock and its hasPrinted tracking).
// Heading mode always separates blocks with a blank line, regardless of
// context; --no-heading mode only separates with GapSeparator when
// ContextEnabled AND GapSeparator isn't nil (verified against real rg:
// two widely-separated matches in plain --no-heading, non-context mode
// get no separator at all, even across files; --no-context-separator
// removes the between-file separator too, not just the intra-file one).
func (p *Standard) interFileSeparator() []byte {
	switch {
	case p.Heading:
		return sepHeading
	case p.ContextEnabled && p.GapSeparator != nil:
		return p.gapSepLine()
	default:
		return nil
	}
}

// gapSepLine returns GapSeparator followed by '\n', freshly built each
// call (GapSeparator's content is caller-owned and may be reused across
// many Standard instances -- see wire.go -- so this must never mutate or
// alias it). Only ever called when GapSeparator != nil.
func (p *Standard) gapSepLine() []byte {
	line := make([]byte, 0, len(p.GapSeparator)+1)
	line = append(line, p.GapSeparator...)
	line = append(line, '\n')
	return line
}

// writeSeparatorIfGap appends a GapSeparator+"\n" line when Context is
// enabled, GapSeparator isn't nil (--no-context-separator), and this
// line is not contiguous with the previously emitted line, within the
// current file. Contiguity is determined by line number when available
// (exact, and covers every case this package's tests exercise); when
// line numbers are disabled it falls back to byte offsets, since Offset
// is always populated regardless of numbering. Between-file separators
// are Finish's job (see interFileSeparator); this only ever fires on
// gaps within one Begin/Finish sequence.
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
		if gap && p.GapSeparator != nil {
			p.buf = append(p.buf, p.GapSeparator...)
			p.buf = append(p.buf, '\n')
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
// byte for byte, including which separator sits between which fields.
// isContext selects MatchFieldSep (false, rg default ":") or
// ContextFieldSep (true, rg default "-"), reused as the separator for
// every prelude field (column, byte offset included) and the path
// itself (unless Null overrides just that one field), same as rg.
//
// column < 0 omits the column field entirely -- used for context lines
// (which never carry one) and for a matched line on which no span was
// found (the Invert case; see Matched's doc).
//
// text is the FINAL printed content -- already run through trimPrefix
// (see Matched/Context/matchedVimgrep/matchedOnlyMatching, every one of
// which trims once, up front, rather than leaving it to writeLine, so a
// Vimgrep/OnlyMatching row's several writeLine calls per line never
// re-trim the same prefix) and, under OnlyMatching, already narrowed to
// just the matched substring. colorSpans/allSpans are both expressed as
// positions within text, not within the original source line.
//
// colorSpans is what actually gets highlighted when Color is on.
// allSpans is every match relevant to text's underlying line, used to
// decide -M/--max-columns' omitted-line wording (the "N matches" count)
// -- these two DIFFER under Vimgrep (colorSpans narrows to this one
// row's own occurrence, allSpans is every match on the line: rg's
// sink_slow per_match branch passes only `[m]` to the shared colored-
// line writer, but still reports the line's FULL match count in the
// omission message) and are the same single span under OnlyMatching (by
// construction: a row's whole text IS the match, nothing else to
// count). nil/empty allSpans means -M should use its "fast path"
// wording (an empty matches list is gg's signal, mirroring rg's own,
// that no span-scan happened for this row at all -- see
// writeOmittedLine's doc).
//
// The preview's "M more matches" REMAINING count is a separate story:
// see writeOmittedPreview's doc for why it prefers colorSpans over
// allSpans specifically when Color is rendering (another real rg
// divergence, this one isolated to Vimgrep+Color+preview).
//
// termPad is 1 for every row shape except an OnlyMatching row (0): see
// matchedOnlyMatching's doc for why an -o row's --max-columns boundary
// is one byte later than every other row's.
func (p *Standard) writeLine(text []byte, lineNumber int64, hasLineNumber bool, isContext bool, column int, offset int64, colorSpans, allSpans []matchSpan, termPad int) {
	sep := p.MatchFieldSep
	if isContext {
		sep = p.ContextFieldSep
	}
	if p.Heading {
		if !p.headingDone {
			if p.ShowPath {
				p.appendPath()
				if p.Null {
					p.buf = append(p.buf, 0)
				} else {
					p.buf = append(p.buf, '\n')
				}
			}
			p.headingDone = true
		}
	} else if p.ShowPath {
		p.appendPath()
		if p.Null {
			p.buf = append(p.buf, 0)
		} else {
			p.buf = append(p.buf, sep...)
		}
	}
	if hasLineNumber {
		p.appendLineNumber(lineNumber)
		p.buf = append(p.buf, sep...)
	}
	if column >= 0 {
		p.appendPlainNumber(int64(column))
		p.buf = append(p.buf, sep...)
	}
	if p.ByteOffset {
		p.appendPlainNumber(offset)
		p.buf = append(p.buf, sep...)
	}
	switch {
	case p.MaxColumns > 0 && len(text)+termPad > p.MaxColumns:
		p.writeOmittedLine(text, isContext, allSpans, colorSpans)
	case p.Color && len(colorSpans) > 0:
		p.appendColoredSpans(text, colorSpans)
	default:
		p.buf = append(p.buf, text...)
	}
	p.buf = append(p.buf, '\n')
}

// writeOmittedLine appends -M/--max-columns' replacement content for a
// row whose text exceeded p.MaxColumns (no trailing newline -- writeLine
// appends that uniformly for every row, omitted or not). matches is
// writeLine's allSpans (every match relevant to this row's underlying
// line -- see writeLine's doc); colorSpans is the subset actually
// highlighted in a preview's visible prefix.
//
// Verified against the real rg 15.1.0 binary across every combination
// this round's probes covered:
//   - Without MaxColumnsPreview: a fixed placeholder. len(matches) == 0
//     selects the plain wording (mirrors rg's own fast-path proxy: an
//     empty matches list means no span-scan happened for this row at
//     all, so rg/gg alike have no count to report -- true for plain -M
//     with neither --column nor Color, and for a context line whose
//     Color-driven re-scan found nothing). OnlyMatching ALSO forces the
//     plain wording even though findSpans did run for -o's own
//     rendering (verified: `rg -M N -o` never says "with 1 matches").
//     isContext picks between "matching"/"context" wording; the
//     "N matches" wording (the only branch NOT tense-adjusted, even at
//     N==1 -- verified) never distinguishes context from matched at all,
//     matching rg's own write_exceeded_line exactly.
//   - With MaxColumnsPreview: a prefix of text cut at a whole RUNE
//     boundary (gg's approximation of rg's actual grapheme-CLUSTER
//     boundary -- see previewCutoff's doc for the documented, narrow
//     divergence) followed by " [... omitted end of long line]" when
//     matches is empty (same empty-matches signal as the non-preview
//     branch), else " [... N more match(es)]", tense-adjusted for N==1
//     unlike the non-preview wording above -- both verified against the
//     real binary, not inferred from source alone.
func (p *Standard) writeOmittedLine(text []byte, isContext bool, matches, colorSpans []matchSpan) {
	if p.MaxColumnsPreview {
		p.writeOmittedPreview(text, matches, colorSpans)
		return
	}
	switch {
	case len(matches) == 0 || p.OnlyMatching:
		if isContext {
			p.buf = append(p.buf, "[Omitted long context line]"...)
		} else {
			p.buf = append(p.buf, "[Omitted long matching line]"...)
		}
	default:
		p.buf = append(p.buf, "[Omitted long line with "...)
		p.buf = strconv.AppendInt(p.buf, int64(len(matches)), 10)
		p.buf = append(p.buf, " matches]"...)
	}
}

// writeOmittedPreview implements --max-columns-preview's replacement
// content: see writeOmittedLine's doc for the wording rules this
// applies, verified against the real rg binary.
func (p *Standard) writeOmittedPreview(text []byte, matches, colorSpans []matchSpan) {
	cut := previewCutoff(text, p.MaxColumns)
	visible := text[:cut]
	if p.Color && len(colorSpans) > 0 {
		p.appendColoredSpans(visible, clipSpansTo(colorSpans, cut))
	} else {
		p.buf = append(p.buf, visible...)
	}
	if len(matches) == 0 {
		p.buf = append(p.buf, " [... omitted end of long line]"...)
		return
	}
	// The "N more matches" remaining-count uses colorSpans (whatever
	// THIS row highlighted) instead of matches (every match on the
	// underlying line) when Color is genuinely rendering -- a real
	// divergence in rg's own implementation this round's re-review
	// caught by reproducing it directly against the binary: `--vimgrep
	// --color=always -M --max-columns-preview` gives each row an
	// INDEPENDENT count (is THIS row's own occurrence visible in ITS
	// own preview?), while the identical invocation without --color
	// gives every row on the line the SAME whole-line count. rg's own
	// write_colored_line, when color actually renders, receives
	// whatever narrowed match list the caller built for this one row
	// (under --vimgrep's per_match branch, just `[m]`) and threads that
	// SAME list through to both the highlighting AND the remaining-
	// count math; its no-color fallback (write_line) ignores that
	// per-row narrowing entirely and always consults the full match
	// list. For every row type OTHER than Vimgrep, colorSpans and
	// matches are already the identical list (see writeLine's doc), so
	// this preference is a no-op there -- it only ever changes anything
	// for Vimgrep+Color.
	countSpans := matches
	if p.Color && len(colorSpans) > 0 {
		countSpans = colorSpans
	}
	remaining := 0
	for _, sp := range countSpans {
		if sp.s >= cut {
			remaining++
		}
	}
	p.buf = append(p.buf, " [... "...)
	p.buf = strconv.AppendInt(p.buf, int64(remaining), 10)
	if remaining == 1 {
		p.buf = append(p.buf, " more match]"...)
	} else {
		p.buf = append(p.buf, " more matches]"...)
	}
}

// previewCutoff returns the byte offset of the end of the Nth grapheme
// CLUSTER in text (or len(text), if text has fewer than n clusters) --
// matching rg's actual cut point exactly (rg's write_exceeded_line's
// preview branch takes N grapheme clusters via the unicode-segmentation
// crate's UAX#29 implementation, not runes or bytes). Uses github.com/
// rivo/uniseg, a UAX#29 grapheme-cluster segmenter, so a combining mark
// or ZWJ sequence straddling the cut point is kept whole exactly where
// rg keeps it whole -- an earlier rune-boundary approximation here
// diverged from the real rg binary at every cut point from the first
// combining mark onward (verified with an "e" + COMBINING ACUTE ACCENT
// fixture rendering as one visual "é": rune-counting cuts one grapheme
// short as soon as the combining mark enters the visible prefix). See
// TestStandard_MaxColumnsPreviewCombiningMarkBoundary and this round's
// e2e case for the fixed regression.
func previewCutoff(text []byte, n int) int {
	cut := 0
	state := -1
	for i := 0; i < n && cut < len(text); i++ {
		cluster, _, _, newState := uniseg.FirstGraphemeCluster(text[cut:], state)
		if len(cluster) == 0 {
			break
		}
		cut += len(cluster)
		state = newState
	}
	return cut
}

// clipSpansTo returns spans (assumed sorted, non-overlapping, per
// findSpans' contract) narrowed to [0, cut): a span starting at or past
// cut is dropped entirely, one straddling cut is truncated to end at
// cut. Always returns a fresh slice -- never mutates spans' backing
// array, which may be p.spanScratch or another row's shared allSpans.
func clipSpansTo(spans []matchSpan, cut int) []matchSpan {
	out := make([]matchSpan, 0, len(spans))
	for _, sp := range spans {
		if sp.s >= cut {
			break
		}
		e := sp.e
		if e > cut {
			e = cut
		}
		out = append(out, matchSpan{sp.s, e})
	}
	return out
}

// trimPrefix strips p.Trim's configured leading ASCII whitespace bytes
// (tab, newline, vertical tab, form feed, carriage return, space -- rg's
// own trim_ascii_prefix's exact set, crates/printer/src/util.rs) from
// text, returning the trimmed slice and the number of bytes cut (0 when
// Trim is off or there was nothing to cut) so callers can shift any
// already-computed span positions by the same amount before using them
// against the now-shorter text -- see shiftSpansAfterTrim.
func (p *Standard) trimPrefix(text []byte) ([]byte, int) {
	if !p.Trim {
		return text, 0
	}
	n := 0
	for n < len(text) && isTrimSpace(text[n]) {
		n++
	}
	return text[n:], n
}

func isTrimSpace(b byte) bool {
	switch b {
	case '\t', '\n', '\v', '\f', '\r', ' ':
		return true
	}
	return false
}

// shiftSpansAfterTrim adjusts spans (assumed sorted, located against the
// UNTRIMMED text) onto the text trimPrefix produced after cutting n
// leading bytes off its front: every span's bounds shift left by n and
// clamp to stay within [0, ...) -- a span that started inside the
// trimmed-away prefix keeps only the portion (if any) that survives past
// the cut, since those bytes are simply no longer part of what's
// printed (reachable in principle -- e.g. a pattern matching literal
// leading whitespace, combined with --trim -- verified this does not
// panic or misrender against the real rg binary, though rg's own
// behavior for that specific corner has not been exhaustively diffed).
//
// Returns spans UNCHANGED (same backing array) when n == 0 -- the common
// case, since --trim is opt-in -- never allocating on that path.
// Otherwise always returns a fresh slice, since spans may be
// p.spanScratch or another row's shared allSpans that must not be
// mutated in place.
func shiftSpansAfterTrim(spans []matchSpan, n int) []matchSpan {
	if n == 0 || len(spans) == 0 {
		return spans
	}
	out := make([]matchSpan, 0, len(spans))
	for _, sp := range spans {
		s, e := sp.s-n, sp.e-n
		if e < 0 {
			continue
		}
		if s < 0 {
			s = 0
		}
		out = append(out, matchSpan{s, e})
	}
	return out
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
	p.spanScratch = findMatchSpans(p.spanScratch[:0], p.Matcher, line)
	return p.spanScratch
}

// CountMatches returns the number of match occurrences on line, using the
// exact same greedy, non-overlapping, empty-match-aware span-finding as
// -o/--count-matches (findMatchSpans) -- so the per-line occurrence count
// that feeds --stats' "N matches" line agrees, byte for byte, with what
// --count-matches would report for the same input. On a line that does not
// match (an inverted -v line delivered as a "matched" result, for
// instance) it returns 0, matching rg's stats accounting, which counts the
// pattern's real occurrences and so reports 0 matches / N matched lines
// under -v. matcher nil returns 0. A scratch slice is allocated per call;
// this is only ever invoked when --stats is active, never on the default
// hot path.
//
// line's trailing '\n' is stripped first (trimLineTerminator), exactly as
// the -o/--count-matches sinks do before their own findSpans: an empty
// pattern must count one position per character plus one before the line's
// end, NOT an extra phantom occurrence at the '\n' itself, so --stats' "N
// matches" stays identical to --count-matches for every pattern (verified:
// empty pattern over the 3-line fixture is 26, not 29).
func CountMatches(matcher match.Matcher, line []byte) int {
	if matcher == nil {
		return 0
	}
	return len(findMatchSpans(nil, matcher, trimLineTerminator(line)))
}

// findMatchSpans is findSpans' actual algorithm, factored out to a
// package-level function (rather than a *Standard method) so Count can
// share it too -- Count.Matched needs exactly this same span-finding
// logic (with its own reused scratch slice, spanScratch, mirroring
// Standard's) to implement -o's "count OCCURRENCES, not matched lines"
// effect on -c (see Count.OnlyMatching's doc). dst is the caller's own
// reused, len-0 scratch slice (append target); matcher nil returns dst
// unchanged (empty).
func findMatchSpans(dst []matchSpan, matcher match.Matcher, line []byte) []matchSpan {
	if matcher == nil {
		return dst
	}
	fa, hasFindAt := matcher.(findAtMatcher)
	lastEnd := -1
	for pos := 0; pos <= len(line); {
		var s, e int
		var ok bool
		if hasFindAt {
			s, e, ok = fa.FindAt(line, pos)
		} else {
			s, e, ok = matcher.Find(line[pos:])
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
		dst = append(dst, matchSpan{s, e})
		lastEnd = e
		if e == s {
			pos = e + 1
			continue
		}
		pos = e
	}
	return dst
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
