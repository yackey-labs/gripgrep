package printer

import (
	"testing"

	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/search"
)

// The expected byte strings below were verified against the real rg
// 15.1.0 binary (see cmd/gg's round-34 differential sweep for the exact
// invocations), covering --column, --vimgrep, and -b/--byte-offset.

func mustMatcher(t *testing.T, cfg match.Config) match.Matcher {
	t.Helper()
	m, err := match.New(cfg)
	if err != nil {
		t.Fatalf("match.New(%+v): %v", cfg, err)
	}
	return m
}

// TestStandard_Column mirrors `rg -n --column cat multi.txt`: the
// column of the FIRST match on the line, inserted between the line
// number and the text.
func TestStandard_Column(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Column = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Matched(&search.Match{Line: []byte("cat at start"), LineNumber: 3, HasLineNumber: true, Offset: 30})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:5:one cat two cat\n" + "multi.txt:3:1:cat at start\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_ColumnNoLineNumber mirrors `rg -N --column cat
// multi.txt`: column stays, the line-number field disappears entirely.
func TestStandard_ColumnNoLineNumber(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Column = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), HasLineNumber: false, Offset: 0})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:5:one cat two cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_ColumnNoSpanFound mirrors `rg --column -v cat
// multi.txt`'s "2:no match here" line: an inverted "match" is a line the
// pattern does NOT match, so Matcher re-confirms zero spans on it, and
// the column field must be omitted entirely (not printed as 0 or empty).
func TestStandard_ColumnNoSpanFound(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Column = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("no match here"), LineNumber: 2, HasLineNumber: true, Offset: 16})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:2:no match here\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q (no column field for a span-less matched line)", got, want)
	}
}

// TestStandard_Vimgrep mirrors `rg --vimgrep cat multi.txt`: one row per
// match OCCURRENCE (line 1 has two), always with the path prefix.
func TestStandard_Vimgrep(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Vimgrep = true
	p.Column = true // wire.go always resolves Column true under Vimgrep by default
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Matched(&search.Match{Line: []byte("cat at start"), LineNumber: 3, HasLineNumber: true, Offset: 30})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:5:one cat two cat\n" +
		"multi.txt:1:13:one cat two cat\n" +
		"multi.txt:3:1:cat at start\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_VimgrepNoColumn mirrors `rg --vimgrep --no-column cat
// multi.txt`: still one row per occurrence, just without the column
// field -- Vimgrep (row count) and Column (field presence) are
// orthogonal, verified against the real rg binary.
func TestStandard_VimgrepNoColumn(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Vimgrep = true
	p.Column = false
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:one cat two cat\n" + "multi.txt:1:one cat two cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_VimgrepInvert mirrors `rg --vimgrep -v cat multi.txt`:
// zero spans found (see TestStandard_ColumnNoSpanFound's doc) collapses
// to exactly one row, no column.
func TestStandard_VimgrepInvert(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Vimgrep = true
	p.Column = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("no match here"), LineNumber: 2, HasLineNumber: true, Offset: 16})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:2:no match here\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_VimgrepZeroWidth mirrors `rg --vimgrep 'x*' multi.txt`'s
// first line: one row per POSITION, including one past the last char.
func TestStandard_VimgrepZeroWidth(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Vimgrep = true
	p.Column = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"x*"}})

	p.Begin("t.txt")
	p.Matched(&search.Match{Line: []byte("cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("t.txt", &search.Stats{Matched: true})

	want := "t.txt:1:1:cat\n" + "t.txt:1:2:cat\n" + "t.txt:1:3:cat\n" + "t.txt:1:4:cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_VimgrepColorSingleSpan proves --color=always --vimgrep
// highlights only the CURRENT row's occurrence, not every occurrence on
// the line (rg's sink_slow per_match branch colors `&[m]`, a single
// match, not the full span list) -- verified against the real rg
// binary.
func TestStandard_VimgrepColorSingleSpan(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Vimgrep = true
	p.Column = true
	p.Color = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("t.txt")
	p.Matched(&search.Match{Line: []byte("cat cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("t.txt", &search.Stats{Matched: true})

	path := "\x1b[0m\x1b[35mt.txt\x1b[0m"
	ln := "\x1b[0m\x1b[32m1\x1b[0m"
	col1 := "\x1b[0m1\x1b[0m"
	col2 := "\x1b[0m5\x1b[0m"
	colored := "\x1b[0m\x1b[1m\x1b[31mcat\x1b[0m"
	want := path + ":" + ln + ":" + col1 + ":" + colored + " cat\n" +
		path + ":" + ln + ":" + col2 + ":" + "cat " + colored + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_ByteOffset mirrors `rg -b cat multi.txt` (line mode: the
// LINE's absolute offset) and `rg -b -n cat multi.txt` (line:offset:text
// field order).
func TestStandard_ByteOffset(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.ByteOffset = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), HasLineNumber: false, Offset: 0})
	p.Matched(&search.Match{Line: []byte("cat at start"), HasLineNumber: false, Offset: 30})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:0:one cat two cat\n" + "multi.txt:30:cat at start\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_ByteOffsetWithLineNumber mirrors `rg -b -n cat
// multi.txt`: "line:offset:text".
func TestStandard_ByteOffsetWithLineNumber(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.ByteOffset = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Matched(&search.Match{Line: []byte("cat at start"), LineNumber: 3, HasLineNumber: true, Offset: 30})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:0:one cat two cat\n" + "multi.txt:3:30:cat at start\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_ByteOffsetColumnOrdering mirrors `rg -b --column cat
// multi.txt`: "line:col:offset:text" -- offset comes LAST, immediately
// before the text, after column.
func TestStandard_ByteOffsetColumnOrdering(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.ByteOffset = true
	p.Column = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Matched(&search.Match{Line: []byte("cat at start"), LineNumber: 3, HasLineNumber: true, Offset: 30})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:5:0:one cat two cat\n" + "multi.txt:3:1:30:cat at start\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_ByteOffsetContext mirrors `rg -b -C1 cat multi.txt`'s
// context line: "2-16-no match here" -- dash-separated, offset present,
// no column (context lines never carry one).
func TestStandard_ByteOffsetContext(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.ByteOffset = true
	p.ContextEnabled = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Context(&search.Ctx{Line: []byte("no match here"), LineNumber: 2, HasLineNumber: true, Offset: 16, After: true})
	p.Matched(&search.Match{Line: []byte("cat at start"), LineNumber: 3, HasLineNumber: true, Offset: 30})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:0:one cat two cat\n" +
		"multi.txt-2-16-no match here\n" +
		"multi.txt:3:30:cat at start\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_VimgrepByteOffsetPerOccurrence mirrors `rg -b --vimgrep
// cat multi.txt`: byte offset is the OCCURRENCE's own absolute offset
// (line offset + match start), NOT the line's start offset like plain
// -b -- verified against the real rg binary (`4` and `12` for line 1's
// two occurrences at line-offset 0, not `0` twice).
func TestStandard_VimgrepByteOffsetPerOccurrence(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Vimgrep = true
	p.Column = true
	p.ByteOffset = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Matched(&search.Match{Line: []byte("cat at start"), LineNumber: 3, HasLineNumber: true, Offset: 30})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:5:4:one cat two cat\n" +
		"multi.txt:1:13:12:one cat two cat\n" +
		"multi.txt:3:1:30:cat at start\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestFindSpans_EmptyAdjacentToRealMatch is a regression test for the
// exact discriminator a naive "advance by one on a zero-width match"
// span-finding loop gets wrong: pattern `a?` over "aaa bbb caat" must
// NOT report a redundant empty match glued to the end of a real "a"
// match right before it. Verified directly against both Go's
// regexp.FindAllIndex (which already implements this suppression
// internally in one call) and the real rg binary (Rust regex engine,
// same rule) -- see findSpans' doc for the exact rule.
func TestFindSpans_EmptyAdjacentToRealMatch(t *testing.T) {
	p := &Standard{Matcher: mustMatcher(t, match.Config{Patterns: []string{"a?"}})}
	line := []byte("aaa bbb caat")

	got := p.findSpans(line)
	want := []matchSpan{
		{0, 1}, {1, 2}, {2, 3}, // "aaa": three real matches
		{4, 4}, {5, 5}, {6, 6}, {7, 7}, {8, 8}, // "bbb c": empty matches (position 3 suppressed)
		{9, 10}, {10, 11}, // "aa": two real matches
		{12, 12}, // end of string: empty match (position 11 suppressed)
	}
	if len(got) != len(want) {
		t.Fatalf("findSpans(%q) = %v (%d spans), want %v (%d spans)", line, got, len(got), want, len(want))
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("findSpans(%q)[%d] = %v, want %v", line, i, g, want[i])
		}
	}
}
