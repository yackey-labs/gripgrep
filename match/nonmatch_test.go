package match

import (
	"regexp/syntax"
	"testing"
)

func TestCanMatchNewline(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{`abc`, false},
		{`a\nb`, true},
		{`.`, false},
		{`(?s).`, true},
		{`[^x]`, true},   // negated class matches '\n' in Go
		{`[^\n]`, false}, // explicitly excludes '\n'
		{`[a-z]`, false},
		{`[\x00-\xff]`, true},
		{`a|b`, false},
		{`a|\n`, true},
		{`a+`, false},
		{`(a\n)+`, true},
		{`^abc`, true},
		{`abc$`, true},
		{`\Aabc`, true},
		{`abc\z`, true},
		{`\babc\b`, false},
	}
	for _, c := range cases {
		re, err := syntax.Parse(c.pattern, syntax.Perl)
		if err != nil {
			t.Fatalf("parse %q: %v", c.pattern, err)
		}
		got := canMatchNewline(re)
		if got != c.want {
			t.Errorf("canMatchNewline(%q) = %v, want %v", c.pattern, got, c.want)
		}
	}
}
