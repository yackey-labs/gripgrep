package search

import (
	"bytes"

	"github.com/yackey-labs/gripgrep/match"
)

// fakeMatcher is a minimal test-local Matcher: a plain bytes.Index literal
// scan, so search's tests aren't blocked on the real match package (built
// in parallel by teammate m1-match). It supports both Confirmed and
// Candidate modes so tests exercise both branches of the fast path.
type fakeMatcher struct {
	literal []byte
	// nonMatchingTerm controls NonMatchingLineTerm(), i.e. which of
	// search's two paths (fast/slow) gets exercised.
	nonMatchingTerm bool
	// candidate, when true, makes FindCandidate report Candidate (forcing
	// callers through Verify) instead of Confirmed (reported directly).
	candidate bool
}

var _ match.Matcher = (*fakeMatcher)(nil)

func literalMatcher(lit string, fastPath bool) *fakeMatcher {
	return &fakeMatcher{literal: []byte(lit), nonMatchingTerm: fastPath}
}

func (m *fakeMatcher) FindCandidate(buf []byte, start int) (int, match.CandidateKind, bool) {
	idx := bytes.Index(buf[start:], m.literal)
	if idx < 0 {
		return 0, 0, false
	}
	kind := match.Confirmed
	if m.candidate {
		kind = match.Candidate
	}
	return start + idx, kind, true
}

func (m *fakeMatcher) Verify(line []byte) bool {
	return bytes.Contains(line, m.literal)
}

func (m *fakeMatcher) Find(line []byte) (int, int, bool) {
	idx := bytes.Index(line, m.literal)
	if idx < 0 {
		return 0, 0, false
	}
	return idx, idx + len(m.literal), true
}

func (m *fakeMatcher) NonMatchingLineTerm() bool {
	return m.nonMatchingTerm
}

// alwaysMatcher simulates zero-width/empty-match patterns like `^`, `$`,
// `^$`, `a*`, `()`: FindCandidate reports a (Confirmed) hit at the exact
// offset it was asked to start scanning from, every single time, and
// Verify always succeeds. This is the worst case for the classic
// "empty match at every position never advances" infinite-loop bug:
// search must rely on always advancing by at least one whole (non-empty)
// line, never on the matcher itself making progress.
type alwaysMatcher struct {
	nonMatchingTerm bool
}

var _ match.Matcher = (*alwaysMatcher)(nil)

func (m *alwaysMatcher) FindCandidate(buf []byte, start int) (int, match.CandidateKind, bool) {
	if start >= len(buf) {
		return 0, 0, false
	}
	return start, match.Confirmed, true
}

func (m *alwaysMatcher) Verify(line []byte) bool { return true }

func (m *alwaysMatcher) Find(line []byte) (int, int, bool) { return 0, 0, true }

func (m *alwaysMatcher) NonMatchingLineTerm() bool { return m.nonMatchingTerm }
