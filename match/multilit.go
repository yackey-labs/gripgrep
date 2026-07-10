package match

import (
	"bytes"

	ahocorasick "github.com/petar-dambovaliev/aho-corasick"
)

// literalScanner finds occurrences of a fixed set of literal byte strings
// in a haystack, reporting the position and length of a hit. It is used
// both as a Strategy-1 "confirmed" matcher (an exact literal hit *is* the
// match) and as a Strategy-2 prefilter (a hit is merely a Candidate that
// the regex engine must confirm on the enclosing line).
type literalScanner interface {
	// find returns the start offset and byte length of the next
	// occurrence at or after start, or ok=false if none remains.
	find(buf []byte, start int) (pos, length int, ok bool)
}

// --- single literal, case-sensitive -----------------------------------

// singleLiteralScanner scans for one exact literal via bytes.Index, or
// via rarest-byte bytes.IndexByte + an inline compare when the literal's
// leading byte is common enough that a direct bytes.Index would spend
// more time verifying false starts (PLAN.md's "Single-literal scan" row).
type singleLiteralScanner struct {
	lit     []byte
	rareIdx int // index of the rarest byte within lit; used when useRare
	useRare bool
}

// commonLeadingByteRank is the threshold above which a literal's first
// byte is considered common enough to prefer scanning on its rarest byte
// instead.
const commonLeadingByteRank = 200

func newSingleLiteralScanner(lit []byte) *singleLiteralScanner {
	s := &singleLiteralScanner{lit: lit}
	if len(lit) > 1 && rank(lit[0]) >= commonLeadingByteRank {
		s.rareIdx = rarestByte(lit)
		s.useRare = true
	}
	return s
}

func (s *singleLiteralScanner) find(buf []byte, start int) (int, int, bool) {
	if start > len(buf) {
		return 0, 0, false
	}
	n := len(s.lit)
	if n == 0 {
		if start > len(buf) {
			return 0, 0, false
		}
		return start, 0, true
	}
	if n == 1 {
		i := bytes.IndexByte(buf[start:], s.lit[0])
		if i < 0 {
			return 0, 0, false
		}
		return start + i, 1, true
	}
	if !s.useRare {
		i := bytes.Index(buf[start:], s.lit)
		if i < 0 {
			return 0, 0, false
		}
		return start + i, n, true
	}
	pos := start
	for {
		i := bytes.IndexByte(buf[pos:], s.lit[s.rareIdx])
		if i < 0 {
			return 0, 0, false
		}
		hit := pos + i
		litStart := hit - s.rareIdx
		// litStart must be >= start (not just >= 0): the rare byte can be
		// found within [pos, ...) while the literal it belongs to began
		// before pos (whenever the byte's offset within the literal is
		// more than pos-start). Accepting that would return a hit before
		// the caller's requested start, which breaks the "next candidate
		// at or after start" contract and can make callers that advance
		// by (returned_offset + 1) loop forever.
		if litStart >= start && litStart+n <= len(buf) && bytes.Equal(buf[litStart:litStart+n], s.lit) {
			return litStart, n, true
		}
		pos = hit + 1
	}
}

// --- single literal, ASCII case-insensitive -----------------------------

// singleLiteralCIScanner implements PLAN.md's dedicated case-insensitive
// single-literal path: scan for a case-invariant rare byte if the
// literal has one (a single IndexByte scan, no case concerns), otherwise
// scan for the rarest letter's upper and lower forms independently and
// take whichever occurs first -- both remain on the SIMD
// bytes.IndexByte path rather than falling through to the regex engine.
type singleLiteralCIScanner struct {
	lit    []byte
	anchor asciiCIAnchor
}

func newSingleLiteralCIScanner(lit []byte) *singleLiteralCIScanner {
	return &singleLiteralCIScanner{lit: lit, anchor: pickASCIICIAnchor(lit)}
}

func (s *singleLiteralCIScanner) find(buf []byte, start int) (int, int, bool) {
	n := len(s.lit)
	if n == 0 {
		if start > len(buf) {
			return 0, 0, false
		}
		return start, 0, true
	}
	pos := start
	for {
		var hit int
		if s.anchor.invariant {
			i := bytes.IndexByte(buf[pos:], s.anchor.lowerByte)
			if i < 0 {
				return 0, 0, false
			}
			hit = pos + i
		} else {
			lo := bytes.IndexByte(buf[pos:], s.anchor.lowerByte)
			up := bytes.IndexByte(buf[pos:], s.anchor.upperByte)
			if lo < 0 && up < 0 {
				return 0, 0, false
			}
			if lo < 0 {
				hit = pos + up
			} else if up < 0 {
				hit = pos + lo
			} else if lo < up {
				hit = pos + lo
			} else {
				hit = pos + up
			}
		}
		litStart := hit - s.anchor.idx
		// See singleLiteralScanner.find for why this must be >= start,
		// not just >= 0.
		if litStart >= start && litStart+n <= len(buf) && asciiEqualFold(buf[litStart:litStart+n], s.lit) {
			return litStart, n, true
		}
		pos = hit + 1
	}
}

// --- small multi-literal, case-sensitive: rare-byte + verify -----------

// rareByteMultiScanner handles a small set (<= ~8) of exact literals by
// scanning for each literal's rarest byte independently and taking
// whichever occurs first, then verifying the full literal at that
// position (PLAN.md's "Multi-literal scan" row, small-N branch).
type rareByteMultiScanner struct {
	lits    [][]byte
	rareIdx []int // rareIdx[i] is the index of lits[i]'s rarest byte
}

func newRareByteMultiScanner(lits [][]byte) *rareByteMultiScanner {
	s := &rareByteMultiScanner{lits: lits, rareIdx: make([]int, len(lits))}
	for i, l := range lits {
		if len(l) == 0 {
			s.rareIdx[i] = 0
			continue
		}
		s.rareIdx[i] = rarestByte(l)
	}
	return s
}

// find merges each literal's independent anchor-byte scan, taking
// whichever occurs first and verifying the full literal there.
//
// Each literal's next anchor occurrence is tracked in next[i] and only
// re-scanned once that specific occurrence is consumed -- not on every
// merge step. An earlier version re-ran bytes.IndexByte for every literal
// on every iteration of the outer loop, which meant a common anchor (say,
// one firing every ~100 bytes) forced the *other* literals' rarer anchors
// to be re-found from scratch that often too, even though their own next
// occurrence hadn't changed. On the corpus that motivated M3 task #22
// (`Sherlock|Watson`, "Sherlock"'s anchor 'k' firing on ~1% of bytes),
// that redundant re-scanning -- not anchor quality, and not this
// scanner's approach in general -- was the dominant cost (profiled at
// ~91% of the query's time in bytes.IndexByte/find). Routing to
// Aho-Corasick was tried first as this task's assigned fix and measured
// to be a dead end (the pure-Go AC library's candidate search was, if
// anything, marginally slower here, not faster -- see the commit message
// for the numbers); this monotonic-sweep rewrite is the fix that actually
// moved the needle, and needed no new dependency.
func (s *rareByteMultiScanner) find(buf []byte, start int) (int, int, bool) {
	var arr [8]int
	var next []int
	if len(s.lits) <= len(arr) {
		next = arr[:len(s.lits)]
	} else {
		// Defensive fallback: newLiteralScanner never constructs this
		// scanner for more than 8 literals, but a caller of
		// newRareByteMultiScanner directly (e.g. a future test) isn't
		// bound by that, so don't index out of bounds -- just accept the
		// one-time allocation this path was never meant to hit.
		next = make([]int, len(s.lits))
	}
	for i, l := range s.lits {
		next[i] = nextAnchor(buf, start, l, s.rareIdx[i])
	}

	for {
		bestHit := -1
		for i := range s.lits {
			if next[i] >= 0 && (bestHit == -1 || next[i] < bestHit) {
				bestHit = next[i]
			}
		}
		if bestHit == -1 {
			return 0, 0, false
		}
		// Several literals can share the same rarest byte value, so more
		// than one can legitimately tie at bestHit; check all of them
		// before advancing any.
		for i, l := range s.lits {
			if len(l) == 0 || next[i] != bestHit {
				continue
			}
			litStart := bestHit - s.rareIdx[i]
			// See singleLiteralScanner.find for why this must be >=
			// start (the original request), not just >= 0.
			if litStart >= start && litStart+len(l) <= len(buf) && bytes.Equal(buf[litStart:litStart+len(l)], l) {
				return litStart, len(l), true
			}
		}
		// No literal matched at bestHit -- advance only the anchor(s)
		// that were actually consumed there; everything else's next
		// occurrence is untouched and must not be re-scanned.
		for i, l := range s.lits {
			if len(l) == 0 || next[i] != bestHit {
				continue
			}
			next[i] = nextAnchor(buf, bestHit+1, l, s.rareIdx[i])
		}
	}
}

// nextAnchor returns the absolute offset of l's anchor byte (l[rareIdx])
// at or after from, or -1 if none remains in buf.
func nextAnchor(buf []byte, from int, l []byte, rareIdx int) int {
	if len(l) == 0 || from > len(buf) {
		return -1
	}
	idx := bytes.IndexByte(buf[from:], l[rareIdx])
	if idx < 0 {
		return -1
	}
	return from + idx
}

// --- large multi-literal (or ASCII case-insensitive multi-literal):
// Aho-Corasick ----------------------------------------------------------

// ahoCorasickScanner wraps github.com/petar-dambovaliev/aho-corasick for
// literal sets too large for the rare-byte approach to stay cheap
// (PLAN.md's >~8 branch), and is also used for ASCII case-insensitive
// multi-literal sets of any size via the library's built-in
// AsciiCaseInsensitive option -- simpler and just as fast as hand-rolling
// a dual-case rare-byte scan across many literals, and it keeps the
// per-literal exactness semantics the rare-byte scanner would otherwise
// need to special-case for folded alternatives.
type ahoCorasickScanner struct {
	ac   ahocorasick.AhoCorasick
	lits [][]byte
}

func newAhoCorasickScanner(lits [][]byte, asciiCaseInsensitive bool) *ahoCorasickScanner {
	b := ahocorasick.NewAhoCorasickBuilder(ahocorasick.Opts{
		AsciiCaseInsensitive: asciiCaseInsensitive,
		MatchKind:            ahocorasick.LeftMostFirstMatch,
		DFA:                  true,
	})
	patterns := make([][]byte, len(lits))
	copy(patterns, lits)
	ac := b.BuildByte(patterns)
	return &ahoCorasickScanner{ac: ac, lits: lits}
}

func (s *ahoCorasickScanner) find(buf []byte, start int) (int, int, bool) {
	if start > len(buf) {
		return 0, 0, false
	}
	it := s.ac.IterByte(buf[start:])
	m := it.Next()
	if m == nil {
		return 0, 0, false
	}
	return start + m.Start(), m.End() - m.Start(), true
}

// newLiteralScanner builds the appropriate scanner for lits per
// PLAN.md's thresholds: a single literal gets the dedicated
// single-literal path; small sets get the rare-byte scan; large sets (or
// any set needing ASCII case folding) get Aho-Corasick.
//
// M3 task #22 considered routing small sets with a poor anchor byte
// (e.g. "Sherlock"'s rarest byte is an ordinary lowercase letter, not a
// genuinely rare one) to Aho-Corasick instead, on the theory that AC's
// automaton-driven throughput wouldn't degrade with anchor commonality
// the way rareByteMultiScanner's bytes.IndexByte-and-verify approach
// does. Measured and rejected: on the subtitles benchmark corpus, AC was
// never faster, and was dramatically worse as the literal count grew
// (`Sherlock|Watson`, N=2: rare-byte 2.13x slower than rg vs AC's 3.46x;
// an 8-literal all-poor-anchor set: rare-byte 5.7x vs AC's 19x) -- this
// pure-Go AC library's per-candidate cost apparently scales worse with
// pattern count than the naive rare-byte scan does with anchor
// commonality. The actual fix for the regression this was chasing turned
// out to be rareByteMultiScanner.find's monotonic-sweep rewrite (see its
// doc): no threshold or routing change needed here at all.
func newLiteralScanner(lits [][]byte, asciiCaseInsensitive bool) literalScanner {
	for _, l := range lits {
		if len(l) == 0 {
			// An empty literal -- alone or alongside others (rg parity:
			// an empty -e/-f pattern OR'd with anything still matches
			// EVERY line, since one alternative trivially matches at
			// every position) -- makes the whole alternation match
			// everywhere, so the rest of the set is redundant for
			// existence purposes. singleLiteralScanner's find already
			// implements exactly that for a lone empty literal (its
			// n==0 branch); reuse it here rather than adding a second
			// "always matches" type. Without this early return, an
			// empty literal reaching rareByteMultiScanner/
			// ahoCorasickScanner below would be silently dropped
			// (nextAnchor's len(l)==0 case returns "not found", and the
			// aho-corasick library has no concept of a zero-length
			// pattern either) -- verified against the real rg binary: a
			// -f pattern file with one blank line among others must
			// still match every line, not just the non-empty ones'.
			return newSingleLiteralScanner(nil)
		}
	}
	if len(lits) == 1 {
		if asciiCaseInsensitive {
			return newSingleLiteralCIScanner(lits[0])
		}
		return newSingleLiteralScanner(lits[0])
	}
	if asciiCaseInsensitive {
		return newAhoCorasickScanner(lits, true)
	}
	const smallSetLimit = 8
	if len(lits) <= smallSetLimit {
		return newRareByteMultiScanner(lits)
	}
	return newAhoCorasickScanner(lits, false)
}
