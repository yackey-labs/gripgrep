//go:build e2e

// Golden e2e coverage for --json, diffed against the pinned rg 15.1.0 (see
// e2e_test.go's TestMain). The whole event stream is byte-contractual EXCEPT
// the three timing objects rg fills with wall-clock values: each end
// message's stats.elapsed (serde struct order secs/nanos/human) and the
// trailing summary's stats.elapsed and elapsed_total (alphabetical order,
// since rg builds the summary through serde_json::json!). normJSONElapsed
// rewrites exactly those objects and returns how many it replaced; every
// case asserts that count (so a stream that gains, loses, or reshapes an
// elapsed object -- or a normalizer that silently over-matches real content
// -- fails rather than being normalized into agreement) before the raw byte
// comparison. -j1 keeps the per-file blocks in the oracle's deterministic
// order; the parallel path's atomicity gets its own test.
package gripgrep_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// jsonFixture writes the answer key's exact fixture (a.txt/sub/b.txt/c.txt
// under a .git-marked repo root). Byte counts are load-bearing for the
// bytes_searched/bytes_printed/absolute_offset numbers, so they mirror the
// oracle exactly.
func jsonFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "alpha needle one\nplain line\nneedle two needle three\n")
	mustWrite(t, filepath.Join(dir, "sub", "b.txt"), "sub needle\n")
	mustWrite(t, filepath.Join(dir, "c.txt"), "no match\n")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

var (
	jsonElapsedStruct = regexp.MustCompile(`"elapsed":\{"secs":[0-9]+,"nanos":[0-9]+,"human":"[0-9.]+s"\}`)
	jsonElapsedAlpha  = regexp.MustCompile(`"elapsed(_total)?":\{"human":"[0-9.]+s","nanos":[0-9]+,"secs":[0-9]+\}`)
)

// normJSONElapsed rewrites the three kinds of elapsed object to a fixed
// placeholder and returns the total replaced. The struct-order form appears
// once per end message; the alphabetical form appears twice in the summary
// (elapsed + elapsed_total). Nothing else in a --json stream matches these
// anchored shapes, so a nonzero-but-unexpected count means the stream itself
// drifted.
func normJSONElapsed(out []byte) (normalized []byte, replaced int) {
	n := 0
	out = jsonElapsedStruct.ReplaceAllFunc(out, func([]byte) []byte {
		n++
		return []byte(`"elapsed":{"secs":0,"nanos":0,"human":"T"}`)
	})
	out = jsonElapsedAlpha.ReplaceAllFunc(out, func(m []byte) []byte {
		n++
		if bytes.HasPrefix(m, []byte(`"elapsed_total"`)) {
			return []byte(`"elapsed_total":{"human":"T","nanos":0,"secs":0}`)
		}
		return []byte(`"elapsed":{"human":"T","nanos":0,"secs":0}`)
	})
	return out, n
}

// TestGoldenVsRipgrep_JSON replays the answer key's --json probes (and the
// extended probes) against real rg, byte-for-byte with only the three
// elapsed objects normalized. elapsed is the expected number of elapsed
// objects: 2 per emitted summary (its elapsed + elapsed_total) plus one per
// end message (i.e. per file that emitted events), or 0 for the summary-mode
// flags that ignore --json and print their own plain output.
func TestGoldenVsRipgrep_JSON(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	cases := []struct {
		name    string
		args    []string
		elapsed int
	}{
		// Core stream shapes (J1-J3, J5).
		{"basic", []string{"-j1", "--json", "needle"}, 4},              // a.txt + sub/b.txt end + summary(2)
		{"no_matches", []string{"-j1", "--json", "absent"}, 2},         // summary only
		{"context", []string{"-j1", "--json", "-A1", "-B1", "plain"}, 3},
		{"invert", []string{"-j1", "--json", "-v", "needle", "a.txt"}, 3},
		// Summary-mode fallbacks: --json is ignored, plain output, 0 elapsed
		// objects (J6/J7/J9 + --count-matches/--files-without-match).
		{"count_fallback", []string{"-j1", "--json", "-c", "needle", "a.txt"}, 0},
		{"files_with_matches_fallback", []string{"-j1", "--json", "-l", "needle", "a.txt"}, 0},
		{"files_fallback", []string{"-j1", "--json", "--files"}, 0},
		{"count_matches_fallback", []string{"-j1", "--json", "--count-matches", "needle", "a.txt"}, 0},
		{"files_without_match_fallback", []string{"-j1", "--json", "--files-without-match", "needle"}, 0},
		// -q -> summary only (J8).
		{"quiet_summary_only", []string{"-j1", "--json", "-q", "needle", "a.txt"}, 2},
		// Flags ignored under --json (J10/J11/J12/J19/J22).
		{"only_matching_ignored", []string{"-j1", "--json", "-o", "needle", "a.txt"}, 3},
		{"stats_adds_nothing", []string{"-j1", "--json", "--stats", "needle", "a.txt"}, 3},
		{"color_ignored", []string{"-j1", "--json", "--color=always", "needle", "a.txt"}, 3},
		{"max_columns_ignored", []string{"-j1", "--json", "-M5", "needle", "a.txt"}, 3},
		{"no_filename_ignored", []string{"-j1", "--json", "--no-filename", "needle", "a.txt"}, 3},
		{"heading_ignored", []string{"-j1", "--json", "--heading", "needle", "a.txt"}, 3},
		{"vimgrep_ignored", []string{"-j1", "--json", "--vimgrep", "needle", "a.txt"}, 3},
		{"column_ignored", []string{"-j1", "--json", "--column", "needle", "a.txt"}, 3},
		{"byte_offset_ignored", []string{"-j1", "--json", "-b", "needle", "a.txt"}, 3},
		// Content encoding (J13/J14/J16/J18/J21).
		{"empty_pattern", []string{"-j1", "--json", "", "a.txt"}, 3},
		{"crlf", []string{"-j1", "--json", "--crlf", "needle", "a.txt"}, 3},
		// Semantics / extended.
		{"passthru", []string{"-j1", "--json", "--passthru", "needle", "a.txt"}, 3},
		{"sort_path", []string{"-j1", "--json", "--sort", "path", "needle"}, 4},
		{"multi_root", []string{"-j1", "--json", "needle", "a.txt", "sub/b.txt"}, 4},
		{"after_context_multi", []string{"-j1", "--json", "-A1", "needle"}, 4},
		{"no_json_negation", []string{"-j1", "--json", "--no-json", "needle", "a.txt"}, 0},
		{"nonexistent_path", []string{"-j1", "--json", "needle", "a.txt", "nonexistent.txt"}, 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := jsonFixture(t)
			rgOut, rgErr, rgCode := runInDir(t, "rg", dir, tc.args)
			ggOut, ggErr, ggCode := runInDir(t, ggBin, dir, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			rgNorm, rgN := normJSONElapsed(rgOut)
			ggNorm, ggN := normJSONElapsed(ggOut)
			if rgN != tc.elapsed {
				t.Fatalf("rg produced %d elapsed objects, expected %d -- oracle/fixture drift:\n%s", rgN, tc.elapsed, rgOut)
			}
			if ggN != tc.elapsed {
				t.Errorf("gg produced %d elapsed objects, expected %d (drifted stream):\n%s", ggN, tc.elapsed, ggOut)
			}
			if !bytes.Equal(rgNorm, ggNorm) {
				t.Errorf("json stream mismatch (elapsed normalized):\n--- rg ---\n%s\n--- gg ---\n%s", rgNorm, ggNorm)
			}
		})
	}
}

// TestGoldenVsRipgrep_JSONNonUTF8 covers rg's Data base64 fallback for
// non-UTF-8 bytes in BOTH line content and the file path (answer key J13/J14):
// a valid-UTF-8 field stays {"text":...}, an invalid one becomes
// {"bytes":<standard base64>}, decided per field so one file can mix them.
func TestGoldenVsRipgrep_JSONNonUTF8(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	t.Run("invalid_utf8_line_content", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "bin.txt"), "needle \xff\xfe tail\nplain needle\n")
		args := []string{"-j1", "--json", "needle", "bin.txt"}
		compareJSON(t, ggBin, dir, args, 3)
	})

	t.Run("invalid_utf8_path", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "bad\xffname.txt"), "needle here\n")
		args := []string{"-j1", "--json", "needle", "bad\xffname.txt"}
		compareJSON(t, ggBin, dir, args, 3)
	})
}

// TestGoldenVsRipgrep_JSONBinaryExplicit covers the answer key's J15/J21: an
// explicitly-named binary file emits its pre-NUL match as escaped text, sets
// end.binary_offset to the NUL offset, and reports bytes_searched clamped to
// that offset -- no plain-text "binary file matches" line, exit 0. --null-data
// treats the NUL as a record terminator instead (no binary detection).
func TestGoldenVsRipgrep_JSONBinaryExplicit(t *testing.T) {
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
		mustWrite(t, filepath.Join(dir, "nul.dat"), "needle\x00binary\n")
		return dir
	}
	t.Run("binary_offset_and_clamped_bytes_searched", func(t *testing.T) {
		compareJSON(t, ggBin, makeDir(t), []string{"-j1", "--json", "needle", "nul.dat"}, 3)
	})
	t.Run("null_data", func(t *testing.T) {
		compareJSON(t, ggBin, makeDir(t), []string{"-j1", "--json", "--null-data", "needle", "nul.dat"}, 3)
	})
}

// TestGoldenVsRipgrep_JSONWalkBinaryQuarantine pins the summary accounting
// for walk-discovered binary files: rg quarantines them (searcher quits at
// the NUL, no events emitted) and attributes them NO bytes_searched -- only a
// searches+1 -- so the summary's bytes_searched sums the TEXT files only. gg
// must not leak a quarantined file's clamped offset into that total. The
// fixture mixes text files (a/b/c.txt), an invalid-UTF-8-but-NUL-free text
// file (bin.txt, which IS searched and DOES emit events), and two NUL-bearing
// files at different offsets (nul.dat @6, conv.bin @24). The contrast an
// explicit-arg binary (which DOES contribute its clamped bytes) is covered by
// TestGoldenVsRipgrep_JSONBinaryExplicit.
func TestGoldenVsRipgrep_JSONWalkBinaryQuarantine(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "a.txt"), "alpha needle one\nplain line\nneedle two needle three\n")
	mustWrite(t, filepath.Join(dir, "sub", "b.txt"), "sub needle\n")
	mustWrite(t, filepath.Join(dir, "c.txt"), "no match\n")
	mustWrite(t, filepath.Join(dir, "bin.txt"), "needle \xff\xfe tail\nplain needle\n")
	mustWrite(t, filepath.Join(dir, "nul.dat"), "needle\x00binary\n")
	mustWrite(t, filepath.Join(dir, "conv.bin"), "first needle line\nsecond\x00needle after nul\nthird needle line\n")

	args := []string{"-j1", "--json", "needle"}
	rgOut, _, rgCode := runInDir(t, "rg", dir, args)
	ggOut, ggErr, ggCode := runInDir(t, ggBin, dir, args)
	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\ngg stderr: %s", rgCode, ggCode, ggErr)
	}
	rgNorm, rgN := normJSONElapsed(rgOut)
	ggNorm, ggN := normJSONElapsed(ggOut)
	if ggN != rgN {
		t.Errorf("gg normalized %d elapsed objects, rg %d", ggN, rgN)
	}
	if !bytes.Equal(rgNorm, ggNorm) {
		t.Errorf("walk-binary quarantine stream mismatch (elapsed normalized):\n--- rg ---\n%s\n--- gg ---\n%s", rgNorm, ggNorm)
	}
	// Guard the specific field: the summary's bytes_searched must sum the text
	// files only (52+11+9+28 = 100), never the quarantined binaries' offsets.
	if !bytes.Contains(ggNorm, []byte(`"bytes_searched":100,`)) {
		t.Errorf("expected summary bytes_searched:100 (text files only):\n%s", ggNorm)
	}
}

// TestJSONParallelAtomicAndSummaryLast verifies that under the DEFAULT
// (parallel) worker pool, every file's begin..end block stays contiguous
// (never interleaved across files) and the summary is always the very last
// line -- the invariants rg's own parallel --json output holds, and gg's
// per-file buffered/atomic Dest.WriteBlock flush preserves. Order across
// files is nondeterministic, so this checks structure, not a byte-golden.
func TestJSONParallelAtomicAndSummaryLast(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		mustWrite(t, filepath.Join(dir, name+".txt"), "needle one\nneedle two\n")
	}
	for i := 0; i < 20; i++ {
		out, _, code := runInDir(t, ggBin, dir, []string{"--json", "needle"})
		if code != 0 {
			t.Fatalf("run %d exited %d", i, code)
		}
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		if len(lines) == 0 {
			t.Fatalf("run %d: no output", i)
		}
		// Summary is the last line.
		if !strings.HasPrefix(lines[len(lines)-1], `{"data":{"elapsed_total":`) {
			t.Fatalf("run %d: last line is not the summary:\n%s", i, lines[len(lines)-1])
		}
		// Walk the stream: within one file, begin -> (match|context)* -> end,
		// and no begin may open while another file's block is still open.
		open := ""
		for _, l := range lines[:len(lines)-1] {
			path := eventPath(t, l)
			switch {
			case strings.HasPrefix(l, `{"type":"begin"`):
				if open != "" {
					t.Fatalf("run %d: begin for %q while %q still open (interleaved block)", i, path, open)
				}
				open = path
			case strings.HasPrefix(l, `{"type":"end"`):
				if open != path {
					t.Fatalf("run %d: end for %q but open block is %q", i, path, open)
				}
				open = ""
			default: // match/context
				if open != path {
					t.Fatalf("run %d: %s line for %q outside its block (open=%q)", i, l[:16], path, open)
				}
			}
		}
		if open != "" {
			t.Fatalf("run %d: block for %q never closed", i, open)
		}
	}
}

var eventPathRe = regexp.MustCompile(`"path":\{"text":"([^"]*)"\}`)

func eventPath(t *testing.T, line string) string {
	t.Helper()
	m := eventPathRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("no text path in event line: %s", line)
	}
	return m[1]
}

// compareJSON runs rg and gg with args in dir and asserts identical exit
// codes and byte-identical output after normalizing exactly wantElapsed
// elapsed objects.
func compareJSON(t *testing.T, ggBin, dir string, args []string, wantElapsed int) {
	t.Helper()
	rgOut, rgErr, rgCode := runInDir(t, "rg", dir, args)
	ggOut, ggErr, ggCode := runInDir(t, ggBin, dir, args)
	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	rgNorm, rgN := normJSONElapsed(rgOut)
	ggNorm, ggN := normJSONElapsed(ggOut)
	if rgN != wantElapsed {
		t.Fatalf("rg produced %d elapsed objects, expected %d:\n%s", rgN, wantElapsed, rgOut)
	}
	if ggN != wantElapsed {
		t.Errorf("gg produced %d elapsed objects, expected %d:\n%s", ggN, wantElapsed, ggOut)
	}
	if !bytes.Equal(rgNorm, ggNorm) {
		t.Errorf("json mismatch (elapsed normalized):\n--- rg ---\n%s\n--- gg ---\n%s", rgNorm, ggNorm)
	}
}
