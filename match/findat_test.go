package match

import "testing"

// TestFindAt covers task #15: printer colors every match occurrence on a
// line by advancing a start offset, so FindAt must evaluate anchors and
// -w word boundaries against the *whole* line at every call, not a
// subslice starting at that offset (which is what a naive
// Find(line[pos:]) loop would do, and which breaks ^ and -w semantics
// for the 2nd+ occurrence on a line).
func TestFindAt(t *testing.T) {
	// "$" only matches at the true end of the line; the correct behavior
	// is exactly one match, at the end, never re-triggering as if a
	// subslice were itself "the line".
	m, err := New(cs(`^`))
	if err != nil {
		t.Fatal(err)
	}
	line := []byte("foofoofoo")
	s, e, ok := m.(interface {
		FindAt([]byte, int) (int, int, bool)
	}).FindAt(line, 0)
	if !ok || s != 0 || e != 0 {
		t.Fatalf("^ FindAt(line,0) = (%d,%d,%v), want (0,0,true)", s, e, ok)
	}
	// From start=1 onward, ^ must NOT match again: unlike
	// Find(line[1:]), which would treat offset 1 as a fresh "start of
	// line" and incorrectly match there too.
	_, _, ok = m.(interface {
		FindAt([]byte, int) (int, int, bool)
	}).FindAt(line, 1)
	if ok {
		t.Fatalf("^ FindAt(line,1) matched, want no match (only true start of line)")
	}

	// Multiple occurrences of a word-boundary pattern on one line: with
	// -w, "foo" must be found at both real word occurrences and nowhere
	// else, and each FindAt call from the position after the previous
	// match must still evaluate boundaries against the full line.
	wm, err := New(word(cs("foo")))
	if err != nil {
		t.Fatal(err)
	}
	fa := wm.(interface {
		FindAt([]byte, int) (int, int, bool)
	}).FindAt
	line2 := []byte("foo barfoo foo xfoo")
	var spans [][2]int
	pos := 0
	for {
		s, e, ok := fa(line2, pos)
		if !ok {
			break
		}
		spans = append(spans, [2]int{s, e})
		pos = e + 1
	}
	// "foo barfoo foo xfoo": standalone "foo" at [0,3) and [11,14);
	// "foo" embedded in "barfoo" (preceded by 'r') and "xfoo" (preceded
	// by 'x') are both word-embedded and must be rejected.
	want := [][2]int{{0, 3}, {11, 14}}
	if len(spans) != len(want) {
		t.Fatalf("FindAt loop over %q = %v, want %v", line2, spans, want)
	}
	for i := range want {
		if spans[i] != want[i] {
			t.Fatalf("FindAt loop over %q = %v, want %v", line2, spans, want)
		}
	}

	// Anchored pattern with multiple potential (but only one real)
	// occurrence: "^foo" must match only when start==0 corresponds to
	// the actual beginning of the line, never re-triggering later just
	// because a subslice happens to begin with "foo".
	am, err := New(cs(`^foo`))
	if err != nil {
		t.Fatal(err)
	}
	afa := am.(interface {
		FindAt([]byte, int) (int, int, bool)
	}).FindAt
	line3 := []byte("foobarfoo")
	if s, e, ok := afa(line3, 0); !ok || s != 0 || e != 3 {
		t.Fatalf("^foo FindAt(line3,0) = (%d,%d,%v), want (0,3,true)", s, e, ok)
	}
	if _, _, ok := afa(line3, 3); ok {
		t.Fatalf("^foo FindAt(line3,3) matched the embedded \"foo\" at offset 6, but ^ must not match mid-line")
	}
}
