package match

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// oracleFind computes the "ground truth" leftmost match bounds for cfg
// against line using stdlib regexp (a genuinely independent engine from
// both the Matcher under test and its grafana/regexp fallback), applying
// the same smart-case resolution and, for -w, the same half-word-
// boundary post-filter that word.go implements (unit-tested standalone
// in word_test.go; there is no independent oracle for -w's specific
// semantics since it's an rg-specific, non-\b definition -- see word.go
// for why `\b...\b` is not equivalent and can't serve as the oracle).
func oracleFind(t testing.TB, cfg Config, line []byte) (s, e int, ok bool) {
	t.Helper()
	caseInsensitive := resolveCaseInsensitive(cfg)

	parts := make([]string, len(cfg.Patterns))
	for i, p := range cfg.Patterns {
		if cfg.Fixed {
			parts[i] = regexp.QuoteMeta(p)
		} else {
			parts[i] = "(?:" + p + ")"
		}
	}
	pattern := strings.Join(parts, "|")
	if caseInsensitive {
		pattern = "(?i:" + pattern + ")"
	}
	if cfg.LineRegexp {
		// -x: whole-line match, via the same (?m)^(?:...)$ technique
		// strategy.go uses -- but compiled independently here with
		// stdlib regexp rather than grafana/regexp, so this remains a
		// genuine second implementation (it exercises the Matcher's
		// FindCandidate/Verify/engine-anchor wiring, not a tautology of
		// strategy.go's own compiled string). Whether (?m)^...$ is the
		// right SEMANTIC choice for -x is verified separately against
		// the real rg binary (see flags_test.go/e2e_test.go).
		pattern = "(?m)^(?:" + pattern + ")$"
		re, err := regexp.Compile(pattern)
		if err != nil {
			t.Fatalf("oracle compile %q: %v", pattern, err)
		}
		loc := re.FindIndex(line)
		if loc == nil {
			return 0, 0, false
		}
		return loc[0], loc[1], true
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("oracle compile %q: %v", pattern, err)
	}

	start := 0
	for start <= len(line) {
		loc := re.FindIndex(line[start:])
		if loc == nil {
			return 0, 0, false
		}
		s, e := start+loc[0], start+loc[1]
		if cfg.Word && !acceptWordBoundary(line, s, e) {
			start = s + 1
			continue
		}
		return s, e, true
	}
	return 0, 0, false
}

// checkLine compiles cfg (failing the test on a compile error) and
// asserts its Find/Verify agree with the oracle on line.
func checkLine(t *testing.T, cfg Config, line string) {
	t.Helper()
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("New(%+v): %v", cfg, err)
	}
	buf := []byte(line)
	wantS, wantE, wantOK := oracleFind(t, cfg, buf)
	gotS, gotE, gotOK := m.Find(buf)
	if gotOK != wantOK || (wantOK && (gotS != wantS || gotE != wantE)) {
		t.Errorf("Find(%q) with patterns=%v fixed=%v case=%v word=%v = (%d,%d,%v), want (%d,%d,%v)",
			line, cfg.Patterns, cfg.Fixed, cfg.CaseMode, cfg.Word, gotS, gotE, gotOK, wantS, wantE, wantOK)
	}
	gotVerify := m.Verify(buf)
	if gotVerify != wantOK {
		t.Errorf("Verify(%q) with patterns=%v = %v, want %v", line, cfg.Patterns, gotVerify, wantOK)
	}
}

// checkMatrix runs checkLine for every pattern x haystack combination.
func checkMatrix(t *testing.T, cfgs []Config, haystacks []string) {
	t.Helper()
	for _, cfg := range cfgs {
		for _, h := range haystacks {
			checkLine(t, cfg, h)
		}
	}
}

func cs(patterns ...string) Config { return Config{Patterns: patterns, CaseMode: CaseSensitive} }
func ci(patterns ...string) Config { return Config{Patterns: patterns, CaseMode: CaseInsensitive} }
func smart(patterns ...string) Config {
	return Config{Patterns: patterns, CaseMode: CaseSmart}
}
func lineRegexp(cfg Config) Config {
	cfg.LineRegexp = true
	return cfg
}
func word(cfg Config) Config {
	cfg.Word = true
	return cfg
}
func fixed(cfg Config) Config {
	cfg.Fixed = true
	return cfg
}

var haystacks = []string{
	"",
	"hello world",
	"the quick brown fox jumps over the lazy dog",
	"PM_RESUME called from pm_suspend at 0x1234",
	"Sherlock Holmes and Doctor Watson",
	"SHERLOCK holmes",
	"foo-bar baz_qux",
	"aaaaaaaaaaaaaaaaaaaa",
	"1234567890",
	"привет мир Шерлок Холмс",
	"ΔΕΛΤΑ δελτα",
	"snowman ☃ snowman",
	"no match here at all zzz",
	"   leading and trailing spaces   ",
	"CamelCaseIdentifier_with_underscore123",
	"a.b.c.d.e.f",
	"multiple   spaces   between   words",
}

func TestMatcherLiterals(t *testing.T) {
	cfgs := []Config{
		cs("foo"),
		cs("PM_RESUME"),
		cs("Sherlock Holmes"),
		cs("quick", "lazy"),
		cs("a", "b", "c", "d", "e", "f", "g", "h", "i"), // >8 literals, forces Aho-Corasick
		ci("PM_RESUME"),
		ci("sherlock holmes"),
		ci("SHERLOCK"),
		smart("foo"),
		smart("Foo"),
		fixed(cs("a.b.c.d.e.f")),
		fixed(cs("foo-bar")),
		fixed(ci("SHERLOCK HOLMES")),
	}
	checkMatrix(t, cfgs, haystacks)
}

func TestMatcherCaseInsensitiveUnicode(t *testing.T) {
	cfgs := []Config{
		ci("δελτα"),
		ci("ΔΕΛΤΑ"),
		ci("шерлок"),
		ci("☃"),
		fixed(ci("δελτα")),
	}
	checkMatrix(t, cfgs, haystacks)
}

func TestMatcherWord(t *testing.T) {
	cfgs := []Config{
		word(cs("foo")),
		word(cs("bar")),
		word(cs("-bar")),
		word(ci("sherlock")),
		word(cs("quick", "lazy")),
		word(fixed(cs("foo"))),
	}
	checkMatrix(t, cfgs, haystacks)
	// The specific x-foo half-boundary case from word.go's doc comment.
	checkLine(t, word(cs("-foo")), "x-foo")
	checkLine(t, word(cs("-foo")), "x -foo")
}

// TestMatcherLineRegexp covers -x/--line-regexp: patterns are anchored to
// whole-line boundaries (rg's `^(?:...)$`, per-line not per-text -- see
// strategy.go's New doc). checkMatrix's haystacks give broad negative
// coverage (an unanchored literal substring occurring mid-line must NOT
// match under -x); exact-line and alternation cases below are added
// separately since none of the shared haystacks equal a tested pattern
// outright.
func TestMatcherLineRegexp(t *testing.T) {
	cfgs := []Config{
		lineRegexp(cs("foo")),
		lineRegexp(ci("sherlock")),
		lineRegexp(cs(`[A-Z]+_RESUME`)),
		lineRegexp(fixed(cs("foo"))),
		lineRegexp(fixed(ci("δελτα"))),
		lineRegexp(cs(`\w+`)),
		lineRegexp(cs(`.*`)),
	}
	checkMatrix(t, cfgs, haystacks)

	// Exact-line positives/negatives not covered by the shared haystacks.
	checkLine(t, lineRegexp(cs("hello world")), "hello world")
	checkLine(t, lineRegexp(cs("hello")), "hello world")
	checkLine(t, lineRegexp(fixed(cs("a.b"))), "a.b")
	checkLine(t, lineRegexp(fixed(cs("a.b"))), "azb")

	// The alternation landmine: a literal-substring-Confirmed shortcut
	// (or a naive "restart the match at s+1 on boundary failure" retry,
	// as word.go uses for -w) gets `a|aa` against "aa" wrong; a real
	// ^(?:a|aa)$ engine scan does not. Verified independently against
	// the real rg binary too (see the -f/-x differential sweep).
	checkLine(t, lineRegexp(cs("a", "aa")), "aa")
	checkLine(t, lineRegexp(cs("a", "aa")), "a")
	checkLine(t, lineRegexp(cs("a", "aa")), "aaa")
}

func TestMatcherAlternation(t *testing.T) {
	cfgs := []Config{
		cs(`ERR_SYS|PME_TURN_OFF|LINK_REQ_RST|CFG_BME_EVT`),
		cs(`Sherlock|Holmes|Moriarty`),
		cs(`foo|bar|baz`),
		ci(`sherlock|holmes`),
	}
	checkMatrix(t, cfgs, haystacks)
}

func TestMatcherRegexWithLiteral(t *testing.T) {
	cfgs := []Config{
		cs(`[A-Z]+_RESUME`),
		cs(`\w+\s+Holmes\s+\w+`),
		cs(`\s+(Sherlock|[A-Z]atso[a-z]|Moriarty)\s+`),
		cs(`foo[0-9]+bar`),
		cs(`a[bc]+d`),
	}
	checkMatrix(t, cfgs, haystacks)
}

func TestMatcherNoLiteralRegex(t *testing.T) {
	cfgs := []Config{
		cs(`\w{5}\s+\w{5}`),
		cs(`.{3,5}`),
		cs(`[a-z]+`),
		cs(`\p{Greek}+`),
	}
	checkMatrix(t, cfgs, haystacks)
}

func TestMatcherAnchors(t *testing.T) {
	cfgs := []Config{
		cs(`^the`),
		cs(`dog$`),
		cs(`^$`),
	}
	checkMatrix(t, cfgs, haystacks)
}

func TestMatcherClasses(t *testing.T) {
	cfgs := []Config{
		cs(`[aeiou]+`),
		cs(`[^aeiou\s]+`),
		cs(`\d+`),
	}
	checkMatrix(t, cfgs, haystacks)
}

// TestMatcherFindCandidateWholeBuffer exercises the FindCandidate ->
// locate-line -> Verify pipeline directly (rather than just Find/Verify
// on a single line), across a multi-line buffer, for every strategy.
func TestMatcherFindCandidateWholeBuffer(t *testing.T) {
	buf := []byte(strings.Join(haystacks, "\n") + "\n")
	cfgs := []Config{
		cs("foo"),
		ci("PM_RESUME"),
		cs("quick", "lazy", "fox"),
		cs(`[A-Z]+_RESUME`),
		cs(`\w{5}\s+\w{5}`),
		word(cs("-foo")),
	}
	for _, cfg := range cfgs {
		m, err := New(cfg)
		if err != nil {
			t.Fatalf("New(%+v): %v", cfg, err)
		}
		lineStarts := lineBoundaries(buf)
		gotLines := candidateLinesConfirmed(t, m, buf, lineStarts)
		wantLines := map[int]bool{}
		for i, ls := range lineStarts {
			line := buf[ls.start:ls.end]
			if _, _, ok := oracleFind(t, cfg, line); ok {
				wantLines[i] = true
			}
		}
		for i := range lineStarts {
			if gotLines[i] != wantLines[i] {
				t.Errorf("cfg=%+v line %d (%q): FindCandidate pipeline matched=%v, oracle matched=%v",
					cfg, i, buf[lineStarts[i].start:lineStarts[i].end], gotLines[i], wantLines[i])
			}
		}
	}
}

type lineSpan struct{ start, end int }

func lineBoundaries(buf []byte) []lineSpan {
	var spans []lineSpan
	start := 0
	for start <= len(buf) {
		nl := bytes.IndexByte(buf[start:], '\n')
		if nl < 0 {
			if start < len(buf) {
				spans = append(spans, lineSpan{start, len(buf)})
			}
			break
		}
		spans = append(spans, lineSpan{start, start + nl})
		start += nl + 1
	}
	return spans
}

// candidateLinesConfirmed mirrors how search.Searcher is expected to use
// a Matcher: the whole-buffer FindCandidate fast path (rg's
// find_by_line_fast) is only sound when NonMatchingLineTerm() is true
// (the pattern is provably unable to match across a '\n'); otherwise the
// documented contract requires falling back to a per-line slow path
// (match_by_line_slow), calling Verify on each line independently.
func candidateLinesConfirmed(t *testing.T, m Matcher, buf []byte, spans []lineSpan) map[int]bool {
	t.Helper()
	result := map[int]bool{}
	if !m.NonMatchingLineTerm() {
		for i, sp := range spans {
			if m.Verify(buf[sp.start:sp.end]) {
				result[i] = true
			}
		}
		return result
	}
	pos := 0
	for {
		off, kind, ok := m.FindCandidate(buf, pos)
		if !ok {
			break
		}
		li := lineIndexFor(spans, off)
		if li < 0 {
			break
		}
		line := buf[spans[li].start:spans[li].end]
		if kind == Confirmed || m.Verify(line) {
			result[li] = true
		}
		pos = spans[li].end + 1
		if pos <= off {
			pos = off + 1
		}
	}
	return result
}

func lineIndexFor(spans []lineSpan, off int) int {
	for i, sp := range spans {
		if off >= sp.start && off <= sp.end {
			return i
		}
	}
	return -1
}
