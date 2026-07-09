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
