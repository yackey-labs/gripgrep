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

func (s *rareByteMultiScanner) find(buf []byte, start int) (int, int, bool) {
	pos := start
	for pos <= len(buf) {
		bestHit := -1
		bestLit := -1
		for i, l := range s.lits {
			if len(l) == 0 {
				continue
			}
			anchor := l[s.rareIdx[i]]
			idx := bytes.IndexByte(buf[pos:], anchor)
			if idx < 0 {
				continue
			}
			hit := pos + idx
			if bestHit == -1 || hit < bestHit {
				bestHit = hit
				bestLit = i
			}
		}
		if bestHit == -1 {
			return 0, 0, false
		}
		// Among all literals, check every one whose anchor byte matches
		// at bestHit (not just bestLit) since several literals can share
		// the same rarest byte value.
		for i, l := range s.lits {
			if len(l) == 0 || l[s.rareIdx[i]] != buf[bestHit] {
				continue
			}
			litStart := bestHit - s.rareIdx[i]
			// See singleLiteralScanner.find for why this must be >=
			// start (the original request), not just >= 0.
			if litStart >= start && litStart+len(l) <= len(buf) && bytes.Equal(buf[litStart:litStart+len(l)], l) {
				return litStart, len(l), true
			}
		}
		_ = bestLit
		pos = bestHit + 1
	}
	return 0, 0, false
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
func newLiteralScanner(lits [][]byte, asciiCaseInsensitive bool) literalScanner {
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
