package match

import (
	"strings"
	"testing"
)

// TestEmptyMatchPatternsTerminate covers patterns that can match the
// empty string at every position (^, $, ^$, a*, ()). The risk is an
// infinite loop in any caller that drives FindCandidate in a loop
// advancing by the returned offset: since a zero-width match's returned
// offset equals the position it was searched from, a naive `pos =
// off + 1` driver only terminates if FindCandidate is guaranteed to
// return offsets >= the requested start (which it is, by contract) --
// this test proves that guarantee holds in practice by bounding the
// iteration count and failing rather than hanging if it's ever violated,
// and separately proves the documented candidate->line->Verify usage
// pattern (mirroring how search.Searcher is expected to drive a Matcher)
// agrees with the oracle on every line.
func TestEmptyMatchPatternsTerminate(t *testing.T) {
	cfgs := []Config{cs(`^`), cs(`$`), cs(`^$`), cs(`a*`), cs(`()`)}
	buf := []byte(strings.Join(haystacks, "\n") + "\n")
	lineStarts := lineBoundaries(buf)

	for _, cfg := range cfgs {
		m, err := New(cfg)
		if err != nil {
			t.Fatalf("New(%+v): %v", cfg, err)
		}

		// Raw FindCandidate loop, advancing naively by off+1: must
		// terminate within len(buf)+2 iterations no matter what, since
		// every returned offset is contractually >= the requested start.
		const guard = 1 << 20 // generous; real bound is len(buf)+2
		pos, iters := 0, 0
		for {
			off, _, ok := m.FindCandidate(buf, pos)
			if !ok {
				break
			}
			if off < pos {
				t.Fatalf("cfg=%+v: FindCandidate returned off=%d < requested start=%d (violates contract, would loop forever)", cfg, off, pos)
			}
			pos = off + 1
			iters++
			if iters > guard {
				t.Fatalf("cfg=%+v: FindCandidate loop did not terminate after %d iterations", cfg, guard)
			}
		}

		// Documented line-based usage (matches TestMatcherFindCandidateWholeBuffer's helper).
		gotLines := candidateLinesConfirmed(t, m, buf, lineStarts)
		for i, ls := range lineStarts {
			line := buf[ls.start:ls.end]
			_, _, want := oracleFind(t, cfg, line)
			if gotLines[i] != want {
				t.Errorf("cfg=%+v line %d (%q): matched=%v, oracle=%v", cfg, i, line, gotLines[i], want)
			}
		}
	}
}

// TestInvalidUTF8StillMatchesByteWise proves that a haystack containing
// invalid UTF-8 elsewhere doesn't prevent byte-wise matching of a valid
// literal before/after it, across all three strategies (pure literal,
// prefiltered regex, engine-only). Go's regexp (and grafana/regexp,
// which shares its lineage) is documented to operate on arbitrary []byte
// without requiring valid UTF-8; only Unicode-aware constructs (\p{...})
// near the invalid bytes are affected, not plain byte/ASCII matching.
func TestInvalidUTF8StillMatchesByteWise(t *testing.T) {
	invalid := []byte{0xff, 0xfe, 0x80, 0x80}
	mkLine := func(prefix, suffix string) []byte {
		line := append([]byte(nil), []byte(prefix)...)
		line = append(line, invalid...)
		line = append(line, []byte(suffix)...)
		return line
	}

	cfgs := []Config{
		cs("needle"),        // Strategy 1: pure literal
		cs(`[A-Z]+_needle`), // Strategy 2: prefiltered regex
		cs(`\w{2}_\w{6}`),   // Strategy 3: engine only (no extractable literal)
		fixed(cs("needle")),
	}
	for _, cfg := range cfgs {
		m, err := New(cfg)
		if err != nil {
			t.Fatalf("New(%+v): %v", cfg, err)
		}
		lineBefore := mkLine("XY_needle before invalid bytes: ", "")
		lineAfter := mkLine("", " XY_needle after invalid bytes")
		for _, line := range [][]byte{lineBefore, lineAfter} {
			if !m.Verify(line) {
				t.Errorf("cfg=%+v: Verify(%q) = false, want true (invalid UTF-8 must not block byte-wise matching)", cfg, line)
			}
			if _, _, ok := m.Find(line); !ok {
				t.Errorf("cfg=%+v: Find(%q) found nothing, want a match", cfg, line)
			}
		}
	}
}

// TestNonMatchingLineTermConservative proves two things for patterns
// that can match '\n': (1) NonMatchingLineTerm() correctly reports false
// (the conservative answer) for each of them, across all three
// strategies; (2) this gating is not just theoretical -- naively driving
// FindCandidate over a raw multi-line buffer for such a pattern (i.e.
// skipping the documented NonMatchingLineTerm() check) really can
// produce a match that spans two lines, which is exactly the unsound
// behavior the flag exists to prevent.
func TestNonMatchingLineTermConservative(t *testing.T) {
	cfgs := []Config{
		cs(`[^x]+`),       // negated class: DOES match '\n' in Go
		cs(`a\s+b`),       // \s includes '\n'
		cs(`(?s)a.b`),     // dot-matches-newline
		cs(`a\nb`),        // explicit '\n' literal
		cs(`^foo`),        // anchor: conservative regardless of '\n' involvement
		fixed(cs("a\nb")), // -F literal containing an embedded '\n'
	}
	for _, cfg := range cfgs {
		m, err := New(cfg)
		if err != nil {
			t.Fatalf("New(%+v): %v", cfg, err)
		}
		if m.NonMatchingLineTerm() {
			t.Errorf("cfg=%+v: NonMatchingLineTerm() = true, want false (pattern can match '\\n')", cfg)
		}
	}

	// Demonstrate why: for a pattern that can match '\n', scanning a
	// whole multi-line buffer with FindCandidate (the fast path
	// NonMatchingLineTerm forbids here) finds a match straddling the
	// line break between "fooa" and "bbar" -- a false cross-line match
	// that a correct per-line search would never produce.
	m, err := New(cs(`a\s+b`))
	if err != nil {
		t.Fatal(err)
	}
	if m.NonMatchingLineTerm() {
		t.Fatal("expected NonMatchingLineTerm() = false for a\\s+b")
	}
	buf := []byte("fooa\nbbar\n")
	off, _, ok := m.FindCandidate(buf, 0)
	if !ok {
		t.Fatal("expected FindCandidate to find the cross-line 'a\\nb' hit demonstrating the hazard")
	}
	s, e, ok := m.Find(buf) // Find has no line-boundary concept; same engine, whole buffer
	if !ok || s > off || e <= 4 {
		t.Fatalf("expected a match spanning the line break at index 4, got Find=(%d,%d,%v)", s, e, ok)
	}
	// A correct per-line scan (as required whenever NonMatchingLineTerm
	// is false) must NOT find this: neither "fooa" nor "bbar" alone
	// contains a whitespace run between an 'a' and a 'b'.
	for _, line := range [][]byte{[]byte("fooa"), []byte("bbar")} {
		if m.Verify(line) {
			t.Fatalf("Verify(%q) = true, want false (no genuine single-line match)", line)
		}
	}
}

// TestAllocsPerRun asserts the zero-allocation-in-steady-state
// requirement for FindCandidate and Verify on the strategies gripgrep
// fully controls: the single-literal and small (rare-byte) multi-literal
// scanners.
//
// Two documented exceptions, not asserted to zero here:
//   - Any path through the regex engine (grafana/regexp, a third-party
//     dependency) -- Strategy 3, and Strategy 2's line-confirm step.
//   - The large-multi-literal / ASCII-CI-multi-literal path, which goes
//     through github.com/petar-dambovaliev/aho-corasick: its public API
//     only exposes a match via AhoCorasick.IterByte, which returns the
//     Iter interface and therefore heap-allocates its iterator (and the
//     prefilterState it carries) on every call -- there's no lower-level
//     FindAtNoState exposed publicly to call without it. Measured at 1
//     alloc/op; left as a known, bounded limitation of the dependency
//     rather than a reason to fork or hand-roll Aho-Corasick.
func TestAllocsPerRun(t *testing.T) {
	line := []byte("the quick brown fox jumps over the lazy dog PM_RESUME")
	buf := []byte(strings.Repeat(string(line)+"\n", 4))

	cases := []struct {
		name       string
		cfg        Config
		zeroAllocs bool
	}{
		{"single_literal", cs("PM_RESUME"), true},
		{"single_literal_ci", ci("pm_resume"), true},
		{"multi_literal_small", cs("fox", "dog", "quick"), true},
		{"multi_literal_large_aho_corasick", cs("a", "b", "c", "d", "e", "f", "g", "h", "i", "j"), false},
	}
	for _, c := range cases {
		m, err := New(c.cfg)
		if err != nil {
			t.Fatalf("New(%s): %v", c.name, err)
		}
		allocsVerify := testing.AllocsPerRun(100, func() {
			m.Verify(line)
		})
		allocsFind := testing.AllocsPerRun(100, func() {
			m.FindCandidate(buf, 0)
		})
		t.Logf("[%s] Verify allocs/op=%v FindCandidate allocs/op=%v", c.name, allocsVerify, allocsFind)
		if c.zeroAllocs {
			if allocsVerify != 0 {
				t.Errorf("[%s] Verify allocs/op = %v, want 0", c.name, allocsVerify)
			}
			if allocsFind != 0 {
				t.Errorf("[%s] FindCandidate allocs/op = %v, want 0", c.name, allocsFind)
			}
		}
	}
}
