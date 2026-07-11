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
func classifyFast(tokens []token) (literal, literal2 string, chunks []string, chainAnchoredStart, chainAnchoredEnd bool, kind patternKind, ok bool) {
	if lit, bok := basenameLiteralOf(tokens); bok {
		return lit, "", nil, false, false, kindBasename, true
	}
	if lit, lok := literalOf(tokens); lok {
		return lit, "", nil, false, false, kindLiteral, true
	}
	if ext, eok := extOfTokens(tokens); eok {
		return ext, "", nil, false, false, kindExt, true
	}
	if suf, sok := suffixOfTokens(tokens); sok {
		return suf, "", nil, false, false, kindSuffix, true
	}
	if pre, pok := prefixOfTokens(tokens); pok {
		return pre, "", nil, false, false, kindPrefix, true
	}
	if sub, cok := containsOfTokens(tokens); cok {
		return sub, "", nil, false, false, kindContains, true
	}
	if pre, suf, wok := betweenOfTokens(tokens); wok {
		return pre, suf, nil, false, false, kindBetween, true
	}
	// The two classes below don't require basenameTokens' `**/`-prefix
	// check, so they're disjoint from every class above: an unanchored
	// pattern always gets a `**/` prefix in compileLine (see
	// hasDoubleStarPrefix there), so any token sequence reaching this
	// point either already failed the classes above or was rooted from
	// the start (leading '/' or an interior '/' elsewhere in the
	// pattern) and could never have matched them anyway.
	if pre, suf, pok := pathBetweenOfTokens(tokens); pok {
		return pre, suf, nil, false, false, kindPathBetween, true
	}
	if cs, as, ae, cok := chainOfTokens(tokens); cok {
		return "", "", cs, as, ae, kindChain, true
	}
	if lit, sok := suffixPathOfTokens(tokens); sok {
		return lit, "", nil, false, false, kindSuffixPath, true
	}
	return "", "", nil, false, false, 0, false
}

// suffixPathOfTokens returns (lit, true) if the pattern is `**/` followed
// by a pure literal run lit that contains at least one '/', e.g.
// `**/.claude/settings.local.json` (lit = ".claude/settings.local.json").
// This is the multi-segment sibling of basenameLiteralOf: the same `**/`
// unanchored prefix, but a literal tail spanning more than one path
// segment, which basenameTokens (and therefore every basename-relative
// class) rejects on its first interior '/'.
//
// The '/' requirement is what keeps this class disjoint from
// basenameLiteralOf and pins the class boundary: a single-segment `**/foo`
// is always claimed by basenameLiteralOf earlier in classifyFast, so it
// never reaches here -- which matters because basenameLiteralOf and this
// class do NOT share match semantics on the (pathological) case of a path
// component containing a newline. This class is faithful to the regex
// `**/S` compiles to today (`^(?:/?|.*/)S$`, whose `.*` cannot cross a
// '\n'); basenameLiteralOf is not (it matches purely on the last path
// component). Requiring '/' here guarantees this class only ever owns
// shapes whose current, pre-optimization behavior IS that regex -- see
// Set.Match's suffixPaths loop for the exact predicate this compiles to.
//
// Found via an evaluation-count census on the linux tree with a real
// global core.excludesFile in play: `**/.claude/settings.local.json` (the
// one pattern in a typical global git ignore file) was landing in the
// regex fallback and evaluated on essentially every one of the tree's
// ~104k entries -- the single largest regex-fallback contributor by a wide
// margin once the basename-anchored and rooted single-wildcard classes had
// already landed.
func suffixPathOfTokens(tokens []token) (string, bool) {
	if len(tokens) < 2 || tokens[0].kind != tRecursivePrefix {
		return "", false
	}
	var sb strings.Builder
	hasSlash := false
	for _, t := range tokens[1:] {
		if t.kind != tLiteral {
			return "", false
		}
		if t.lit == '/' {
			hasSlash = true
		}
		sb.WriteRune(t.lit)
	}
	if !hasSlash || sb.Len() == 0 {
		return "", false
	}
	return sb.String(), true
}

// prefixOfTokens returns (prefix, true) if the pattern is `**/{lit}*` for
// a non-empty literal lit -- a run of literal characters followed by
// exactly one trailing `*`, and nothing else. A path's basename matches
// if and only if it starts with prefix (bytes.HasPrefix), regardless of
// what comes after.
//
// Found via an evaluation-count census on the linux tree: patterns
// like "cscope.*", "ncscope.*", "patches-*", and the implicit-dotfile
// pattern gitignore's own hidden-file handling produces (".*") were each
// landing in the regex fallback and being evaluated on nearly every one
// of the tree's ~104k files, since nothing upstream of the regex list
// ever improved on them -- among the single largest contributors to the
// ~50%-of-CPU regex cost this task exists to cut down.
func prefixOfTokens(tokens []token) (string, bool) {
	rest, ok := basenameTokens(tokens)
	if !ok || len(rest) < 2 {
		return "", false
	}
	last := rest[len(rest)-1]
	if last.kind != tZeroOrMore {
		return "", false
	}
	var sb strings.Builder
	for _, t := range rest[:len(rest)-1] {
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

// containsOfTokens returns (lit, true) if the pattern is `**/*{lit}*` for
// a non-empty literal lit -- a leading `*`, a run of literal characters,
// and a trailing `*`, and nothing else. A path's basename matches if and
// only if it contains lit anywhere (bytes.Contains).
//
// Found via the same census as prefixOfTokens: "*.o.*" (matching
// any name with ".o." anywhere, not just as a suffix -- extOfTokens and
// suffixOfTokens both require the literal to reach the end of the name)
// was the single most-evaluated regex pattern on the linux tree, at
// essentially one evaluation per file walked.
func containsOfTokens(tokens []token) (string, bool) {
	rest, ok := basenameTokens(tokens)
	if !ok || len(rest) < 3 {
		return "", false
	}
	if rest[0].kind != tZeroOrMore {
		return "", false
	}
	last := rest[len(rest)-1]
	if last.kind != tZeroOrMore {
		return "", false
	}
	var sb strings.Builder
	for _, t := range rest[1 : len(rest)-1] {
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

// betweenOfTokens returns (prefix, suffix, true) if the pattern is
// `**/{prefix}*{suffix}` for non-empty literals prefix and suffix with
// exactly one `*` strictly between them -- e.g. `#*#` (Emacs backup
// files). A path's basename matches if and only if it starts with prefix
// AND ends with suffix (bytes.HasPrefix + bytes.HasSuffix); unlike
// containsOfTokens, the two literal runs are anchored to opposite ends
// of the name, not free-floating, so this can't be folded into that
// class without weakening the match (a "contains" check alone would
// accept prefix and suffix appearing in the wrong order, or not at the
// ends at all).
//
// Found via the same census as prefixOfTokens/containsOfTokens:
// after those two landed, "#*#" was the single largest remaining
// regex-evaluation contributor on the linux tree, at essentially one
// evaluation per file walked.
func betweenOfTokens(tokens []token) (prefix, suffix string, ok bool) {
	rest, bok := basenameTokens(tokens)
	if !bok || len(rest) < 3 {
		return "", "", false
	}
	starIdx := -1
	for i, t := range rest {
		if t.kind == tZeroOrMore {
			if starIdx != -1 {
				return "", "", false // more than one wildcard
			}
			starIdx = i
			continue
		}
		if t.kind != tLiteral {
			return "", "", false
		}
	}
	if starIdx <= 0 || starIdx >= len(rest)-1 {
		// The wildcard must be strictly between a non-empty prefix and a
		// non-empty suffix -- starIdx==0 or starIdx==len(rest)-1 means
		// one side is empty, which prefixOfTokens/suffixOfTokens (or
		// containsOfTokens, for a leading wildcard) already cover.
		return "", "", false
	}
	var pre, suf strings.Builder
	for _, t := range rest[:starIdx] {
		pre.WriteRune(t.lit)
	}
	for _, t := range rest[starIdx+1:] {
		suf.WriteRune(t.lit)
	}
	return pre.String(), suf.String(), true
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

// pathBetweenOfTokens returns (prefix, suffix, true) if tokens is a
// literal run (possibly containing '/', possibly empty), then exactly
// one `*`, then another literal run (same conditions) -- e.g. the
// compiled form of a rooted pattern like `/*.spec` (prefix="",
// suffix=".spec"), `/load_address_*` (prefix="load_address_",
// suffix=""), or `arch/*/include/generated` (prefix="arch/",
// suffix="/include/generated", from the gitignore pattern
// `/arch/*/include/generated/`).
//
// Unlike prefixOfTokens/containsOfTokens/betweenOfTokens (which require a
// `**/` prefix and operate on just the basename), this class has no such
// prefix: a rooted pattern is anchored to the start of the *whole* path
// Match receives (already relative to the governing ignore file's
// directory -- see gitignore's rule that a leading or interior '/'
// anchors a pattern there), so this matches against path directly, not
// basename. A path matches if and only if it starts with prefix, ends
// with suffix, is long enough for both without overlapping, and --
// critically, since `*` never crosses '/' -- the stretch of path between
// prefix and suffix contains no '/' of its own. That last check is what
// makes a bare `/*.spec` (prefix="", suffix=".spec") correctly reject a
// nested "sub/foo.spec": the "middle" there is "sub/foo", which contains
// a '/'. See Set.Match's pathBetweens loop for where that check lives.
//
// Found via a follow-up census on the linux tree:
// `/*.spec`, `/arch/*/include/generated/`, `/processed-schema*.yaml`,
// `/processed-schema*.json`, `/test_fortify/*.log`, `po/*.gmo`,
// `/*.skel.h`, `policy/*.conf`, and `/load_address_*` were the largest
// remaining regex-fallback contributors once the basename-anchored
// classes had already landed -- every one of them anchored (by a leading
// '/' or an interior '/' elsewhere in the pattern) rather than basename-
// relative, which is exactly what basenameTokens' `**/`-prefix
// requirement excludes.
func pathBetweenOfTokens(tokens []token) (prefix, suffix string, ok bool) {
	starIdx := -1
	for i, t := range tokens {
		switch t.kind {
		case tZeroOrMore:
			if starIdx != -1 {
				return "", "", false // more than one wildcard
			}
			starIdx = i
		case tLiteral:
			// ok, may include '/' -- unlike basenameTokens' literal scan,
			// a '/' here is a real path separator this class intends to
			// match literally, not a boundary violation.
		default:
			return "", "", false
		}
	}
	if starIdx == -1 {
		return "", "", false // no wildcard at all -- literalOf's job
	}
	var pre, suf strings.Builder
	for _, t := range tokens[:starIdx] {
		pre.WriteRune(t.lit)
	}
	for _, t := range tokens[starIdx+1:] {
		suf.WriteRune(t.lit)
	}
	return pre.String(), suf.String(), true
}

// chainOfTokens returns (chunks, anchoredStart, anchoredEnd, true) if
// tokens (after stripping a `**/` prefix, so this is a basename-relative
// class like prefixOfTokens/containsOfTokens/betweenOfTokens) is a
// sequence of literal runs separated by one or more `*` each -- the
// general case those three classes leave uncovered once there are two or
// more separating wildcards, e.g. `*.c.0*.*` (chunks=[".c.0", "."],
// unanchored on both ends since it starts and ends with `*`).
// anchoredStart is true when the first chunk has no leading `*` (must be
// a prefix of the basename); anchoredEnd is true when the last chunk has
// no trailing `*` (must be a suffix). A run of two or more consecutive
// `*` tokens (possible from a literal "**" mid-pattern -- see
// parseStar) collapses to a single gap, same as one `*` would.
//
// Match existence for this shape reduces to a simple greedy left-to-right
// scan (see matchChain): find each interior chunk via the first
// occurrence at or after the end of the previous match, since basename
// (unlike a general path) is guaranteed free of '/' -- so unlike
// pathBetweenOfTokens's single-wildcard class, there is no "did the
// wildcard cross a separator" check to make; every position in basename
// is fair game for every wildcard here. This greedy-leftmost strategy is
// the standard technique for existence-only wildcard matching (it never
// needs to backtrack: matching each chunk as early as possible only ever
// leaves *more* room for the chunks still to come, never less).
//
// Found via the census: after prefixOfTokens/containsOfTokens/
// betweenOfTokens and pathBetweenOfTokens had already
// landed, `*.c.[012]*.*` was the single largest remaining regex
// contributor on the linux tree -- its post-char-class-expansion variants
// like `*.c.0*.*` need two wildcard gaps, one more than betweenOfTokens
// allows.
func chainOfTokens(tokens []token) (chunks []string, anchoredStart, anchoredEnd bool, ok bool) {
	rest, bok := basenameTokens(tokens)
	if !bok || len(rest) == 0 {
		return nil, false, false, false
	}
	anchoredStart = rest[0].kind != tZeroOrMore
	anchoredEnd = rest[len(rest)-1].kind != tZeroOrMore

	var sb strings.Builder
	inLiteral := false
	for _, t := range rest {
		switch t.kind {
		case tLiteral:
			sb.WriteRune(t.lit)
			inLiteral = true
		case tZeroOrMore:
			if inLiteral {
				chunks = append(chunks, sb.String())
				sb.Reset()
				inLiteral = false
			}
			// A consecutive `*` (no literal since the last one) is just
			// a wider gap -- nothing to close.
		default:
			// tAny, tClass, tAlternates, or a recursive token: give up,
			// not worth the complexity of reasoning through these here.
			return nil, false, false, false
		}
	}
	if inLiteral {
		chunks = append(chunks, sb.String())
	}
	if len(chunks) == 0 {
		// An all-`*` pattern (e.g. a bare "*"): matches everything,
		// not worth a dedicated fast class here.
		return nil, false, false, false
	}
	return chunks, anchoredStart, anchoredEnd, true
}
