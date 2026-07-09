//go:build e2e

// This file is only compiled with -tags e2e; it drives both gg and rg
// over testdata/corpus and diffs their output. Every case is t.Skip'd
// until M2 wires cmd/gg's real flag matrix — see PLAN.md's "Definition
// of done (v1)": byte-identical output to rg on this golden matrix.
//
// Parallel search in both gg and rg completes files in nondeterministic
// order, so raw stdout is NOT byte-comparable across runs even when both
// tools are correct. Per PLAN.md's M0/M2 addenda, this harness
// sort-normalizes each tool's output line-by-line before diffing (rather
// than forcing -j1, so the default parallel path is what's exercised).
// Exit codes are still compared exactly, unnormalized.
//
// Known blind spot for M2: sort-normalization checks line-set (multiset)
// membership only, not ordering or grouping. The "context" (-C) case
// will therefore not catch mis-ordered or mis-grouped context lines —
// only a wrong set of lines. If context-block structure ever needs its
// own correctness gate, add a second, order-sensitive comparison
// specifically for context cases rather than relying on this harness.
package gripgrep_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestGoldenVsRipgrep(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Dir(thisFile)
	corpus := filepath.Join(root, "testdata", "corpus")

	ggBin := buildGG(t, root)

	cases := []struct {
		name string
		args []string
	}{
		{"literal", []string{"-n", "hello", corpus}},
		{"case_insensitive", []string{"-n", "-i", "HELLO", corpus}},
		{"regex_alternation", []string{"-n", "hello|needle", corpus}},
		{"word_boundary", []string{"-n", "-w", "cat", corpus}},
		{"invert_match", []string{"-n", "-v", "hello", corpus}},
		{"count_mode", []string{"-c", "hello", corpus}},
		{"files_with_matches", []string{"-l", "hello", corpus}},
		{"hidden_excluded_by_default", []string{"-n", "secret", corpus}},
		{"hidden_included", []string{"-n", "--hidden", "secret", corpus}},
		{"gitignore_respected", []string{"-n", "secret", corpus}},
		{"no_ignore", []string{"-n", "--no-ignore", "secret", corpus}},
		{"context", []string{"-n", "-C", "1", "hello", corpus}},
		{"binary_quit_by_default", []string{"-n", "needle", filepath.Join(corpus, "binary.bin")}},
		{"binary_text_mode", []string{"-n", "-a", "needle", filepath.Join(corpus, "binary.bin")}},
		{"unicode_content", []string{"-n", "Привет", corpus}},
		{"long_line_over_64kb", []string{"-n", "needle", filepath.Join(corpus, "longline.txt")}},
		{"crlf_line_endings", []string{"-n", "needle", filepath.Join(corpus, "crlf.txt")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}

			// Only stdout is diffed: stderr carries tool-specific
			// diagnostics (e.g. rg's "binary file matches" note) that rg
			// and gg have no obligation to phrase identically, and
			// folding it into the comparison would reintroduce exactly
			// the flakiness sort-normalization is meant to remove.
			rgLines := sortedLines(rgOut)
			ggLines := sortedLines(ggOut)
			if diff := diffLines(rgLines, ggLines); diff != "" {
				t.Errorf("sort-normalized stdout mismatch (order-independent line diff):\n%s\n--- raw rg stdout ---\n%s\n--- raw gg stdout ---\n%s\n--- rg stderr ---\n%s\n--- gg stderr ---\n%s",
					diff, rgOut, ggOut, rgErr, ggErr)
			}
		})
	}
}

// TestGoldenVsRipgrep_ContextOrdering closes the sort-normalization
// blind spot this file's top comment documents: TestGoldenVsRipgrep's
// "context" case only proves gg and rg produce the same *set* of lines,
// not that "--" block boundaries and within-block ordering match.
//
// This deliberately targets a single explicit file (not a directory):
// with more than one file in play, gg's and rg's walk order can
// legitimately differ file-to-file even at -j1 (each tool's own
// unsorted-readdir traversal strategy, not a correctness contract --
// verified empirically: pinning -j1 over testdata/corpus still
// reordered which *file* came first between the two tools, which would
// make a byte-for-byte multi-file comparison flaky for a reason that has
// nothing to do with context-block correctness). A single file removes
// that variable entirely, so a raw byte-for-byte diff here can only mean
// a real within-file context/grouping bug -- exactly what this test
// exists to catch.
//
// The fixture has two matches far enough apart that -C1 must produce two
// separate blocks joined by "--": lines 1-2 (match then trailing
// context) and lines 5-6 (leading context then match), with lines 3-4
// excluded from both.
func TestGoldenVsRipgrep_ContextOrdering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocks.txt")
	content := "hello block one\n" +
		"context after one\n" +
		"filler A\n" +
		"filler B\n" +
		"context before two\n" +
		"hello block two\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	args := []string{"-j1", "-n", "-C", "1", "hello", path}

	rgOut, rgErr, rgCode := run(t, "rg", args)
	ggOut, ggErr, ggCode := run(t, ggBin, args)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
	}
}

// TestGoldenVsRipgrep_MmapExplicitFile targets M3's mmap wiring
// (cmd/gg/mmap.go) directly: an explicitly-named single file (not a
// directory) is exactly the case mmapEligible's default (Auto) policy
// turns on for both gg and real rg (crates/core/flags/hiargs.rs: mmap
// when <=10 paths are given and every one is a regular file) -- so this
// drives gg's SearchBytes-over-syscall.Mmap path against rg's own
// memory-mapped SliceByLine path, not just gg's default streaming Search
// path (which is what every *directory*-rooted case in this file already
// exercises instead).
//
// Deliberately well-defined (non-binary) input: a plain text file with
// several matches spread across more than one DefaultBufferSize's worth
// of content, so the comparison validates exactly what mmap wiring is
// for (correct results via a memory-mapped read path instead of
// buffered reads) without any binary-detection edge case entangled in
// it. Explicit files with a NUL byte surfaced a separate, real
// discrepancy between gg and rg (in both the streaming *and* mmap
// paths, i.e. mmap-independent) during this same investigation -- that
// is tracked and tested as its own issue rather than folded in here;
// see the team communication log for M3's mmap task, not this file.
//
// Explicit --mmap and --no-mmap are both exercised (rather than relying
// on mmapEligible's default for one of them) to remove any doubt that
// the mmap-specific code path, not just the same-answer streaming path,
// is what's being compared.
func TestGoldenVsRipgrep_MmapExplicitFile(t *testing.T) {
	dir := t.TempDir()
	var content []byte
	line := []byte("the quick brown fox jumps over the lazy dog\n")
	for i := 0; len(content) < 200000; i++ {
		content = append(content, line...)
		if i%997 == 0 {
			content = append(content, []byte("mmaptest_needle marks a match here\n")...)
		}
	}
	path := filepath.Join(dir, "mmaptext.txt")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	for _, mode := range []string{"--mmap", "--no-mmap"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"-j1", "-n", mode, "mmaptest_needle", path}

			rgOut, rgErr, rgCode := run(t, "rg", args)
			ggOut, ggErr, ggCode := run(t, ggBin, args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if !bytes.Equal(rgOut, ggOut) {
				t.Errorf("raw stdout mismatch (%s):\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", mode, rgOut, ggOut)
			}
		})
	}
}

// TestGoldenVsRipgrep_BinaryMatchBeforeNUL is a regression test for
// task #20: a walk-discovered (not explicitly-named) binary file whose
// first NUL byte comes well after a real match must still show that
// match, followed by rg's "WARNING: stopped searching binary file after
// match..." line -- NOT the total, silent exclusion gg used to apply to
// every BinaryQuit file regardless of where its matches fell (verified
// against the real rg binary on ../ripgrep/tests/data/sherlock-nul.txt,
// which has this same shape: real matches print, then that exact
// warning).
//
// The fixture is deliberately larger than search.DefaultBufferSize
// (64KB) with its one match on line 1 and its NUL near the very end, so
// the match and the NUL fall in different underlying reads even in the
// real gg binary (not just in a search-package unit test with a
// shrunk buffer). It lives in its own temp directory rather than
// testdata/corpus deliberately: a pooled search.Searcher's read buffer
// can grow permanently past 64KB after handling corpus/longline.txt (a
// single line forcing ensureCapacity's doubling), which would then pull
// this fixture's NUL into the SAME oversized read as its match --
// mirroring how rg's own eager buffer reuse works, but making the
// pass/fail of this specific case depend on incidental walk/worker
// ordering rather than the behavior under test. See -j1's own
// per-worker-buffer-growth caveat in TestGoldenVsRipgrep_ContextOrdering.
func TestGoldenVsRipgrep_BinaryMatchBeforeNUL(t *testing.T) {
	dir := t.TempDir()
	var content []byte
	content = append(content, "chunktest_matchbeforenul first line\n"...)
	filler := []byte("filler filler filler filler filler filler filler filler\n")
	for len(content) < 70000 {
		content = append(content, filler...)
	}
	content = append(content, 0)
	content = append(content, "chunktest_matchbeforenul after nul should not appear in quit mode\n"...)
	if err := os.WriteFile(filepath.Join(dir, "chunkbinary.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	args := []string{"-j1", "-n", "chunktest_matchbeforenul", dir}

	rgOut, rgErr, rgCode := run(t, "rg", args)
	ggOut, ggErr, ggCode := run(t, ggBin, args)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("raw stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
	}
}

// TestGoldenVsRipgrep_ExplicitFileBinaryMidLineNUL is task #21's
// regression test: an explicitly-named file (not a directory) whose
// NUL byte lands in the *middle* of what SearchBytes/mmap treats as one
// long "line" (no newline immediately before it) -- the realistic shape
// for actual binary content, which has no reason to respect text line
// boundaries. Established empirically against the installed rg binary
// via an offset sweep (60000/65000/65536/65600/70000, both --mmap and
// --no-mmap): matches whose own line reaches or crosses the NUL's
// offset must be suppressed exactly like ones entirely after it, not
// just ones that start after it -- gg's matchTracker originally only
// checked line-start, which let a "filler...<NUL>needle after" line
// (one real line straddling the NUL, since the streaming path's NUL
// rewrite doesn't apply to SearchBytes's read-only slice) through
// uncaught.
func TestGoldenVsRipgrep_ExplicitFileBinaryMidLineNUL(t *testing.T) {
	var content []byte
	content = append(content, "midlinenul_needle at the very first line\n"...)
	filler := []byte("filler filler filler filler filler filler filler filler\n")
	for len(content) < 70000 {
		content = append(content, filler...)
	}
	// Deliberately mid-line: no trailing '\n' was just written, so the
	// NUL (and everything up to the next real '\n') shares one line
	// with whatever filler content precedes it.
	content = content[:70000]
	content = append(content, 0)
	content = append(content, "midlinenul_needle right after the nul, same broken line\n"...)

	dir := t.TempDir()
	path := filepath.Join(dir, "midlinenul.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	for _, mode := range []string{"--mmap", "--no-mmap"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"-j1", "-n", mode, "midlinenul_needle", path}

			rgOut, rgErr, rgCode := run(t, "rg", args)
			ggOut, ggErr, ggCode := run(t, ggBin, args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if !bytes.Equal(rgOut, ggOut) {
				t.Errorf("raw stdout mismatch (%s):\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", mode, rgOut, ggOut)
			}
		})
	}

	// Same fixture, -c: both tools must report the true total (2
	// matches), not a truncated count -- BinaryConvert's suppression is
	// a standard-mode display rule only.
	t.Run("-c", func(t *testing.T) {
		args := []string{"-j1", "-c", "midlinenul_needle", path}
		rgOut, rgErr, rgCode := run(t, "rg", args)
		ggOut, ggErr, ggCode := run(t, ggBin, args)
		if rgCode != ggCode {
			t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
		}
		if !bytes.Equal(rgOut, ggOut) {
			t.Errorf("raw stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
		}
	})
}

// TestGoldenVsRipgrep_ExplicitFileBinaryFarNULUntouched covers the other
// half of task #21's boundary, found via advisor review after the
// mid-line-NUL fix above: a NUL past DefaultBufferSize that no
// matched/context line's own bytes ever reach must NOT produce a
// "binary file matches" message under --mmap, even though the file does
// have an earlier match. Verified against the installed rg binary: rg's
// own --mmap leaves this message off entirely here (its SliceByLine
// never scans bytes it doesn't otherwise need to visit), while
// --no-mmap's streaming path does add it (it scans every byte it reads
// regardless of matches) -- so this test intentionally only asserts
// parity for --mmap, the mode gg's SearchBytes/mmap path is meant to
// match. --no-mmap parity for the analogous streaming case is already
// covered by TestGoldenVsRipgrep_ExplicitFileBinaryMidLineNUL and
// pre-existing walk-file binary tests.
func TestGoldenVsRipgrep_ExplicitFileBinaryFarNULUntouched(t *testing.T) {
	var content []byte
	content = append(content, "farnul_needle at the very first line\n"...)
	filler := []byte("filler filler filler filler filler filler filler filler\n")
	for len(content) < 500000 {
		content = append(content, filler...)
	}
	content = append(content, 0)
	content = append(content, "more filler after the nul, no match anywhere near it\n"...)

	dir := t.TempDir()
	path := filepath.Join(dir, "farnul.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	args := []string{"-j1", "-n", "--mmap", "farnul_needle", path}
	rgOut, rgErr, rgCode := run(t, "rg", args)
	ggOut, ggErr, ggCode := run(t, ggBin, args)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("raw stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
	}
}

// sortedLines splits out on '\n', drops the single trailing empty
// element a terminal newline produces, and sorts the result so that
// nondeterministic parallel-search completion order doesn't cause a
// false mismatch between two otherwise-identical result sets.
func sortedLines(out []byte) []string {
	s := strings.TrimRight(string(out), "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	sort.Strings(lines)
	return lines
}

// diffLines returns a human-readable summary of lines present in only
// one of want/got, or "" if the (already-sorted) slices are equal.
func diffLines(want, got []string) string {
	if slicesEqual(want, got) {
		return ""
	}
	wantSet := make(map[string]bool, len(want))
	for _, l := range want {
		wantSet[l] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, l := range got {
		gotSet[l] = true
	}

	var b strings.Builder
	for _, l := range want {
		if !gotSet[l] {
			b.WriteString("- (rg only) " + l + "\n")
		}
	}
	for _, l := range got {
		if !wantSet[l] {
			b.WriteString("+ (gg only) " + l + "\n")
		}
	}
	return b.String()
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSortNormalization exercises sortedLines/diffLines directly (no gg
// or rg binary involved) so the normalization logic itself has coverage
// now, rather than only being exercised implicitly once M2 unskips
// TestGoldenVsRipgrep's cases.
func TestSortNormalization(t *testing.T) {
	cases := []struct {
		name     string
		rgOut    string
		ggOut    string
		wantDiff bool
	}{
		{
			name:     "identical order",
			rgOut:    "a.txt:1:hello\nb.txt:2:hello\n",
			ggOut:    "a.txt:1:hello\nb.txt:2:hello\n",
			wantDiff: false,
		},
		{
			name:     "same lines, different completion order",
			rgOut:    "b.txt:2:hello\na.txt:1:hello\n",
			ggOut:    "a.txt:1:hello\nb.txt:2:hello\n",
			wantDiff: false,
		},
		{
			name:     "both empty",
			rgOut:    "",
			ggOut:    "",
			wantDiff: false,
		},
		{
			name:     "genuine mismatch",
			rgOut:    "a.txt:1:hello\n",
			ggOut:    "a.txt:1:hello\nb.txt:2:hello\n",
			wantDiff: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diff := diffLines(sortedLines([]byte(tc.rgOut)), sortedLines([]byte(tc.ggOut)))
			if got := diff != ""; got != tc.wantDiff {
				t.Errorf("diffLines: got mismatch=%v (diff=%q), want mismatch=%v", got, diff, tc.wantDiff)
			}
		})
	}
}

func buildGG(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gg")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/gg")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building gg: %v\n%s", err, out)
	}
	return bin
}

// run executes bin with args and returns stdout, stderr, and the exit
// code as three separate values — callers must not merge them, since
// only stdout is meaningful for the golden diff (see TestGoldenVsRipgrep).
func run(t *testing.T, bin string, args []string) (stdout, stderr []byte, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running %s: %v", bin, err)
	}
	return outBuf.Bytes(), errBuf.Bytes(), code
}
