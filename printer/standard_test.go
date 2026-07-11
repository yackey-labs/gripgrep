package printer

import (
	"bytes"
	"testing"

	"github.com/yackey-labs/gripgrep/search"
)

// The expected byte strings in this file were captured by running
// `rg` directly against testdata/corpus/a/b/foo.txt (see foo.txt's
// contents below) and copying its exact stdout bytes, per M1-printer's
// golden-test mandate. foo.txt:
//
//	1: hello world
//	2: this is a plain test file used by the golden test harness
//	3: the cat sat on the mat
//	4: CATERPILLAR should not match a whole-word search for "cat"
//	5: another line without the needle

const (
	fooLine1 = "hello world"
	fooLine2 = "this is a plain test file used by the golden test harness"
	fooLine3 = "the cat sat on the mat"
	fooLine4 = `CATERPILLAR should not match a whole-word search for "cat"`
	fooLine5 = "another line without the needle"
)

func newTestDest() (*Dest, *bytes.Buffer) {
	var buf bytes.Buffer
	return NewDest(&buf), &buf
}

// TestStandard_PipedBasic mirrors `rg -n cat a/b/foo.txt`: plain
// "path:line:text" per match, no color, no context.
func TestStandard_PipedBasic(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)

	if _, err := p.Begin("a/b/foo.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Matched(&search.Match{Line: []byte(fooLine3), LineNumber: 3, HasLineNumber: true, Offset: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Matched(&search.Match{Line: []byte(fooLine4), LineNumber: 4, HasLineNumber: true, Offset: 0}); err != nil {
		t.Fatal(err)
	}
	if err := p.Finish("a/b/foo.txt", &search.Stats{Matched: true, MatchCount: 2}); err != nil {
		t.Fatal(err)
	}

	want := "a/b/foo.txt:3:" + fooLine3 + "\n" + "a/b/foo.txt:4:" + fooLine4 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_LineIncludesTerminator is a regression test for an M2
// integration bug: search.Searcher's real Match/Ctx.Line values include
// their trailing '\n' (search's line-boundary scan treats the
// terminator as part of the line -- see search/lines.go's lineStep doc),
// but Standard's own unit tests (like TestStandard_PipedBasic above) all
// feed terminator-free fixture strings, which silently masked a
// double-newline bug (and matching wrong gap-detection math) that only
// showed up once cmd/gg wired a real Searcher in front of a real
// Standard. Feed a Line WITH the terminator here, matching what the
// Searcher really sends.
func TestStandard_LineIncludesTerminator(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)

	if _, err := p.Begin("a/b/foo.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Matched(&search.Match{Line: []byte(fooLine3 + "\n"), LineNumber: 3, HasLineNumber: true, Offset: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Matched(&search.Match{Line: []byte(fooLine4 + "\n"), LineNumber: 4, HasLineNumber: true, Offset: int64(len(fooLine3) + 1)}); err != nil {
		t.Fatal(err)
	}
	if err := p.Finish("a/b/foo.txt", &search.Stats{Matched: true, MatchCount: 2}); err != nil {
		t.Fatal(err)
	}

	want := "a/b/foo.txt:3:" + fooLine3 + "\n" + "a/b/foo.txt:4:" + fooLine4 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q (must not double the newline, and must not treat contiguous lines as a gap)", got, want)
	}
}

// TestStandard_NoLineNumbers mirrors `rg --no-line-number cat
// a/b/foo.txt`: "path:text" with no line number field at all.
func TestStandard_NoLineNumbers(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine3), HasLineNumber: false})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt:" + fooLine3 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStandard_ZeroMatches verifies a file with no matches produces
// literally no output (Dest.Write must never be called with anything).
func TestStandard_ZeroMatches(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)

	p.Begin("a/b/foo.txt")
	p.Finish("a/b/foo.txt", &search.Stats{Matched: false})

	if out.Len() != 0 {
		t.Errorf("expected no output for a zero-match file, got %q", out.String())
	}
}

// TestStandard_ContextGap mirrors `rg -n -A1 -B1 "hello|another"
// a/b/foo.txt`:
//
//	1:hello world
//	2-this is a plain test file used by the golden test harness
//	--
//	4-CATERPILLAR should not match a whole-word search for "cat"
//	5:another line without the needle
func TestStandard_ContextGap(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.ContextEnabled = true

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine1), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Context(&search.Ctx{Line: []byte(fooLine2), LineNumber: 2, HasLineNumber: true, Offset: int64(len(fooLine1) + 1), After: true})
	p.Context(&search.Ctx{Line: []byte(fooLine4), LineNumber: 4, HasLineNumber: true, Offset: 999, After: false})
	p.Matched(&search.Match{Line: []byte(fooLine5), LineNumber: 5, HasLineNumber: true, Offset: 999 + int64(len(fooLine4)) + 1})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt:1:" + fooLine1 + "\n" +
		"a/b/foo.txt-2-" + fooLine2 + "\n" +
		"--\n" +
		"a/b/foo.txt-4-" + fooLine4 + "\n" +
		"a/b/foo.txt:5:" + fooLine5 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_NoGapSeparatorWithoutContext verifies the "--" is only
// ever emitted when ContextEnabled is true, matching rg's observed
// behavior: two widely-separated matches in default mode never get a
// separator, only in -A/-B/-C mode (verified empirically: `rg -n
// "hello|another" a/b/foo.txt` prints lines 1 and 5 back to back with
// no "--" in between).
func TestStandard_NoGapSeparatorWithoutContext(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	// ContextEnabled left false (the default zero value).

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine1), LineNumber: 1, HasLineNumber: true})
	p.Matched(&search.Match{Line: []byte(fooLine5), LineNumber: 5, HasLineNumber: true})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt:1:" + fooLine1 + "\n" + "a/b/foo.txt:5:" + fooLine5 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q (unexpected \"--\" without context enabled)", got, want)
	}
}

// TestStandard_CustomFieldSeparators covers --field-match-
// separator/--field-context-separator: replaces every ':'/'-' prelude
// separator (including the path's own, when Null is false) with the
// custom value, on match and context lines respectively -- verified
// against the real rg binary (the differential sweep).
func TestStandard_CustomFieldSeparators(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MatchFieldSep = []byte("|")
	p.ContextFieldSep = []byte("%")
	p.ContextEnabled = true

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine1), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Context(&search.Ctx{Line: []byte(fooLine2), LineNumber: 2, HasLineNumber: true, Offset: int64(len(fooLine1) + 1), After: true})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt|1|" + fooLine1 + "\n" +
		"a/b/foo.txt%2%" + fooLine2 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_CustomFieldSeparatorsWithNull covers Null taking
// precedence over MatchFieldSep for the path field specifically, while
// every other field keeps using the custom separator -- verified
// against the real rg binary: `--field-match-separator='|' --null -H
// -n` renders "path\x00N|text", NUL for the path only.
func TestStandard_CustomFieldSeparatorsWithNull(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MatchFieldSep = []byte("|")
	p.Null = true

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine1), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt\x001|" + fooLine1 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_CustomGapSeparator covers --context-separator:
// replaces "--" with a custom value, both for the intra-file gap and the
// inter-file separator -- verified against the real rg binary.
func TestStandard_CustomGapSeparator(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.ContextEnabled = true
	p.GapSeparator = []byte("SEP")

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine1), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Context(&search.Ctx{Line: []byte(fooLine4), LineNumber: 4, HasLineNumber: true, Offset: 999})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt:1:" + fooLine1 + "\n" +
		"SEP\n" +
		"a/b/foo.txt-4-" + fooLine4 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_NoGapSeparatorDisablesEntirely covers --no-context-
// separator: GapSeparator == nil means NO separator line at all --
// neither the intra-file gap marker nor the inter-file one -- verified
// against the real rg binary (the differential sweep: files run
// together with no blank line and no "--" at all, unlike
// --context-separator='' which still inserts a bare line break).
func TestStandard_NoGapSeparatorDisablesEntirely(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.ContextEnabled = true
	p.GapSeparator = nil // --no-context-separator

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine1), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Context(&search.Ctx{Line: []byte(fooLine4), LineNumber: 4, HasLineNumber: true, Offset: 999})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt:1:" + fooLine1 + "\n" +
		"a/b/foo.txt-4-" + fooLine4 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q (--no-context-separator must suppress the gap marker entirely)", got, want)
	}
}

// TestStandard_ContextGapNoLineNumbers verifies gap detection falls
// back to Offset-based contiguity when line numbers are disabled,
// mirroring rg -C1 --no-line-number's separator placement.
func TestStandard_ContextGapNoLineNumbers(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.ContextEnabled = true

	// Line values carry their trailing '\n', exactly as the searcher
	// delivers them (line terminators are part of the line) -- the
	// offset-based gap check spans the full delivered length, so the
	// terminator byte must be present for contiguous offsets to line up.
	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine1 + "\n"), Offset: 0})
	p.Context(&search.Ctx{Line: []byte(fooLine2 + "\n"), Offset: int64(len(fooLine1) + 1), After: true})
	// Gap: skip line 3 entirely, jump straight to line 4 at some later offset.
	p.Context(&search.Ctx{Line: []byte(fooLine4 + "\n"), Offset: 5000})
	p.Matched(&search.Match{Line: []byte(fooLine5 + "\n"), Offset: 5000 + int64(len(fooLine4)) + 1})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt:" + fooLine1 + "\n" +
		"a/b/foo.txt-" + fooLine2 + "\n" +
		"--\n" +
		"a/b/foo.txt-" + fooLine4 + "\n" +
		"a/b/foo.txt:" + fooLine5 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_Heading mirrors `rg -n -H --heading cat a/b/foo.txt`:
// the path is printed once above the matches, not per line.
func TestStandard_Heading(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Heading = true

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine3), LineNumber: 3, HasLineNumber: true})
	p.Matched(&search.Match{Line: []byte(fooLine4), LineNumber: 4, HasLineNumber: true})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt\n" + "3:" + fooLine3 + "\n" + "4:" + fooLine4 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_ShowPathFalse mirrors rg's single-explicit-file
// suppression of the path prefix.
func TestStandard_ShowPathFalse(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.ShowPath = false

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine3), LineNumber: 3, HasLineNumber: true})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "3:" + fooLine3 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStandard_Color mirrors `rg -n --color=always --no-heading cat
// a/b/foo.txt`'s captured byte sequence exactly:
//
//	\x1b[0m\x1b[35ma/b/foo.txt\x1b[0m:\x1b[0m\x1b[32m3\x1b[0m:the \x1b[0m\x1b[1m\x1b[31mcat\x1b[0m sat on the mat
func TestStandard_Color(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Color = true
	p.Matcher = &literalMatcher{lit: []byte("cat")}

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine3), LineNumber: 3, HasLineNumber: true})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "\x1b[0m\x1b[35ma/b/foo.txt\x1b[0m:" +
		"\x1b[0m\x1b[32m3\x1b[0m:" +
		"the \x1b[0m\x1b[1m\x1b[31mcat\x1b[0m sat on the mat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_ColorMultipleMatchesPerLine mirrors rg coloring every
// occurrence on a line, not just the leftmost (verified against `rg
// --color=always -n cat` on a line containing "cat" three times).
func TestStandard_ColorMultipleMatchesPerLine(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Color = true
	p.Matcher = &literalMatcher{lit: []byte("cat")}

	p.Begin("t.txt")
	p.Matched(&search.Match{Line: []byte("cat cat cat"), LineNumber: 1, HasLineNumber: true})
	p.Finish("t.txt", &search.Stats{Matched: true})

	c := "\x1b[0m\x1b[1m\x1b[31mcat\x1b[0m"
	want := "\x1b[0m\x1b[35mt.txt\x1b[0m:\x1b[0m\x1b[32m1\x1b[0m:" + c + " " + c + " " + c + "\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_ColorFullyElidedWithoutColor verifies Matcher.Find is
// never called when Color is false, per the design mandate that color
// work must be FULLY elided (not merely a no-op) on the piped path.
func TestStandard_ColorFullyElidedWithoutColor(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Matcher = &panicOnFindMatcher{}
	// Color left false.

	p.Begin("a/b/foo.txt")
	if _, err := p.Matched(&search.Match{Line: []byte(fooLine3), LineNumber: 3, HasLineNumber: true}); err != nil {
		t.Fatal(err)
	}
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt:3:" + fooLine3 + "\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

type panicOnFindMatcher struct{ literalMatcher }

func (m *panicOnFindMatcher) Find(line []byte) (int, int, bool) {
	panic("Find must not be called when Color is disabled")
}

// TestStandard_MultiFileSequential drives two files through the same
// Standard instance (as a reused per-worker printer would). With
// ContextEnabled, Finish now also inserts rg's between-file "--" (via
// Dest.WriteBlock's interFileSeparator, see #14) — so the combined
// output legitimately gets a second "--" between first.txt's last line
// and second.txt's first. To make sure that isn't accidentally coming
// from *leaked* within-file gap-tracking state instead (the bug this
// test originally guarded against), it inspects Dest's actual per-Write
// chunks directly: first.txt's own block must contain exactly the
// within-file "--" (lines 1 and 9 are non-contiguous), and second.txt's
// own block must contain no "--" at all — proving Begin reset the gap
// tracker for the new file, and any separator between the two chunks
// came from WriteBlock, not a leaked lastLine/haveLast value.
func TestStandard_MultiFileSequential(t *testing.T) {
	rw := &recordingWriter{}
	dest := NewDest(rw)
	p := NewStandard(dest)
	p.ContextEnabled = true

	p.Begin("first.txt")
	p.Matched(&search.Match{Line: []byte("alpha"), LineNumber: 1, HasLineNumber: true})
	p.Matched(&search.Match{Line: []byte("beta"), LineNumber: 9, HasLineNumber: true})
	p.Finish("first.txt", &search.Stats{Matched: true})

	p.Begin("second.txt")
	p.Matched(&search.Match{Line: []byte("gamma"), LineNumber: 1, HasLineNumber: true})
	p.Finish("second.txt", &search.Stats{Matched: true})

	// Two blocks means two WriteBlock calls; the second one additionally
	// triggers a separator write beforehand (recorded as its own chunk).
	if len(rw.chunks) != 3 {
		t.Fatalf("got %d Write calls, want 3 (first.txt block, separator, second.txt block); chunks: %q", len(rw.chunks), rw.chunks)
	}

	wantFirst := "first.txt:1:alpha\n--\nfirst.txt:9:beta\n"
	wantSep := "--\n"
	wantSecond := "second.txt:1:gamma\n"

	if got := string(rw.chunks[0]); got != wantFirst {
		t.Errorf("first.txt block: got %q, want %q", got, wantFirst)
	}
	if got := string(rw.chunks[1]); got != wantSep {
		t.Errorf("inter-file separator: got %q, want %q", got, wantSep)
	}
	if got := string(rw.chunks[2]); got != wantSecond {
		t.Errorf("second.txt block: got %q, want %q (a leading \"--\" here would mean gap-tracking state leaked across Begin)", got, wantSecond)
	}
}

// TestStandard_HeadingShowPathFalse mirrors the `rg --heading -I`
// (gg's -I clears ShowPath, --heading sets Heading): the per-file path
// header line must be dropped, but the blank-line separator BETWEEN file
// groups must still appear -- interFileSeparator only reads p.Heading,
// never p.ShowPath (see its doc). Verified against the real rg 15.1.0
// binary (the handoff differential sweep).
func TestStandard_HeadingShowPathFalse(t *testing.T) {
	rw := &recordingWriter{}
	dest := NewDest(rw)
	p := NewStandard(dest)
	p.Heading = true
	p.ShowPath = false

	p.Begin("first.txt")
	p.Matched(&search.Match{Line: []byte("alpha"), LineNumber: 1, HasLineNumber: true})
	p.Finish("first.txt", &search.Stats{Matched: true})

	p.Begin("second.txt")
	p.Matched(&search.Match{Line: []byte("beta"), LineNumber: 1, HasLineNumber: true})
	p.Finish("second.txt", &search.Stats{Matched: true})

	if len(rw.chunks) != 3 {
		t.Fatalf("got %d Write calls, want 3 (first.txt block, blank-line separator, second.txt block); chunks: %q", len(rw.chunks), rw.chunks)
	}
	wantFirst := "1:alpha\n"
	wantSep := "\n"
	wantSecond := "1:beta\n"
	if got := string(rw.chunks[0]); got != wantFirst {
		t.Errorf("first.txt block: got %q, want %q (no path header -- ShowPath is false)", got, wantFirst)
	}
	if got := string(rw.chunks[1]); got != wantSep {
		t.Errorf("inter-file separator: got %q, want %q (heading mode's blank line must survive -I)", got, wantSep)
	}
	if got := string(rw.chunks[2]); got != wantSecond {
		t.Errorf("second.txt block: got %q, want %q", got, wantSecond)
	}
}

// TestStandard_HeadingNoTrailingSeparatorAfterLastGroup confirms Dest's
// hasPrinted-gated separator never appears after the LAST heading group
// (only BETWEEN groups) -- a single Begin/Finish must produce no
// trailing blank line at all.
func TestStandard_HeadingNoTrailingSeparatorAfterLastGroup(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Heading = true

	p.Begin("only.txt")
	p.Matched(&search.Match{Line: []byte("alpha"), LineNumber: 1, HasLineNumber: true})
	p.Finish("only.txt", &search.Stats{Matched: true})

	want := "only.txt\n1:alpha\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q (no leading/trailing blank line for a single group)", got, want)
	}
}
