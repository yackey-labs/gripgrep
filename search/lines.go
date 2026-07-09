package search

import "bytes"

const lineTerm = '\n'

// lineStep is an explicit line iterator over buf[pos:end] (end is fixed at
// construction; pos advances with each next call), ported from rg's
// LineStep. Line terminators are considered part of the line they
// terminate; the returned ranges are always non-empty.
type lineStep struct {
	pos, end int
}

func newLineStep(start, end int) lineStep {
	return lineStep{pos: start, end: end}
}

func (ls *lineStep) next(buf []byte) (start, end int, ok bool) {
	region := buf[:ls.end]
	if ls.pos > len(region) {
		return 0, 0, false
	}
	if i := bytes.IndexByte(region[ls.pos:], lineTerm); i >= 0 {
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

// lineCount returns the number of line terminators in buf.
func lineCount(buf []byte) int64 {
	return int64(bytes.Count(buf, []byte{lineTerm}))
}

// withoutTerminator strips a single trailing line terminator, if present.
// Used before handing a line to the matcher so `(?m)^$`-style anchors
// don't spuriously match past the terminator.
func withoutTerminator(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == lineTerm {
		return line[:n-1]
	}
	return line
}

// lineLocate expands the byte range [start,end) to the bounds of the
// line(s) it falls within. Ported from rg's lines::locate.
func lineLocate(buf []byte, start, end int) (lineStart, lineEnd int) {
	if i := bytes.LastIndexByte(buf[:start], lineTerm); i >= 0 {
		lineStart = i + 1
	} else {
		lineStart = 0
	}
	if end > lineStart && buf[end-1] == lineTerm {
		lineEnd = end
	} else if i := bytes.IndexByte(buf[end:], lineTerm); i >= 0 {
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
func linePreceding(buf []byte, count int) int {
	return precedingByPos(buf, len(buf), count)
}

func precedingByPos(buf []byte, pos, count int) int {
	if pos == 0 {
		return 0
	}
	if buf[pos-1] == lineTerm {
		pos--
	}
	for {
		i := bytes.LastIndexByte(buf[:pos], lineTerm)
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
