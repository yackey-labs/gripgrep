package printer

import (
	"bytes"

	"github.com/yackey-labs/gripgrep/match"
)

// literalMatcher is a minimal match.Matcher fake used only to exercise
// Standard's color path in tests: it finds every non-overlapping
// occurrence of a fixed literal via Find, and stubs the rest of the
// interface (unused by the printer package, which never calls
// FindCandidate/Verify/NonMatchingLineTerm itself).
type literalMatcher struct {
	lit []byte
}

var _ match.Matcher = (*literalMatcher)(nil)

func (m *literalMatcher) FindCandidate(buf []byte, start int) (int, match.CandidateKind, bool) {
	panic("not used by printer tests")
}

func (m *literalMatcher) Verify(line []byte) bool {
	panic("not used by printer tests")
}

func (m *literalMatcher) Find(line []byte) (s, e int, ok bool) {
	i := bytes.Index(line, m.lit)
	if i < 0 {
		return 0, 0, false
	}
	return i, i + len(m.lit), true
}

func (m *literalMatcher) NonMatchingLineTerm() bool {
	return true
}
