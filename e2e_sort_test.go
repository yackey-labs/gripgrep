//go:build e2e

// Golden e2e coverage for --sort and --sortr (path/modified/accessed/
// created), diffed BYTE-FOR-BYTE against the pinned rg 15.1.0 (see
// e2e_test.go's TestMain). Sorting IS the contract here, so nearly every
// case compares raw stdout directly rather than as a sorted multiset: the
// emitted order is exactly what is under test. rg and gg run with identical
// args in the SAME isolated temp dir, so their single-threaded readdir
// discovery order is identical -- which is what makes equal-key ties
// (modified/accessed) come out in the same order without any recorded
// golden. The one exception is `--sort none`: it runs the parallel,
// unordered walk in both tools, whose order is deliberately undefined (rg
// itself is nondeterministic there), so those cases compare the '\n'-sorted
// set of paths plus the exit code.
//
// Controlled timestamps come from os.Chtimes (atime==mtime, mirroring the
// oracle's `touch -d`, which sets both) so --sort accessed reproduces the
// same order as --sort modified on this fixture. Time-keyed cases stay in
// --files mode: --files never opens a file, so the sort's stat never
// perturbs atime on a relatime mount.
package gripgrep_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// day builds a local midnight time for 2026-01-DD, matching the oracle's
// `touch -d "2026-01-0D"`.
func day(d int) time.Time {
	return time.Date(2026, 1, d, 0, 0, 0, 0, time.Local)
}

// sortFixture writes the answer-key fixture: five files across the root and
// two subdirs, each stamped with the oracle's mtime (== atime). A .git
// marker makes the walk treat the dir as a repo root. File CONTENT is
// "needle\n" everywhere so the search-mode cases (-n/-c/-l/-A) all match.
func sortFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := []struct {
		name string
		when int
	}{
		{"bravo.txt", 3},
		{"alpha.txt", 5},
		{"sub/charlie.txt", 1},
		{"zz/delta.txt", 4},
		{"echo.txt", 3}, // mtime tie with bravo.txt
	}
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.name))
		mustWrite(t, p, "needle\n")
		when := day(f.when)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// dirFileFixture writes the component-vs-byte discriminator: a directory
// "a" (containing a/deep.txt) beside a file "a+.txt", and a directory "b"
// (containing b/m.txt) beside files "b.txt"/"ba.txt". A component-wise path
// sort (rg's) orders a/deep.txt before a+.txt and b/m.txt before b.txt,
// because the '/' boundary is a component break -- a byte-wise sort would
// invert both (0x2B '+' and 0x2E '.' precede 0x2F '/').
func dirFileFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a/deep.txt", "a+.txt", "b/m.txt", "b.txt", "ba.txt"} {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(name)), "needle\n")
	}
	return dir
}

// symlinkFixture writes a target (newest mtime), a plain file, and a symlink
// to the target. rg keys the symlink by its TARGET's time (Path::metadata()
// follows), so link.txt sorts next to target.txt, not by the link's own
// (creation-time-fresh) mtime. Returns "" to skip on platforms without
// symlink support.
func symlinkFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.txt")
	mustWrite(t, target, "needle\n")
	if err := os.Chtimes(target, day(10), day(10)); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "plain.txt")
	mustWrite(t, plain, "needle\n")
	if err := os.Chtimes(plain, day(2), day(2)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	return dir
}

// TestGoldenVsRipgrep_Sort diffs raw stdout bytes and exit code against rg
// for every --sort/--sortr answer-key probe plus the component-wise,
// symlink, global-multi-root, and parse-error cases discovered against the
// real binary.
func TestGoldenVsRipgrep_Sort(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Dir(thisFile)
	ggBin := buildGG(t, root)

	const (
		fxKey     = "key"     // sortFixture
		fxDirFile = "dirfile" // dirFileFixture
		fxSymlink = "symlink" // symlinkFixture
	)

	cases := []struct {
		name    string
		fixture string
		args    []string
		// unordered compares the '\n'-sorted set of lines instead of raw
		// bytes -- used only for --sort none, whose order is undefined.
		unordered bool
	}{
		// --- answer key S1-S17 ---
		{name: "sort_path_files", fixture: fxKey, args: []string{"--files", "--sort", "path"}},
		{name: "sortr_path_files", fixture: fxKey, args: []string{"--files", "--sortr", "path"}},
		{name: "sort_modified_files", fixture: fxKey, args: []string{"--files", "--sort", "modified"}},
		{name: "sortr_modified_files", fixture: fxKey, args: []string{"--files", "--sortr", "modified"}},
		{name: "sort_accessed_files", fixture: fxKey, args: []string{"--files", "--sort", "accessed"}},
		{name: "sort_created_errors", fixture: fxKey, args: []string{"--files", "--sort", "created"}},
		{name: "sort_path_search_n", fixture: fxKey, args: []string{"-n", "--sort", "path", "needle"}},
		{name: "sort_path_explicit_j4", fixture: fxKey, args: []string{"--files", "--sort", "path", "-j4"}},
		{name: "sort_bogus_kind", fixture: fxKey, args: []string{"--files", "--sort", "bogus"}},
		{name: "sort_then_sortr_lastwins", fixture: fxKey, args: []string{"--files", "--sort", "path", "--sortr", "modified"}},
		{name: "sort_none", fixture: fxKey, args: []string{"--files", "--sort", "none"}, unordered: true},
		{name: "sort_modified_count", fixture: fxKey, args: []string{"-c", "--sort", "modified", "needle"}},
		{name: "sort_path_maxcount", fixture: fxKey, args: []string{"--sort", "path", "-m1", "-n", "needle"}},
		{name: "sort_path_after_context", fixture: fxKey, args: []string{"--sort", "path", "-A1", "needle"}},
		{name: "sortr_path_files_with_matches", fixture: fxKey, args: []string{"-l", "--sortr", "path", "needle"}},
		{name: "sort_path_multiroot", fixture: fxKey, args: []string{"--files", "--sort", "path", "sub", "zz", "."}},

		// --- component-wise path ordering (dir vs file prefix) ---
		{name: "sort_path_component_wise", fixture: fxDirFile, args: []string{"--files", "--sort", "path"}},
		{name: "sortr_path_component_wise", fixture: fxDirFile, args: []string{"--files", "--sortr", "path"}},

		// --- global (not per-root) collect-and-sort for the non-asc-path kinds ---
		{name: "sortr_path_global_multiroot", fixture: fxKey, args: []string{"--files", "--sortr", "path", "sub", "zz", "."}},
		{name: "sort_modified_global_multiroot", fixture: fxKey, args: []string{"--files", "--sort", "modified", "sub", "zz", "."}},
		{name: "sortr_modified_global_multiroot", fixture: fxKey, args: []string{"--files", "--sortr", "modified", "sub", "zz", "."}},

		// --- symlink keyed by target time under -L ---
		{name: "sort_modified_follow_symlink", fixture: fxSymlink, args: []string{"-L", "--files", "--sort", "modified"}},

		// --- parse / equals-form / kind-share edge cases ---
		{name: "sort_equals_none", fixture: fxKey, args: []string{"--files", "--sort=none"}, unordered: true},
		{name: "sort_empty_kind", fixture: fxKey, args: []string{"--files", "--sort", ""}},
		{name: "sortr_created_errors", fixture: fxKey, args: []string{"--files", "--sortr", "created"}},
		{name: "sortr_then_sort_lastwins", fixture: fxKey, args: []string{"--files", "--sortr", "modified", "--sort", "path"}},

		// --- explicit (reordered) file arguments ---
		// Ascending path keeps ARG order for explicit files (each is a
		// depth-0 root, and asc-path is per-root, so roots are not re-sorted).
		{name: "sort_path_explicit_files_keep_argorder", fixture: fxKey, args: []string{"--files", "--sort", "path", "bravo.txt", "alpha.txt", "echo.txt"}},
		{name: "sortr_path_explicit_files_global", fixture: fxKey, args: []string{"--files", "--sortr", "path", "bravo.txt", "alpha.txt", "echo.txt"}},
		{name: "sort_modified_explicit_files_global", fixture: fxKey, args: []string{"--files", "--sort", "modified", "bravo.txt", "alpha.txt", "echo.txt"}},
		{name: "sort_modified_explicit_search_l", fixture: fxKey, args: []string{"-l", "--sort", "modified", "needle", "bravo.txt", "alpha.txt", "echo.txt"}},

		// --- sort composes with --null path terminator and --heading ---
		{name: "sort_path_null", fixture: fxKey, args: []string{"--files", "--sort", "path", "--null"}},
		{name: "sort_path_heading", fixture: fxKey, args: []string{"--heading", "--sort", "path", "needle"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			switch tc.fixture {
			case fxDirFile:
				dir = dirFileFixture(t)
			case fxSymlink:
				dir = symlinkFixture(t)
			default:
				dir = sortFixture(t)
			}

			rgOut, rgErr, rgCode := runInDir(t, "rg", dir, tc.args)
			ggOut, ggErr, ggCode := runInDir(t, ggBin, dir, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}

			want, got := rgOut, ggOut
			if tc.unordered {
				want = sortLines(want)
				got = sortLines(got)
			}
			if !bytes.Equal(want, got) {
				t.Errorf("stdout mismatch\n--- rg (%d bytes) ---\n%q\n--- gg (%d bytes) ---\n%q\n--- rg stderr ---\n%s\n--- gg stderr ---\n%s",
					len(rgOut), rgOut, len(ggOut), ggOut, rgErr, ggErr)
			}
		})
	}
}
