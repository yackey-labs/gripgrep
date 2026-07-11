package printer

import (
	"strings"
	"testing"

	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/search"
)

// The expected byte strings below were verified against the real rg
// 15.1.0 binary (see this round's differential sweep), covering
// -o/--only-matching, -M/--max-columns(-preview), and --trim.

// TestStandard_OnlyMatching mirrors `rg -o -n cat multi.txt`: one row
// per occurrence, the line number repeated for each, text narrowed to
// just the matched substring.
func TestStandard_OnlyMatching(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.OnlyMatching = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Matched(&search.Match{Line: []byte("cat at start"), LineNumber: 3, HasLineNumber: true, Offset: 30})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:cat\n" + "multi.txt:1:cat\n" + "multi.txt:3:cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_OnlyMatchingColumnAndByteOffset mirrors `rg -o --column
// -b cat multi.txt`: column and byte-offset are the OCCURRENCE's own
// position (verified: -b reports the match's own offset, not the
// line's, under -o -- unlike plain -b).
func TestStandard_OnlyMatchingColumnAndByteOffset(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.OnlyMatching = true
	p.Column = true
	p.ByteOffset = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:5:4:cat\n" + "multi.txt:1:13:12:cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_OnlyMatchingInvert mirrors `rg -o -v cat multi.txt`: not
// an error, prints the whole non-matching line with no column, exactly
// like the non-OnlyMatching Invert fallback.
func TestStandard_OnlyMatchingInvert(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.OnlyMatching = true
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

// TestStandard_OnlyMatchingEmptyMatch mirrors `rg -o 'x*' multi.txt`:
// an empty match still gets its own (blank) row, never suppressed.
func TestStandard_OnlyMatchingEmptyMatch(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.OnlyMatching = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"x*"}})

	p.Begin("t.txt")
	p.Matched(&search.Match{Line: []byte("ab"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("t.txt", &search.Stats{Matched: true})

	// "x*" matches empty at positions 0, 1, 2 on "ab" (2 bytes) -- three
	// blank rows, mirroring findSpans' own empty-adjacent suppression
	// (verified against the real rg binary and Go's own FindAllIndex).
	want := "t.txt:1:\n" + "t.txt:1:\n" + "t.txt:1:\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_OnlyMatchingVimgrepWins mirrors `rg -o --vimgrep cat
// multi.txt`: OnlyMatching narrows the TEXT even under Vimgrep -- both
// flags cause per-occurrence rows, but OnlyMatching's "just the match"
// content wins over Vimgrep's "whole line" content (rg's own sink_slow
// checks only_matching before per_match in one if/else-if chain).
func TestStandard_OnlyMatchingVimgrepWins(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.OnlyMatching = true
	p.Vimgrep = true
	p.Column = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:5:cat\n" + "multi.txt:1:13:cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_OnlyMatchingContextUnaffected mirrors `rg -o -A1 cat
// multi.txt`: context lines print WHOLE and untouched, only matched
// lines get the per-occurrence-only-match treatment.
func TestStandard_OnlyMatchingContextUnaffected(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.OnlyMatching = true
	p.ContextEnabled = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Context(&search.Ctx{Line: []byte("no match here"), LineNumber: 2, HasLineNumber: true, Offset: 16})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1:cat\n" + "multi.txt:1:cat\n" + "multi.txt-2-no match here\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_MaxColumnsOmitsLine mirrors `rg -M 15 cat trim.txt`'s
// fast path (no --column/color): a line whose length -- INCLUDING its
// trailing line terminator byte, a quirk of rg's own internal buffer
// convention this round's probes uncovered -- exceeds the limit is
// replaced wholesale, using the plain wording (no span-scan happened, so
// no "N matches" count exists to report).
func TestStandard_MaxColumnsOmitsLine(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MaxColumns = 15

	p.Begin("trim.txt")
	// "   indented cat" is exactly 15 bytes; +1 for its (already-
	// stripped-by-caller, but still counted) line terminator pushes it
	// over the 15-byte limit -- verified against the real rg binary:
	// -M15 omits it, -M16 does not.
	p.Matched(&search.Match{Line: []byte("   indented cat\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Matched(&search.Match{Line: []byte("\tTAB cat\n"), LineNumber: 2, HasLineNumber: true, Offset: 16})
	p.Finish("trim.txt", &search.Stats{Matched: true})

	want := "trim.txt:1:[Omitted long matching line]\n" + "trim.txt:2:\tTAB cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_MaxColumnsWithColumnShowsMatchCount mirrors `rg -M 15
// --column cat trim.txt`: --column forces the span-scanning path, so
// the omission message switches to the "N matches" wording -- NEVER
// tense-adjusted, even at N==1 (verified: rg always says "1 matches",
// not "1 match", outside preview mode).
func TestStandard_MaxColumnsWithColumnShowsMatchCount(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MaxColumns = 15
	p.Column = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("trim.txt")
	p.Matched(&search.Match{Line: []byte("   indented cat\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("trim.txt", &search.Stats{Matched: true})

	want := "trim.txt:1:13:[Omitted long line with 1 matches]\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_MaxColumnsContextWording mirrors `rg -M 15 -A1 cat
// ctx.txt`: an over-long CONTEXT line uses "[Omitted long context
// line]", distinct from a matched line's wording.
func TestStandard_MaxColumnsContextWording(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MaxColumns = 15
	p.ContextEnabled = true

	p.Begin("ctx.txt")
	p.Matched(&search.Match{Line: []byte("short\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Context(&search.Ctx{Line: []byte("this is a very very very long context line indeed\n"), LineNumber: 2, HasLineNumber: true, Offset: 6})
	p.Finish("ctx.txt", &search.Stats{Matched: true})

	want := "ctx.txt:1:short\n" + "ctx.txt-2-[Omitted long context line]\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_MaxColumnsOnlyMatchingStrictBoundary mirrors `rg -M 3 -o
// cat longmatch.txt`: an -o row's boundary is STRICT length > M (no +1
// terminator quirk -- there is no terminator involved in a bare match
// substring), one byte later than the general path's boundary.
func TestStandard_MaxColumnsOnlyMatchingStrictBoundary(t *testing.T) {
	run := func(limit int) string {
		dest, out := newTestDest()
		p := NewStandard(dest)
		p.OnlyMatching = true
		p.MaxColumns = limit
		p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})
		p.Begin("t.txt")
		p.Matched(&search.Match{Line: []byte("xxx cat\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
		p.Finish("t.txt", &search.Stats{Matched: true})
		return out.String()
	}

	if got, want := run(2), "t.txt:1:[Omitted long matching line]\n"; got != want {
		t.Errorf("-M2: got %q want %q", got, want)
	}
	if got, want := run(3), "t.txt:1:cat\n"; got != want {
		t.Errorf("-M3: got %q want %q", got, want)
	}
}

// TestStandard_MaxColumnsAlwaysUnlimitedZero mirrors `rg -M 0 cat
// trim.txt`: 0 means unlimited, not "omit all".
func TestStandard_MaxColumnsAlwaysUnlimitedZero(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MaxColumns = 0

	p.Begin("trim.txt")
	p.Matched(&search.Match{Line: []byte("   indented cat\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("trim.txt", &search.Stats{Matched: true})

	want := "trim.txt:1:   indented cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_MaxColumnsPreview mirrors `rg -M 20
// --max-columns-preview cat trim.txt`'s fast path (no color/column): a
// byte prefix followed by " [... omitted end of long line]" (no span
// count exists, mirroring the non-preview plain wording's condition).
func TestStandard_MaxColumnsPreview(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MaxColumns = 20
	p.MaxColumnsPreview = true

	p.Begin("trim.txt")
	p.Matched(&search.Match{Line: []byte("plain cat with a very long tail abcdefghijklmnopqrstuvwxyz\n"), LineNumber: 3, HasLineNumber: true, Offset: 25})
	p.Finish("trim.txt", &search.Stats{Matched: true})

	want := "trim.txt:3:plain cat with a ver [... omitted end of long line]\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_MaxColumnsPreviewWithColumnCountsRemaining mirrors `rg -M
// 20 --max-columns-preview --column cat multimatch.txt`: --column forces
// the span-scanning path, so the preview switches from "omitted end of
// long line" to "N more matches" (tense-adjusted at N==1, unlike the
// non-preview "N matches" wording).
func TestStandard_MaxColumnsPreviewWithColumnCountsRemaining(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MaxColumns = 20
	p.MaxColumnsPreview = true
	p.Column = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multimatch.txt")
	p.Matched(&search.Match{Line: []byte("catstart catmiddle padding padding padding cat cat end\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("multimatch.txt", &search.Stats{Matched: true})

	want := "multimatch.txt:1:1:catstart catmiddle p [... 2 more matches]\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_MaxColumnsPreviewSingularMatch verifies the preview
// wording's tense adjustment at exactly 1 remaining match: "1 more
// match", not "1 more matches" -- verified against the real rg binary.
func TestStandard_MaxColumnsPreviewSingularMatch(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MaxColumns = 20
	p.MaxColumnsPreview = true
	p.Color = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("onematch.txt")
	p.Matched(&search.Match{Line: []byte("cat padding padding padding cat end\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("onematch.txt", &search.Stats{Matched: true})

	got := out.String()
	if want := " [... 1 more match]\n"; len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Errorf("got:\n%q\nwant suffix:\n%q", got, want)
	}
}

// TestStandard_MaxColumnsPreviewVimgrepColorPerRowCount covers a
// regression this round's re-review caught by reproducing it directly
// against the real rg binary: under `--vimgrep --color=always -M
// --max-columns-preview`, each row's "N more matches" count is
// INDEPENDENT -- whether THAT row's own occurrence happens to still be
// visible within its own preview cutoff -- not the whole line's total
// remaining count. rg's real color-rendering branch threads whatever
// narrowed match list it built for one row (under --vimgrep, just that
// row's own occurrence) through to BOTH highlighting and the remaining-
// count math; only its no-color fallback ignores that narrowing and
// always uses the full line's match list (see
// TestStandard_MaxColumnsPreviewVimgrepNoColorWholeLineCount for that
// contrasting case).
//
// Fixture: "catstart catmiddle padding... cat cat end" has 4
// occurrences at bytes 0, 9, 44, 48. At -M20, the first two are inside
// the 20-byte visible prefix, the last two are past it -- so under
// color, row 1 (own occurrence at 0) and row 2 (own occurrence at 9)
// each say "0 more matches", while row 3 (44) and row 4 (48) each say
// "1 more match" -- NOT "2 more matches" for every row, which is what a
// whole-line count would produce (and what this exact fixture gives
// without color).
func TestStandard_MaxColumnsPreviewVimgrepColorPerRowCount(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Vimgrep = true
	p.Column = true
	p.Color = true
	p.MaxColumns = 20
	p.MaxColumnsPreview = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multimatch.txt")
	p.Matched(&search.Match{Line: []byte("catstart catmiddle padding padding padding cat cat end\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("multimatch.txt", &search.Stats{Matched: true})

	got := out.String()
	wantSuffixes := []string{
		" [... 0 more matches]\n", // row 1: own occurrence (byte 0) visible
		" [... 0 more matches]\n", // row 2: own occurrence (byte 9) visible
		" [... 1 more match]\n",   // row 3: own occurrence (byte 44) not visible
		" [... 1 more match]\n",   // row 4: own occurrence (byte 48) not visible
	}
	for _, want := range wantSuffixes {
		idx := strings.Index(got, want)
		if idx < 0 {
			t.Fatalf("output missing expected row suffix %q; full output:\n%q", want, got)
		}
		got = got[idx+len(want):]
	}
}

// TestStandard_MaxColumnsPreviewVimgrepNoColorWholeLineCount is the
// no-color contrast to TestStandard_MaxColumnsPreviewVimgrepColorPerRowCount:
// without Color, every row on the line reports the SAME whole-line
// remaining count (2 -- the two occurrences past the -M20 cutoff),
// regardless of that row's own occurrence's position -- verified
// against the real rg binary.
func TestStandard_MaxColumnsPreviewVimgrepNoColorWholeLineCount(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Vimgrep = true
	p.Column = true
	p.MaxColumns = 20
	p.MaxColumnsPreview = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multimatch.txt")
	p.Matched(&search.Match{Line: []byte("catstart catmiddle padding padding padding cat cat end\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("multimatch.txt", &search.Stats{Matched: true})

	got := out.String()
	count := strings.Count(got, " [... 2 more matches]\n")
	if count != 4 {
		t.Errorf("expected all 4 rows to say \"2 more matches\", found %d occurrences; full output:\n%q", count, got)
	}
}

// TestStandard_MaxColumnsPreviewMultibyteBoundary mirrors this round's
// probe against the real rg binary: the preview cut never splits a
// UTF-8 rune. "café" 's é here is a single, precomposed U+00E9 (2 bytes,
// but exactly one rune AND one grapheme cluster) -- see
// TestStandard_MaxColumnsPreviewCombiningMarkBoundary for the fixture
// that actually distinguishes rune-counting from grapheme-cluster
// counting (this one alone would pass under either).
func TestStandard_MaxColumnsPreviewMultibyteBoundary(t *testing.T) {
	line := "abcdefghijcafé cat more text here padding padding\n"
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.MaxColumns = 14
	p.MaxColumnsPreview = true

	p.Begin("utf8.txt")
	p.Matched(&search.Match{Line: []byte(line), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("utf8.txt", &search.Stats{Matched: true})

	want := "utf8.txt:1:abcdefghijcafé [... omitted end of long line]\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_MaxColumnsPreviewCombiningMarkBoundary mirrors a
// regression this round's re-review caught by reproducing it directly
// against the real rg binary: "e" + U+0301 COMBINING ACUTE ACCENT (two
// runes, ONE grapheme cluster, rendering as one visual "é") straddling
// the preview cut. A rune-boundary approximation (gg's first attempt at
// previewCutoff) diverged from rg at EVERY cut point from -M 11 through
// -M 16 on this exact fixture -- it counted the bare "e" as a whole
// rune-cluster of its own, landing one grapheme short for the rest of
// the line. previewCutoff now uses github.com/rivo/uniseg's UAX#29
// grapheme-cluster segmentation (the same standard rg's own
// unicode-segmentation dependency implements), matching exactly.
func TestStandard_MaxColumnsPreviewCombiningMarkBoundary(t *testing.T) {
	line := "combining é mark cat and more text here after\n"
	cases := []struct {
		limit int
		want  string
	}{
		{8, "combinin"},
		{9, "combining"},
		{10, "combining "},
		{11, "combining é"},
		{12, "combining é "},
		{13, "combining é m"},
	}
	for _, tc := range cases {
		dest, out := newTestDest()
		p := NewStandard(dest)
		p.MaxColumns = tc.limit
		p.MaxColumnsPreview = true

		p.Begin("combining.txt")
		p.Matched(&search.Match{Line: []byte(line), LineNumber: 1, HasLineNumber: true, Offset: 0})
		p.Finish("combining.txt", &search.Stats{Matched: true})

		want := "combining.txt:1:" + tc.want + " [... omitted end of long line]\n"
		if got := out.String(); got != want {
			t.Errorf("-M %d: got:\n%q\nwant:\n%q", tc.limit, got, want)
		}
	}
}

// TestStandard_MaxColumnsPreviewZWJEmojiBoundary verifies a ZWJ-joined
// multi-codepoint emoji sequence (family: man+ZWJ+woman+ZWJ+girl+ZWJ+boy
// -- 4 codepoints, several ZWJ joiners, ONE grapheme cluster, 25 bytes)
// is kept atomically whole by the preview cut, never split mid-sequence
// -- verified against the real rg binary at the exact cut point where a
// naive per-rune (or per-codepoint) counter would slice into it.
func TestStandard_MaxColumnsPreviewZWJEmojiBoundary(t *testing.T) {
	const family = "\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466"
	line := "start " + family + " cat end text here padding padding\n"

	// Grapheme clusters: s-t-a-r-t-<space>-<family>-<space>-c-a-t-...
	// The family emoji is cluster #7 -- excluded entirely at -M6 (only
	// "start " survives), included WHOLE at -M7 (all 25 of its bytes at
	// once, no partial cut).
	dest6, out6 := newTestDest()
	p6 := NewStandard(dest6)
	p6.MaxColumns = 6
	p6.MaxColumnsPreview = true
	p6.Begin("zwj.txt")
	p6.Matched(&search.Match{Line: []byte(line), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p6.Finish("zwj.txt", &search.Stats{Matched: true})
	if want, got := "zwj.txt:1:start  [... omitted end of long line]\n", out6.String(); got != want {
		t.Errorf("-M 6: got:\n%q\nwant:\n%q", got, want)
	}

	dest7, out7 := newTestDest()
	p7 := NewStandard(dest7)
	p7.MaxColumns = 7
	p7.MaxColumnsPreview = true
	p7.Begin("zwj.txt")
	p7.Matched(&search.Match{Line: []byte(line), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p7.Finish("zwj.txt", &search.Stats{Matched: true})
	if want, got := "zwj.txt:1:start "+family+" [... omitted end of long line]\n", out7.String(); got != want {
		t.Errorf("-M 7: got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_Trim mirrors `rg --trim cat trim.txt`: leading ASCII
// whitespace stripped from every printed line, matched and context
// alike.
func TestStandard_Trim(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Trim = true
	p.ContextEnabled = true

	p.Begin("trim.txt")
	p.Matched(&search.Match{Line: []byte("   indented cat\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Context(&search.Ctx{Line: []byte("\tTAB context\n"), LineNumber: 2, HasLineNumber: true, Offset: 16})
	p.Finish("trim.txt", &search.Stats{Matched: true})

	want := "trim.txt:1:indented cat\n" + "trim.txt-2-TAB context\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_TrimColumnUnaffected mirrors `rg --trim --column cat
// trim.txt`: the reported column is the position in the UNTRIMMED line,
// never recomputed against the shorter, trimmed printed text.
func TestStandard_TrimColumnUnaffected(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Trim = true
	p.Column = true
	p.ByteOffset = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("trim.txt")
	p.Matched(&search.Match{Line: []byte("   indented cat\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("trim.txt", &search.Stats{Matched: true})

	// Column 13 and byte offset 0 are BOTH computed against the original
	// "   indented cat" line (cat starts at 1-indexed column 13, line
	// starts at byte 0) -- untouched by trimming the printed text down
	// to "indented cat".
	want := "trim.txt:1:13:0:indented cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_TrimAppliesBeforeMaxColumns mirrors `rg --trim -M 15 cat
// trim.txt`: trimming happens BEFORE the length check, so a line that
// would otherwise be omitted can survive once its leading whitespace no
// longer counts toward the limit.
func TestStandard_TrimAppliesBeforeMaxColumns(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Trim = true
	p.MaxColumns = 15

	p.Begin("trim.txt")
	// "   indented cat\n" is 16 bytes (with terminator) untrimmed --
	// would be omitted at -M15 (see TestStandard_MaxColumnsOmitsLine).
	// Trimmed to "indented cat\n" (13 bytes), it survives.
	p.Matched(&search.Match{Line: []byte("   indented cat\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("trim.txt", &search.Stats{Matched: true})

	want := "trim.txt:1:indented cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestStandard_TrimOnlyMatchingLeadingSpaceInMatch mirrors `rg --trim -o
// -e ' cat' trim.txt`: --trim also strips leading whitespace from the
// printed text when it's the MATCH substring itself (under -o) that
// starts with whitespace -- not just a line's own natural indentation.
func TestStandard_TrimOnlyMatchingLeadingSpaceInMatch(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)
	p.Trim = true
	p.OnlyMatching = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{" cat"}})

	p.Begin("trim.txt")
	p.Matched(&search.Match{Line: []byte("   indented cat\n"), LineNumber: 1, HasLineNumber: true, Offset: 0})
	p.Finish("trim.txt", &search.Stats{Matched: true})

	want := "trim.txt:1:cat\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestCount_OnlyMatchingCountsOccurrences mirrors `rg -o -c cat
// multi.txt`: counts OCCURRENCES (3), not matched LINES (2).
func TestCount_OnlyMatchingCountsOccurrences(t *testing.T) {
	dest, out := newTestDest()
	p := NewCount(dest)
	p.OnlyMatching = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("one cat two cat\n")})
	p.Matched(&search.Match{Line: []byte("cat at start\n")})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:3\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestCount_OnlyMatchingInvertFallsBackToLineCount mirrors `rg -o -c -v
// cat multi.txt`: -o's occurrence-counting carve-out does NOT extend to
// an inverted search -- it counts lines (1), matching plain `-c -v`,
// not 0 (Matcher legitimately finds zero spans on a genuinely
// non-matching inverted line).
func TestCount_OnlyMatchingInvertFallsBackToLineCount(t *testing.T) {
	dest, out := newTestDest()
	p := NewCount(dest)
	p.OnlyMatching = true
	p.Matcher = mustMatcher(t, match.Config{Patterns: []string{"cat"}})

	p.Begin("multi.txt")
	p.Matched(&search.Match{Line: []byte("no match here\n")})
	p.Finish("multi.txt", &search.Stats{Matched: true})

	want := "multi.txt:1\n"
	if got := out.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}
