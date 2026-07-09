package printer

import (
	"testing"

	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/search"
)

// boundaryRule is shared logic for the two fakes below: lit matches at
// position i in the ORIGINAL line iff i==0 or line[i-1] != lit. It's a
// stand-in for any real boundary/anchor check (word boundaries, ^) that
// needs the true preceding byte, not whatever a caller happens to slice
// off for it.
func boundaryRule(line []byte, i int, lit byte) bool {
	return line[i] == lit && (i == 0 || line[i-1] != lit)
}

// noFindAtBoundaryMatcher implements only the frozen match.Matcher
// contract (no FindAt), so Standard is forced onto the
// appendColoredLineSubslice fallback. Its Find is written exactly the
// way a real anchored/word-boundary matcher naturally would be: it
// checks "is line[0] preceded by something other than lit" using
// whatever slice it was actually given — which, once a caller starts
// resuming from line[pos:], can no longer see what came before pos in
// the real line. That's not a bug in this fake; it's the structural
// limitation Find's signature.
type noFindAtBoundaryMatcher struct {
	lit byte
}

var _ match.Matcher = (*noFindAtBoundaryMatcher)(nil)

func (m *noFindAtBoundaryMatcher) FindCandidate(buf []byte, start int) (int, match.CandidateKind, bool) {
	panic("not used by printer tests")
}

func (m *noFindAtBoundaryMatcher) Verify(line []byte) bool {
	panic("not used by printer tests")
}

func (m *noFindAtBoundaryMatcher) NonMatchingLineTerm() bool { return true }

func (m *noFindAtBoundaryMatcher) Find(line []byte) (s, e int, ok bool) {
	for i := 0; i < len(line); i++ {
		if boundaryRule(line, i, m.lit) {
			return i, i + 1, true
		}
	}
	return 0, 0, false
}

// findAtBoundaryMatcher additionally implements findAtMatcher, so
// Standard prefers it. FindAt is given the true start index into the
// ORIGINAL line on every call, so boundaryRule always sees the real
// preceding byte, regardless of where scanning resumes from.
type findAtBoundaryMatcher struct {
	noFindAtBoundaryMatcher
}

var _ findAtMatcher = (*findAtBoundaryMatcher)(nil)

func (m *findAtBoundaryMatcher) FindAt(line []byte, start int) (s, e int, ok bool) {
	for i := start; i < len(line); i++ {
		if boundaryRule(line, i, m.lit) {
			return i, i + 1, true
		}
	}
	return 0, 0, false
}

// TestStandard_ColorFindAt_CorrectAcrossResumedScan proves FindAt fixes
// the bug #15 was filed for: "XX" has exactly one true boundary match
// (position 0; position 1 is preceded by 'X' so the rule rejects it),
// and appendColoredLine must color only that one when the Matcher
// implements findAtMatcher.
func TestStandard_ColorFindAt_CorrectAcrossResumedScan(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Color = true
	p.Matcher = &findAtBoundaryMatcher{noFindAtBoundaryMatcher{lit: 'X'}}

	p.Begin("t.txt")
	p.Matched(&search.Match{Line: []byte("XX"), LineNumber: 1, HasLineNumber: true})
	p.Finish("t.txt", &search.Stats{Matched: true})

	colored := "\x1b[0m\x1b[1m\x1b[31mX\x1b[0m"
	want := "\x1b[0m\x1b[35mt.txt\x1b[0m:\x1b[0m\x1b[32m1\x1b[0m:" + colored + "X\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q (only the first X should be colored)", got, want)
	}
}

// TestStandard_ColorSubsliceFallback_KnownLimitation documents the
// fallback's known limitation (see appendColoredLineSubslice's doc
// comment and task #15): without FindAt, a boundary/anchor-aware
// matcher scanned via subslice can wrongly color a second occurrence,
// because a subslice's position 0 always looks like "start of line" to
// a plain Find call. This is expected, not a regression — the whole
// point of FindAt is to fix exactly this. If this test ever starts
// failing because the fallback stopped being wrong, that's fine; it
// would just mean appendColoredLineSubslice's documented caveat can be
// deleted.
func TestStandard_ColorSubsliceFallback_KnownLimitation(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Color = true
	p.Matcher = &noFindAtBoundaryMatcher{lit: 'X'} // no FindAt: forces the subslice fallback

	p.Begin("t.txt")
	p.Matched(&search.Match{Line: []byte("XX"), LineNumber: 1, HasLineNumber: true})
	p.Finish("t.txt", &search.Stats{Matched: true})

	colored := "\x1b[0m\x1b[1m\x1b[31mX\x1b[0m"
	wrongButExpected := "\x1b[0m\x1b[35mt.txt\x1b[0m:\x1b[0m\x1b[32m1\x1b[0m:" + colored + colored + "\n"
	if got := out.String(); got != wrongButExpected {
		t.Errorf("fallback behavior changed: got:\n%q\nwant (documented-wrong):\n%q", got, wrongButExpected)
	}
}

// TestStandard_ColorWordBoundary_RealMatcher is the end-to-end
// integration test for #15, using match.New's real *matcherImpl (not a
// synthetic fake): a -w ("cat") pattern against a line with two
// legitimate whole-word occurrences and one embedded, non-word-boundary
// occurrence inside "scatter" that must NOT be colored. matcherImpl
// implements the optional findAtMatcher interface (see
// match/strategy.go's FindAt), so Standard's type assertion should pick
// it up automatically with no printer-side special-casing.
func TestStandard_ColorWordBoundary_RealMatcher(t *testing.T) {
	m, err := match.New(match.Config{Patterns: []string{"cat"}, Word: true})
	if err != nil {
		t.Fatalf("match.New: %v", err)
	}

	// Sanity: confirm this Matcher actually exposes FindAt, so the test
	// is exercising the real fix rather than silently falling back.
	if _, ok := m.(findAtMatcher); !ok {
		t.Fatalf("match.New's Matcher does not implement findAtMatcher; #15's real fix is not being exercised by this test")
	}

	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Color = true
	p.Matcher = m

	line := []byte("cat scatter cat")
	p.Begin("t.txt")
	p.Matched(&search.Match{Line: line, LineNumber: 1, HasLineNumber: true})
	p.Finish("t.txt", &search.Stats{Matched: true})

	colored := "\x1b[0m\x1b[1m\x1b[31mcat\x1b[0m"
	want := "\x1b[0m\x1b[35mt.txt\x1b[0m:\x1b[0m\x1b[32m1\x1b[0m:" +
		colored + " scatter " + colored + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q (only the two whole-word \"cat\"s should be colored, not the one embedded in \"scatter\")", got, want)
	}
}
