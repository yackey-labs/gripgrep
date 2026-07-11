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
	s.lineTerm = resolveLineTerm(s.NullData)
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
	s.matchLimitReached = s.MaxCount != nil && *s.MaxCount <= 0
	s.matchLimitReachedAtStart = s.matchLimitReached
	s.hasBinaryOffset = false
	s.binaryOffset = 0
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
// PassThru always forces the slow path (mirrors rg's own
// is_line_by_line_fast, which returns false unconditionally when
// passthru is set): the fast path's candidate-scanning loop has no
// notion of "sink every non-matching line too," and passthru's -m
// interaction (see matchByLineSlow's PassThru branch) depends on the
// slow path's per-line Verify call being made (or skipped) exactly
// once per line.
func (s *Searcher) isFastPath() bool {
	return s.Matcher.NonMatchingLineTerm() && !s.PassThru
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
//
// -m/--max-count: once matchLimitReached is set (by an earlier iteration
// reaching MaxCount, or already true from the start for MaxCount==0 --
// see resetRun/runChunk), success is forced false regardless of what
// Verify returned, so no FURTHER match is ever found or sunk -- but the
// `else if afterContextLeft >= 1` branch still fires normally, draining
// any trailing after-context still owed to the last counted match. Once
// both the limit is reached AND that trailing context is fully drained,
// the loop returns scanStop immediately rather than continuing to Verify
// lines that can no longer produce any output -- UNLESS PassThru is set
// (see the branch below and its own doc), which needs the scan to keep
// running all the way to EOF, sinking every remaining line as context.
//
// PassThru's own -m interaction is a real, verified rg quirk, not a gg
// invention: rg's own shortest_match (crates/searcher/src/searcher/
// core.rs) unconditionally returns "no match" once has_exceeded_match_
// limit() is true, REGARDLESS of passthru -- it's the surrounding
// success/passthru dispatch that differs. Without passthru, that forced
// "no match" is moot (the scan already stopped in the same iteration the
// limit was first reached, well before any later line could observe it
// -- see the bottom-of-loop check). With passthru, the scan keeps going,
// so every line from then on computes success as `false != Invert`:
// under normal (non-inverted) search this just means "render as context,
// same as any other non-matching line" (matched forced false, XOR
// Invert=false stays false) -- but under -v it means every SUBSEQUENT
// line, matching or not, renders as a match instead (false != true =
// true), verified directly against the real rg binary (`--passthru -v -m
// N`: everything after the Nth real inverted match becomes ':'-rendered,
// including literal pattern matches that would otherwise be '-'
// context). skipVerify mirrors rg's own shortest_match short-circuit
// (never even re-invokes the matcher once the limit is exceeded under
// passthru, since its result is discarded either way).
func (s *Searcher) matchByLineSlow(buf []byte, sink Sink) (scanOutcome, error) {
	step := newLineStep(s.lineTerm, s.pos, len(buf))
	for {
		start, end, ok := step.next(buf)
		if !ok {
			break
		}
		s.pos = end

		var success bool
		if s.PassThru && s.matchLimitReached && !s.matchLimitReachedAtStart {
			success = s.Invert
		} else if s.PassThru && s.matchLimitReachedAtStart {
			// MaxCount<=0: the limit was already exceeded before this
			// scan started at all (see matchLimitReachedAtStart's doc) --
			// success is unconditionally false here, same as the ordinary
			// (non-PassThru) `&& !s.matchLimitReached` branch below would
			// give, WITHOUT applying the invert-flip the mid-scan case
			// gets: rg's own matches_possible() zero-output skip for
			// MaxCount==Some(0) applies regardless of -v (verified against
			// the real rg binary: `--passthru -v -m0` also prints
			// nothing, not "every line", even though a plain --passthru
			// -v with no -m at all inverts the render of every line).
			success = false
		} else {
			matched := s.Matcher.Verify(withoutTerminator(buf[start:end], s.lineTerm, s.CRLF))
			success = matched != s.Invert && !s.matchLimitReached
		}
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
			if s.MaxCount != nil && s.matchCount >= int64(*s.MaxCount) {
				s.matchLimitReached = true
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
		} else if s.PassThru && !s.matchLimitReachedAtStart {
			// Every line that is neither a real match nor owed after-
			// context (which never applies under pure passthru anyway --
			// AfterContext is always 0 here, see PassThru's doc) still
			// gets printed, rendered exactly like context (rg's own
			// SinkContextKind::Other -- the printer doesn't distinguish
			// context kinds, so a plain Context call reproduces this for
			// free). after=false here is arbitrary (printer.Standard
			// never reads it -- see Standard.Context's doc); it's neither
			// "before" nor "after" a match in the usual sense.
			//
			// The matchLimitReachedAtStart exclusion is MaxCount<=0's
			// "search never really starts" case (see its doc): without
			// it, this branch would fire for every line, matching rg's
			// SOURCE-LEVEL branch structure exactly -- but not rg's
			// OBSERVABLE behavior, since rg's own CLI layer never even
			// reaches this code for that case at all (matches_possible()
			// skips calling the searcher entirely). Only relevant when
			// matchLimitReached is true from the very first iteration;
			// once matchLimitReached becomes true MID-scan (real matches
			// happened first), matchLimitReachedAtStart is still false,
			// so this exclusion never applies there.
			more, err := s.sinkContext(buf, sink, start, end, false)
			if err != nil {
				return scanStop, err
			}
			if !more {
				return scanStop, nil
			}
		}
		// PassThru must keep scanning to EEOF even after the -m limit is
		// hit (see the doc above) -- EXCEPT when the limit was already
		// exceeded before this file's scan even started
		// (matchLimitReachedAtStart, i.e. MaxCount<=0): that case prints
		// NOTHING at all, matching the real rg binary (`--passthru -m 0`
		// -- see matchLimitReachedAtStart's doc), so there is nothing
		// left for continued scanning to ever produce and it should stop
		// immediately, same as the non-PassThru case.
		if s.matchLimitReached && s.afterContextLeft == 0 && (!s.PassThru || s.matchLimitReachedAtStart) {
			return scanStop, nil
		}
	}
	return scanContinue, nil
}

// matchByLineFast jumps straight to candidate bytes via Matcher.FindCandidate
// over the whole buffer, expanding each hit to its enclosing line, rather
// than stepping line by line. Ported from rg's match_by_line_fast.
//
// -m/--max-count: matchLimitReached is checked at the TOP of the loop
// (before either the invert or non-invert branch runs), so once the
// limit is hit no further FindCandidate/matchByLineFastInvert work is
// ever attempted -- the loop just falls straight through to the same
// trailing afterContextByLine call that normally runs at natural
// buffer-end, which correctly drains any after-context still owed to the
// last counted match regardless of which chunk/window it spans. Once
// that context is fully drained too, scanStop is returned immediately
// (rather than scanContinue) so Search's caller stops reading further
// input for this file entirely -- for MaxCount==0 (matchLimitReached
// already true from the very first call, see resetRun/runChunk) this
// means at most one read chunk is ever consumed.
func (s *Searcher) matchByLineFast(buf []byte, sink Sink) (scanOutcome, error) {
	for s.pos < len(buf) {
		if s.matchLimitReached {
			break
		}
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
		if s.MaxCount != nil && s.matchCount >= int64(*s.MaxCount) {
			s.matchLimitReached = true
		}
	}
	if outcome, err := s.afterContextByLine(buf, sink, len(buf)); outcome == scanStop || err != nil {
		return scanStop, err
	}
	s.pos = len(buf)
	if s.matchLimitReached && s.afterContextLeft == 0 {
		return scanStop, nil
	}
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

	step := newLineStep(s.lineTerm, gapStart, gapEnd)
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
		if s.MaxCount != nil && s.matchCount >= int64(*s.MaxCount) {
			// -m + -v: stop emitting further lines from THIS gap once
			// the limit is hit -- s.pos is already correctly positioned
			// past the whole gap (set above, before this inner loop
			// started), so no line is lost or reprocessed. keepGoing
			// still returns true: matchByLineFast's own top-of-loop
			// matchLimitReached check (not this function) is what stops
			// the outer scan and drains any trailing after-context.
			s.matchLimitReached = true
			break
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
		ls, le := lineLocate(buf, s.lineTerm, off, off)
		if ls == len(buf) {
			// Matched beyond the end of the buffer; not a real hit.
			pos = len(buf)
			continue
		}
		if kind == match.Confirmed {
			return ls, le, true, nil
		}
		if s.Matcher.Verify(withoutTerminator(buf[ls:le], s.lineTerm, s.CRLF)) {
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
	beforeStart := rangeStart + linePreceding(buf[rangeStart:upto], s.lineTerm, s.BeforeContext-1)

	step := newLineStep(s.lineTerm, beforeStart, upto)
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
	step := newLineStep(s.lineTerm, s.lastLineVisited, upto)
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
	s.lineNumber += lineCount(buf[s.lastLineCounted:upto], s.lineTerm)
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
		contextStart := linePreceding(buf, s.lineTerm, s.BeforeContext)
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
