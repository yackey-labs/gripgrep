package match

import (
	"bytes"
	"regexp/syntax"
	"strings"
	"unicode/utf8"

	"github.com/grafana/regexp"
)

// core is the internal, word-boundary-agnostic matching primitive each
// strategy implements. matcherImpl (the public Matcher) wraps a core
// with uniform word-boundary retry logic (see word.go) so that -w works
// identically regardless of which strategy compiled.
type core interface {
	// scanCandidate is used by the public FindCandidate: a whole-buffer
	// scan starting at start. kind reports whether the hit at [s,e) is
	// already a real match (Confirmed) or merely a literal prefilter hit
	// that Verify/Find must confirm on the enclosing line (Candidate).
	scanCandidate(buf []byte, start int) (s, e int, kind CandidateKind, ok bool)
	// scanConfirmed is used by the public Verify/Find: always runs the
	// real pattern (never just a prefilter) over line, starting at
	// start, and returns genuine match bounds.
	scanConfirmed(line []byte, start int) (s, e int, ok bool)
}

// matcherImpl is the concrete Matcher shared by all three strategies. It
// is safe for concurrent use: core implementations hold only read-only
// compiled state (compiled regexes, literal tables), and no per-call
// scratch is stored on the struct.
type matcherImpl struct {
	core                core
	word                bool
	nonMatchingLineTerm bool
}

func (m *matcherImpl) NonMatchingLineTerm() bool { return m.nonMatchingLineTerm }

func (m *matcherImpl) FindCandidate(buf []byte, start int) (int, CandidateKind, bool) {
	for start <= len(buf) {
		s, e, kind, ok := m.core.scanCandidate(buf, start)
		if !ok {
			return 0, 0, false
		}
		if kind == Confirmed && m.word && !acceptWordBoundary(buf, s, e) {
			start = s + 1
			continue
		}
		return s, kind, true
	}
	return 0, 0, false
}

func (m *matcherImpl) Verify(line []byte) bool {
	start := 0
	for start <= len(line) {
		s, e, ok := m.core.scanConfirmed(line, start)
		if !ok {
			return false
		}
		if m.word && !acceptWordBoundary(line, s, e) {
			start = s + 1
			continue
		}
		return true
	}
	return false
}

func (m *matcherImpl) Find(line []byte) (int, int, bool) {
	return m.FindAt(line, 0)
}

// FindAt returns the leftmost match beginning at or after byte offset
// start within line, with every assertion (^, $, \b, -w's half-word-
// boundary check, ...) evaluated relative to the whole line, not a
// subslice. This matters for callers that need repeated same-line
// lookups (e.g. a printer coloring every occurrence on one line): doing
// that by looping Find over successive subslices (line[pos:]) shifts
// what "start of line" or "word boundary" means for anchored/-w
// patterns, since Find has no way to know bytes before its argument
// existed. FindAt is additive -- it is not part of the frozen Matcher
// interface -- so callers that need it should type-assert for
// interface{ FindAt([]byte, int) (int, int, bool) }.
func (m *matcherImpl) FindAt(line []byte, start int) (int, int, bool) {
	for start <= len(line) {
		s, e, ok := m.core.scanConfirmed(line, start)
		if !ok {
			return 0, 0, false
		}
		if m.word && !acceptWordBoundary(line, s, e) {
			start = s + 1
			continue
		}
		return s, e, true
	}
	return 0, 0, false
}

// --- Strategy 1: pure literal(s), no engine at all ---------------------

type literalCore struct {
	scan literalScanner
}

func (c *literalCore) scanCandidate(buf []byte, start int) (int, int, CandidateKind, bool) {
	s, n, ok := c.scan.find(buf, start)
	if !ok {
		return 0, 0, Confirmed, false
	}
	return s, s + n, Confirmed, true
}

func (c *literalCore) scanConfirmed(line []byte, start int) (int, int, bool) {
	s, n, ok := c.scan.find(line, start)
	if !ok {
		return 0, 0, false
	}
	return s, s + n, true
}

// --- Strategy 2: literal-prefiltered regex -----------------------------

type prefilterCore struct {
	scan literalScanner
	eng  *engine
}

func (c *prefilterCore) scanCandidate(buf []byte, start int) (int, int, CandidateKind, bool) {
	s, n, ok := c.scan.find(buf, start)
	if !ok {
		return 0, 0, Candidate, false
	}
	return s, s + n, Candidate, true
}

func (c *prefilterCore) scanConfirmed(line []byte, start int) (int, int, bool) {
	return c.eng.find(line, start)
}

// --- Strategy 3: engine everywhere --------------------------------------

type engineCore struct {
	eng *engine
}

func (c *engineCore) scanCandidate(buf []byte, start int) (int, int, CandidateKind, bool) {
	s, e, ok := c.eng.find(buf, start)
	if !ok {
		return 0, 0, Confirmed, false
	}
	return s, e, Confirmed, true
}

func (c *engineCore) scanConfirmed(line []byte, start int) (int, int, bool) {
	return c.eng.find(line, start)
}

// --- compilation pipeline ------------------------------------------------

func runesToUTF8(runes []rune) []byte {
	buf := make([]byte, 0, len(runes)*2)
	for _, r := range runes {
		var b [utf8.UTFMax]byte
		n := utf8.EncodeRune(b[:], r)
		buf = append(buf, b[:n]...)
	}
	return buf
}

// resolveCaseInsensitive applies smart-case resolution (Config.CaseMode)
// over the (already combined) pattern set. Smart case mirrors rg's
// AstAnalysis: case-insensitive iff the patterns contain a literal
// character at all and none of those literal characters is uppercase.
func resolveCaseInsensitive(cfg Config) bool {
	switch cfg.CaseMode {
	case CaseInsensitive:
		return true
	case CaseSensitive:
		return false
	default: // CaseSmart
		anyLiteral, anyUpper := false, false
		for _, p := range cfg.Patterns {
			var l, u bool
			if cfg.Fixed {
				l, u = analyzeFixedStringCase(p)
			} else {
				l, u = analyzePatternCase(p)
			}
			anyLiteral = anyLiteral || l
			anyUpper = anyUpper || u
		}
		return anyLiteral && !anyUpper
	}
}

func analyzeFixedStringCase(s string) (anyLiteral, anyUppercase bool) {
	for _, r := range s {
		markLiteral(r, &anyLiteral, &anyUppercase)
	}
	return anyLiteral, anyUppercase
}

// extractPureLiteralAlternation reports whether re is exactly a literal,
// an alternation of literals, or the empty match -- i.e. a pattern the
// regex engine would never need to run on at all (rg's
// is_alternation_literal / bare fixed-strings fast path). foldMixed is
// true when some but not all branches are case-folded, a rare
// combination (e.g. `foo|(?i)BAR`) this function declines to handle so
// the caller falls through to the general extractor/engine path instead.
func extractPureLiteralAlternation(re *syntax.Regexp) (lits [][]byte, fold, foldMixed, ok bool) {
	switch re.Op {
	case syntax.OpEmptyMatch:
		return [][]byte{{}}, false, false, true
	case syntax.OpLiteral:
		return [][]byte{runesToUTF8(re.Rune)}, re.Flags&syntax.FoldCase != 0, false, true
	case syntax.OpAlternate:
		lits = make([][]byte, 0, len(re.Sub))
		foldCount := 0
		for _, sub := range re.Sub {
			if sub.Op != syntax.OpLiteral && sub.Op != syntax.OpEmptyMatch {
				return nil, false, false, false
			}
			if sub.Op == syntax.OpEmptyMatch {
				lits = append(lits, []byte{})
				continue
			}
			lits = append(lits, runesToUTF8(sub.Rune))
			if sub.Flags&syntax.FoldCase != 0 {
				foldCount++
			}
		}
		if foldCount != 0 && foldCount != len(re.Sub) {
			return nil, false, true, false
		}
		return lits, foldCount == len(re.Sub) && len(re.Sub) > 0, false, true
	default:
		return nil, false, false, false
	}
}

func seqToLiterals(s seq) [][]byte {
	lits := make([][]byte, len(s.lits))
	for i, l := range s.lits {
		lits[i] = l.bytes
	}
	return lits
}

func anyLiteralHasNewline(lits [][]byte) bool {
	for _, l := range lits {
		if bytes.IndexByte(l, '\n') >= 0 {
			return true
		}
	}
	return false
}

func allASCII(lits [][]byte) bool {
	for _, l := range lits {
		if !isASCII(l) {
			return false
		}
	}
	return true
}

// New compiles cfg into a Matcher.
//
// Pipeline: parse patterns (or take them as literal strings under -F) ->
// resolve smart case -> combine into one alternation -> try the
// pure-literal fast path (Strategy 1) -> else run inner-literal
// extraction for a prefiltered-regex path (Strategy 2) -> else fall back
// to running the engine over the whole buffer (Strategy 3). Word
// wrapping (-w) is never baked into the compiled pattern; it is applied
// uniformly as a post-match boundary check (see word.go) regardless of
// which strategy compiled, since Go's regexp/syntax has no equivalent of
// rg's asymmetric half-word-boundary look used for -w.
//
// LineRegexp (-x), unlike -w, IS baked directly into the compiled pattern
// text as a real ^(?:...)$ anchor pair, using Go's (?m) multi-line mode so
// ^/$ bind to LINE boundaries within a whole multi-line search buffer
// (rather than (?)-default text boundaries, which would only ever match
// at the very start/end of an entire file) -- see newRegexMatcher's doc.
// Wrapping the pattern text itself, rather than post-filtering match
// bounds the way -w does, is deliberate: it lets the ordinary regex
// engine (or FindAllIndex's anchor-aware scan, already used for any
// anchored pattern) enforce correctness even for patterns whose match
// depends on trying multiple alternatives per start position (e.g.
// `-x -e 'a|aa'` against "aa"), which a simple "restart at s+1 if the
// bounds don't span the whole line" retry loop -- as word.go uses for -w
// -- cannot get right in general.
func New(cfg Config) (Matcher, error) {
	if len(cfg.Patterns) == 0 {
		return nil, errNoPatterns
	}
	caseInsensitive := resolveCaseInsensitive(cfg)

	if cfg.Fixed {
		return newFixedMatcher(cfg, caseInsensitive)
	}
	return newRegexMatcher(cfg, caseInsensitive)
}

func newFixedMatcher(cfg Config, caseInsensitive bool) (Matcher, error) {
	lits := make([][]byte, len(cfg.Patterns))
	for i, p := range cfg.Patterns {
		lits[i] = []byte(p)
	}
	nonMatchingLineTerm := !anyLiteralHasNewline(lits)

	// Under -x, the plain literal-substring fast path is unsound (a
	// literal occurring ANYWHERE in a line is not the same as the line
	// EQUALING the literal -- PLAN.md's round-31 landmine), so -x always
	// routes -F through the engine below with real ^(?:...)$ anchors,
	// same as the Unicode-fold fallback already does. This forgoes the
	// literal fast path under -x; acceptable per this round's brief
	// ("perf is not the goal here; correctness is").
	if !cfg.LineRegexp && (!caseInsensitive || allASCII(lits)) {
		scan := newLiteralScanner(lits, caseInsensitive)
		return &matcherImpl{
			core:                &literalCore{scan: scan},
			word:                cfg.Word,
			nonMatchingLineTerm: nonMatchingLineTerm,
		}, nil
	}

	// A -F pattern needs Unicode-aware case folding beyond what the
	// ASCII anchor scan can provide (or -x forces the engine path
	// regardless): fall back to the engine, escaping each literal so
	// regex metacharacters in it are treated literally.
	quoted := make([]string, len(cfg.Patterns))
	for i, p := range cfg.Patterns {
		quoted[i] = regexp.QuoteMeta(p)
	}
	inner := strings.Join(quoted, "|")
	if caseInsensitive {
		inner = "(?i:" + inner + ")"
	}
	pattern := inner
	hasAnchors := false
	if cfg.LineRegexp {
		pattern = "(?m)^(?:" + inner + ")$"
		hasAnchors = true
	}
	eng, err := compileEngine(pattern, hasAnchors)
	if err != nil {
		return nil, err
	}
	return &matcherImpl{
		core:                &engineCore{eng: eng},
		word:                cfg.Word,
		nonMatchingLineTerm: nonMatchingLineTerm,
	}, nil
}

func newRegexMatcher(cfg Config, caseInsensitive bool) (Matcher, error) {
	parts := make([]string, len(cfg.Patterns))
	for i, p := range cfg.Patterns {
		parts[i] = "(?:" + p + ")"
	}
	combined := strings.Join(parts, "|")
	if cfg.LineRegexp {
		// (?m) makes ^/$ bind to LINE boundaries within the whole
		// multi-line search buffer, not just start/end of the entire
		// text (Go's default) -- see New's doc. Wrapping combined itself,
		// before syntax.Parse, means every downstream step (the AST used
		// for Strategy 1/2 selection, nonMatchingLineTerm, hasAnchors,
		// and enginePattern below) sees the anchors consistently: in
		// particular, extractPureLiteralAlternation's root-Op check
		// naturally rejects Strategy 1 for an anchored pattern (its root
		// becomes OpConcat, not OpLiteral/OpAlternate), so the
		// Confirmed-without-verification literal shortcut is never taken
		// under -x -- exactly PLAN.md's round-31 landmine requirement,
		// achieved by construction rather than a special case.
		combined = "(?m)^(?:" + combined + ")$"
	} else if cfg.MultiLine {
		// --null-data: a record may span '\n', so `^`/`$` must anchor at
		// interior '\n' boundaries within the record window handed to the
		// matcher (Go's (?m)), while `.` still does not match '\n'. This is
		// a no-op for anchor-free patterns (a bare literal parses to the
		// same OpLiteral with or without the flag, keeping Strategy 1) and
		// is subsumed by the LineRegexp branch above (already (?m)). Verified
		// against the real rg binary: `--null-data 'foo$'` anchors before an
		// interior '\n' inside a record, `--null-data 'foo.bar'` does not
		// cross one.
		combined = "(?m)" + combined
	}

	baseFlags := syntax.Perl
	if caseInsensitive {
		baseFlags |= syntax.FoldCase
	}
	re, err := syntax.Parse(combined, baseFlags)
	if err != nil {
		return nil, err
	}
	nonMatchingLineTerm := !canMatchNewline(re)
	hasAnchors := containsAnchorOrBoundary(re)

	enginePattern := combined
	if caseInsensitive {
		enginePattern = "(?i:" + combined + ")"
	}

	// Strategy 1: the whole pattern is a literal or an alternation of
	// literals -- no engine needed at all.
	if lits, fold, foldMixed, ok := extractPureLiteralAlternation(re); ok && !foldMixed {
		if !fold || allASCII(lits) {
			scan := newLiteralScanner(lits, fold)
			return &matcherImpl{
				core:                &literalCore{scan: scan},
				word:                cfg.Word,
				nonMatchingLineTerm: nonMatchingLineTerm,
			}, nil
		}
		// Non-ASCII literals under case folding: the anchor-based ASCII
		// scan doesn't apply; fall through to the engine below rather
		// than to literal extraction (the literals are already known,
		// extraction would just rediscover them).
		eng, err := compileEngine(enginePattern, hasAnchors)
		if err != nil {
			return nil, err
		}
		return &matcherImpl{
			core:                &engineCore{eng: eng},
			word:                cfg.Word,
			nonMatchingLineTerm: nonMatchingLineTerm,
		}, nil
	}

	// Strategy 2: extract an inner-literal prefilter and confirm with
	// the engine on candidate lines.
	litSeq := extractInnerLiterals(re)
	if litSeq.isFinite() {
		if n, _ := litSeq.length(); n > 0 {
			lits := seqToLiterals(litSeq)
			eng, err := compileEngine(enginePattern, hasAnchors)
			if err != nil {
				return nil, err
			}
			scan := newLiteralScanner(lits, false)
			return &matcherImpl{
				core:                &prefilterCore{scan: scan, eng: eng},
				word:                cfg.Word,
				nonMatchingLineTerm: nonMatchingLineTerm,
			}, nil
		}
	}

	// Strategy 3: no usable literal prefilter; run the engine everywhere.
	eng, err := compileEngine(enginePattern, hasAnchors)
	if err != nil {
		return nil, err
	}
	return &matcherImpl{
		core:                &engineCore{eng: eng},
		word:                cfg.Word,
		nonMatchingLineTerm: nonMatchingLineTerm,
	}, nil
}
