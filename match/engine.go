package match

import (
	"regexp/syntax"

	"github.com/grafana/regexp"
)

// engine wraps the fallback regex engine (grafana/regexp, speedup
// branch -- a drop-in fork of stdlib regexp with the literal/onepass
// optimizations from golang/go#26623 upstreamed but never merged). It is
// used to confirm Candidate hits on a single line (Strategy 2) and, when
// no useful literal prefilter exists, to scan directly (Strategy 3). All
// calls take []byte only -- *regexp.Regexp already exposes byte-native
// methods (FindIndex, FindAllIndex), so no string conversion is ever
// needed on the hot path.
type engine struct {
	re *regexp.Regexp
	// hasAnchors is true when the compiled pattern contains ^, $, \A,
	// \z, \b, or \B anywhere. It gates find's slow path (see find):
	// only patterns that can actually observe "where does the text
	// begin" pay the cost of avoiding a subslice.
	hasAnchors bool
}

func compileEngine(pattern string, hasAnchors bool) (*engine, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return &engine{re: re, hasAnchors: hasAnchors}, nil
}

// containsAnchorOrBoundary reports whether re contains ^, $, \A, \z,
// \b, or \B anywhere in its tree.
func containsAnchorOrBoundary(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	case syntax.OpCapture, syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		return containsAnchorOrBoundary(re.Sub[0])
	case syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if containsAnchorOrBoundary(sub) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// find returns the leftmost match at or after start within buf.
//
// For start == 0, or for any pattern that provably contains no anchor
// or word-boundary construct, this is a plain FindIndex -- on buf
// directly when start == 0 (buf[0:] == buf, so nothing is lost), or on
// buf[start:] otherwise (safe precisely because there is no ^, \A, \b,
// etc. to be confused by the subslice looking like a fresh start of
// text). This is the common case and stays a single cheap engine call
// regardless of how large buf is or how many times find is called
// across it.
//
// For start > 0 on a pattern that DOES contain an anchor or boundary,
// subslicing would silently change what "start of text" or "word
// boundary" means relative to the real buffer (e.g. `^foo` would wrongly
// re-match at any later occurrence of "foo", since the subslice's own
// position 0 looks like a fresh start of text to the engine -- see
// engine_test.go). Avoiding that requires searching the untouched
// buffer; FindAllIndex is used and the first hit at or after start is
// taken. This path is deliberately reserved for the rarer anchored/
// word-boundary case -- it's what -w's retry loop and the printer's
// FindAt (task #15, driven over one line at a time) pay for, not the
// common whole-buffer literal/class scan.
func (e *engine) find(buf []byte, start int) (s, end int, ok bool) {
	if start > len(buf) {
		return 0, 0, false
	}
	if start == 0 || !e.hasAnchors {
		loc := e.re.FindIndex(buf[start:])
		if loc == nil {
			return 0, 0, false
		}
		return start + loc[0], start + loc[1], true
	}
	for _, loc := range e.re.FindAllIndex(buf, -1) {
		if loc[0] >= start {
			return loc[0], loc[1], true
		}
	}
	return 0, 0, false
}
