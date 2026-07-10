package glob

import (
	"bytes"

	"github.com/grafana/regexp"
)

// Set is a compiled collection of gitignore-style glob patterns, queried
// as one combined unit per path.
//
// Patterns are dispatched through nine fast classes before falling back
// to regexp: an exact full-path literal map, a basename literal map, an
// extension map (for patterns of the exact shape `**/*.ext`), a suffix
// list (`**/*<literal tail>` whose tail isn't a single dot-segment, e.g.
// `**/*.dtb.S` -- see suffixOfTokens), a prefix list (`**/<literal>*`,
// e.g. `cscope.*` -- see prefixOfTokens), a contains list
// (`**/*<literal>*`, e.g. `*.o.*` -- see containsOfTokens), a between
// list (`**/<prefix>*<suffix>`, e.g. `#*#` -- see betweenOfTokens), a
// path-between list for rooted single-wildcard patterns matched against
// the whole path rather than just the basename (e.g. `/*.spec` or
// `/arch/*/include/generated/` -- see pathBetweenOfTokens), and a chain
// list for basename patterns with two or more wildcards separating
// literal runs (e.g. `*.c.0*.*` -- see chainOfTokens). The
// prefix/contains/between classes were added by M3 #23's
// evaluation-count census of real-world .gitignore files (the Linux
// kernel's own); path-between and chain were added by round #27's
// follow-up census once those three no longer left much regex-fallback
// weight to find. Every one of them was landing in the regex fallback
// and being evaluated on nearly every path in the entire tree. Every
// other pattern (containing `?`, character classes, alternates, or more
// wildcards than a class here handles) compiles to a regexp and is tried
// linearly. See Match for how these combine to resolve gitignore's
// last-match-wins precedence.
type Set struct {
	literalMap   map[string][]patternRef
	basenameMap  map[string][]patternRef
	extMap       map[string][]patternRef
	suffixes     []suffixEntry
	prefixes     []prefixEntry
	contains     []containsEntry
	betweens     []betweenEntry
	pathBetweens []pathBetweenEntry
	chains       []chainEntry
	regexes      []regexEntry
}

// patternRef is everything Match needs about a pattern once it has
// matched: its Builder.Add-order index (last-match-wins precedence),
// whether it's a `!`-whitelist entry, and whether it only matches
// directories.
type patternRef struct {
	index       int
	isWhitelist bool
	isOnlyDir   bool
}

// suffixEntry is a compiled kindSuffix pattern: suffix is the literal
// tail a basename must end with, precomputed as []byte once at Build
// time so Match's bytes.HasSuffix check never allocates or converts.
type suffixEntry struct {
	patternRef
	suffix []byte
}

// prefixEntry is a compiled kindPrefix pattern: prefix is the literal
// head a basename must start with, precomputed as []byte once at Build
// time so Match's bytes.HasPrefix check never allocates or converts.
type prefixEntry struct {
	patternRef
	prefix []byte
}

// containsEntry is a compiled kindContains pattern: substr is the literal
// a basename must contain anywhere, precomputed as []byte once at Build
// time so Match's bytes.Contains check never allocates or converts.
type containsEntry struct {
	patternRef
	substr []byte
}

// betweenEntry is a compiled kindBetween pattern: a basename must both
// start with prefix and end with suffix, each precomputed as []byte once
// at Build time so Match's bytes.HasPrefix/bytes.HasSuffix checks never
// allocate or convert.
type betweenEntry struct {
	patternRef
	prefix []byte
	suffix []byte
}

// pathBetweenEntry is a compiled kindPathBetween pattern: a rooted,
// single-wildcard pattern matched against the whole path (not just the
// basename) -- see pathBetweenOfTokens. prefix/suffix are precomputed as
// []byte once at Build time so Match's HasPrefix/HasSuffix checks never
// allocate or convert.
type pathBetweenEntry struct {
	patternRef
	prefix []byte
	suffix []byte
}

// chainEntry is a compiled kindChain pattern: chunks are a basename's
// required literal runs in match order, each precomputed as []byte once
// at Build time; anchoredStart/anchoredEnd mirror chainOfTokens' return
// values. See chainMatches for how these combine into a match test.
type chainEntry struct {
	patternRef
	chunks        [][]byte
	anchoredStart bool
	anchoredEnd   bool
}

type regexEntry struct {
	patternRef
	re *regexp.Regexp
}

// Builder accumulates glob patterns before compiling them into a Set.
// The zero value is ready to use.
type Builder struct {
	patterns []string
}

// Add registers one gitignore-style pattern. A pattern prefixed with '!'
// is a whitelist (re-include) entry, matching gitignore syntax. Add
// never fails outright; a malformed pattern is recorded and surfaced by
// Build. Add returns the Builder to allow chaining.
//
// A pattern that gitignore syntax defines as inert — a `#`-comment line
// or a blank line — is accepted here too and simply contributes nothing
// to the compiled Set.
func (b *Builder) Add(pattern string) *Builder {
	b.patterns = append(b.patterns, pattern)
	return b
}

// Build compiles all patterns added via Add into a single Set. Patterns
// are numbered by their Add-order for last-match-wins resolution;
// compilation stops at (and reports) the first invalid pattern.
func (b *Builder) Build() (*Set, error) {
	s := &Set{
		literalMap:  make(map[string][]patternRef),
		basenameMap: make(map[string][]patternRef),
		extMap:      make(map[string][]patternRef),
	}
	for i, raw := range b.patterns {
		cps, err := compileLine(i, raw)
		if err != nil {
			return nil, err
		}
		for _, cp := range cps {
			ref := patternRef{index: cp.index, isWhitelist: cp.isWhitelist, isOnlyDir: cp.isOnlyDir}
			switch cp.kind {
			case kindLiteral:
				s.literalMap[cp.literal] = append(s.literalMap[cp.literal], ref)
			case kindBasename:
				s.basenameMap[cp.literal] = append(s.basenameMap[cp.literal], ref)
			case kindExt:
				s.extMap[cp.literal] = append(s.extMap[cp.literal], ref)
			case kindSuffix:
				s.suffixes = append(s.suffixes, suffixEntry{patternRef: ref, suffix: []byte(cp.literal)})
			case kindPrefix:
				s.prefixes = append(s.prefixes, prefixEntry{patternRef: ref, prefix: []byte(cp.literal)})
			case kindContains:
				s.contains = append(s.contains, containsEntry{patternRef: ref, substr: []byte(cp.literal)})
			case kindBetween:
				s.betweens = append(s.betweens, betweenEntry{patternRef: ref, prefix: []byte(cp.literal), suffix: []byte(cp.literal2)})
			case kindPathBetween:
				s.pathBetweens = append(s.pathBetweens, pathBetweenEntry{patternRef: ref, prefix: []byte(cp.literal), suffix: []byte(cp.literal2)})
			case kindChain:
				chunks := make([][]byte, len(cp.chunks))
				for i, c := range cp.chunks {
					chunks[i] = []byte(c)
				}
				s.chains = append(s.chains, chainEntry{patternRef: ref, chunks: chunks, anchoredStart: cp.chainAnchoredStart, anchoredEnd: cp.chainAnchoredEnd})
			case kindRegex:
				s.regexes = append(s.regexes, regexEntry{patternRef: ref, re: cp.re})
			}
		}
	}
	return s, nil
}

// MatchResult is the outcome of matching one path against a Set.
type MatchResult uint8

const (
	// NoMatch means no pattern in the Set matched this path.
	NoMatch MatchResult = iota
	// Ignored means the winning match (last match, per gitignore
	// last-match-wins precedence) was an ordinary (non-whitelist)
	// pattern: the path should be excluded.
	Ignored
	// Whitelisted means the winning match was a '!'-prefixed pattern:
	// the path should be included even though an earlier/outer pattern
	// matched it.
	Whitelisted
)

// Match reports how path matches the compiled Set.
//
// path is the byte slice being tested and is never converted to string
// on this hot path — Match is called once per walked entry. isDir lets
// directory-only patterns (trailing '/' in the source pattern) match
// correctly. Match does not allocate in steady state: map lookups keyed
// by a []byte-derived string (path, its basename, its extension) are
// recognized by the compiler and don't allocate, and every candidate
// slice/regexp was built once in Build.
//
// Semantics: gitignore resolves conflicting patterns by last-match-wins
// (the highest Builder.Add-order index that matches decides the
// outcome), so Match doesn't stop at the first hit — it tracks the
// highest-index match seen across every fast class (the three maps and
// the suffix list) and the
// regexp fallback list, skipping any pattern whose is-directory-only
// requirement isn't met.
func (s *Set) Match(path []byte, isDir bool) MatchResult {
	bestIdx := -1
	bestWhitelist := false

	if refs, ok := s.literalMap[string(path)]; ok {
		for _, r := range refs {
			if r.isOnlyDir && !isDir {
				continue
			}
			if r.index > bestIdx {
				bestIdx, bestWhitelist = r.index, r.isWhitelist
			}
		}
	}

	base := basename(path)
	if refs, ok := s.basenameMap[string(base)]; ok {
		for _, r := range refs {
			if r.isOnlyDir && !isDir {
				continue
			}
			if r.index > bestIdx {
				bestIdx, bestWhitelist = r.index, r.isWhitelist
			}
		}
	}

	if ext := extOf(base); ext != nil {
		if refs, ok := s.extMap[string(ext)]; ok {
			for _, r := range refs {
				if r.isOnlyDir && !isDir {
					continue
				}
				if r.index > bestIdx {
					bestIdx, bestWhitelist = r.index, r.isWhitelist
				}
			}
		}
	}

	for i := range s.suffixes {
		suf := &s.suffixes[i]
		if suf.index <= bestIdx {
			// Can't improve the outcome even if it matches.
			continue
		}
		if suf.isOnlyDir && !isDir {
			continue
		}
		if bytes.HasSuffix(base, suf.suffix) {
			bestIdx, bestWhitelist = suf.index, suf.isWhitelist
		}
	}

	for i := range s.prefixes {
		pre := &s.prefixes[i]
		if pre.index <= bestIdx {
			continue
		}
		if pre.isOnlyDir && !isDir {
			continue
		}
		if bytes.HasPrefix(base, pre.prefix) {
			bestIdx, bestWhitelist = pre.index, pre.isWhitelist
		}
	}

	for i := range s.contains {
		c := &s.contains[i]
		if c.index <= bestIdx {
			continue
		}
		if c.isOnlyDir && !isDir {
			continue
		}
		if bytes.Contains(base, c.substr) {
			bestIdx, bestWhitelist = c.index, c.isWhitelist
		}
	}

	for i := range s.betweens {
		b := &s.betweens[i]
		if b.index <= bestIdx {
			continue
		}
		if b.isOnlyDir && !isDir {
			continue
		}
		// The `*` between prefix and suffix matches zero or more
		// characters, so a basename shorter than prefix+suffix combined
		// can't possibly fit both non-overlapping -- without this guard,
		// e.g. "#*#" would wrongly match the single-character base "#"
		// (HasPrefix and HasSuffix would each trivially match the same
		// one character).
		if len(base) >= len(b.prefix)+len(b.suffix) &&
			bytes.HasPrefix(base, b.prefix) && bytes.HasSuffix(base, b.suffix) {
			bestIdx, bestWhitelist = b.index, b.isWhitelist
		}
	}

	for i := range s.pathBetweens {
		pb := &s.pathBetweens[i]
		if pb.index <= bestIdx {
			continue
		}
		if pb.isOnlyDir && !isDir {
			continue
		}
		// Matched against path directly (not base): a pathBetween entry
		// is already anchored to the start of whatever path Match
		// receives (see pathBetweenOfTokens' doc), unlike every class
		// above which matches within base.
		if len(path) >= len(pb.prefix)+len(pb.suffix) &&
			bytes.HasPrefix(path, pb.prefix) && bytes.HasSuffix(path, pb.suffix) {
			mid := path[len(pb.prefix) : len(path)-len(pb.suffix)]
			if bytes.IndexByte(mid, '/') < 0 {
				bestIdx, bestWhitelist = pb.index, pb.isWhitelist
			}
		}
	}

	for i := range s.chains {
		c := &s.chains[i]
		if c.index <= bestIdx {
			continue
		}
		if c.isOnlyDir && !isDir {
			continue
		}
		if chainMatches(base, c.chunks, c.anchoredStart, c.anchoredEnd) {
			bestIdx, bestWhitelist = c.index, c.isWhitelist
		}
	}

	for i := range s.regexes {
		re := &s.regexes[i]
		if re.index <= bestIdx {
			// Can't improve the outcome even if it matches.
			continue
		}
		if re.isOnlyDir && !isDir {
			continue
		}
		if re.re.Match(path) {
			bestIdx, bestWhitelist = re.index, re.isWhitelist
		}
	}

	switch {
	case bestIdx < 0:
		return NoMatch
	case bestWhitelist:
		return Whitelisted
	default:
		return Ignored
	}
}

// chainMatches reports whether base matches the kindChain shape
// described by chunks/anchoredStart/anchoredEnd (see chainOfTokens):
// each chunk in turn, greedily matched at or after the position the
// previous chunk left off. This is safe as an existence-only test
// (no need to backtrack across alternative chunk positions) because
// matching each chunk as early as possible only ever leaves *more* of
// base available for the chunks still to come, never less -- the
// standard greedy-leftmost strategy for wildcard existence matching.
//
// base is guaranteed free of '/' (it came from basenameTokens via
// chainOfTokens), so unlike pathBetweenOfTokens' single-wildcard class,
// there's no "did a wildcard cross a separator" check to make here: every
// position in base is fair game for every gap between chunks.
func chainMatches(base []byte, chunks [][]byte, anchoredStart, anchoredEnd bool) bool {
	pos := 0
	last := len(chunks) - 1
	for i, c := range chunks {
		switch {
		case i == 0 && anchoredStart && i == last && anchoredEnd:
			// A single chunk anchored on both ends is a plain literal
			// equality check -- not reachable via classifyFast (that
			// shape is basenameLiteralOf's job), but correct in
			// isolation regardless.
			return bytes.Equal(base, c)
		case i == 0 && anchoredStart:
			if !bytes.HasPrefix(base, c) {
				return false
			}
			pos = len(c)
		case i == last && anchoredEnd:
			start := len(base) - len(c)
			if start < pos {
				return false
			}
			return bytes.HasSuffix(base, c)
		default:
			idx := bytes.Index(base[pos:], c)
			if idx < 0 {
				return false
			}
			pos += idx + len(c)
		}
	}
	return true
}
