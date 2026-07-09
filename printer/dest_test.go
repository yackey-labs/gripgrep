package printer

import (
	"testing"

	"github.com/yackey-labs/gripgrep/search"
)

// TestDest_WriteBlock_FirstBlockNoSeparator verifies the very first
// block written to a Dest never gets a leading separator, regardless of
// what sep is.
func TestDest_WriteBlock_FirstBlockNoSeparator(t *testing.T) {
	dest, out := newTestDest()
	if err := dest.WriteBlock([]byte("first\n"), sepContextBreak); err != nil {
		t.Fatal(err)
	}
	want := "first\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestDest_WriteBlock_SubsequentBlocksGetSeparator verifies the second
// and later blocks are preceded by sep.
func TestDest_WriteBlock_SubsequentBlocksGetSeparator(t *testing.T) {
	dest, out := newTestDest()
	dest.WriteBlock([]byte("first\n"), sepContextBreak)
	dest.WriteBlock([]byte("second\n"), sepContextBreak)
	dest.WriteBlock([]byte("third\n"), sepContextBreak)

	want := "first\n" + "--\n" + "second\n" + "--\n" + "third\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestDest_WriteBlock_EmptyBlockSkipped verifies an empty block is
// skipped entirely: no write, no separator, and no state change (a
// later real block still counts as "first").
func TestDest_WriteBlock_EmptyBlockSkipped(t *testing.T) {
	dest, out := newTestDest()
	if err := dest.WriteBlock(nil, sepContextBreak); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output from an empty block, got %q", out.String())
	}
	dest.WriteBlock([]byte("real\n"), sepContextBreak)
	want := "real\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q (empty block should not have counted as \"first\")", got, want)
	}
}

// TestDest_WriteBlock_NilOrEmptySepBehavesLikeWrite verifies a nil/empty
// sep never inserts a separator, matching Standard's plain --no-heading
// non-context mode.
func TestDest_WriteBlock_NilOrEmptySepBehavesLikeWrite(t *testing.T) {
	dest, out := newTestDest()
	dest.WriteBlock([]byte("first\n"), nil)
	dest.WriteBlock([]byte("second\n"), nil)
	dest.WriteBlock([]byte("third\n"), []byte{})

	want := "first\n" + "second\n" + "third\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStandard_HeadingMultiFile_BlankLineBetweenBlocks mirrors `rg -n
// --heading hello .` across multiple files: a blank line separates
// consecutive heading blocks, with none leading and none trailing.
func TestStandard_HeadingMultiFile_BlankLineBetweenBlocks(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Heading = true

	p.Begin("a.txt")
	p.Matched(&search.Match{Line: []byte("hello"), LineNumber: 1, HasLineNumber: true})
	p.Finish("a.txt", &search.Stats{Matched: true})

	p.Begin("b.txt")
	p.Matched(&search.Match{Line: []byte("hello"), LineNumber: 1, HasLineNumber: true})
	p.Finish("b.txt", &search.Stats{Matched: true})

	want := "a.txt\n1:hello\n" + "\n" + "b.txt\n1:hello\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_HeadingSingleFile_NoTrailingBlank verifies a single
// file's heading block gets no leading or trailing blank line at all.
func TestStandard_HeadingSingleFile_NoTrailingBlank(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Heading = true

	p.Begin("a.txt")
	p.Matched(&search.Match{Line: []byte("hello"), LineNumber: 1, HasLineNumber: true})
	p.Finish("a.txt", &search.Stats{Matched: true})

	want := "a.txt\n1:hello\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStandard_NoHeadingNoContext_NoInterFileSeparator verifies plain
// --no-heading, non-context mode gets no separator at all between
// files, matching rg's observed behavior.
func TestStandard_NoHeadingNoContext_NoInterFileSeparator(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	// Heading and ContextEnabled both left false.

	p.Begin("a.txt")
	p.Matched(&search.Match{Line: []byte("hello"), LineNumber: 1, HasLineNumber: true})
	p.Finish("a.txt", &search.Stats{Matched: true})

	p.Begin("b.txt")
	p.Matched(&search.Match{Line: []byte("hello"), LineNumber: 1, HasLineNumber: true})
	p.Finish("b.txt", &search.Stats{Matched: true})

	want := "a.txt:1:hello\n" + "b.txt:1:hello\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
