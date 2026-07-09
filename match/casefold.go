package match

// ASCII case-insensitive literal scanning helpers (PLAN.md's
// "Case-insensitive literal" row): prefer a case-invariant rare byte
// (digit/punctuation/underscore -- anything that isn't an ASCII letter,
// so it has exactly one byte value regardless of case) as the
// bytes.IndexByte anchor; if the literal has no such byte (e.g. it's
// all letters), fall back to two IndexByte scans -- one for the upper
// form and one for the lower form of the literal's rarest letter --
// taking whichever occurs first. Both approaches stay on the SIMD
// bytes.IndexByte path; only literals containing non-ASCII bytes need
// to fall through to the regex engine (case folding there is
// Unicode-aware and not representable as a single invariant byte or an
// upper/lower byte pair).

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func asciiToUpper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - ('a' - 'A')
	}
	return b
}

func asciiToLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// asciiEqualFold reports whether a and b are equal ASCII byte strings
// under case folding (non-letter bytes must match exactly).
func asciiEqualFold(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if asciiToLower(a[i]) != asciiToLower(b[i]) {
			return false
		}
	}
	return true
}

// asciiCIAnchor describes how to scan for an ASCII case-insensitive
// literal using bytes.IndexByte: either a single case-invariant byte at
// idx (invariant == true, lowerByte == upperByte), or a pair of letter
// bytes at idx requiring two scans (invariant == false).
type asciiCIAnchor struct {
	idx                  int
	invariant            bool
	lowerByte, upperByte byte
}

// pickASCIICIAnchor chooses the best bytes.IndexByte anchor for an
// all-ASCII literal under case-insensitive matching. It prefers the
// rarest case-invariant byte (a digit, punctuation, or underscore --
// anything whose upper and lower forms are identical, so a single
// IndexByte scan suffices with no false-negative risk from case). If no
// such byte exists (the literal is composed entirely of letters), it
// picks the letter position whose upper/lower pair is jointly rarest,
// for a two-scan (upper, then lower) approach.
func pickASCIICIAnchor(lit []byte) asciiCIAnchor {
	bestInvariantIdx := -1
	bestInvariantRank := 256
	bestLetterIdx := 0
	bestLetterRank := -1
	for i, b := range lit {
		if !isASCIILetter(b) {
			r := rank(b)
			if r < bestInvariantRank {
				bestInvariantRank = r
				bestInvariantIdx = i
			}
			continue
		}
		lo, up := asciiToLower(b), asciiToUpper(b)
		combined := rank(lo) + rank(up)
		if bestLetterRank == -1 || combined < bestLetterRank {
			bestLetterRank = combined
			bestLetterIdx = i
		}
	}
	if bestInvariantIdx >= 0 {
		b := lit[bestInvariantIdx]
		return asciiCIAnchor{idx: bestInvariantIdx, invariant: true, lowerByte: b, upperByte: b}
	}
	b := lit[bestLetterIdx]
	return asciiCIAnchor{
		idx:       bestLetterIdx,
		invariant: false,
		lowerByte: asciiToLower(b),
		upperByte: asciiToUpper(b),
	}
}
