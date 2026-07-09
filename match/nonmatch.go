package match

import "regexp/syntax"

// canMatchNewline reports whether re can possibly match a string
// containing the '\n' byte (0x0A), walking the parsed tree in the same
// spirit as ripgrep's non_matching_bytes (crates/regex/src/non_matching.rs)
// but specialized to the single question the searcher needs answered:
// can this pattern match across a line terminator at all.
//
// This must be conservative: any construct not explicitly proven '\n'-free
// causes a true result (i.e. NonMatchingLineTerm on the Matcher becomes
// false, forcing the slower per-line search path). A false negative here
// (reporting a pattern can't match '\n' when it actually can) would let
// the searcher take the unsound whole-buffer fast path and silently miss
// or corrupt matches; a false positive only costs performance.
//
// Notable Go-specific traps mirrored from rg's non_matching.rs:
//   - A negated class like [^x] DOES match '\n' in Go (Go, unlike some
//     engines, does not implicitly exclude the line terminator from
//     negated classes) -- this falls out naturally here since we just
//     check whether '\n' is one of the class's rune ranges.
//   - OpAnyCharNotNL ('.' without (?s)) does NOT match '\n' by
//     construction.
//   - OpAnyChar ('.' with (?s), or explicit \C-like "any byte") DOES
//     match '\n'.
//   - An explicit '\n' literal (rune 0x0A) matches '\n'.
//   - Anchors (^ $ \A \z, in any multi-line mode) are treated as
//     potentially interacting with '\n' handling in the searcher (rg
//     does the same, conservatively, per grep-searcher's anchored-search
//     caveat) so their presence also forces the conservative answer.
func canMatchNewline(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return false

	case syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText:
		// Conservative: rg's grep-searcher fast path is unsound in the
		// presence of these anchors regardless of whether '\n' itself
		// is "matched" by them, so gripgrep's searcher must not take the
		// whole-buffer path either.
		return true

	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if r == '\n' {
				return true
			}
		}
		return false

	case syntax.OpCharClass:
		for i := 0; i+1 < len(re.Rune); i += 2 {
			if re.Rune[i] <= '\n' && '\n' <= re.Rune[i+1] {
				return true
			}
		}
		return false

	case syntax.OpAnyCharNotNL:
		return false

	case syntax.OpAnyChar:
		return true

	case syntax.OpCapture, syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		return canMatchNewline(re.Sub[0])

	case syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if canMatchNewline(sub) {
				return true
			}
		}
		return false

	case syntax.OpNoMatch:
		return false

	default:
		// Unknown op: be conservative.
		return true
	}
}
