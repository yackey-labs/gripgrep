package match

import "testing"

func TestAcceptWordBoundary(t *testing.T) {
	cases := []struct {
		buf  string
		s, e int
		want bool
	}{
		// "x-foo": match "-foo" at [1,5). Preceding char 'x' is a word
		// char, so this must be rejected even though a naive \b...\b
		// wrap would accept it (word/non-word transition exists at the
		// x|- boundary, but the half-boundary check specifically wants
		// the character before the match to be non-word).
		{"x-foo", 1, 5, false},
		// " -foo": preceding char is a space (non-word) -> accept.
		{" -foo", 1, 5, true},
		// Match at the very start of the buffer.
		{"-foo bar", 0, 4, true},
		// Match at the very end of the buffer.
		{"bar -foo", 4, 8, true},
		// Plain word match surrounded by word chars: reject both sides.
		{"xfooy", 1, 4, false},
		// Plain word match surrounded by non-word: accept.
		{" foo ", 1, 4, true},
		// '\n' acts as a delimiter.
		{"bar\nfoo\nbaz", 4, 7, true},
		{"barfoo\nbaz", 3, 6, false}, // "foo" immediately after "bar", no boundary
	}
	for _, c := range cases {
		got := acceptWordBoundary([]byte(c.buf), c.s, c.e)
		if got != c.want {
			t.Errorf("acceptWordBoundary(%q, %d, %d) = %v, want %v", c.buf, c.s, c.e, got, c.want)
		}
	}
}
