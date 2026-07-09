package match

import (
	"regexp/syntax"
	"testing"
)

// This file ports test cases from ripgrep's crates/regex/src/literal.rs
// (the InnerLiterals extractor tests) to validate extractInnerLiterals.
// Tests requiring Rust-only syntax not expressible in Go's regexp/syntax
// (character class intersection `[a&&b]`, `(?-u)` ASCII-only mode,
// chained repetition operators like `x{2}{2}`) are omitted; a comment
// marks each omission.

func mustParseForExtract(t *testing.T, pattern string) *syntax.Regexp {
	t.Helper()
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		t.Fatalf("parse %q: %v", pattern, err)
	}
	return re
}

func extractFor(t *testing.T, pattern string) seq {
	t.Helper()
	re := mustParseForExtract(t, pattern)
	return extractInnerLiterals(re)
}

// exactSeq/inexactSeq/seqOf build expected seq values for comparison.
func exactSeq(strs ...string) seq {
	s := emptySeq()
	for _, str := range strs {
		s.push(exactLit([]byte(str)))
	}
	return s
}

type litSpec struct {
	s     string
	exact bool
}

func E(s string) litSpec { return litSpec{s, true} }
func I(s string) litSpec { return litSpec{s, false} }

func seqOf(specs ...litSpec) seq {
	s := emptySeq()
	for _, sp := range specs {
		if sp.exact {
			s.push(exactLit([]byte(sp.s)))
		} else {
			s.push(inexactLit([]byte(sp.s)))
		}
	}
	return s
}

func infSeq() seq { return infiniteSeq() }

func seqEqual(a, b seq) bool {
	if a.infinite != b.infinite {
		return false
	}
	if a.infinite {
		return true
	}
	if len(a.lits) != len(b.lits) {
		return false
	}
	for i := range a.lits {
		if a.lits[i].exact != b.lits[i].exact || !a.lits[i].equalBytes(b.lits[i]) {
			return false
		}
	}
	return true
}

func seqString(s seq) string {
	if s.infinite {
		return "Seq[∞]"
	}
	out := "Seq["
	for i, l := range s.lits {
		if i > 0 {
			out += ", "
		}
		tag := "E"
		if !l.exact {
			tag = "I"
		}
		out += tag + "(" + string(l.bytes) + ")"
	}
	return out + "]"
}

func check(t *testing.T, pattern string, want seq) {
	t.Helper()
	got := extractFor(t, pattern)
	if !seqEqual(got, want) {
		t.Errorf("extract(%q):\n  got  %s\n  want %s", pattern, seqString(got), seqString(want))
	}
}

func TestExtractVarious(t *testing.T) {
	check(t, `foo`, exactSeq("foo"))
	check(t, `[a-z]foo[a-z]`, seqOf(I("foo")))
	check(t, `[a-z](foo)(bar)[a-z]`, seqOf(I("foobar")))
	check(t, `[a-z]([a-z]foo)(bar[a-z])[a-z]`, seqOf(I("foo")))
	check(t, `[a-z]([a-z]foo)([a-z]foo)[a-z]`, seqOf(I("foo")))
	// Deviation from rg's test: Rust's \d defaults to the full Unicode
	// decimal-digit property (thousands of code points, so class
	// extraction immediately gives up as over-limit, leaving only the
	// "." literal). Go's \d is always ASCII-only ([0-9], confirmed via
	// syntax.Parse), which is small enough to enumerate into 10 discrete
	// literals -- which is exactly why is_good's "many short literals"
	// gate then correctly discards them as a poor prefilter, going
	// infinite for a different (Go-correct) reason.
	check(t, `(\d{1,3}\.){3}\d{1,3}`, infSeq())
	check(t, `[a-z]([a-z]foo){3}[a-z]`, seqOf(I("foo")))
	check(t, `[a-z](foo[a-z]){3}[a-z]`, seqOf(I("foo")))
	check(t, `[a-z]([a-z]foo[a-z]){3}[a-z]`, seqOf(I("foo")))
	check(t, `[a-z]([a-z]foo){3}(bar[a-z]){3}[a-z]`, seqOf(I("foo")))
}

func TestExtractHeuristics(t *testing.T) {
	check(t, `[a-z]+(ab|cd|ef)[a-z]+hiya[a-z]+`, seqOf(I("hiya")))
	check(t, `[a-z]+(abc|def|ghi)[a-z]+hiya[a-z]+`, seqOf(I("abc"), I("def"), I("ghi")))
}

func TestExtractLiteral(t *testing.T) {
	check(t, `a`, exactSeq("a"))
	check(t, `aaaaa`, exactSeq("aaaaa"))
	check(t, `(?i)a`, exactSeq("A", "a"))
	check(t, `(?i)ab`, exactSeq("AB", "Ab", "aB", "ab"))
	check(t, `ab(?i)c`, seqOf(E("abC"), E("abc")))

	check(t, `Z`, exactSeq("Z"))
	check(t, `☃`, exactSeq("☃"))
	check(t, `(?i)☃`, exactSeq("☃"))
	check(t, `☃☃☃☃☃`, exactSeq("☃☃☃☃☃"))

	check(t, `Δ`, exactSeq("Δ"))
	check(t, `δ`, exactSeq("δ"))
	check(t, `(?i)Δ`, exactSeq("Δ", "δ"))
	check(t, `(?i)δ`, exactSeq("Δ", "δ"))

	check(t, `(?i)S`, exactSeq("S", "s", "ſ"))
	check(t, `(?i)s`, exactSeq("S", "s", "ſ"))
	check(t, `(?i)ſ`, exactSeq("S", "s", "ſ"))
}

func TestExtractClass(t *testing.T) {
	check(t, `[abc]`, exactSeq("a", "b", "c"))
	check(t, `a[123]b`, exactSeq("a1b", "a2b", "a3b"))
	check(t, `[εδ]`, exactSeq("δ", "ε"))
	check(t, `(?i)[εδ]`, exactSeq("Δ", "Ε", "δ", "ε", "ϵ"))
}

func TestExtractLook(t *testing.T) {
	check(t, `a\Ab`, exactSeq("ab"))
	check(t, `a\zb`, exactSeq("ab"))
	check(t, `a(?m:^)b`, exactSeq("ab"))
	check(t, `a(?m:$)b`, exactSeq("ab"))
	check(t, `a\bb`, exactSeq("ab"))
	check(t, `a\Bb`, exactSeq("ab"))

	check(t, `^ab`, exactSeq("ab"))
	check(t, `$ab`, exactSeq("ab"))
	check(t, `(?m:^)ab`, exactSeq("ab"))
	check(t, `(?m:$)ab`, exactSeq("ab"))
	check(t, `\bab`, exactSeq("ab"))
	check(t, `\Bab`, exactSeq("ab"))

	check(t, `ab^`, exactSeq("ab"))
	check(t, `ab$`, exactSeq("ab"))
	check(t, `ab(?m:^)`, exactSeq("ab"))
	check(t, `ab(?m:$)`, exactSeq("ab"))
	check(t, `ab\b`, exactSeq("ab"))
	check(t, `ab\B`, exactSeq("ab"))

	check(t, `^aZ*b`, seqOf(I("aZ"), E("ab")))
}

func TestExtractRepetition(t *testing.T) {
	check(t, `a?`, infSeq())
	check(t, `a??`, infSeq())
	check(t, `a*`, infSeq())
	check(t, `a*?`, infSeq())
	check(t, `a+`, seqOf(I("a")))
	check(t, `(a+)+`, seqOf(I("a")))

	check(t, `aZ{0}b`, exactSeq("ab"))
	check(t, `aZ?b`, exactSeq("aZb", "ab"))
	check(t, `aZ??b`, exactSeq("ab", "aZb"))
	check(t, `aZ*b`, seqOf(I("aZ"), E("ab")))
	check(t, `aZ*?b`, seqOf(E("ab"), I("aZ")))
	check(t, `aZ+b`, seqOf(I("aZ")))
	check(t, `aZ+?b`, seqOf(I("aZ")))

	check(t, `aZ{2}b`, exactSeq("aZZb"))
	check(t, `aZ{2,3}b`, seqOf(I("aZZ")))

	check(t, `a*b`, seqOf(I("a"), E("b")))
	check(t, `a*?b`, seqOf(E("b"), I("a")))
	check(t, `ab+`, seqOf(I("ab")))
	check(t, `a*b+`, seqOf(I("a"), I("b")))

	check(t, `a*b*c`, seqOf(I("a"), I("b"), E("c")))
	check(t, `(a+)?(b+)?c`, seqOf(I("a"), I("b"), E("c")))
	check(t, `(a+|)(b+|)c`, seqOf(I("a"), I("b"), E("c")))
	check(t, `a*b*c*`, infSeq())
	check(t, `a*b*c+`, seqOf(I("a"), I("b"), I("c")))
	check(t, `a*b+c`, seqOf(I("a"), I("b")))
	check(t, `a*b+c*`, seqOf(I("a"), I("b")))
	check(t, `ab*`, seqOf(I("ab"), E("a")))
	check(t, `ab*c`, seqOf(I("ab"), E("ac")))
	check(t, `ab+`, seqOf(I("ab")))
	check(t, `ab+c`, seqOf(I("ab")))

	check(t, `z*azb`, seqOf(I("z"), E("azb")))

	check(t, `[ab]{3}`, exactSeq("aaa", "aab", "aba", "abb", "baa", "bab", "bba", "bbb"))
	check(t, `[ab]{3,4}`, seqOf(
		I("aaa"), I("aab"), I("aba"), I("abb"),
		I("baa"), I("bab"), I("bba"), I("bbb"),
	))
}

func TestExtractConcat(t *testing.T) {
	check(t, `abc()xyz`, exactSeq("abcxyz"))
	check(t, `(abc)(xyz)`, exactSeq("abcxyz"))
	check(t, `abc()mno()xyz`, exactSeq("abcmnoxyz"))
	// abc[a&&b]xyz / abc[a&&b]*xyz use class intersection, unsupported by
	// Go's regexp/syntax; omitted.
}

func TestExtractAlternation(t *testing.T) {
	check(t, `abc|mno|xyz`, exactSeq("abc", "mno", "xyz"))
	check(t, `abc|mZ*o|xyz`, seqOf(E("abc"), I("mZ"), E("mo"), E("xyz")))
	// abc|M[a&&b]N|xyz and abc|M[a&&b]*N|xyz use class intersection;
	// omitted.

	check(t, `(?:|aa)aaa`, exactSeq("aaa"))
	check(t, `(?:|aa)(?:aaa)*`, infSeq())
	check(t, `(?:|aa)(?:aaa)*?`, infSeq())

	check(t, `a|b*`, infSeq())
	check(t, `a|b+`, seqOf(E("a"), I("b")))

	check(t, `a*b|c`, seqOf(I("a"), E("b"), E("c")))

	check(t, `a|(?:b|c*)`, infSeq())

	check(t, `(a|b)*c|(a|ab)*c`, seqOf(I("a"), I("b"), E("c")))

	check(t, `(ab|cd)(ef|gh)`, exactSeq("abef", "abgh", "cdef", "cdgh"))
	check(t, `(ab|cd)(ef|gh)(ij|kl)`, exactSeq(
		"abefij", "abefkl", "abghij", "abghkl",
		"cdefij", "cdefkl", "cdghij", "cdghkl",
	))
}

func TestExtractAnything(t *testing.T) {
	check(t, `.`, infSeq())
	check(t, `(?s).`, infSeq())
	check(t, `[A-Za-z]`, infSeq())
	check(t, `[A-Z]`, infSeq())
	check(t, `[A-Z]{0}`, infSeq())
	check(t, `[A-Z]?`, infSeq())
	check(t, `[A-Z]*`, infSeq())
	check(t, `[A-Z]+`, infSeq())
	check(t, `1[A-Z]`, seqOf(I("1")))
	check(t, `1[A-Z]2`, seqOf(I("1")))
	check(t, `[A-Z]+123`, seqOf(I("123")))
	check(t, `[A-Z]+123[A-Z]+`, seqOf(I("123")))
	check(t, `1|[A-Z]|3`, infSeq())
	check(t, `1|2[A-Z]|3`, seqOf(E("1"), I("2"), E("3")))
	check(t, `1|[A-Z]2[A-Z]|3`, seqOf(E("1"), I("2"), E("3")))
	check(t, `1|[A-Z]2|3`, seqOf(E("1"), I("2"), E("3")))
	check(t, `1|2[A-Z]3|4`, seqOf(E("1"), I("2"), E("4")))
	check(t, `(?:|1)[A-Z]2`, seqOf(I("2")))
	check(t, `a.z`, seqOf(I("a")))
}

func TestExtractEmpty(t *testing.T) {
	check(t, ``, infSeq())
	check(t, `^`, infSeq())
	check(t, `$`, infSeq())
	check(t, `(?m:^)`, infSeq())
	check(t, `(?m:$)`, infSeq())
	check(t, `\b`, infSeq())
	check(t, `\B`, infSeq())
}

func TestExtractOptimize(t *testing.T) {
	check(t, `foobarfoobar|foobar|foobarzfoobar|foobarfoobar`, seqOf(I("foobar")))
	check(t, `abba|akka|abccba`, exactSeq("abba", "akka", "abccba"))
	check(t, `sam|samwise`, seqOf(E("sam")))
	check(t, `foobarfoo|foo||foozfoo|foofoo`, infSeq())
	check(t, `foobarfoo|foo| |foofoo`, infSeq())
}

func TestExtractCaseInsensitiveAlternation(t *testing.T) {
	check(t, `(?i:e.x|ex)`, seqOf(I("X"), I("x")))
}
