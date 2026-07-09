package match

import "unicode"

// analyzePatternCase ports ripgrep's AstAnalysis (crates/regex/src/ast.rs):
// a scan of the pattern's raw source text that reports whether it
// contains any literal character at all (anyLiteral) and whether any
// literal character is uppercase (anyUppercase). Smart case then enables
// case-insensitive matching iff anyLiteral && !anyUppercase.
//
// Crucially this looks at literal characters as written in the source,
// regardless of whether they sit inside a `(?i)` group -- rg's own
// AstAnalysis walks the pre-translation AST, which carries no case-fold
// state on individual literal nodes, so `(?i)Foo` is still considered to
// contain an uppercase literal. It also does not count characters that
// are part of a class-shorthand or Unicode-property escape (\d \w \s
// \p{Lu} ...) since those aren't literal text -- only actual literal
// characters, including ones written inside a bracket expression like
// [A-Z] or after an escape like \\S (backslash-backslash then literal
// S), count.
//
// Go's regexp/syntax has no equivalent pre-translation AST exposed
// publicly (case folding and Unicode-property classes are both
// collapsed into the same OpCharClass shape by the time Parse returns),
// so this is implemented as a small text scanner over the pattern
// instead of a tree walk.
func analyzePatternCase(pattern string) (anyLiteral, anyUppercase bool) {
	runes := []rune(pattern)
	i := 0
	for i < len(runes) {
		r := runes[i]
		switch r {
		case '\\':
			i++
			if i >= len(runes) {
				break
			}
			esc := runes[i]
			switch esc {
			case 'p', 'P':
				// \p{Name} or \P{Name}: a Unicode property class, not a
				// literal. Skip to the closing brace if present;
				// otherwise (e.g. \pL) it's a single-letter shorthand,
				// consume just that letter too.
				i++
				if i < len(runes) && runes[i] == '{' {
					for i < len(runes) && runes[i] != '}' {
						i++
					}
				}
				i++
				continue
			case 'd', 'D', 's', 'S', 'w', 'W', 'b', 'B', 'A', 'z':
				// Class shorthand or zero-width assertion: not literal.
				i++
				continue
			case 'Q':
				// \Q...\E literal quoting: everything inside is literal
				// text (Go's regexp/syntax doesn't support \Q\E, but
				// guard against it defensively rather than mis-scan).
				i++
				for i < len(runes) {
					if runes[i] == '\\' && i+1 < len(runes) && runes[i+1] == 'E' {
						i += 2
						break
					}
					markLiteral(runes[i], &anyLiteral, &anyUppercase)
					i++
				}
				continue
			default:
				// Any other escaped character (\. \( \x41 \x{1F600} \0
				// ...) is a literal character once decoded. We don't
				// bother decoding numeric escapes precisely here: the
				// only thing that matters is whether the resulting rune
				// is an uppercase letter, and escapes like \xHH or \x{...}
				// or \0 never spell an uppercase ASCII/Unicode letter via
				// a bare escaped-letter path, so treat plain single-char
				// escapes as literal and skip hex/unicode escape bodies
				// without trying to decode them (they cannot mark
				// anyUppercase since they're numeric).
				if esc == 'x' || esc == 'u' || esc == 'U' {
					i++
					if i < len(runes) && runes[i] == '{' {
						for i < len(runes) && runes[i] != '}' {
							i++
						}
						i++
					} else {
						// \xHH: consume up to 2 hex digits.
						for n := 0; n < 2 && i < len(runes) && isHexDigit(runes[i]); n++ {
							i++
						}
					}
					anyLiteral = true
					continue
				}
				markLiteral(esc, &anyLiteral, &anyUppercase)
				i++
				continue
			}
		case '[':
			i = scanClass(runes, i, &anyLiteral, &anyUppercase)
			continue
		case '.', '^', '$', '(', ')', '|', '*', '+', '?', '{', '}':
			// Meta characters with no literal semantics of their own
			// here (repetition counts inside {} are digits, never
			// letters).
			i++
			continue
		default:
			markLiteral(r, &anyLiteral, &anyUppercase)
			i++
		}
	}
	return anyLiteral, anyUppercase
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func markLiteral(r rune, anyLiteral, anyUppercase *bool) {
	*anyLiteral = true
	if unicode.IsUpper(r) {
		*anyUppercase = true
	}
}

// scanClass scans a bracket expression starting at runes[start] == '['
// and returns the index just past its closing ']', marking any literal
// characters found inside (but not those belonging to nested \p{...} or
// \d \w \s escapes, or named POSIX classes like [:alpha:]).
func scanClass(runes []rune, start int, anyLiteral, anyUppercase *bool) int {
	i := start + 1
	if i < len(runes) && runes[i] == '^' {
		i++
	}
	if i < len(runes) && runes[i] == ']' {
		// A leading ']' right after '[' or '[^' is a literal ']'.
		markLiteral(']', anyLiteral, anyUppercase)
		i++
	}
	depth := 1
	for i < len(runes) && depth > 0 {
		switch runes[i] {
		case '\\':
			i++
			if i >= len(runes) {
				return i
			}
			esc := runes[i]
			switch esc {
			case 'p', 'P':
				i++
				if i < len(runes) && runes[i] == '{' {
					for i < len(runes) && runes[i] != '}' {
						i++
					}
				}
				i++
			case 'd', 'D', 's', 'S', 'w', 'W':
				i++
			default:
				markLiteral(esc, anyLiteral, anyUppercase)
				i++
			}
			continue
		case '[':
			if i+1 < len(runes) && runes[i+1] == ':' {
				// POSIX class [:alpha:]; skip to closing ':]'.
				j := i + 2
				for j+1 < len(runes) && !(runes[j] == ':' && runes[j+1] == ']') {
					j++
				}
				i = j + 2
				continue
			}
			depth++
			i++
		case ']':
			depth--
			i++
		default:
			markLiteral(runes[i], anyLiteral, anyUppercase)
			i++
		}
	}
	return i
}
