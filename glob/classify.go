package glob

import "strings"

// The functions below classify a parsed token sequence into a fast match
// class, mirroring Glob::literal / Glob::ext / Glob::basename_literal in
// ../ripgrep/crates/globset/src/glob.rs. They assume literal_separator is
// always true (gitignore semantics: `*`/`?` never cross `/`), which lets
// several of the upstream checks (which exist only to guard against a
// `literal_separator=false` config) be dropped or simplified — noted
// inline where that happens.

// literalOf returns (lit, true) if every token in the sequence is a plain
// literal, meaning the pattern matches if and only if the entire
// candidate path equals lit exactly.
func literalOf(tokens []token) (string, bool) {
	var sb strings.Builder
	for _, t := range tokens {
		if t.kind != tLiteral {
			return "", false
		}
		sb.WriteRune(t.lit)
	}
	if sb.Len() == 0 {
		return "", false
	}
	return sb.String(), true
}

// extOfTokens returns (ext, true) if the pattern is exactly `**/*.ext`
// (an unanchored, extension-only match), meaning a path matches the
// pattern if and only if its extension (see extOf) equals ext.
//
// Upstream also permits this classification when there is no `**/`
// prefix, provided literal_separator is false (so a bare `*` can cross
// `/`). We never allow that, since literal_separator is always true here:
// a bare `*.ext` (no recursive prefix) only ever matches top-level names,
// so it is intentionally left to the regex fallback instead.
func extOfTokens(tokens []token) (string, bool) {
	if len(tokens) == 0 || tokens[0].kind != tRecursivePrefix {
		return "", false
	}
	start := 1
	if start >= len(tokens) || tokens[start].kind != tZeroOrMore {
		return "", false
	}
	if start+1 >= len(tokens) || tokens[start+1].kind != tLiteral || tokens[start+1].lit != '.' {
		return "", false
	}
	var sb strings.Builder
	sb.WriteByte('.')
	for _, t := range tokens[start+2:] {
		if t.kind != tLiteral || t.lit == '.' || t.lit == '/' {
			return "", false
		}
		sb.WriteRune(t.lit)
	}
	return sb.String(), true
}

// suffixOfTokens returns (suffix, true) if the pattern is `**/*` followed
// by one or more plain literal tokens containing no `/` -- e.g. `**/*.rs`
// (also covered by extOfTokens) but critically also multi-dot tails like
// `**/*.dtb.S` or `**/*.mod.c`, which extOfTokens rejects (it only ever
// isolates the single segment after the *last* dot, so a literal tail
// containing an interior dot doesn't qualify there). Since the leading
// `*` matches any run of non-'/' characters and everything after it is a
// plain literal, a path's basename matches the whole pattern if and only
// if it ends with suffix -- exactly what bytes.HasSuffix computes,
// regardless of how many dots suffix itself contains.
//
// This exists as its own (linearly-scanned) class rather than folded into
// extMap's O(1) map lookup because extMap's key is deliberately just the
// last dot-segment; a general suffix has no such fixed-shape key to hash
// on. Patterns extOfTokens already classifies are cheaper to leave there
// (map lookup beats a HasSuffix scan), so pattern.go only calls this as a
// fallback after extOfTokens fails.
func suffixOfTokens(tokens []token) (string, bool) {
	if len(tokens) < 3 || tokens[0].kind != tRecursivePrefix || tokens[1].kind != tZeroOrMore {
		return "", false
	}
	var sb strings.Builder
	for _, t := range tokens[2:] {
		if t.kind != tLiteral || t.lit == '/' {
			return "", false
		}
		sb.WriteRune(t.lit)
	}
	if sb.Len() == 0 {
		return "", false
	}
	return sb.String(), true
}

// maxClassSize is the largest a single character class may be to
// qualify for expansion into literal alternates (expandClasses),
// mirroring the bound rg's own literal extractor uses for the
// equivalent decision on regex character classes (limit_class=10; see
// docs/research/ripgrep-internals.md §2a). A class larger than this
// stays a class-based regex Match, as today.
const maxClassSize = 10

// maxExpandedPatterns bounds the total number of concrete patterns one
// compileLine call may expand into. A pattern can contain more than one
// class, and expansion is a cross product across all of them, so this
// guards against combinatorial blowup -- mirrors rg's limit_total=64.
const maxExpandedPatterns = 64

// expandClasses returns every concrete token sequence produced by
// substituting a single literal character for each non-negated,
// small-enough tClass token in tokens (the cross product, when more
// than one such class is present), or (nil, false) if tokens contains
// no eligible class at all.
//
// Motivation: a class like `[ch]` only ever contributes two bytes of
// entropy, but classify.go's fast-path classifiers (literalOf,
// basenameLiteralOf, extOfTokens, suffixOfTokens) all require an
// all-literal token sequence, so any pattern containing one -- even an
// otherwise-trivial suffix like `*.asn1.[ch]` -- was falling all the
// way through to the regex fallback. Turning `[ch]` into two literal
// variants ("...c" / "...h") lets each variant re-enter the same
// fast-path classification a plain literal pattern gets; the caller
// (compileLine) is responsible for verifying every returned variant
// actually lands in a fast class before using this expansion, and for
// discarding it (keeping the single original regex) otherwise --
// expandClasses itself only enumerates candidates, it doesn't classify
// them.
//
// A negated class (`[!...]`) is never expanded (its whole point is
// "not these characters", which isn't a small enumerable positive set);
// nor is one whose member count exceeds maxClassSize, or whose
// contribution to the running cross-product total exceeds
// maxExpandedPatterns -- both cases return (nil, false) and leave
// tokens to be regex-compiled unexpanded, exactly as before this
// existed.
func expandClasses(tokens []token) ([][]token, bool) {
	type classSpot struct {
		idx   int
		chars []rune
	}
	var spots []classSpot
	total := 1
	for i, t := range tokens {
		if t.kind != tClass {
			continue
		}
		if t.negated {
			return nil, false
		}
		var chars []rune
		for _, r := range t.ranges {
			for c := r[0]; c <= r[1]; c++ {
				chars = append(chars, c)
				if len(chars) > maxClassSize {
					return nil, false
				}
			}
		}
		if len(chars) == 0 {
			return nil, false
		}
		total *= len(chars)
		if total > maxExpandedPatterns {
			return nil, false
		}
		spots = append(spots, classSpot{idx: i, chars: chars})
	}
	if len(spots) == 0 {
		return nil, false
	}

	variants := [][]token{tokens}
	for _, spot := range spots {
		next := make([][]token, 0, len(variants)*len(spot.chars))
		for _, base := range variants {
			for _, c := range spot.chars {
				v := make([]token, len(base))
				copy(v, base)
				v[spot.idx] = token{kind: tLiteral, lit: c}
				next = append(next, v)
			}
		}
		variants = next
	}
	return variants, true
}

// classifyFast attempts to classify tokens into one of Set's fast
// (non-regex) match classes, trying them in the same precedence order
// compileLine uses for an ordinary pattern: whole-basename literal,
// whole-path literal, extension, then literal suffix. It never touches
// the regex fallback -- ok is false if tokens fit none of these.
func classifyFast(tokens []token) (literal string, kind patternKind, ok bool) {
	if lit, bok := basenameLiteralOf(tokens); bok {
		return lit, kindBasename, true
	}
	if lit, lok := literalOf(tokens); lok {
		return lit, kindLiteral, true
	}
	if ext, eok := extOfTokens(tokens); eok {
		return ext, kindExt, true
	}
	if suf, sok := suffixOfTokens(tokens); sok {
		return suf, kindSuffix, true
	}
	return "", 0, false
}

// basenameTokens returns the sub-sequence of tokens that applies only to
// a path's basename, if and only if any match of that sub-sequence
// against a basename implies a match of the whole pattern against the
// whole path. This requires a `**/` prefix (so the parent portion of the
// path is unconstrained) and no token in the remainder that could itself
// examine or cross a `/`.
func basenameTokens(tokens []token) ([]token, bool) {
	if len(tokens) == 0 || tokens[0].kind != tRecursivePrefix {
		return nil, false
	}
	rest := tokens[1:]
	if len(rest) == 0 {
		return nil, false
	}
	for _, t := range rest {
		switch t.kind {
		case tLiteral:
			if t.lit == '/' {
				return nil, false
			}
		case tAny, tZeroOrMore:
			// literal_separator is always true, so these can't cross
			// out of the basename.
		default:
			// tRecursivePrefix, tRecursiveSuffix, tRecursiveZeroOrMore,
			// tClass, tAlternates: give up, not worth the complexity of
			// reasoning through these here.
			return nil, false
		}
	}
	return rest, true
}

// basenameLiteralOf returns (lit, true) if the pattern is exactly
// `**/{lit}` for a literal lit containing no `/`, meaning a path matches
// if and only if its basename (see basename) equals lit exactly.
func basenameLiteralOf(tokens []token) (string, bool) {
	bt, ok := basenameTokens(tokens)
	if !ok {
		return "", false
	}
	var sb strings.Builder
	for _, t := range bt {
		if t.kind != tLiteral {
			return "", false
		}
		sb.WriteRune(t.lit)
	}
	return sb.String(), true
}
