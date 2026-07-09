package search

import "github.com/yackey-labs/gripgrep/match"

// scanOutcome reports why matchByLine stopped processing a buffer window.
type scanOutcome uint8

const (
	// scanContinue means the whole window [pos:len(buf)] was processed;
	// s.pos == len(buf) on return.
	scanContinue scanOutcome = iota
	// scanStop means the Sink returned more=false; the search must abort
	// for the rest of this file.
	scanStop
)

// resetRun clears the per-file scan state. Called at the start of every
// Search/SearchBytes call; the pooled buffers/scratch structs are left
// alone so they carry over across files on the same *Searcher.
func (s *Searcher) resetRun() {
	s.pos = 0
	s.absOffsetBase = 0
	if s.LineNumbers {
		s.lineNumber = 1
	} else {
		s.lineNumber = 0
	}
	s.lastLineCounted = 0
	s.lastLineVisited = 0
	s.afterContextLeft = 0
	s.hasMatched = false
	s.matchCount = 0
}

func (s *Searcher) maxContext() int {
	if s.BeforeContext > s.AfterContext {
		return s.BeforeContext
	}
	return s.AfterContext
}

// isFastPath mirrors rg's is_line_by_line_fast: a whole-buffer literal
// scan is only sound when the pattern is provably unable to match across
// a line terminator, and inversion needs its own (still fast-pathed, see
// matchByLineFastInvert) bookkeeping around the gaps between matches.
func (s *Searcher) isFastPath() bool {
	return s.Matcher.NonMatchingLineTerm()
}

// matchByLine scans buf starting at s.pos, dispatching to the fast
// whole-buffer candidate path or the slow per-line path, and returns once
// the whole window is processed (scanContinue, s.pos == len(buf)) or the
// Sink asked to stop (scanStop).
func (s *Searcher) matchByLine(buf []byte, sink Sink) (scanOutcome, error) {
	if s.isFastPath() {
		return s.matchByLineFast(buf, sink)
	}
	return s.matchByLineSlow(buf, sink)
}

// matchByLineSlow steps line by line, calling Matcher.Verify on each. It
// handles Invert directly (a non-match becomes a "match" and vice versa)
// since there is no whole-buffer prefilter to exploit.
func (s *Searcher) matchByLineSlow(buf []byte, sink Sink) (scanOutcome, error) {
	step := newLineStep(s.pos, len(buf))
	for {
		start, end, ok := step.next(buf)
		if !ok {
			break
		}
		matched := s.Matcher.Verify(withoutTerminator(buf[start:end]))
		s.pos = end

		success := matched != s.Invert
		if success {
			s.hasMatched = true
			s.matchCount++
			if outcome, err := s.beforeContextByLine(buf, sink, start); outcome == scanStop || err != nil {
				return scanStop, err
			}
			more, err := s.sinkMatched(buf, sink, start, end)
			if err != nil {
				return scanStop, err
			}
			if !more {
				return scanStop, nil
			}
		} else if s.afterContextLeft >= 1 {
			more, err := s.sinkContext(buf, sink, start, end, true)
			if err != nil {
				return scanStop, err
			}
			if !more {
				return scanStop, nil
			}
			s.afterContextLeft--
		}
	}
	return scanContinue, nil
}

// matchByLineFast jumps straight to candidate bytes via Matcher.FindCandidate
// over the whole buffer, expanding each hit to its enclosing line, rather
// than stepping line by line. Ported from rg's match_by_line_fast.
func (s *Searcher) matchByLineFast(buf []byte, sink Sink) (scanOutcome, error) {
	for s.pos < len(buf) {
		if s.Invert {
			keepGoing, err := s.matchByLineFastInvert(buf, sink)
			if err != nil {
				return scanStop, err
			}
			if !keepGoing {
				return scanStop, nil
			}
			continue
		}

		lineStart, lineEnd, found, err := s.findByLineFast(buf)
		if err != nil {
			return scanStop, err
		}
		if !found {
			break
		}
		s.hasMatched = true
		s.matchCount++
		if s.maxContext() > 0 {
			if outcome, err := s.afterContextByLine(buf, sink, lineStart); outcome == scanStop || err != nil {
				return scanStop, err
			}
			if outcome, err := s.beforeContextByLine(buf, sink, lineStart); outcome == scanStop || err != nil {
				return scanStop, err
			}
		}
		s.pos = lineEnd
		more, err := s.sinkMatched(buf, sink, lineStart, lineEnd)
		if err != nil {
			return scanStop, err
		}
		if !more {
			return scanStop, nil
		}
	}
	if outcome, err := s.afterContextByLine(buf, sink, len(buf)); outcome == scanStop || err != nil {
		return scanStop, err
	}
	s.pos = len(buf)
	return scanContinue, nil
}

// matchByLineFastInvert handles -v on the fast path: it finds the next
// real match (to know where the "gap" of non-matching lines ends) and
// reports every line in that gap as a match. Ported from rg's
// match_by_line_fast_invert.
func (s *Searcher) matchByLineFastInvert(buf []byte, sink Sink) (keepGoing bool, err error) {
	lineStart, lineEnd, found, err := s.findByLineFast(buf)
	if err != nil {
		return false, err
	}

	var gapStart, gapEnd int
	if !found {
		gapStart, gapEnd = s.pos, len(buf)
		s.pos = gapEnd
	} else {
		gapStart, gapEnd = s.pos, lineStart
		s.pos = lineEnd
	}
	if gapStart == gapEnd {
		return true, nil
	}
	s.hasMatched = true
	if outcome, err := s.afterContextByLine(buf, sink, gapStart); outcome == scanStop || err != nil {
		return false, err
	}
	if outcome, err := s.beforeContextByLine(buf, sink, gapStart); outcome == scanStop || err != nil {
		return false, err
	}

	step := newLineStep(gapStart, gapEnd)
	for {
		start, end, ok := step.next(buf)
		if !ok {
			break
		}
		s.matchCount++
		more, err := s.sinkMatched(buf, sink, start, end)
		if err != nil {
			return false, err
		}
		if !more {
			return false, nil
		}
	}
	return true, nil
}

// findByLineFast scans buf from s.pos for the next genuine match, without
// advancing s.pos itself (callers decide what to do with the bounds).
// Candidate hits are verified against just their enclosing line; a
// confirmed hit is reported directly with no further work.
func (s *Searcher) findByLineFast(buf []byte) (lineStart, lineEnd int, found bool, err error) {
	pos := s.pos
	for pos < len(buf) {
		off, kind, ok := s.Matcher.FindCandidate(buf, pos)
		if !ok {
			return 0, 0, false, nil
		}
		ls, le := lineLocate(buf, off, off)
		if ls == len(buf) {
			// Matched beyond the end of the buffer; not a real hit.
			pos = len(buf)
			continue
		}
		if kind == match.Confirmed {
			return ls, le, true, nil
		}
		if s.Matcher.Verify(withoutTerminator(buf[ls:le])) {
			return ls, le, true, nil
		}
		pos = le
	}
	return 0, 0, false, nil
}

// beforeContextByLine emits up to BeforeContext lines preceding upto that
// haven't already been visited (sunk as a match or as context).
func (s *Searcher) beforeContextByLine(buf []byte, sink Sink, upto int) (scanOutcome, error) {
	if s.BeforeContext == 0 {
		return scanContinue, nil
	}
	rangeStart := s.lastLineVisited
	if rangeStart >= upto {
		return scanContinue, nil
	}
	beforeStart := rangeStart + linePreceding(buf[rangeStart:upto], s.BeforeContext-1)

	step := newLineStep(beforeStart, upto)
	for {
		start, end, ok := step.next(buf)
		if !ok {
			break
		}
		more, err := s.sinkContext(buf, sink, start, end, false)
		if err != nil {
			return scanStop, err
		}
		if !more {
			return scanStop, nil
		}
	}
	return scanContinue, nil
}

// afterContextByLine emits up to s.afterContextLeft lines starting at
// s.lastLineVisited, up to upto.
func (s *Searcher) afterContextByLine(buf []byte, sink Sink, upto int) (scanOutcome, error) {
	if s.afterContextLeft == 0 {
		return scanContinue, nil
	}
	step := newLineStep(s.lastLineVisited, upto)
	for {
		start, end, ok := step.next(buf)
		if !ok {
			break
		}
		more, err := s.sinkContext(buf, sink, start, end, true)
		if err != nil {
			return scanStop, err
		}
		if !more {
			return scanStop, nil
		}
		s.afterContextLeft--
		if s.afterContextLeft == 0 {
			break
		}
	}
	return scanContinue, nil
}

// countLines lazily advances the line-number counter up to buf offset
// upto. A no-op unless LineNumbers is set, and cheap even then when
// called repeatedly at monotonically non-decreasing offsets.
func (s *Searcher) countLines(buf []byte, upto int) {
	if !s.LineNumbers {
		return
	}
	if s.lastLineCounted >= upto {
		return
	}
	s.lineNumber += lineCount(buf[s.lastLineCounted:upto])
	s.lastLineCounted = upto
}

func (s *Searcher) sinkMatched(buf []byte, sink Sink, start, end int) (bool, error) {
	s.countLines(buf, start)
	m := &s.matchScratch
	m.Line = buf[start:end]
	m.Offset = s.absOffsetBase + int64(start)
	if s.LineNumbers {
		m.LineNumber = s.lineNumber
		m.HasLineNumber = true
	} else {
		m.LineNumber = 0
		m.HasLineNumber = false
	}
	more, err := sink.Matched(m)
	if err != nil || !more {
		return false, err
	}
	s.lastLineVisited = end
	s.afterContextLeft = s.AfterContext
	return true, nil
}

func (s *Searcher) sinkContext(buf []byte, sink Sink, start, end int, after bool) (bool, error) {
	s.countLines(buf, start)
	c := &s.ctxScratch
	c.Line = buf[start:end]
	c.Offset = s.absOffsetBase + int64(start)
	c.After = after
	if s.LineNumbers {
		c.LineNumber = s.lineNumber
		c.HasLineNumber = true
	} else {
		c.LineNumber = 0
		c.HasLineNumber = false
	}
	more, err := sink.Context(c)
	if err != nil || !more {
		return false, err
	}
	s.lastLineVisited = end
	return true, nil
}

// rollConsume decides how many bytes of the just-fully-scanned buf may be
// discarded before the next fill(), retaining just enough trailing,
// not-yet-sunk data to serve as before-context for a match in the next
// window. Ported from rg's Core::roll. Must only be called once
// s.pos == len(buf) (the window has been fully processed).
func (s *Searcher) rollConsume(buf []byte) int {
	var consumed int
	if s.maxContext() == 0 {
		consumed = len(buf)
	} else {
		contextStart := linePreceding(buf, s.BeforeContext)
		consumed = contextStart
		if s.lastLineVisited > consumed {
			consumed = s.lastLineVisited
		}
	}
	s.countLines(buf, consumed)
	s.absOffsetBase += int64(consumed)
	s.lastLineCounted = 0
	s.lastLineVisited = 0
	s.pos = len(buf) - consumed
	return consumed
}
