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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestMain refuses to run the e2e suite against anything but the rg
// version pinned in internal/bench/rg-version.txt — the same pin the CI
// workflows install. "Byte-identical to rg" is only a meaningful claim
// against a known rg: a drifted local rg silently weakens every golden
// case in this package, so this is a hard failure with install
// instructions, not a skip.
func TestMain(m *testing.M) {
	if err := checkPinnedRG(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e suite:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func checkPinnedRG() error {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("could not determine test file location")
	}
	pinPath := filepath.Join(filepath.Dir(thisFile), "internal", "bench", "rg-version.txt")
	pin, err := os.ReadFile(pinPath)
	if err != nil {
		return fmt.Errorf("reading rg version pin: %w", err)
	}
	want := strings.TrimSpace(string(pin))
	out, err := exec.Command("rg", "--version").Output()
	if err != nil {
		return fmt.Errorf("rg not runnable on PATH: %w", err)
	}
	fields := strings.Fields(string(out)) // "ripgrep X.Y.Z (rev ...)"
	if len(fields) < 2 || fields[1] != want {
		return fmt.Errorf("rg on PATH is %q, but the golden suite is pinned to ripgrep %s "+
			"(internal/bench/rg-version.txt, same pin as CI) — install it, e.g.:\n"+
			"  curl -fsSL https://github.com/BurntSushi/ripgrep/releases/download/%s/ripgrep-%s-x86_64-unknown-linux-musl.tar.gz"+
			" | tar xz --strip-components=1 -C ~/.local/bin ripgrep-%s-x86_64-unknown-linux-musl/rg",
			strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), want, want, want, want)
	}
	return nil
}

func TestGoldenVsRipgrep(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Dir(thisFile)
	corpus := filepath.Join(root, "testdata", "corpus")

	ggBin := buildGG(t, root)

	// Pattern files for -f/--file cases (real files on disk; -f's own I/O
	// reads them at gg's execute time, same as rg's).
	patDir := t.TempDir()
	multiPats := filepath.Join(patDir, "multi.txt")
	if err := os.WriteFile(multiPats, []byte("hello\nneedle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blankLinePats := filepath.Join(patDir, "blank.txt")
	if err := os.WriteFile(blankLinePats, []byte("nomatchatall_zzz\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
	}{
		{"literal", []string{"-n", "hello", corpus}},
		{"file_flag_multi_pattern", []string{"-n", "-f", multiPats, corpus}},
		{"file_flag_empty_line_matches_everything", []string{"-n", "-f", blankLinePats, corpus}},
		{"file_flag_combined_with_regexp", []string{"-n", "-f", multiPats, "-e", "secret", corpus}},
		{"case_insensitive", []string{"-n", "-i", "HELLO", corpus}},
		{"regex_alternation", []string{"-n", "hello|needle", corpus}},
		{"word_boundary", []string{"-n", "-w", "cat", corpus}},
		{"line_regexp", []string{"-n", "-x", "second line", corpus}},
		{"line_regexp_fixed", []string{"-n", "-x", "-F", "hello there", corpus}},
		{"line_regexp_ignorecase", []string{"-n", "-x", "-i", "HELLO THERE", corpus}},
		{"line_regexp_word_last_wins", []string{"-n", "-x", "-w", "cat", corpus}},
		{"word_line_regexp_last_wins", []string{"-n", "-w", "-x", "second line", corpus}},
		{"line_regexp_no_match", []string{"-n", "-x", "short second", corpus}},
		{"invert_match", []string{"-n", "-v", "hello", corpus}},
		{"max_count_1", []string{"-n", "-m", "1", "hello", corpus}},
		{"max_count_0", []string{"-n", "-m", "0", "hello", corpus}},
		{"max_count_with_count_mode", []string{"-c", "-m", "1", "hello", corpus}},
		{"max_count_with_context", []string{"-n", "-m", "1", "-C", "1", "hello", corpus}},
		{"max_count_with_invert", []string{"-n", "-m", "2", "-v", "hello", corpus}},
		{"max_count_with_files_with_matches", []string{"-l", "-m", "1", "hello", corpus}},
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
		// Round #32: -d/--max-depth, -H/--with-filename, -I/--no-filename,
		// --heading/--no-heading. "hello" appears at depth 0 (crlf.txt,
		// unicode.txt) and depth 2 (a/b/foo.txt) in corpus -- -d 1 must
		// exclude the depth-2 match while keeping the depth-0 ones.
		{"max_depth_1", []string{"-n", "-d", "1", "hello", corpus}},
		{"max_depth_2_with_hidden_noignore", []string{"-n", "-d", "2", "--hidden", "--no-ignore", "hello", corpus}},
		{"max_depth_0_dir_root", []string{"-n", "-d", "0", "hello", corpus}},
		{"max_depth_0_explicit_file_root", []string{"-n", "-d", "0", "hello", filepath.Join(corpus, "a", "b", "foo.txt")}},
		{"max_depth_files_mode", []string{"-d", "1", "--files", corpus}},
		{"with_filename_forced_single_file", []string{"-n", "-H", "hello", filepath.Join(corpus, "crlf.txt")}},
		{"with_filename_forced_single_file_count", []string{"-H", "-c", "hello", filepath.Join(corpus, "crlf.txt")}},
		{"no_filename_dir", []string{"-n", "-I", "hello", corpus}},
		{"no_filename_dir_count", []string{"-I", "-c", "hello", corpus}},
		{"with_filename_last_wins", []string{"-n", "-I", "-H", "hello", filepath.Join(corpus, "crlf.txt")}},
		{"no_filename_last_wins", []string{"-n", "-H", "-I", "hello", corpus}},
		{"heading_explicit", []string{"-n", "--heading", "hello", corpus}},
		{"no_heading_explicit", []string{"-n", "--no-heading", "hello", corpus}},
		{"heading_with_count_mode_ignored", []string{"--heading", "-c", "hello", corpus}},
		{"heading_with_no_filename", []string{"-n", "--heading", "-I", "hello", corpus}},
		{"heading_last_wins", []string{"-n", "--heading", "--no-heading", "hello", corpus}},
		// Round #34: --column, --vimgrep, -b/--byte-offset. Order-sensitive
		// (multi-occurrence row ordering, exact field placement, context
		// interleaving) properties sort-normalization can't catch are
		// covered separately below (TestGoldenVsRipgrep_Vimgrep*,
		// TestGoldenVsRipgrep_ByteOffsetColumnFieldOrdering).
		{"column", []string{"--column", "hello", corpus}},
		{"column_no_line_number", []string{"--column", "-N", "hello", corpus}},
		{"column_invert", []string{"--column", "-v", "hello", corpus}},
		{"vimgrep", []string{"--vimgrep", "hello", corpus}},
		{"vimgrep_invert", []string{"--vimgrep", "-v", "hello", corpus}},
		{"vimgrep_no_column", []string{"--vimgrep", "--no-column", "hello", corpus}},
		{"vimgrep_count_mode", []string{"--vimgrep", "-c", "hello", corpus}},
		{"vimgrep_heading_ignored", []string{"--vimgrep", "--heading", "hello", corpus}},
		{"vimgrep_explicit_no_filename_wins", []string{"--vimgrep", "-I", "hello", filepath.Join(corpus, "crlf.txt")}},
		{"byte_offset", []string{"-b", "hello", corpus}},
		{"byte_offset_line_number", []string{"-b", "-n", "hello", corpus}},
		{"byte_offset_column", []string{"-b", "--column", "hello", corpus}},
		{"byte_offset_count_mode", []string{"-b", "-c", "hello", corpus}},
		{"byte_offset_files_with_matches", []string{"-b", "-l", "hello", corpus}},
		{"byte_offset_vimgrep", []string{"-b", "--vimgrep", "hello", corpus}},
		{"byte_offset_context", []string{"-b", "-C", "1", "hello", corpus}},
		// Round #36: -o/--only-matching, -M/--max-columns(-preview),
		// --trim. Order-sensitive (multi-occurrence ordering, exact
		// omitted-line text) properties are covered separately below
		// (TestGoldenVsRipgrep_OnlyMatching*, TestGoldenVsRipgrep_
		// MaxColumns*, TestGoldenVsRipgrep_TrimContextOrdering).
		{"only_matching", []string{"-o", "hello", corpus}},
		{"only_matching_line_number", []string{"-o", "-n", "hello", corpus}},
		{"only_matching_column", []string{"-o", "--column", "hello", corpus}},
		{"only_matching_byte_offset", []string{"-o", "-b", "hello", corpus}},
		{"only_matching_invert", []string{"-o", "-v", "hello", corpus}},
		{"only_matching_count_mode", []string{"-o", "-c", "hello", corpus}},
		{"only_matching_count_mode_invert", []string{"-o", "-c", "-v", "hello", corpus}},
		{"only_matching_files_with_matches", []string{"-o", "-l", "hello", corpus}},
		{"only_matching_vimgrep", []string{"-o", "--vimgrep", "hello", corpus}},
		{"only_matching_context", []string{"-o", "-C", "1", "hello", corpus}},
		{"max_columns", []string{"-M", "20", "-n", "hello", corpus}},
		{"max_columns_zero_unlimited", []string{"-M", "0", "-n", "hello", corpus}},
		{"max_columns_preview", []string{"-M", "20", "--max-columns-preview", "-n", "hello", corpus}},
		{"max_columns_column", []string{"-M", "20", "--column", "hello", corpus}},
		{"max_columns_only_matching", []string{"-M", "20", "-o", "hello", corpus}},
		{"max_columns_context", []string{"-M", "20", "-C", "1", "hello", corpus}},
		{"trim", []string{"--trim", "-n", "hello", corpus}},
		{"trim_column", []string{"--trim", "--column", "hello", corpus}},
		{"trim_byte_offset", []string{"--trim", "-b", "hello", corpus}},
		{"trim_max_columns", []string{"--trim", "-M", "20", "-n", "hello", corpus}},
		{"trim_only_matching", []string{"--trim", "-o", "hello", corpus}},
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

// TestGoldenVsRipgrep_Files covers M3 #25 (--files): every case the task
// mandate calls for, run against testdata/corpus, which already has the
// hidden/gitignore fixtures (.hidden/, ignored/, *.secret) --files needs
// to compose correctly with. --files takes no PATTERN at all, so these
// args never include one.
func TestGoldenVsRipgrep_Files(t *testing.T) {
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
		{"bare", []string{"--files", corpus}},
		{"hidden", []string{"--files", "--hidden", corpus}},
		{"no_ignore", []string{"--files", "--no-ignore", corpus}},
		{"glob_filter", []string{"--files", "-g", "*.txt", corpus}},
		{"explicit_subdir_path_arg", []string{"--files", filepath.Join(corpus, "a")}},
		{"mode_precedence_dash_l_then_files_still_lists", []string{"-l", "--files", corpus}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			rgLines := sortedLines(rgOut)
			ggLines := sortedLines(ggOut)
			if diff := diffLines(rgLines, ggLines); diff != "" {
				t.Errorf("sort-normalized stdout mismatch:\n%s\n--- raw rg stdout ---\n%s\n--- raw gg stdout ---\n%s",
					diff, rgOut, ggOut)
			}
		})
	}
}

// TestGoldenVsRipgrep_FilesQuietExitCodes covers --files -q specifically:
// no output at all, exit code alone communicates found-or-not. Verified
// against the real rg binary directly (see flags.go's ModeFiles doc):
// -q suppresses --files' path listing entirely but still reflects a real
// find as exit 0.
func TestGoldenVsRipgrep_FilesQuietExitCodes(t *testing.T) {
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
		{"found", []string{"--files", "-q", corpus}},
		{"not_found_via_glob", []string{"--files", "-q", "-g", "*.this-extension-does-not-exist", corpus}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if len(rgOut) != 0 || len(ggOut) != 0 {
				t.Errorf("-q must produce no stdout at all: rg=%q gg=%q", rgOut, ggOut)
			}
		})
	}
}

// TestGoldenVsRipgrep_FilesOnRepoTree runs --files against gripgrep's own
// repo root: a real, non-synthetic tree with its own .gitignore, several
// nested directories, and (crucially, unlike testdata/corpus) enough
// depth and file variety that a full unfiltered listing is a meaningful
// exercise of gitignore/glob composition end to end, not just the
// hand-picked fixtures.
func TestGoldenVsRipgrep_FilesOnRepoTree(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Dir(thisFile)
	ggBin := buildGG(t, root)

	cases := []struct {
		name string
		args []string
	}{
		{"bare_no_path_arg", []string{"--files"}},
		{"explicit_dot", []string{"--files", "."}},
		{"hidden", []string{"--files", "--hidden"}},
		{"glob_filter_go", []string{"--files", "-g", "*.go"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgCmd := exec.Command("rg", tc.args...)
			rgCmd.Dir = root
			var rgOut, rgErr bytes.Buffer
			rgCmd.Stdout, rgCmd.Stderr = &rgOut, &rgErr
			rgErrRun := rgCmd.Run()
			rgCode := 0
			if exitErr, ok := rgErrRun.(*exec.ExitError); ok {
				rgCode = exitErr.ExitCode()
			} else if rgErrRun != nil {
				t.Fatalf("running rg: %v", rgErrRun)
			}

			ggCmd := exec.Command(ggBin, tc.args...)
			ggCmd.Dir = root
			var ggOut, ggErr bytes.Buffer
			ggCmd.Stdout, ggCmd.Stderr = &ggOut, &ggErr
			ggErrRun := ggCmd.Run()
			ggCode := 0
			if exitErr, ok := ggErrRun.(*exec.ExitError); ok {
				ggCode = exitErr.ExitCode()
			} else if ggErrRun != nil {
				t.Fatalf("running gg: %v", ggErrRun)
			}

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr.String(), ggErr.String())
			}
			rgLines := sortedLines(rgOut.Bytes())
			ggLines := sortedLines(ggOut.Bytes())
			if diff := diffLines(rgLines, ggLines); diff != "" {
				t.Errorf("sort-normalized stdout mismatch:\n%s", diff)
			}
		})
	}
}

// TestGoldenVsRipgrep_FilesOnLinuxTree is the mandate's "benchmark-data/
// linux" corpus case: skipped (not failed) when that corpus isn't
// checked out, since it's gitignored and not universally present. Where
// present, this is the exact scenario that surfaced a real bug during
// #25's development: benchmark-data/linux is its own nested git repo,
// and gg's ignore-stack construction used to leak this outer repo's own
// top-level ".gitignore" (which excludes "*.exe" build artifacts) into
// that inner repo's walk, wrongly excluding a real, tracked Linux kernel
// test fixture (tools/perf/tests/pe-file.exe) that real rg does not
// exclude -- fixed in walk/ignore.go's buildParentChain (see its doc).
func TestGoldenVsRipgrep_FilesOnLinuxTree(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Dir(thisFile)
	linuxTree := filepath.Join(root, "benchmark-data", "linux")
	if _, err := os.Stat(linuxTree); err != nil {
		t.Skipf("benchmark-data/linux not present (gitignored corpus, not checked out): %v", err)
	}
	ggBin := buildGG(t, root)

	cases := []struct {
		name string
		args []string
	}{
		{"bare", []string{"--files", linuxTree}},
		{"hidden", []string{"--files", "--hidden", linuxTree}},
		{"glob_filter_c", []string{"--files", "-g", "*.c", linuxTree}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			rgLines := sortedLines(rgOut)
			ggLines := sortedLines(ggOut)
			if diff := diffLines(rgLines, ggLines); diff != "" {
				t.Errorf("sort-normalized stdout mismatch:\n%s", diff)
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

// TestGoldenVsRipgrep_MaxCountContextOrdering covers round 31's -m
// requirement that trailing -A context after the FINAL counted match
// still prints, while everything past the limit (including further
// matches with their own trailing context) must not -- an ordering/
// exact-block-boundary property sort-normalization can't catch (see
// TestGoldenVsRipgrep_ContextOrdering's doc for why this needs its own
// raw, single-file, -j1 byte comparison instead).
//
// Fixture: three matches. -m2 must produce exactly match1's own
// after-context and match2's own after-context (with the "--" block
// separator the gap at line 4 requires), and nothing at all from match3
// or its context, even though both are physically present in the file.
func TestGoldenVsRipgrep_MaxCountContextOrdering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "maxcount.txt")
	content := "hello one\n" +
		"ctx1a\n" +
		"ctx1b\n" +
		"filler\n" +
		"hello two\n" +
		"ctx2a\n" +
		"ctx2b\n" +
		"hello three\n" +
		"ctx3a\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	args := []string{"-j1", "-n", "-A", "2", "-m", "2", "hello", path}

	rgOut, rgErr, rgCode := run(t, "rg", args)
	ggOut, ggErr, ggCode := run(t, ggBin, args)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
	}
}

// TestGoldenVsRipgrep_VimgrepMultiOccurrenceOrdering covers round #34's
// --vimgrep row-per-occurrence property, which sort-normalization can't
// catch: multiple rows from the SAME line must appear consecutively, in
// left-to-right column order, interleaved correctly with single-
// occurrence rows from other lines -- a set comparison would pass even
// if the rows for line 1 came out in the wrong order or were split
// apart by line 3's row.
func TestGoldenVsRipgrep_VimgrepMultiOccurrenceOrdering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	content := "one cat two cat\nno match here\ncat at start\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	args := []string{"-j1", "--vimgrep", "cat", path}

	rgOut, rgErr, rgCode := run(t, "rg", args)
	ggOut, ggErr, ggCode := run(t, ggBin, args)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
	}
}

// TestGoldenVsRipgrep_ByteOffsetColumnFieldOrdering covers round #34's
// "line:col:offset:text" field ordering (-b --column together): a set
// comparison of whole lines happens to pass even on a wrong field order
// as long as gg is INTERNALLY consistent with itself across the corpus
// (every line would be wrong the same way), so this needs an exact,
// known-good fixture compared byte for byte against the real rg binary,
// not sort-normalization.
func TestGoldenVsRipgrep_ByteOffsetColumnFieldOrdering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	content := "one cat two cat\nno match here\ncat at start\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	args := []string{"-j1", "-b", "--column", "cat", path}

	rgOut, rgErr, rgCode := run(t, "rg", args)
	ggOut, ggErr, ggCode := run(t, ggBin, args)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
	}
}

// TestGoldenVsRipgrep_VimgrepContextInterleaving covers round #34's
// "--vimgrep -C1" property that context lines print ONCE (never
// per-occurrence) and correctly interleave with the multi-row match
// output, including the path-prefixed dash-separated context line
// format --vimgrep forces even for context.
func TestGoldenVsRipgrep_VimgrepContextInterleaving(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	content := "one cat two cat\nno match here\ncat at start\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	args := []string{"-j1", "--vimgrep", "-C", "1", "cat", path}

	rgOut, rgErr, rgCode := run(t, "rg", args)
	ggOut, ggErr, ggCode := run(t, ggBin, args)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
	}
}

// TestGoldenVsRipgrep_OnlyMatchingMultiOccurrenceOrdering covers round
// #36's -o property that a line with multiple occurrences becomes
// multiple, per-occurrence rows in left-to-right order -- a set
// comparison would pass even if rows came out in the wrong order or
// with the wrong text per row, since (for this fixture) every row is
// the literal text "cat", indistinguishable from any other by a
// sort-normalized diff.
func TestGoldenVsRipgrep_OnlyMatchingMultiOccurrenceOrdering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	content := "one cat two cat\nno match here\ncat at start\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	args := []string{"-j1", "-o", "-n", "--column", "-b", "cat", path}

	rgOut, rgErr, rgCode := run(t, "rg", args)
	ggOut, ggErr, ggCode := run(t, ggBin, args)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
	}
}

// TestGoldenVsRipgrep_MaxColumnsOmittedTextExactness covers round #36's
// -M/--max-columns exact replacement text -- "[Omitted long matching
// line]" vs "[Omitted long line with N matches]" vs the preview's
// truncated-prefix wording all differ only in their exact bytes, which
// a sort-normalized set comparison can still pass on if gg picks a
// consistently-wrong wording. Exercises the plain, --column-driven, and
// --max-columns-preview wordings all in one fixture, byte for byte.
func TestGoldenVsRipgrep_MaxColumnsOmittedTextExactness(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.txt")
	content := "short cat line\n" +
		"catstart catmiddle padding padding padding cat cat end\n" +
		"short cat line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"plain", []string{"-j1", "-n", "-M", "20", "cat", path}},
		{"column", []string{"-j1", "-n", "--column", "-M", "20", "cat", path}},
		{"preview", []string{"-j1", "-n", "-M", "20", "--max-columns-preview", "cat", path}},
		{"preview_color", []string{"-j1", "-n", "-M", "20", "--max-columns-preview", "--color=always", "cat", path}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if !bytes.Equal(rgOut, ggOut) {
				t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
			}
		})
	}
}

// TestGoldenVsRipgrep_TrimContextOrdering covers round #36's --trim
// property that leading whitespace is stripped from CONTEXT lines too,
// interleaved correctly with trimmed matched lines -- a sort-normalized
// diff of TRIMMED lines could pass even if gg forgot to trim context
// lines specifically, since the untrimmed context text wouldn't be
// present to compare against (it just wouldn't be trimmed), so this
// needs the raw, ordered byte stream.
func TestGoldenVsRipgrep_TrimContextOrdering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trim.txt")
	content := "   indented cat\n\tTAB context after\nplain cat line\n    more indented context\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	args := []string{"-j1", "--trim", "-n", "-A", "1", "cat", path}

	rgOut, rgErr, rgCode := run(t, "rg", args)
	ggOut, ggErr, ggCode := run(t, ggBin, args)

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
	}
}

// TestGoldenVsRipgrep_MaxColumnsPreviewGraphemeClusterBoundary covers a
// regression this round's re-review caught: --max-columns-preview's cut
// point must land on grapheme CLUSTER boundaries (rg's own
// unicode-segmentation dependency), not rune or byte boundaries. An "e"
// + COMBINING ACUTE ACCENT (two runes, one visual "é") straddling the
// cut is the exact fixture that distinguishes the two -- a rune-boundary
// approximation diverges from the real rg binary at every -M value from
// the point the combining mark enters the visible prefix onward. Swept
// across every -M value that matters for this fixture, not just one.
func TestGoldenVsRipgrep_MaxColumnsPreviewGraphemeClusterBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "combining.txt")
	content := "combining é mark cat and more text here after\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	for m := 8; m <= 16; m++ {
		t.Run(fmt.Sprintf("M%d", m), func(t *testing.T) {
			args := []string{"-j1", "-M", fmt.Sprintf("%d", m), "--max-columns-preview", "cat", path}

			rgOut, rgErr, rgCode := run(t, "rg", args)
			ggOut, ggErr, ggCode := run(t, ggBin, args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if !bytes.Equal(rgOut, ggOut) {
				t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
			}
		})
	}
}

// TestGoldenVsRipgrep_VimgrepColorPreviewPerRowCount covers a regression
// this round's re-review caught: under `--vimgrep --color=always -M
// --max-columns-preview`, each row's "N more matches" count must be
// INDEPENDENT (is THAT row's own occurrence still visible within its
// own preview cutoff?), not the whole line's total remaining count --
// a real divergence in rg's own implementation between its color-
// rendering and no-color code paths. Sort-normalization can't catch a
// wrong-but-internally-consistent count here (every row having the same
// wrong number would still be a valid multiset), so this needs the raw,
// ordered, byte-exact stream, with and without color to lock in both
// sides of the divergence.
func TestGoldenVsRipgrep_VimgrepColorPreviewPerRowCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multimatch.txt")
	content := "catstart catmiddle padding padding padding cat cat end\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"color", []string{"-j1", "--vimgrep", "--color=always", "-M", "20", "--max-columns-preview", "cat", path}},
		{"no_color", []string{"-j1", "--vimgrep", "-M", "20", "--max-columns-preview", "cat", path}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if !bytes.Equal(rgOut, ggOut) {
				t.Errorf("raw (unsorted, -j1, single-file) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
			}
		})
	}
}

// TestGoldenVsRipgrep_HeadingGrouping closes the sort-normalization blind
// spot for round #32's --heading: TestGoldenVsRipgrep's heading cases
// only prove the same *set* of lines came out, not that blank-line group
// separators land between (and only between) file groups, in the right
// place, with no trailing blank after the last group.
//
// Deliberately a FLAT directory with no subdirectories at all, and the
// directory itself passed as the one PATH argument (never individual
// files enumerated on the command line): raw readdir(3) order is a
// filesystem property both gg (worker.go's File.ReadDir(-1)) and rg read
// unsorted and verbatim, so for a single flat directory that order is
// identical for both tools. TestGoldenVsRipgrep_ContextOrdering's doc
// warns that this does NOT hold once directories are nested (each tool's
// own recursion/steal order can interleave files and subdirectories
// differently) -- this test avoids that entirely by having no
// subdirectories to interleave, unlike that test's single-file target
// (heading grouping needs 2+ files to exercise the between-group
// separator at all, so single-file isn't an option here).
func TestGoldenVsRipgrep_HeadingGrouping(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a_match.txt":   "hello first\nfiller\n",
		"b_nomatch.txt": "nothing here\n",
		"c_match.txt":   "hello second\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	cases := []struct {
		name string
		args []string
	}{
		{"heading", []string{"-j1", "-n", "--heading", "hello", dir}},
		{"heading_no_filename", []string{"-j1", "-n", "--heading", "-I", "hello", dir}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)
			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if !bytes.Equal(rgOut, ggOut) {
				t.Errorf("raw (unsorted, -j1, flat-dir) stdout mismatch:\n--- rg stdout ---\n%s\n--- gg stdout ---\n%s", rgOut, ggOut)
			}
		})
	}
}

// TestGoldenVsRipgrep_GlobCaseInsensitive covers round #32's --iglob and
// --glob-case-insensitive on a dedicated fixture tree with a nested
// upper-case .TXT file (a case-insensitive match target that a
// case-SENSITIVE -g '*.txt' must miss) -- kept out of testdata/corpus
// deliberately, since adding a .TXT file there could perturb other
// implementers' corpus-based expectations. Sort-normalized: these cases
// don't depend on inter-file ordering, only on which files matched.
func TestGoldenVsRipgrep_GlobCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"root.txt":         "needle here\n",
		"nested/a.txt":     "needle a\n",
		"nested/UPPER.TXT": "needle upper\n",
		"nested/b.md":      "needle md\n",
	}
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	cases := []struct {
		name string
		args []string
	}{
		{"iglob_matches_upper", []string{"--files", "--iglob", "*.txt", dir}},
		{"glob_case_sensitive_misses_upper", []string{"--files", "-g", "*.txt", dir}},
		{"glob_case_insensitive_matches_upper", []string{"--files", "--glob-case-insensitive", "-g", "*.txt", dir}},
		{"iglob_negation", []string{"--files", "--iglob", "!*.txt", dir}},
		{"iglob_overrides_glob_regardless_of_cli_order", []string{"--files", "--iglob", "*.txt", "-g", "!*.TXT", dir}},
		{"glob_then_iglob_reversed_cli_order_same_result", []string{"--files", "-g", "!*.TXT", "--iglob", "*.txt", dir}},
		{"iglob_search_mode", []string{"-n", "--iglob", "*.txt", "needle", dir}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)
			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			rgLines := sortedLines(rgOut)
			ggLines := sortedLines(ggOut)
			if diff := diffLines(rgLines, ggLines); diff != "" {
				t.Errorf("sort-normalized stdout mismatch:\n%s\n--- raw rg stdout ---\n%s\n--- raw gg stdout ---\n%s", diff, rgOut, ggOut)
			}
		})
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
