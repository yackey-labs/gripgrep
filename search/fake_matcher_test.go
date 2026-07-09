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
