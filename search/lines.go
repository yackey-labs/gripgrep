package search

import "bytes"

// defaultLineTerm is the line terminator for the default and --crlf
// search paths ('\n'); --null-data uses '\x00' instead. The active
// terminator is carried per-Searcher (Searcher.lineTerm, resolved from
// NullData) and threaded into every line-scanning helper below as an
// explicit byte argument -- reading it as a value rather than a package
// constant costs nothing on the default hot path (bytes.IndexByte takes
// the byte in a register either way), which keeps the zero-cost rule.
const defaultLineTerm = '\n'

// resolveLineTerm maps NullData to the record/line terminator byte: '\x00'
// when null-data delimiting is active, otherwise the default '\n' (CRLF
// still splits on '\n', so it is not distinguished here).
func resolveLineTerm(nullData bool) byte {
	if nullData {
		return 0
	}
	return defaultLineTerm
}

// lineStep is an explicit line iterator over buf[pos:end] (end is fixed at
// construction; pos advances with each next call), ported from rg's
// LineStep. Line terminators are considered part of the line they
// terminate; the returned ranges are always non-empty. term is the
// terminator byte this iterator splits on ('\n' or '\x00').
type lineStep struct {
	pos, end int
	term     byte
}

func newLineStep(term byte, start, end int) lineStep {
	return lineStep{pos: start, end: end, term: term}
}

func (ls *lineStep) next(buf []byte) (start, end int, ok bool) {
	region := buf[:ls.end]
	if ls.pos > len(region) {
		return 0, 0, false
	}
	if i := bytes.IndexByte(region[ls.pos:], ls.term); i >= 0 {
		start, end = ls.pos, ls.pos+i+1
		ls.pos = end
		return start, end, true
	}
	if ls.pos < len(region) {
		start, end = ls.pos, len(region)
		ls.pos = end
		return start, end, true
	}
	return 0, 0, false
}

// lineCount returns the number of line terminators (term) in buf.
func lineCount(buf []byte, term byte) int64 {
	return int64(bytes.Count(buf, []byte{term}))
}

// withoutTerminator strips a single trailing line terminator, if present,
// before a line is handed to the matcher so `(?m)^$`-style anchors don't
// spuriously match past the terminator. Under CRLF a trailing '\r' before
// the stripped '\n' is removed too, so the match window never sees the
// carriage return (mirrors rg's crlf regex, where '.' and spans do not
// see the trailing '\r' -- an interior lone '\r' with no following '\n' is
// NOT a terminator and stays in the window, a documented divergence from
// rg's full crlf regex mode which Go's regexp cannot express). The `if
// crlf` branch is a single, perfectly-predicted no-op on the default and
// null-data paths (crlf is always false there), so it adds no measurable
// cost.
func withoutTerminator(line []byte, term byte, crlf bool) []byte {
	n := len(line)
	if n == 0 {
		return line
	}
	if line[n-1] == term {
		n--
		if crlf && n > 0 && line[n-1] == '\r' {
			n--
		}
	}
	return line[:n]
}

// lineLocate expands the byte range [start,end) to the bounds of the
// line(s) it falls within. Ported from rg's lines::locate.
func lineLocate(buf []byte, term byte, start, end int) (lineStart, lineEnd int) {
	if i := bytes.LastIndexByte(buf[:start], term); i >= 0 {
		lineStart = i + 1
	} else {
		lineStart = 0
	}
	if end > lineStart && buf[end-1] == term {
		lineEnd = end
	} else if i := bytes.IndexByte(buf[end:], term); i >= 0 {
		lineEnd = end + i + 1
	} else {
		lineEnd = len(buf)
	}
	return lineStart, lineEnd
}

// linePreceding returns the minimal starting offset of the line that
// occurs count lines before the last line in buf. If buf ends with a line
// terminator, the terminator is considered part of the last line. Ported
// from rg's lines::preceding / preceding_by_pos.
func linePreceding(buf []byte, term byte, count int) int {
	return precedingByPos(buf, term, len(buf), count)
}

func precedingByPos(buf []byte, term byte, pos, count int) int {
	if pos == 0 {
		return 0
	}
	if buf[pos-1] == term {
		pos--
	}
	for {
		i := bytes.LastIndexByte(buf[:pos], term)
		if i < 0 {
			return 0
		}
		if count == 0 {
			return i + 1
		}
		if i == 0 {
			return 0
		}
		count--
		pos = i
	}
}
