package main

import (
	"bytes"
	"sync/atomic"
	"testing"

	"github.com/yackey-labs/gripgrep/glob"
	"github.com/yackey-labs/gripgrep/printer"
	"github.com/yackey-labs/gripgrep/search"
)

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

// TestMatchTracker_BinaryQuitDiscardsEverything mirrors real rg's
// default recursive-walk behavior for a binary file: the whole file's
// output is discarded and it doesn't count as a match, even though the
// underlying Standard sink already buffered a real matched line before
// Finish was called (see matchTracker.Finish's doc).
func TestMatchTracker_BinaryQuitDiscardsEverything(t *testing.T) {
	var out bytes.Buffer
	dest := printer.NewDest(&out)
	std := printer.NewStandard(dest)

	std.Begin("bin.dat")
	std.Matched(&search.Match{Line: []byte("needle before NUL\n"), LineNumber: 1, HasLineNumber: true})

	var matched atomic.Bool
	tr := &matchTracker{Sink: std, matched: &matched, standard: true, binMode: search.BinaryQuit, dest: dest}
	if err := tr.Finish("bin.dat", &search.Stats{Matched: true, Binary: true, BinaryOffset: 42}); err != nil {
		t.Fatal(err)
	}

	if out.Len() != 0 {
		t.Errorf("expected no output at all, got %q", out.String())
	}
	if matched.Load() {
		t.Error("expected matched to remain false (BinaryQuit discards the file entirely)")
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
