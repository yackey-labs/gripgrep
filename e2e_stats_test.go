//go:build e2e

// Golden e2e coverage for --stats and --line-buffered/--block-buffered,
// diffed against the pinned rg 15.1.0 (see e2e_test.go's TestMain). The
// --stats block is byte-contractual EXCEPT its two timing lines ("N.NNNNNN
// seconds spent searching" / "... total"), whose values are
// nondeterministic; normStatsTiming rewrites exactly those two lines (and
// asserts it matched the expected count, so a drifted block fails rather
// than being silently normalized away) before the byte comparison. The
// buffering flags are byte-invisible, so their coverage asserts output is
// identical to the same run without them.
package gripgrep_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// statsFixture writes the answer-key fixture (a.txt/b.txt/sub/c.txt under a
// dir with a .git marker so the walk treats it as a repo root) and returns
// its path. Content byte counts are load-bearing for the stats numbers, so
// they mirror the oracle exactly.
func statsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "needle one\nneedle two\nhay\n")
	mustWrite(t, filepath.Join(dir, "b.txt"), "no match here\n")
	mustWrite(t, filepath.Join(dir, "sub", "c.txt"), "needle needle needle\n")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runInDir runs bin in dir with an isolated HOME/XDG_CONFIG_HOME (so no
// user-global ignore or config file perturbs the walk), returning stdout,
// stderr, and the exit code. It mirrors the probe harness the oracle was
// generated with, so the stats numbers are reproducible.
func runInDir(t *testing.T, bin, dir string, args []string) (stdout, stderr []byte, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "XDG_CONFIG_HOME=" + dir}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running %s %v: %v", bin, args, err)
	}
	return outBuf.Bytes(), errBuf.Bytes(), code
}

var statsTimingLine = regexp.MustCompile(`(?m)^[0-9]+\.[0-9]+ (seconds spent searching|seconds total)$`)

// normStatsTiming rewrites the numeric prefix of the two timing lines to a
// fixed placeholder and returns how many lines it replaced. Callers assert
// that count (2 for a real block, 0 for a mode that prints none), so a
// stats block that gains, loses, or reshapes a timing line -- or drops the
// block entirely -- fails the comparison instead of being normalized into
// agreement.
func normStatsTiming(out []byte) (normalized []byte, replaced int) {
	n := 0
	res := statsTimingLine.ReplaceAllFunc(out, func(line []byte) []byte {
		n++
		// Keep the label, fix the number: "T <label>".
		idx := bytes.IndexByte(line, ' ')
		return append([]byte("T"), line[idx:]...)
	})
	return res, n
}

// TestGoldenVsRipgrep_Stats replays the answer key's S/B probes (and the
// extended probes) against real rg, byte-for-byte with only the two timing
// lines normalized. -j1 keeps the result lines above the block in a
// deterministic order so the whole output is byte-comparable; the parallel
// path's aggregate counts get their own test below.
func TestGoldenVsRipgrep_Stats(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	cases := []struct {
		name string
		args []string
		// timing is how many timing lines the output must contain: 2 when a
		// stats block prints, 0 when it must not (--files, --no-stats).
		timing int
	}{
		{"basic", []string{"-j1", "--stats", "needle"}, 2},
		{"no_matches", []string{"-j1", "--stats", "absent"}, 2},
		{"count", []string{"-j1", "--stats", "-c", "needle"}, 2},
		{"files_with_matches", []string{"-j1", "--stats", "-l", "needle"}, 2},
		{"quiet", []string{"-j1", "--stats", "-q", "needle"}, 2},
		{"files_no_block", []string{"-j1", "--stats", "--files"}, 0},
		{"count_matches", []string{"-j1", "--stats", "--count-matches", "needle"}, 2},
		{"only_matching", []string{"-j1", "--stats", "-o", "needle"}, 2},
		{"no_stats_negation", []string{"-j1", "--stats", "--no-stats", "needle"}, 0},
		{"no_stats_first", []string{"-j1", "--no-stats", "--stats", "needle"}, 2},
		{"invert", []string{"-j1", "--stats", "-v", "needle"}, 2},
		{"nonexistent", []string{"-j1", "--stats", "needle", "unreadable-nonexistent.txt"}, 2},
		{"single_file", []string{"-j1", "--stats", "needle", "sub/c.txt"}, 2},
		{"color_always", []string{"-j1", "--stats", "--color=always", "needle"}, 2},
		{"null", []string{"-j1", "--stats", "--null", "needle"}, 2},
		{"passthru", []string{"-j1", "--stats", "--passthru", "needle"}, 2},
		{"files_without_match", []string{"-j1", "--stats", "--files-without-match", "needle"}, 2},
		{"count_include_zero", []string{"-j1", "--stats", "-c", "--include-zero", "needle"}, 2},
		{"multi_root", []string{"-j1", "--stats", "needle", "a.txt", "sub/c.txt"}, 2},
		{"after_context", []string{"-j1", "--stats", "-A1", "needle"}, 2},
		{"context", []string{"-j1", "--stats", "-C1", "needle"}, 2},
		{"word", []string{"-j1", "--stats", "-w", "needle"}, 2},
		{"only_matching_null", []string{"-j1", "--stats", "-o", "--null", "needle"}, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := statsFixture(t)
			rgOut, rgErr, rgCode := runInDir(t, "rg", dir, tc.args)
			ggOut, ggErr, ggCode := runInDir(t, ggBin, dir, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}

			rgNorm, rgN := normStatsTiming(rgOut)
			ggNorm, ggN := normStatsTiming(ggOut)
			if rgN != tc.timing {
				t.Fatalf("rg produced %d timing lines, expected %d -- oracle/fixture drift:\n%s", rgN, tc.timing, rgOut)
			}
			if ggN != tc.timing {
				t.Errorf("gg produced %d timing lines, expected %d (drifted --stats block):\n%s", ggN, tc.timing, ggOut)
			}
			if !bytes.Equal(rgNorm, ggNorm) {
				t.Errorf("stats output mismatch (timing normalized):\n--- rg ---\n%s\n--- gg ---\n%s", rgNorm, ggNorm)
			}
		})
	}
}

// TestGoldenVsRipgrep_StatsParallelBlock exercises the parallel stats
// accumulator: a fixture large enough to fan out across cross-file workers
// AND to split one file into intra-file chunks at the default -j. The
// aggregate counts are order-independent, so this compares only the stats
// block (dropping the nondeterministic timing lines) rather than the whole
// output, and re-runs gg to assert the counts are stable across runs.
func TestGoldenVsRipgrep_StatsParallelBlock(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Many small files -> cross-file worker fan-out.
	for i := 0; i < 200; i++ {
		mustWrite(t, filepath.Join(dir, fmt.Sprintf("f%d.txt", i)),
			"needle here\nnothing\nneedle again\n")
	}
	// One large file (~3.5MB, well above ParallelMinBytes) -> intra-file
	// chunking on the mmap/SearchBytes path.
	var big strings.Builder
	for i := 0; i < 200000; i++ {
		if i%3 == 0 {
			big.WriteString("needle line ")
		} else {
			big.WriteString("filler line ")
		}
		big.WriteString(strconv.Itoa(i))
		big.WriteByte('\n')
	}
	mustWrite(t, filepath.Join(dir, "huge.txt"), big.String())

	args := []string{"--stats", "needle"}

	rgBlock := statsBlockCounts(t, mustNormRun(t, "rg", dir, args))
	ggBlock := statsBlockCounts(t, mustNormRun(t, ggBin, dir, args))
	if rgBlock != ggBlock {
		t.Errorf("parallel stats counts differ from rg:\n--- rg ---\n%s\n--- gg ---\n%s", rgBlock, ggBlock)
	}
	// Determinism: the same gg invocation must report identical counts on a
	// repeat run, despite nondeterministic worker/chunk completion order.
	ggBlock2 := statsBlockCounts(t, mustNormRun(t, ggBin, dir, args))
	if ggBlock != ggBlock2 {
		t.Errorf("gg parallel stats counts not stable across runs:\n--- run1 ---\n%s\n--- run2 ---\n%s", ggBlock, ggBlock2)
	}
}

func mustNormRun(t *testing.T, bin, dir string, args []string) []byte {
	t.Helper()
	out, errOut, code := runInDir(t, bin, dir, args)
	if code != 0 {
		t.Fatalf("%s exited %d: %s", bin, code, errOut)
	}
	norm, n := normStatsTiming(out)
	if n != 2 {
		t.Fatalf("%s: expected 2 timing lines, got %d:\n%s", bin, n, out)
	}
	return norm
}

// statsBlockCounts extracts just the count lines of the --stats block (the
// eight lines after the trailing blank line, minus the two normalized
// timing lines), so a parallel comparison ignores the order-dependent
// result lines above it.
func statsBlockCounts(t *testing.T, normalized []byte) string {
	t.Helper()
	lines := strings.Split(string(normalized), "\n")
	var kept []string
	for _, l := range lines {
		if strings.HasSuffix(l, " matches") ||
			strings.HasSuffix(l, " matched lines") ||
			strings.HasSuffix(l, " files contained matches") ||
			strings.HasSuffix(l, " files searched") ||
			strings.HasSuffix(l, " bytes printed") ||
			strings.HasSuffix(l, " bytes searched") {
			kept = append(kept, l)
		}
	}
	if len(kept) != 6 {
		t.Fatalf("expected 6 stats count lines, found %d:\n%s", len(kept), normalized)
	}
	return strings.Join(kept, "\n")
}

// TestGoldenVsRipgrep_QuietStatsExitCodes locks in the -q/--stats exit
// precedence against rg: -q keeps its "a confirmed match locks exit 0
// regardless of any per-file error" contract even under --stats (where the
// fast early-exit path is off and gg runs a full silent walk). A no-match
// run with an error still exits 2, and a clean no-match run exits 1.
func TestGoldenVsRipgrep_QuietStatsExitCodes(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	cases := []struct {
		name string
		args []string
	}{
		{"match_plus_error_locks_zero", []string{"-j1", "-q", "--stats", "needle", "a.txt", "nonexistent.txt"}},
		{"no_match_plus_error_is_two", []string{"-j1", "-q", "--stats", "absent", "a.txt", "nonexistent.txt"}},
		{"clean_no_match_is_one", []string{"-j1", "-q", "--stats", "absent", "a.txt"}},
		{"clean_match_is_zero", []string{"-j1", "-q", "--stats", "needle", "a.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := statsFixture(t)
			_, _, rgCode := runInDir(t, "rg", dir, tc.args)
			_, _, ggCode := runInDir(t, ggBin, dir, tc.args)
			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d", rgCode, ggCode)
			}
		})
	}
}

// TestGoldenVsRipgrep_StatsBinaryFile covers --stats over a file with a
// NUL and a match on each side of it, on both binary paths. rg stops
// COUNTING at the binary-detection point even when it keeps reading to
// reproduce the "binary file matches" message, so only the pre-NUL match
// counts. The walk-discovered path (binary-quit) is compared byte-for-byte;
// the explicit-arg path (binary-convert) is compared with the
// bytes-searched line dropped, since gg deliberately reports the full
// deterministic extent there where rg reports the consumed-to-NUL offset (a
// documented deviation -- see docs/rg-parity.md).
func TestGoldenVsRipgrep_StatsBinaryFile(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	makeDir := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "bin.dat"), "needle here\n\x00binary tail with needle\n")
		return dir
	}
	dropBytesSearched := func(b []byte) []byte {
		var out [][]byte
		for _, l := range bytes.Split(b, []byte("\n")) {
			if bytes.HasSuffix(l, []byte(" bytes searched")) {
				continue
			}
			out = append(out, l)
		}
		return bytes.Join(out, []byte("\n"))
	}

	t.Run("explicit_arg_counts_stop_at_nul", func(t *testing.T) {
		dir := makeDir(t)
		args := []string{"-j1", "--stats", "needle", "bin.dat"}
		rgOut, _, rgCode := runInDir(t, "rg", dir, args)
		ggOut, _, ggCode := runInDir(t, ggBin, dir, args)
		if rgCode != ggCode {
			t.Errorf("exit code mismatch: rg=%d gg=%d", rgCode, ggCode)
		}
		rgNorm, rgN := normStatsTiming(rgOut)
		ggNorm, ggN := normStatsTiming(ggOut)
		if rgN != 2 || ggN != 2 {
			t.Fatalf("expected 2 timing lines each, got rg=%d gg=%d", rgN, ggN)
		}
		if !bytes.Equal(dropBytesSearched(rgNorm), dropBytesSearched(ggNorm)) {
			t.Errorf("explicit-arg binary stats mismatch (bytes-searched excluded):\n--- rg ---\n%s\n--- gg ---\n%s", rgNorm, ggNorm)
		}
		// Pin the specific fix: exactly one match counted (the pre-NUL one).
		if !bytes.Contains(ggNorm, []byte("\n1 matches\n1 matched lines\n")) {
			t.Errorf("expected 1 matches / 1 matched lines (counting stops at the NUL), got:\n%s", ggNorm)
		}
	})

	t.Run("walk_discovered_full_block", func(t *testing.T) {
		dir := makeDir(t)
		args := []string{"-j1", "--stats", "needle"}
		rgOut, _, rgCode := runInDir(t, "rg", dir, args)
		ggOut, _, ggCode := runInDir(t, ggBin, dir, args)
		if rgCode != ggCode {
			t.Errorf("exit code mismatch: rg=%d gg=%d", rgCode, ggCode)
		}
		rgNorm, _ := normStatsTiming(rgOut)
		ggNorm, _ := normStatsTiming(ggOut)
		if !bytes.Equal(rgNorm, ggNorm) {
			t.Errorf("walk-discovered binary stats mismatch:\n--- rg ---\n%s\n--- gg ---\n%s", rgNorm, ggNorm)
		}
	})
}

// TestGoldenVsRipgrep_StatsEmptyPattern covers the empty-pattern occurrence
// count: rg counts one position per character plus one before the line
// terminator (never a phantom occurrence at the '\n'), so --stats' "N
// matches" must strip the terminator before its re-scan and agree with
// --count-matches and -o, all three of which are checked here against rg.
func TestGoldenVsRipgrep_StatsEmptyPattern(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	argsets := [][]string{
		{"-j1", "--stats", "", "a.txt"},
		{"-j1", "--count-matches", "", "a.txt"},
		{"-j1", "-o", "", "a.txt"},
		{"-j1", "-c", "--stats", "", "a.txt"},
	}
	for _, args := range argsets {
		t.Run(strings.Join(args[1:], "_"), func(t *testing.T) {
			dir := statsFixture(t)
			rgOut, _, rgCode := runInDir(t, "rg", dir, args)
			ggOut, _, ggCode := runInDir(t, ggBin, dir, args)
			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d", rgCode, ggCode)
			}
			rgNorm, _ := normStatsTiming(rgOut)
			ggNorm, _ := normStatsTiming(ggOut)
			if !bytes.Equal(rgNorm, ggNorm) {
				t.Errorf("empty-pattern output mismatch:\n--- rg ---\n%s\n--- gg ---\n%s", rgNorm, ggNorm)
			}
		})
	}
}

// TestGoldenVsRipgrep_BufferingByteInvariant confirms the buffering flags
// are byte-invisible: --line-buffered, --block-buffered, both together (last
// wins), and the negations must all produce EXACTLY the bytes and exit code
// of the same search without any buffering flag, in every output mode.
func TestGoldenVsRipgrep_BufferingByteInvariant(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	// Each base is a full search invocation; every buffering variant of it
	// must match the base byte-for-byte.
	bases := [][]string{
		{"-j1", "needle"},
		{"-j1", "-c", "needle"},
		{"-j1", "-l", "needle"},
		{"-j1", "--stats", "needle"},
		{"-j1", "-A1", "needle"},
		{"-j1", "--files"},
	}
	variants := [][]string{
		{"--line-buffered"},
		{"--block-buffered"},
		{"--line-buffered", "--block-buffered"},
		{"--block-buffered", "--line-buffered"},
		{"--line-buffered", "--no-line-buffered"},
		{"--block-buffered", "--no-block-buffered"},
	}

	for _, base := range bases {
		base := base
		t.Run(strings.Join(base, "_"), func(t *testing.T) {
			dir := statsFixture(t)
			// The buffering flag never changes bytes, but --stats' timing
			// lines are still nondeterministic run-to-run, so normalize them
			// before comparing base against each variant.
			wantOut, _, wantCode := runInDir(t, ggBin, dir, base)
			wantNorm, _ := normStatsTiming(wantOut)
			// Cross-check the base itself matches rg, so this isn't just gg
			// agreeing with gg.
			rgOut, _, rgCode := runInDir(t, "rg", dir, base)
			rgNorm, _ := normStatsTiming(rgOut)
			if rgCode != wantCode || !bytes.Equal(rgNorm, wantNorm) {
				t.Fatalf("base disagrees with rg before buffering variants:\n--- rg ---\n%s\n--- gg ---\n%s", rgNorm, wantNorm)
			}

			for _, v := range variants {
				args := append(append([]string{}, base...), v...)
				gotOut, _, gotCode := runInDir(t, ggBin, dir, args)
				gotNorm, _ := normStatsTiming(gotOut)
				if gotCode != wantCode {
					t.Errorf("%v: exit %d, want %d", v, gotCode, wantCode)
				}
				if !bytes.Equal(gotNorm, wantNorm) {
					t.Errorf("%v changed output bytes:\n--- without ---\n%s\n--- with ---\n%s", v, wantNorm, gotNorm)
				}
				// And rg agrees the variant is byte-invisible too.
				rgVarOut, _, _ := runInDir(t, "rg", dir, args)
				rgVarNorm, _ := normStatsTiming(rgVarOut)
				if !bytes.Equal(rgVarNorm, rgNorm) {
					t.Errorf("%v: rg's own output changed (unexpected):\n%s", v, rgVarNorm)
				}
			}
		})
	}
}
