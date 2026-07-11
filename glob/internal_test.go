package glob

import (
	"reflect"
	"regexp"
	"testing"
)

// These tests exercise the parser and regex translator directly (parseGlob
// + tokensToRegex), bypassing compileLine's gitignore line-rewriting
// (leading '!', anchoring, trailing '/', the "**/" prefix injection).
// They are a port of the `matches!`/`nmatches!` table in
// ../ripgrep/crates/globset/src/glob.rs.
//
// That upstream suite runs by default with literal_separator=false (a
// bare `*`/`?` crosses `/`) unless a test opts into SLASHLIT. This
// package has no such option — literal_separator is unconditionally true,
// matching gitignore's only mode (GitignoreBuilder always calls
// `.literal_separator(true)`). So only cases whose expected outcome is
// unaffected by that setting (or that explicitly used SLASHLIT upstream)
// are ported; a few upstream `matches!` cases that depend on `*`
// crossing `/` are ported here as their literal_separator=true opposite
// (noted inline) rather than dropped silently.

func rawRegex(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	toks, err := parseGlob(pattern)
	if err != nil {
		t.Fatalf("parseGlob(%q): %v", pattern, err)
	}
	re, err := regexp.Compile(tokensToRegex(toks))
	if err != nil {
		t.Fatalf("compile regex for %q (%q): %v", pattern, tokensToRegex(toks), err)
	}
	return re
}

func TestGlobSyntaxMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
	}{
		{"match1", "a", "a"},
		{"match2", "a*b", "a_b"},
		{"match3", "a*b*c", "abc"},
		{"match4", "a*b*c", "a_b_c"},
		{"match5", "a*b*c", "a___b___c"},
		{"match6", "abc*abc*abc", "abcabcabcabcabcabcabc"},
		{"match7", "a*a*a*a*a*a*a*a*a", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"match8", "a*b[xyz]c*d", "abxcdbxcddd"},
		{"match9", "*.rs", ".rs"},
		{"match10", "☃", "☃"},

		{"matchrec1", "some/**/needle.txt", "some/needle.txt"},
		{"matchrec2", "some/**/needle.txt", "some/one/needle.txt"},
		{"matchrec3", "some/**/needle.txt", "some/one/two/needle.txt"},
		{"matchrec4", "some/**/needle.txt", "some/other/needle.txt"},
		{"matchrec5", "**", "abcde"},
		{"matchrec6", "**", ""},
		{"matchrec7", "**", ".asdf"},
		{"matchrec8", "**", "/x/.asdf"},
		{"matchrec9", "some/**/**/needle.txt", "some/needle.txt"},
		{"matchrec10", "some/**/**/needle.txt", "some/one/needle.txt"},
		{"matchrec11", "some/**/**/needle.txt", "some/one/two/needle.txt"},
		{"matchrec12", "some/**/**/needle.txt", "some/other/needle.txt"},
		{"matchrec13", "**/test", "one/two/test"},
		{"matchrec14", "**/test", "one/test"},
		{"matchrec15", "**/test", "test"},
		{"matchrec16", "/**/test", "/one/two/test"},
		{"matchrec17", "/**/test", "/one/test"},
		{"matchrec18", "/**/test", "/test"},
		{"matchrec19", "**/.*", ".abc"},
		{"matchrec20", "**/.*", "abc/.abc"},
		{"matchrec21", "**/foo/bar", "foo/bar"},
		{"matchrec22", ".*/**", ".abc/abc"},
		{"matchrec23", "test/**", "test/"},
		{"matchrec24", "test/**", "test/one"},
		{"matchrec25", "test/**", "test/one/two"},
		{"matchrec26", "some/*/needle.txt", "some/one/needle.txt"},

		{"matchrange1", "a[0-9]b", "a0b"},
		{"matchrange2", "a[0-9]b", "a9b"},
		{"matchrange3", "a[!0-9]b", "a_b"},
		{"matchrange4", "[a-z123]", "1"},
		{"matchrange5", "[1a-z23]", "1"},
		{"matchrange6", "[123a-z]", "1"},
		{"matchrange7", "[abc-]", "-"},
		{"matchrange8", "[-abc]", "-"},
		{"matchrange9", "[-a-c]", "b"},
		{"matchrange10", "[a-c-]", "b"},
		{"matchrange11", "[-]", "-"},
		{"matchrange12", "a[^0-9]b", "a_b"},

		{"matchpat1", "*hello.txt", "hello.txt"},
		{"matchpat2", "*hello.txt", "gareth_says_hello.txt"},
		{"matchpat4", "*hello.txt", `some\path\to\hello.txt`}, // no real '/' in the candidate
		{"matchpat6", "*some/path/to/hello.txt", "some/path/to/hello.txt"},

		{"matchescape", "_[[]_[]]_[?]_[*]_!_", "_[_]_?_*_!_"},

		{"matchalt1", "a,b", "a,b"},
		{"matchalt2", ",", ","},
		{"matchalt3", "{a,b}", "a"},
		{"matchalt4", "{a,b}", "b"},
		{"matchalt5", "{**/src/**,foo}", "abc/src/bar"},
		{"matchalt6", "{**/src/**,foo}", "foo"},
		{"matchalt7", "{[}],foo}", "}"},
		{"matchalt8", "{foo}", "foo"},
		{"matchalt9", "{}", ""},
		{"matchalt10", "{,}", ""},
		{"matchalt11", "{*.foo,*.bar,*.wat}", "test.foo"},
		{"matchalt12", "{*.foo,*.bar,*.wat}", "test.bar"},
		{"matchalt13", "{*.foo,*.bar,*.wat}", "test.wat"},
		{"matchalt14", "foo{,.txt}", "foo.txt"},
		{"matchalt17", "{a,b{c,d}}", "bc"},
		{"matchalt18", "{a,b{c,d}}", "bd"},
		{"matchalt19", "{a,b{c,d}}", "a"},

		{"matchslash1", "abc/def", "abc/def"},
		{"matchslash4", "abc[/]def", "abc/def"}, // classes can match '/' even though bare */? can't

		{"matchbackslash1", `\[`, "["},
		{"matchbackslash2", `\?`, "?"},
		{"matchbackslash3", `\*`, "*"},
		{"matchbackslash7", `\a`, "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			re := rawRegex(t, c.pattern)
			if !re.MatchString(c.path) {
				t.Errorf("glob %q should match %q (regex %q)", c.pattern, c.path, re.String())
			}
		})
	}
}

func TestGlobSyntaxNoMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
	}{
		{"matchnot1", "a*b*c", "abcd"},
		{"matchnot2", "abc*abc*abc", "abcabcabcabcabcabcabca"},
		{"matchnot3", "some/**/needle.txt", "some/other/notthis.txt"},
		{"matchnot4", "some/**/**/needle.txt", "some/other/notthis.txt"},
		{"matchnot5", "/**/test", "test"},
		{"matchnot6", "/**/test", "/one/notthis"},
		{"matchnot7", "/**/test", "/notthis"},
		{"matchnot8", "**/.*", "ab.c"},
		{"matchnot9", "**/.*", "abc/ab.c"},
		{"matchnot10", ".*/**", "a.bc"},
		{"matchnot11", ".*/**", "abc/a.bc"},
		{"matchnot12", "a[0-9]b", "a_b"},
		{"matchnot13", "a[!0-9]b", "a0b"},
		{"matchnot14", "a[!0-9]b", "a9b"},
		{"matchnot15", "[!-]", "-"},
		{"matchnot16", "*hello.txt", "hello.txt-and-then-some"},
		{"matchnot17", "*hello.txt", "goodbye.txt"},
		{"matchnot18", "*some/path/to/hello.txt", "some/path/to/hello.txt-and-then-some"},
		{"matchnot19", "*some/path/to/hello.txt", "some/other/path/to/hello.txt"},
		{"matchnot20", "a", "foo/a"},
		{"matchnot21", "./foo", "foo"},
		{"matchnot22", "**/foo", "foofoo"},
		{"matchnot23", "**/foo/bar", "foofoo/bar"},
		{"matchnot24", "/*.c", "mozilla-sha1/sha1.c"},
		{"matchnot25", "*.c", "mozilla-sha1/sha1.c"},
		{"matchnot26", "**/m4/ltoptions.m4", "csharp/src/packages/repositories.config"},
		{"matchnot27", "a[^0-9]b", "a0b"},
		{"matchnot28", "a[^0-9]b", "a9b"},
		{"matchnot29", "[^-]", "-"},
		{"matchnot30", "some/*/needle.txt", "some/needle.txt"},
		{"matchrec31", "some/*/needle.txt", "some/one/two/needle.txt"},
		{"matchrec32", "some/*/needle.txt", "some/one/two/three/needle.txt"},
		{"matchrec33", ".*/**", ".abc"},
		{"matchrec34", "foo/**", "foo"},
		{"matchalt15", "foo{,.txt}", "foo"},
		{"matchslash2", "abc?def", "abc/def"},
		{"matchslash3", "abc*def", "abc/def"},
		{"matchslash5", `abc\def`, "abc/def"},

		// literal_separator=true opposites of upstream's default-mode
		// (literal_separator=false) matches!() cases: a leading `*`
		// can't cross a `/` in gitignore semantics, so these don't
		// match here even though they do upstream by default.
		{"matchpat3-litsep", "*hello.txt", "some/path/to/hello.txt"},
		{"matchpat5-litsep", "*hello.txt", "/an/absolute/path/to/hello.txt"},
		{"matchpat7-litsep", "*some/path/to/hello.txt", "a/bigger/some/path/to/hello.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			re := rawRegex(t, c.pattern)
			if re.MatchString(c.path) {
				t.Errorf("glob %q should NOT match %q (regex %q)", c.pattern, c.path, re.String())
			}
		})
	}
}

func TestLiteralOf(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
		ok      bool
	}{
		{"foo", "foo", true},
		{"/foo", "/foo", true},
		{"/foo/", "/foo/", true},
		{"/foo/bar", "/foo/bar", true},
		{"*.foo", "", false},
		{"foo/bar", "foo/bar", true},
		{"**/foo/bar", "", false},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			toks, err := parseGlob(c.pattern)
			if err != nil {
				t.Fatalf("parseGlob(%q): %v", c.pattern, err)
			}
			got, ok := literalOf(toks)
			if ok != c.ok || got != c.want {
				t.Errorf("literalOf(%q) = (%q, %v), want (%q, %v)", c.pattern, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestExtOfTokens(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
		ok      bool
	}{
		{"**/*.rs", ".rs", true},
		{"**/*.rs.bak", "", false},
		{"a*.rs", "", false},
		{"/*.c", "", false},
		{"*.c", "", false}, // no recursive prefix: left to the regex fallback
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			toks, err := parseGlob(c.pattern)
			if err != nil {
				t.Fatalf("parseGlob(%q): %v", c.pattern, err)
			}
			got, ok := extOfTokens(toks)
			if ok != c.ok || got != c.want {
				t.Errorf("extOfTokens(%q) = (%q, %v), want (%q, %v)", c.pattern, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestSuffixOfTokens(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
		ok      bool
	}{
		// Multi-dot tails: exactly the class extOfTokens rejects (it only
		// isolates the segment after the *last* dot) but a plain literal
		// suffix scan handles fine -- the motivating real-world case
		// (linux kernel .gitignore) for adding this class at all.
		{"**/*.dtb.S", ".dtb.S", true},
		{"**/*.mod.c", ".mod.c", true},
		{"**/*.so.dbg", ".so.dbg", true},
		// Single dot-segment: also valid here (suffixOfTokens is a
		// superset of extOfTokens' coverage), even though pattern.go
		// prefers extOfTokens's map-based classification for these.
		{"**/*.rs", ".rs", true},
		// No recursive prefix: left to the regex fallback, same as
		// extOfTokens.
		{"*.c", "", false},
		{"/*.c", "", false},
		// A further wildcard/class after the leading '*' isn't a pure
		// literal tail.
		{"**/*.o.*", "", false},
		{"**/*.tab.[ch]", "", false},
		// Bare `**/*` (no literal tail at all) isn't a suffix pattern.
		{"**/*", "", false},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			toks, err := parseGlob(c.pattern)
			if err != nil {
				t.Fatalf("parseGlob(%q): %v", c.pattern, err)
			}
			got, ok := suffixOfTokens(toks)
			if ok != c.ok || got != c.want {
				t.Errorf("suffixOfTokens(%q) = (%q, %v), want (%q, %v)", c.pattern, got, ok, c.want, c.ok)
			}
		})
	}
}

// TestPrefixOfTokens covers prefixOfTokens directly: found via the
// evaluation-count census (real linux kernel .gitignore patterns like
// "cscope.*" were landing in the regex fallback and evaluated on nearly
// every file in the tree).
func TestPrefixOfTokens(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
		ok      bool
	}{
		{"**/cscope.*", "cscope.", true},
		{"**/patches-*", "patches-", true},
		// No recursive prefix: parseGlob alone (unlike compileLine, which
		// prepends "**/" to a no-slash pattern before calling parseGlob --
		// the real shape these patterns actually arrive in from a
		// .gitignore line) never adds one, so this is genuinely
		// unprefixed and correctly left to the regex fallback.
		{"cscope.*", "", false},
		{"/cscope.*", "", false},
		// A leading wildcard disqualifies this as a prefix pattern (it's
		// containsOfTokens's shape instead).
		{"**/*cscope.*", "", false},
		// No trailing wildcard at all: not this shape (kindLiteral or
		// kindBasename would already have claimed this earlier in
		// classifyFast anyway).
		{"**/cscope.", "", false},
		// A '/' inside what would be the prefix isn't a basename-only
		// pattern.
		{"**/a/*", "", false},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			toks, err := parseGlob(c.pattern)
			if err != nil {
				t.Fatalf("parseGlob(%q): %v", c.pattern, err)
			}
			got, ok := prefixOfTokens(toks)
			if ok != c.ok || got != c.want {
				t.Errorf("prefixOfTokens(%q) = (%q, %v), want (%q, %v)", c.pattern, got, ok, c.want, c.ok)
			}
		})
	}
}

// TestContainsOfTokens covers containsOfTokens directly: found via the
// same census as prefixOfTokens ("*.o.*" was the single most-evaluated
// regex pattern on the linux tree).
func TestContainsOfTokens(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
		ok      bool
	}{
		{"**/*.o.*", ".o.", true},
		// Only a leading wildcard (no trailing one): that's suffixOfTokens's
		// shape, not this one.
		{"**/*.o", "", false},
		// Only a trailing wildcard (no leading one): that's
		// prefixOfTokens's shape.
		{"**/.o.*", "", false},
		// No recursive prefix: parseGlob alone (unlike compileLine, which
		// prepends "**/" to a no-slash pattern before calling parseGlob)
		// never adds one, so this is genuinely unprefixed and correctly
		// left to the regex fallback.
		{"*.o.*", "", false},
		{"/*.o.*", "", false},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			toks, err := parseGlob(c.pattern)
			if err != nil {
				t.Fatalf("parseGlob(%q): %v", c.pattern, err)
			}
			got, ok := containsOfTokens(toks)
			if ok != c.ok || got != c.want {
				t.Errorf("containsOfTokens(%q) = (%q, %v), want (%q, %v)", c.pattern, got, ok, c.want, c.ok)
			}
		})
	}
}

// TestBetweenOfTokens covers betweenOfTokens directly, including the
// overlap edge case a first version of this class got wrong: a basename
// shorter than prefix+suffix combined must not match, since the two
// literal runs would have to overlap (e.g. "#*#" must NOT match the
// single-character basename "#" -- HasPrefix("#","#") and
// HasSuffix("#","#") are each trivially true on their own, but there's
// only one "#" in the name, not the two the pattern requires).
func TestBetweenOfTokens(t *testing.T) {
	cases := []struct {
		pattern    string
		wantPrefix string
		wantSuffix string
		ok         bool
	}{
		{"**/#*#", "#", "#", true},
		// No recursive prefix: parseGlob alone (unlike compileLine, which
		// prepends "**/" for a no-slash pattern before calling parseGlob)
		// never adds one, so this is genuinely unprefixed here.
		{"#*#", "", "", false},
		// Only one wildcard side present: that's prefixOfTokens's or
		// containsOfTokens's shape, not this one.
		{"**/prefix*", "", "", false},
		{"**/*suffix", "", "", false},
		// More than one wildcard: not this shape.
		{"**/*mid*", "", "", false},
		{"**/a*b*c", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			toks, err := parseGlob(c.pattern)
			if err != nil {
				t.Fatalf("parseGlob(%q): %v", c.pattern, err)
			}
			gotPre, gotSuf, ok := betweenOfTokens(toks)
			if ok != c.ok || gotPre != c.wantPrefix || gotSuf != c.wantSuffix {
				t.Errorf("betweenOfTokens(%q) = (%q, %q, %v), want (%q, %q, %v)", c.pattern, gotPre, gotSuf, ok, c.wantPrefix, c.wantSuffix, c.ok)
			}
		})
	}
}

// TestBetweenMatchRejectsOverlap is the Set.Match-level regression test
// for the overlap bug found while implementing kindBetween: "#*#" must
// match "a#b#c" (prefix "#"... wait, no -- basename must literally start
// with "#" and end with "#") but must NOT match the single-character
// name "#", where the required prefix and suffix would have to share the
// same byte.
func TestBetweenMatchRejectsOverlap(t *testing.T) {
	var b Builder
	// The real linux kernel .gitignore writes this escaped, as `\#*#` --
	// a bare leading "#" is a gitignore comment (compileLine's very first
	// check), so this pattern must be escaped to mean anything at all.
	b.Add(`\#*#`)
	s, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want MatchResult
	}{
		{"#", NoMatch},   // too short: prefix and suffix would overlap
		{"##", Ignored},  // exactly prefix+suffix, zero characters between
		{"#x#", Ignored}, // one character between
		{"x#x", NoMatch}, // doesn't start with "#"
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := s.Match([]byte(c.path), false); got != c.want {
				t.Errorf("Match(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// TestExpandClasses covers expandClasses directly: which patterns it
// declines to expand (negated, oversized, or no class at all), and that
// eligible ones produce the exact expected cross product of literal
// variants (as rendered back through classifyFast/literalOf, since
// asserting on raw tokens would be unreadable).
func TestExpandClasses(t *testing.T) {
	render := func(t *testing.T, toks []token) string {
		t.Helper()
		lit, _, _, _, _, _, ok := classifyFast(toks)
		if !ok {
			t.Fatalf("expanded variant %v didn't classify as fast", toks)
		}
		return lit
	}

	t.Run("single class expands to N literal variants", func(t *testing.T) {
		toks, err := parseGlob("**/*.asn1.[ch]")
		if err != nil {
			t.Fatal(err)
		}
		variants, ok := expandClasses(toks)
		if !ok {
			t.Fatal("expandClasses(*.asn1.[ch]) = (_, false), want true")
		}
		var got []string
		for _, v := range variants {
			got = append(got, render(t, v))
		}
		want := []string{".asn1.c", ".asn1.h"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("expandClasses(*.asn1.[ch]) variants = %v, want %v", got, want)
		}
	})

	t.Run("two classes expand to the cross product", func(t *testing.T) {
		toks, err := parseGlob("**/*.[ab][xy]")
		if err != nil {
			t.Fatal(err)
		}
		variants, ok := expandClasses(toks)
		if !ok {
			t.Fatal("expandClasses = (_, false), want true")
		}
		var got []string
		for _, v := range variants {
			got = append(got, render(t, v))
		}
		want := []string{".ax", ".ay", ".bx", ".by"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("expandClasses(*.[ab][xy]) variants = %v, want %v", got, want)
		}
	})

	t.Run("negated class is never expanded", func(t *testing.T) {
		toks, err := parseGlob("**/*.[!ch]")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := expandClasses(toks); ok {
			t.Error("expandClasses([!ch]) = (_, true), want false (negated classes must stay regex)")
		}
	})

	t.Run("class over maxClassSize is not expanded", func(t *testing.T) {
		toks, err := parseGlob("**/*.[a-z]") // 26 > maxClassSize(10)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := expandClasses(toks); ok {
			t.Error("expandClasses([a-z]) = (_, true), want false (26 chars exceeds maxClassSize)")
		}
	})

	t.Run("class exactly at maxClassSize is expanded", func(t *testing.T) {
		toks, err := parseGlob("**/*.[a-j]") // exactly 10 chars
		if err != nil {
			t.Fatal(err)
		}
		variants, ok := expandClasses(toks)
		if !ok {
			t.Fatal("expandClasses([a-j]) = (_, false), want true (10 chars is within maxClassSize)")
		}
		if len(variants) != 10 {
			t.Errorf("expandClasses([a-j]) produced %d variants, want 10", len(variants))
		}
	})

	t.Run("cross product over maxExpandedPatterns is not expanded", func(t *testing.T) {
		// 3 classes of 9 chars each = 729 > maxExpandedPatterns(64).
		toks, err := parseGlob("**/*.[abcdefghi][abcdefghi][abcdefghi]")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := expandClasses(toks); ok {
			t.Error("expandClasses(729-way cross product) = (_, true), want false")
		}
	})

	t.Run("no class at all is not expanded", func(t *testing.T) {
		toks, err := parseGlob("**/*.rs")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := expandClasses(toks); ok {
			t.Error("expandClasses(no class) = (_, true), want false")
		}
	})
}

func TestBasenameLiteralOf(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
		ok      bool
	}{
		{"**/foo", "foo", true},
		{"foo", "", false},
		{"*foo", "", false},
		{"*/foo", "", false},
	}
	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			toks, err := parseGlob(c.pattern)
			if err != nil {
				t.Fatalf("parseGlob(%q): %v", c.pattern, err)
			}
			got, ok := basenameLiteralOf(toks)
			if ok != c.ok || got != c.want {
				t.Errorf("basenameLiteralOf(%q) = (%q, %v), want (%q, %v)", c.pattern, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestBasenameTokensRequiresLiteralSeparatorSafety(t *testing.T) {
	// "**/fo*o": under literal_separator=true (always, here) the `*`
	// can't escape the basename, so this classifies; upstream only
	// allows this with its SLASHLIT option.
	toks, err := parseGlob("**/fo*o")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := basenameTokens(toks)
	if !ok {
		t.Fatalf("basenameTokens(\"**/fo*o\") = (_, false), want ok=true")
	}
	want := []token{{kind: tLiteral, lit: 'f'}, {kind: tLiteral, lit: 'o'}, {kind: tZeroOrMore}, {kind: tLiteral, lit: 'o'}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("basenameTokens(\"**/fo*o\") = %+v, want %+v", got, want)
	}
}

// TestAddCIMatchesCaseInsensitively covers --iglob/
// --glob-case-insensitive support (Builder.AddCI): a pattern that would
// otherwise classify into a fast class (kindExt here -- "*.txt" is the
// exact "**/*.ext" shape) must still match a differently-cased basename.
func TestAddCIMatchesCaseInsensitively(t *testing.T) {
	var b Builder
	b.AddCI("*.txt")
	s, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, path := range []string{"UPPER.TXT", "Mixed.Txt", "lower.txt"} {
		if got := s.Match([]byte(path), false); got != Ignored {
			t.Errorf("AddCI(%q).Match(%q) = %v, want Ignored", "*.txt", path, got)
		}
	}
	if got := s.Match([]byte("other.md"), false); got != NoMatch {
		t.Errorf("AddCI(%q).Match(%q) = %v, want NoMatch", "*.txt", "other.md", got)
	}
}

// TestAddCIBypassesFastClasses confirms AddCI patterns never populate any
// of Set's fast-class slices/maps (see AddCI's doc for why those aren't
// safe to use for a case-insensitive pattern) -- every AddCI pattern
// must land in s.regexes instead, even for a pattern shape (like "*.txt")
// that Add would classify into extMap.
func TestAddCIBypassesFastClasses(t *testing.T) {
	var b Builder
	b.AddCI("*.txt")
	s, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n := len(s.literalMap) + len(s.basenameMap) + len(s.extMap) + len(s.suffixes) +
		len(s.prefixes) + len(s.contains) + len(s.betweens) + len(s.pathBetweens) + len(s.chains); n != 0 {
		t.Errorf("AddCI(%q) populated a fast class (total %d entries across all fast classes), want 0", "*.txt", n)
	}
	if len(s.regexes) != 1 {
		t.Fatalf("AddCI(%q) produced %d regex entries, want 1", "*.txt", len(s.regexes))
	}
}

// TestAddPlainRemainsCaseSensitive is AddCI's negative control: an
// ordinary Add pattern of the identical shape must NOT match a
// differently-cased basename, confirming AddCI's case-folding is opt-in
// per pattern, not a global Set-wide change.
func TestAddPlainRemainsCaseSensitive(t *testing.T) {
	s := buildSet(t, "*.txt")
	if got := s.Match([]byte("UPPER.TXT"), false); got == Ignored {
		t.Errorf("Add(%q).Match(%q) = Ignored, want NoMatch (case-sensitive by default)", "*.txt", "UPPER.TXT")
	}
	if got := s.Match([]byte("lower.txt"), false); got != Ignored {
		t.Errorf("Add(%q).Match(%q) = %v, want Ignored", "*.txt", "lower.txt", got)
	}
}
