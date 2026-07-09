package main

import (
	"bytes"
	"io"
	"sync/atomic"
	"testing"

	"github.com/yackey-labs/gripgrep/glob"
	"github.com/yackey-labs/gripgrep/printer"
	"github.com/yackey-labs/gripgrep/search"
)

// TestStripUTF8BOM_NonBOMPreservesReadBoundary is a regression test for
// task #20's manual-diff finding: io.MultiReader (the original
// implementation) never combines two underlying readers into one Read
// call, so wrapping a peeked-and-put-back 3-byte prefix with it split a
// single-read file into a 3-byte read followed by the rest. That
// artificial split moved a later NUL into a different chunk than an
// unwrapped read would ever produce, which search's BinaryQuit whole-
// chunk-discard (linebuffer.go) is sensitive to -- a 3-byte leading
// fragment of a walk-discovered binary file was wrongly treated as its
// own clean, NUL-free line (caught by TestGoldenVsRipgrep/invert_match
// showing a corrupted "binary.bin:1:nee" match). A single Read on the
// wrapped reader must return everything in one call, exactly like the
// unwrapped source would have.
func TestStripUTF8BOM_NonBOMPreservesReadBoundary(t *testing.T) {
	data := []byte("needle before NUL byte\n\x00needle after NUL byte\n")
	// bytes.Reader.Read already exhibits the same "fill as much of p as
	// possible in one call" behavior a real regular-file read() does
	// (unlike io.MultiReader wrapping two readers, which never combines
	// them into a single call) -- exactly the property this test pins.
	r, err := stripUTF8BOM(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, len(data))
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != len(data) {
		t.Fatalf("first Read returned %d bytes, want all %d in one call (read boundary must match the unwrapped source)", n, len(data))
	}
	if !bytes.Equal(buf[:n], data) {
		t.Fatalf("content mismatch: got %q, want %q", buf[:n], data)
	}
}

func TestStripUTF8BOM_StripsRealBOM(t *testing.T) {
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello\n")...)
	r, err := stripUTF8BOM(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Errorf("got %q, want %q", got, "hello\n")
	}
}

// TestBuildGlobs_PolarityFlip verifies the flipped-polarity encoding
// buildGlobs uses to reuse glob.Set's gitignore-shaped Match result for
// -g's CLI-level "plain = include, '!' = exclude" semantics (see
// buildGlobs's doc, and task #12's resolution): a plain -g pattern must
// match as glob.Whitelisted, a '!'-g pattern must match as glob.Ignored,
// and requireMatch must be true iff at least one plain pattern was
// given.
func TestBuildGlobs_PolarityFlip(t *testing.T) {
	set, requireMatch, err := buildGlobs([]string{"*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !requireMatch {
		t.Error("requireMatch = false, want true (a plain -g pattern was given)")
	}
	if got := set.Match([]byte("main.go"), false); got != glob.Whitelisted {
		t.Errorf("plain -g pattern: got %v, want Whitelisted", got)
	}
	if got := set.Match([]byte("main.txt"), false); got != glob.NoMatch {
		t.Errorf("plain -g pattern, non-matching path: got %v, want NoMatch", got)
	}
}

func TestBuildGlobs_NegatedIsPlainExclude(t *testing.T) {
	set, requireMatch, err := buildGlobs([]string{"!*.md"})
	if err != nil {
		t.Fatal(err)
	}
	if requireMatch {
		t.Error("requireMatch = true, want false (only a negated -g pattern was given, no plain include)")
	}
	if got := set.Match([]byte("README.md"), false); got != glob.Ignored {
		t.Errorf("negated -g pattern: got %v, want Ignored", got)
	}
}

func TestBuildGlobs_MixedRequiresMatchTrue(t *testing.T) {
	_, requireMatch, err := buildGlobs([]string{"!*.md", "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !requireMatch {
		t.Error("requireMatch = false, want true (a plain -g pattern is present alongside a negated one)")
	}
}

func TestBuildGlobs_Empty(t *testing.T) {
	set, requireMatch, err := buildGlobs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if set != nil || requireMatch {
		t.Errorf("buildGlobs(nil) = (%v, %v), want (nil, false)", set, requireMatch)
	}
}

func TestBuildGlobs_InvalidPatternErrors(t *testing.T) {
	// An unclosed alternate group ("{" without "}") is one of the few
	// inputs glob.Builder rejects at Build time.
	if _, _, err := buildGlobs([]string{"a{"}); err == nil {
		t.Error("expected an error for a malformed glob pattern")
	}
}

func TestResolveBinaryMode(t *testing.T) {
	cases := []struct {
		name     string
		cfg      BinaryMode
		explicit bool
		want     search.BinaryMode
	}{
		{"auto walk-discovered", BinaryAuto, false, search.BinaryQuit},
		{"auto explicit", BinaryAuto, true, search.BinaryConvert},
		{"text always none, walk", BinaryAsText, false, search.BinaryNone},
		{"text always none, explicit", BinaryAsText, true, search.BinaryNone},
		{"uuu always convert, walk", BinarySearchAndSuppress, false, search.BinaryConvert},
		{"uuu always convert, explicit", BinarySearchAndSuppress, true, search.BinaryConvert},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveBinaryMode(tc.cfg, tc.explicit); got != tc.want {
				t.Errorf("resolveBinaryMode(%v, %v) = %v, want %v", tc.cfg, tc.explicit, got, tc.want)
			}
		})
	}
}

// TestMatchTracker_BinaryQuitFlushesEarlierMatchesThenWarns is a
// regression test for task #20: real rg's walk-mode BinaryQuit does NOT
// discard the whole file -- matches search's own Searcher already found
// in earlier, NUL-free reads (already sunk into the Standard sink's
// buffer before Finish ever runs) are flushed normally, followed by rg's
// own "WARNING: stopped searching binary file after match..." line
// (verified against the real rg binary on
// ../ripgrep/tests/data/sherlock-nul.txt).
func TestMatchTracker_BinaryQuitFlushesEarlierMatchesThenWarns(t *testing.T) {
	var out bytes.Buffer
	dest := printer.NewDest(&out)
	std := printer.NewStandard(dest)
	std.ShowPath = true

	std.Begin("bin.dat")
	std.Matched(&search.Match{Line: []byte("needle before NUL\n"), LineNumber: 1, HasLineNumber: true})

	var matched atomic.Bool
	tr := &matchTracker{Sink: std, matched: &matched, standard: true, binMode: search.BinaryQuit, showPath: true, dest: dest}
	if err := tr.Finish("bin.dat", &search.Stats{Matched: true, Binary: true, BinaryOffset: 42}); err != nil {
		t.Fatal(err)
	}

	want := "bin.dat:1:needle before NUL\n" +
		`bin.dat: WARNING: stopped searching binary file after match (found "\0" byte around offset 42)` + "\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !matched.Load() {
		t.Error("expected matched to be true (a real match was found in an earlier, NUL-free read)")
	}
}

// TestMatchTracker_BinaryQuitSilentWhenNoMatchAtAll mirrors real rg's
// behavior when the NUL falls in the very first read, so no match was
// ever found at all: totally silent, no warning either (rg's own
// write_binary_message guards on has_match(); verified against the real
// rg binary: a tiny file whose one-and-only read contains a match
// immediately followed by a NUL reports zero matches and prints no
// warning).
func TestMatchTracker_BinaryQuitSilentWhenNoMatchAtAll(t *testing.T) {
	var out bytes.Buffer
	dest := printer.NewDest(&out)
	std := printer.NewStandard(dest)

	std.Begin("bin.dat")
	// No Matched calls at all: the NUL-containing chunk was discarded
	// before anything in it (or before it) was searched.

	var matched atomic.Bool
	tr := &matchTracker{Sink: std, matched: &matched, standard: true, binMode: search.BinaryQuit, showPath: true, dest: dest}
	if err := tr.Finish("bin.dat", &search.Stats{Matched: false, Binary: true, BinaryOffset: 23}); err != nil {
		t.Fatal(err)
	}

	if out.Len() != 0 {
		t.Errorf("expected no output at all, got %q", out.String())
	}
	if matched.Load() {
		t.Error("expected matched to remain false")
	}
}

// TestMatchTracker_BinaryQuitDiscardsNonStandardEvenWithMatch mirrors
// `rg -c`/`rg -l` on a walk-discovered binary file with a real match in
// an earlier read: unlike Standard mode, -c/-l show nothing at all and
// don't count as matched (verified against the real rg binary on
// sherlock-nul.txt: `rg -c sherlock <dir>` and `rg -l sherlock <dir>`
// both print nothing and exit 1, unlike the real count/path they'd show
// for an explicitly-named Convert-mode binary file).
func TestMatchTracker_BinaryQuitDiscardsNonStandardEvenWithMatch(t *testing.T) {
	var out bytes.Buffer
	dest := printer.NewDest(&out)
	c := printer.NewCount(dest)
	c.ShowPath = true

	c.Begin("bin.dat")
	c.Matched(&search.Match{Line: []byte("needle\n")})

	var matched atomic.Bool
	tr := &matchTracker{Sink: c, matched: &matched, standard: false, binMode: search.BinaryQuit, showPath: true, dest: dest}
	if err := tr.Finish("bin.dat", &search.Stats{Matched: true, Binary: true, BinaryOffset: 42}); err != nil {
		t.Fatal(err)
	}

	if out.Len() != 0 {
		t.Errorf("expected no output at all, got %q", out.String())
	}
	if matched.Load() {
		t.Error("expected matched to remain false (-c/-l discard the whole file under BinaryQuit)")
	}
}

// TestMatchTracker_BinaryConvertWritesGenericMessage mirrors
// `rg -n pat explicit-binary-file`: real rg replaces the file's normal
// per-line output with one generic "binary file matches" line. -c/-l
// (standard=false) are unaffected -- see
// TestMatchTracker_NonStandardBinaryConvertPassesThrough.
func TestMatchTracker_BinaryConvertWritesGenericMessage(t *testing.T) {
	var out bytes.Buffer
	dest := printer.NewDest(&out)
	std := printer.NewStandard(dest)

	std.Begin("bin.dat")
	std.Matched(&search.Match{Line: []byte("needle before NUL\n"), LineNumber: 1, HasLineNumber: true})

	var matched atomic.Bool
	tr := &matchTracker{Sink: std, matched: &matched, standard: true, binMode: search.BinaryConvert, showPath: true, dest: dest}
	if err := tr.Finish("bin.dat", &search.Stats{Matched: true, Binary: true, BinaryOffset: 23}); err != nil {
		t.Fatal(err)
	}

	want := `bin.dat: binary file matches (found "\0" byte around offset 23)` + "\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !matched.Load() {
		t.Error("expected matched to be true (a real match occurred)")
	}
}

// TestMatchTracker_NonStandardBinaryConvertPassesThrough verifies -c/-l
// sinks (standard=false) are never overridden by the binary message
// path: rg shows their real count/path exactly as it would for a text
// file.
func TestMatchTracker_NonStandardBinaryConvertPassesThrough(t *testing.T) {
	var out bytes.Buffer
	dest := printer.NewDest(&out)
	c := printer.NewCount(dest)
	c.ShowPath = true

	c.Begin("bin.dat")
	c.Matched(&search.Match{Line: []byte("needle\n")})
	c.Matched(&search.Match{Line: []byte("needle2\n")})

	var matched atomic.Bool
	tr := &matchTracker{Sink: c, matched: &matched, standard: false, binMode: search.BinaryConvert, showPath: true, dest: dest}
	if err := tr.Finish("bin.dat", &search.Stats{Matched: true, Binary: true, BinaryOffset: 23}); err != nil {
		t.Fatal(err)
	}

	want := "bin.dat:2\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q (the real count, not the generic binary message)", got, want)
	}
	if !matched.Load() {
		t.Error("expected matched to be true")
	}
}
