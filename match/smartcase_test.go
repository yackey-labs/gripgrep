package match

import "testing"

// Ported from ripgrep's crates/regex/src/ast.rs AstAnalysis tests.
func TestAnalyzePatternCase(t *testing.T) {
	cases := []struct {
		pattern      string
		anyUppercase bool
		anyLiteral   bool
	}{
		{``, false, false},
		{`foo`, false, true},
		{`Foo`, true, true},
		{`foO`, true, true},
		{`foo\\`, false, true},
		{`foo\w`, false, true},
		{`foo\S`, false, true},
		{`foo\p{Ll}`, false, true},
		{`foo[a-z]`, false, true},
		{`foo[A-Z]`, true, true},
		{`foo[\S\t]`, false, true},
		{`foo\\S`, true, true},
		{`\p{Ll}`, false, false},
		{`aBc\w`, true, true},
		{`a\x61`, false, true},
	}
	for _, c := range cases {
		gotLit, gotUpper := analyzePatternCase(c.pattern)
		if gotUpper != c.anyUppercase || gotLit != c.anyLiteral {
			t.Errorf("analyzePatternCase(%q) = (anyLiteral=%v, anyUppercase=%v), want (anyLiteral=%v, anyUppercase=%v)",
				c.pattern, gotLit, gotUpper, c.anyLiteral, c.anyUppercase)
		}
	}
}
