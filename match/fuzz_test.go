package match

import (
	"testing"
)

// fuzzMatchers is a fixed set of representative pre-compiled Matchers,
// one per strategy/case-mode combination, checked against the stdlib
// oracle on random single-line inputs. Patterns are fixed (not part of
// the fuzz corpus) since an arbitrary fuzzed pattern string is usually
// not even valid regex; go native fuzzing instead stresses the
// haystack side of FindCandidate/Verify/Find against known-good
// compiled matchers, per M1-match's brief.
type fuzzCase struct {
	name string
	cfg  Config
}

var fuzzCases = []fuzzCase{
	{"literal", cs("foo")},
	{"literal_ci", ci("PM_RESUME")},
	{"literal_ci_unicode", ci("δελτα")},
	{"multi_literal_small", cs("foo", "bar", "baz")},
	{"multi_literal_large", cs("a", "b", "c", "d", "e", "f", "g", "h", "i", "j")},
	{"word", word(cs("foo"))},
	{"prefiltered_regex", cs(`[A-Z]+_RESUME`)},
	{"engine_only", cs(`\w{3}\s+\w{3}`)},
	{"fixed", fixed(cs("a.b*c"))},
	// Patterns that CAN match '\n' (negated class, and \s which includes
	// '\n' in Go): included so the fuzz corpus exercises
	// NonMatchingLineTerm()==false patterns too, not just the
	// line-terminator-safe ones above. See
	// TestNonMatchingLineTermConservative for the dedicated proof that
	// the gating itself is conservative and why it matters.
	{"can_match_newline_class", cs(`[^x]+`)},
	{"can_match_newline_whitespace", cs(`a\s+b`)},
}

func FuzzMatcherAgreesWithOracle(f *testing.F) {
	seeds := []string{
		"",
		"foo",
		"FOO",
		"PM_RESUME",
		"pm_resume",
		"foobarbaz",
		"a b c d e f g h i j",
		"x-foo",
		" -foo ",
		"δελτα ΔΕΛΤΑ",
		"[A-Z]_RESUME matches XY_RESUME here",
		"aaa   bbb   ccc",
		"\x00\x01binary-ish\xff",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// Compile once; Matchers are documented safe for concurrent/repeated
	// use with read-only compiled state.
	compiled := make(map[string]Matcher, len(fuzzCases))
	for _, fc := range fuzzCases {
		m, err := New(fc.cfg)
		if err != nil {
			f.Fatalf("New(%s): %v", fc.name, err)
		}
		compiled[fc.name] = m
	}

	f.Fuzz(func(t *testing.T, data string) {
		// Keep this a single line: Find/Verify operate on one line, and
		// FindCandidate's whole-buffer contract requires the caller to
		// have already established NonMatchingLineTerm (exercised
		// separately in TestMatcherFindCandidateWholeBuffer); collapse
		// any embedded newlines so this fuzz target stays focused on
		// FindCandidate+Verify agreement on arbitrary byte content.
		line := []byte(data)
		for i, b := range line {
			if b == '\n' {
				line[i] = ' '
			}
		}

		for _, fc := range fuzzCases {
			m := compiled[fc.name]
			wantS, wantE, wantOK := oracleFind(t, fc.cfg, line)

			gotVerify := m.Verify(line)
			if gotVerify != wantOK {
				t.Fatalf("[%s] Verify(%q) = %v, want %v", fc.name, line, gotVerify, wantOK)
			}

			gotS, gotE, gotOK := m.Find(line)
			if gotOK != wantOK || (wantOK && (gotS != wantS || gotE != wantE)) {
				t.Fatalf("[%s] Find(%q) = (%d,%d,%v), want (%d,%d,%v)", fc.name, line, gotS, gotE, gotOK, wantS, wantE, wantOK)
			}

			// Self-consistency: driving FindCandidate over this same
			// single line (as its own "buffer") and following the
			// documented Candidate->Verify protocol must reach the same
			// yes/no decision as calling Verify directly.
			viaCandidate := false
			pos := 0
			for {
				off, kind, ok := m.FindCandidate(line, pos)
				if !ok {
					break
				}
				if kind == Confirmed || m.Verify(line) {
					viaCandidate = true
					break
				}
				pos = off + 1
				if pos > len(line) {
					break
				}
			}
			if viaCandidate != wantOK {
				t.Fatalf("[%s] FindCandidate+Verify(%q) = %v, want %v", fc.name, line, viaCandidate, wantOK)
			}
		}
	})
}
