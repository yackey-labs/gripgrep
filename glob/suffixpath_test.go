package glob

import (
	"regexp"
	"strings"
	"testing"
)

// TestSuffixPathOfTokens covers suffixPathOfTokens directly:
// the `**/`-prefixed multi-segment literal shape (a literal tail
// containing at least one '/'), and every reason a token sequence is
// rejected -- no `**/` prefix, a single-segment tail (basenameLiteralOf's
// job, claimed earlier in classifyFast), or any non-literal token.
func TestSuffixPathOfTokens(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
		ok      bool
	}{
		{"**/.claude/settings.local.json", ".claude/settings.local.json", true},
		{"**/a/b", "a/b", true},
		{"**/a/b/c", "a/b/c", true},
		{"**/π/λ", "π/λ", true},
		// Single-segment tail: no interior '/', so this is basenameLiteralOf's
		// shape -- suffixPathOfTokens declines it (the '/' requirement pins
		// the class boundary; see its doc).
		{"**/foo", "", false},
		// No recursive prefix: parseGlob alone (unlike compileLine, which
		// prepends "**/" only to a no-slash pattern) never adds one, so a
		// rooted or interior-slash literal is genuinely unprefixed here and
		// is literalOf's / pathBetweenOfTokens' job.
		{"a/b", "", false},
		{"/a/b", "", false},
		// A wildcard anywhere in the tail disqualifies it (not a pure
		// literal): those are pathBetween/chain/etc. shapes.
		{"**/a/*.b", "", false},
		{"**/a/b*", "", false},
		// Bare `**` (no tail at all).
		{"**", "", false},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			toks, err := parseGlob(c.pattern)
			if err != nil {
				t.Fatalf("parseGlob(%q): %v", c.pattern, err)
			}
			got, ok := suffixPathOfTokens(toks)
			if ok != c.ok || got != c.want {
				t.Errorf("suffixPathOfTokens(%q) = (%q, %v), want (%q, %v)", c.pattern, got, ok, c.want, c.ok)
			}
		})
	}
}

// oldSuffixPathMatch computes what a `**/S` pattern did BEFORE this
// fast class existed: the earlier fallback compiled it to the regex
// tokensToRegex emits, tested against the whole path, gated by the
// directory-only flag a trailing '/' sets. This is the oracle the new fast
// class must reproduce byte for byte. It deliberately rebuilds the regex
// from scratch (not via compileLine, which now routes these to the fast
// class) so the comparison is against the genuine old behavior.
func oldSuffixPathMatch(t testing.TB, pattern, path string, isDir bool) bool {
	t.Helper()
	line := pattern
	isOnlyDir := false
	if strings.HasSuffix(line, "/") {
		isOnlyDir = true
		line = line[:len(line)-1]
	}
	toks, err := parseGlob(line)
	if err != nil {
		t.Fatalf("parseGlob(%q): %v", line, err)
	}
	re, err := regexp.Compile(tokensToRegex(toks))
	if err != nil {
		t.Fatalf("compile regex for %q: %v", line, err)
	}
	if isOnlyDir && !isDir {
		return false
	}
	return re.Match([]byte(path))
}

// newSuffixPathMatch builds a Set from the single pattern and reports
// whether Match ignores path. It also asserts the pattern actually landed
// in the suffixPaths fast class (and nowhere else), so a future refactor
// that silently rerouted the shape back to regex would fail loudly here
// rather than making the differential vacuous.
func newSuffixPathMatch(t testing.TB, pattern, path string, isDir bool) bool {
	t.Helper()
	var b Builder
	b.Add(pattern)
	s, err := b.Build()
	if err != nil {
		t.Fatalf("Build(%q): %v", pattern, err)
	}
	if len(s.suffixPaths) != 1 {
		t.Fatalf("pattern %q produced %d suffixPath entries, want 1 (did it fall to another class?)", pattern, len(s.suffixPaths))
	}
	if n := len(s.regexes) + len(s.literalMap) + len(s.basenameMap) + len(s.extMap) +
		len(s.suffixes) + len(s.prefixes) + len(s.contains) + len(s.betweens) +
		len(s.pathBetweens) + len(s.chains); n != 0 {
		t.Fatalf("pattern %q also populated %d non-suffixPath entries, want 0", pattern, n)
	}
	return s.Match([]byte(path), isDir) == Ignored
}

// suffixPathPatterns and suffixPathPaths are the corpus for the
// differential oracle. The paths deliberately include the inputs that
// distinguish the regex-faithful semantics from the naive "ends with /S":
// a component containing '\n' (must NOT match -- RE2's `.` can't cross it),
// an invalid-UTF-8 byte before the tail (decodes to U+FFFD, which `.`
// matches, so it MUST match), and an empty prefix ("/"+S, must match).
var (
	suffixPathPatterns = []string{
		"**/a/b",
		"**/a/b/c",
		"**/.claude/settings.local.json",
		"**/a/b/",   // directory-only
		"**/a/b/c/", // directory-only, 3-segment
		"**/π/λ",    // unicode
		"**/foo/bar.txt",
		"**/dir/.hidden",
	}
	suffixPathPaths = []string{
		// exact-equality (p == S) hits and near-misses
		"a/b", "a/b/c", ".claude/settings.local.json", "π/λ", "foo/bar.txt", "dir/.hidden",
		"a", "b", "ab", "a/bc", "za/b", "a/bz", "",
		// ends-with "/"+S hits at varying depth
		"x/a/b", "x/y/a/b", "x/y/z/a/b", "x/a/b/c", "sub/foo/bar.txt", "z/π/λ", "w/dir/.hidden",
		"/a/b", "/a/b/c", // empty-prefix (leading slash) branch
		// depth / boundary near-misses
		"a/b/c/d", "xa/b", "a/b/cx", "notdir/a/b/c/extra",
		// discriminating: newline in the prefix (must NOT match)
		"a\nb/a/b", "x/\n/a/b", "p\nq/a/b/c",
		// discriminating: invalid UTF-8 byte in the prefix (must match)
		"\xff/a/b", "\xff\xfe/a/b/c", "x/\xc3/foo/bar.txt",
		// newline INSIDE the matched tail region is impossible (S is a fixed
		// literal), but a newline right at the separator boundary is worth
		// exercising both ways
		"\n/a/b",
	}
)

// TestSuffixPathMatchesRegexOracle is the differential gate: for
// every (pattern, path, isDir) triple, the new fast class's verdict must
// equal the earlier regex's, byte for byte.
func TestSuffixPathMatchesRegexOracle(t *testing.T) {
	for _, pat := range suffixPathPatterns {
		for _, p := range suffixPathPaths {
			for _, isDir := range []bool{false, true} {
				old := oldSuffixPathMatch(t, pat, p, isDir)
				got := newSuffixPathMatch(t, pat, p, isDir)
				if old != got {
					t.Errorf("pattern=%q path=%q isDir=%v: fast=%v regex=%v", pat, p, isDir, got, old)
				}
			}
		}
	}
}

// TestAddCISuffixPathBypass is the case-insensitive guard for the new
// class: a CI-added `**/S` multi-segment pattern must NOT
// populate the suffixPaths fast class (its byte-comparison predicate has
// no case folding) and must instead match case-insensitively via the
// regex fallback. This is a dedicated test rather than an edit to
// TestAddCIBypassesFastClasses, whose slice-sum counter predates this
// class and wouldn't include s.suffixPaths.
func TestAddCISuffixPathBypass(t *testing.T) {
	var b Builder
	b.AddCI("**/a/b")
	s, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(s.suffixPaths) != 0 {
		t.Errorf("AddCI(%q) populated suffixPaths (%d entries), want 0", "**/a/b", len(s.suffixPaths))
	}
	if len(s.regexes) != 1 {
		t.Fatalf("AddCI(%q) produced %d regex entries, want 1", "**/a/b", len(s.regexes))
	}
	for _, path := range []string{"a/b", "X/A/B", "x/A/b", "sub/a/B"} {
		if got := s.Match([]byte(path), false); got != Ignored {
			t.Errorf("AddCI(%q).Match(%q) = %v, want Ignored", "**/a/b", path, got)
		}
	}
	if got := s.Match([]byte("a/c"), false); got != NoMatch {
		t.Errorf("AddCI(%q).Match(%q) = %v, want NoMatch", "**/a/b", "a/c", got)
	}
}

// FuzzSuffixPathVsRegex drives the same fast-vs-regex differential as
// TestSuffixPathMatchesRegexOracle over generator-produced (pattern, path,
// isDir) triples: build a `**/S` pattern (S = 1-4 literal segments drawn
// from a vocabulary that includes '\n' and invalid-UTF-8 bytes) and a
// path, then assert the fast class agrees with the earlier regex. Under
// plain `go test` only the seed corpus runs; run with
// `-fuzz=FuzzSuffixPathVsRegex` for open-ended exploration.
func FuzzSuffixPathVsRegex(f *testing.F) {
	for _, s := range [][]byte{
		{0, 1, 2, 3, 4, 5, 6, 7},
		{1, 1, 1, 1, 1, 1, 1, 1},
		{7, 3, 9, 2, 8, 4, 6, 1, 5},
		{255, 254, 10, 253, 10, 252, 251},
		{2, 10, 2, 10, 2, 10, 2, 10},
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c := &fuzzCursor{data: data}
		pattern, path, isDir, ok := genSuffixPathCase(c)
		if !ok {
			t.Skip("degenerate case")
		}
		// The generated pattern must be exactly the shape this class owns;
		// if genSuffixPathCase ever emits something else, newSuffixPathMatch
		// asserts and we learn, rather than silently comparing nothing.
		old := oldSuffixPathMatch(t, pattern, path, isDir)
		got := newSuffixPathMatch(t, pattern, path, isDir)
		if old != got {
			t.Fatalf("pattern=%q path=%q isDir=%v: fast=%v regex=%v", pattern, path, isDir, got, old)
		}
	})
}

// suffixPathVocab is the segment alphabet for the fuzz generator. It
// deliberately includes a newline and an invalid-UTF-8 byte so the fuzzer
// stresses the exact bytes that distinguish the regex-faithful predicate
// from a naive suffix check.
var suffixPathVocab = []string{"a", "b", "foo", ".claude", "settings.local.json", "π", "x\ny", "\xff", "z"}

// genSuffixPathCase builds a `**/S` pattern with a 2-to-4-segment literal
// tail S (segments from suffixPathVocab, so S always contains at least one
// '/'), plus a candidate path assembled from the same vocabulary with an
// optional prefix depth. It returns ok=false only for the rare case where
// the chosen segments would make S ambiguous to parse (a segment
// containing '\n' or an invalid byte is fine for matching but must not be
// used to *build the pattern*, since a real gitignore line can't contain a
// raw newline -- those bytes are reserved for the path side).
func genSuffixPathCase(c *fuzzCursor) (pattern, path string, isDir, ok bool) {
	// Build S from "clean" segments only (a gitignore line is one text line
	// -- no raw '\n'; keep invalid UTF-8 out of the pattern too).
	clean := []string{"a", "b", "foo", ".claude", "settings.local.json", "π", "z"}
	nseg := 2 + c.intn(3) // 2..4 segments -> S always has a '/'
	segs := make([]string, nseg)
	for i := range segs {
		segs[i] = clean[c.intn(len(clean))]
	}
	s := strings.Join(segs, "/")
	trailingSlash := c.bool()
	pattern = "**/" + s
	if trailingSlash {
		pattern += "/"
	}

	// Build a path: an optional prefix of arbitrary segments (from the full
	// vocab, so '\n' and invalid bytes appear here), then usually the tail S
	// (so matches actually happen), occasionally a perturbed tail.
	var sb strings.Builder
	depth := c.intn(4) // 0..3 prefix segments
	if depth > 0 && c.bool() {
		sb.WriteByte('/') // sometimes a leading slash (empty-prefix branch)
	}
	for i := 0; i < depth; i++ {
		if i > 0 {
			sb.WriteByte('/')
		}
		sb.WriteString(suffixPathVocab[c.intn(len(suffixPathVocab))])
	}
	if depth > 0 {
		sb.WriteByte('/')
	}
	switch c.intn(4) {
	case 0:
		// perturb the tail so it usually doesn't match
		sb.WriteString(s)
		sb.WriteString(suffixPathVocab[c.intn(len(suffixPathVocab))])
	default:
		sb.WriteString(s)
	}
	path = sb.String()
	isDir = c.bool()
	return pattern, path, isDir, true
}

// The two benchmarks below measure the new fast class against the exact
// regex it replaces, on the case that dominates a real walk: a moderately
// deep path that does NOT match the `**/S` pattern (the miss is what runs
// ~104k times per walk, once per tree entry). BenchmarkSuffixPathFast
// exercises the whole Set.Match dispatch for one such pattern;
// BenchmarkSuffixPathRegex is the earlier cost -- the raw grafana/regexp
// Match this class exists to avoid.
var benchSuffixPathPattern = "**/.claude/settings.local.json"
var benchSuffixPathMissPath = []byte("arch/x86/kernel/cpu/microcode/intel.c")
var benchSuffixPathHitPath = []byte("tools/testing/.claude/settings.local.json")

func benchSuffixPathSet(b *testing.B) *Set {
	b.Helper()
	var bld Builder
	bld.Add(benchSuffixPathPattern)
	s, err := bld.Build()
	if err != nil {
		b.Fatal(err)
	}
	if len(s.suffixPaths) != 1 {
		b.Fatalf("pattern did not land in suffixPaths (%d entries)", len(s.suffixPaths))
	}
	return s
}

func BenchmarkSuffixPathFast(b *testing.B) {
	s := benchSuffixPathSet(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Match(benchSuffixPathMissPath, false)
	}
}

func BenchmarkSuffixPathFastHit(b *testing.B) {
	s := benchSuffixPathSet(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Match(benchSuffixPathHitPath, false)
	}
}

func BenchmarkSuffixPathRegex(b *testing.B) {
	toks, err := parseGlob(benchSuffixPathPattern)
	if err != nil {
		b.Fatal(err)
	}
	re, err := regexp.Compile(tokensToRegex(toks))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = re.Match(benchSuffixPathMissPath)
	}
}
