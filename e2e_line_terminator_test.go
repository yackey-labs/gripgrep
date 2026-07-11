//go:build e2e

// Golden e2e coverage for --crlf and --null-data (the line-terminator
// cluster), diffed BYTE-FOR-BYTE against the pinned rg 15.1.0 (see
// e2e_test.go's TestMain). Unlike the order-independent, '\n'-sorted main
// golden suite, these cases compare raw stdout bytes directly: the
// terminator bytes ('\r', '\x00') ARE the contract, and sorting on '\n'
// would corrupt '\x00'-delimited output. Every fixture is written from a
// byte slice via os.WriteFile so a '\r'/'\x00' never round-trips through a
// shell or a string literal. Each case runs rg and gg with identical args
// in the same isolated temp dir and asserts identical stdout and exit
// code; --stats normalizes only its two nondeterministic timing lines
// (normStatsTiming, shared with e2e_stats_test.go).
package gripgrep_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// ltFixture writes the line-terminator fixtures (CRLF files, NUL-delimited
// records, mixed endings, no-terminator tails) into a fresh temp dir with a
// .git marker so the walk treats it as a repo root, and returns the dir.
// Byte counts and terminator bytes are load-bearing, so each file is
// written from an explicit []byte.
func ltFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("crlf.txt", []byte("foo\r\nbar\r\nfoobar\r\n"))
	write("crlf6.txt", []byte("foo\r\nb\r\nc\r\nd\r\ne\r\nfoo2\r\n"))
	write("mixed.txt", []byte("foo\nbar\r\nmixed\n"))
	write("noterm.txt", []byte("last no term\r\ntail"))
	write("lonecr.txt", []byte("x\ry\rz\r"))
	write("nul.dat", []byte("rec1 needle\x00rec2\x00needle again\x00"))
	write("nofin.dat", []byte("no final term needle"))
	write("empty.dat", []byte("a\x00\x00needle\x00"))
	write("nlinside.dat", []byte("text with\nnewline needle\x00second\x00"))
	write("multi.dat", []byte("A needle\x00B\x00C needle\x00D\x00E needle\x00"))
	write("spananchor.dat", []byte("foo\nbar\x00zzz\x00"))
	write("spanx.dat", []byte("hi\nfoo\nbye\x00qq\x00"))
	write("gap.dat", []byte("r0 aaa\x00r1\x00r2\x00r3\x00r4 ddd\x00"))
	return dir
}

// TestGoldenVsRipgrep_LineTerminator diffs raw stdout bytes and exit code
// against rg for every --crlf/--null-data answer-key probe plus the
// extended flag-order, anchor, and multi-file cases discovered against the
// real binary.
func TestGoldenVsRipgrep_LineTerminator(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Dir(thisFile)
	ggBin := buildGG(t, root)

	cases := []struct {
		name string
		args []string
		// stats normalizes the two nondeterministic timing lines before the
		// byte compare; walk sorts the '\x00'-delimited records before the
		// compare (multi-file walk order is not contractual).
		stats bool
		walk  bool
	}{
		// --- --crlf ---
		{name: "crlf_dollar", args: []string{"-j1", "-n", "--crlf", "foo$", "crlf.txt"}},
		{name: "crlf_no_flag_dollar_misses", args: []string{"-j1", "-n", "foo$", "crlf.txt"}},
		{name: "crlf_line_regexp", args: []string{"-j1", "-n", "--crlf", "-x", "foo", "crlf.txt"}},
		{name: "crlf_line_regexp_no_flag_misses", args: []string{"-j1", "-n", "-x", "foo", "crlf.txt"}},
		{name: "crlf_only_matching_dot", args: []string{"-j1", "--crlf", "-o", "foo.", "crlf.txt"}},
		{name: "crlf_mixed_lf_line", args: []string{"-j1", "-n", "--crlf", "mixed$", "mixed.txt"}},
		{name: "crlf_mixed_crlf_line", args: []string{"-j1", "-n", "--crlf", "bar$", "mixed.txt"}},
		{name: "crlf_no_trailing_terminator", args: []string{"-j1", "-n", "--crlf", "tail$", "noterm.txt"}},
		{name: "crlf_negation_last_wins", args: []string{"-j1", "-n", "--crlf", "--no-crlf", "foo$", "crlf.txt"}},
		{name: "crlf_after_context", args: []string{"-j1", "--crlf", "-A1", "foo$", "crlf.txt"}},
		{name: "crlf_count", args: []string{"-j1", "--crlf", "-c", "foo$", "crlf.txt"}},
		{name: "crlf_stats_count_anchored", args: []string{"-j1", "--crlf", "--stats", "-c", "foo$", "crlf.txt"}, stats: true},
		{name: "crlf_stats_anchored", args: []string{"-j1", "--crlf", "--stats", "foo$", "crlf.txt"}, stats: true},
		{name: "crlf_count_matches_anchored", args: []string{"-j1", "--crlf", "--count-matches", "foo$", "crlf.txt"}},
		{name: "crlf_column_byteoffset", args: []string{"-j1", "-b", "--column", "--crlf", "bar$", "crlf.txt"}},
		{name: "crlf_context_gap_separator", args: []string{"-j1", "--crlf", "-C1", "-e", "foo$", "-e", "foo2$", "crlf6.txt"}},
		{name: "crlf_heading", args: []string{"-j1", "--crlf", "--heading", "-n", "-H", "foo", "crlf.txt"}},
		{name: "crlf_null_data_lastwins", args: []string{"-j1", "--null-data", "--crlf", "needle", "nul.dat"}},

		// --- --null-data ---
		{name: "null_basic", args: []string{"-j1", "--null-data", "needle", "nul.dat"}},
		{name: "null_line_numbers", args: []string{"-j1", "-n", "--null-data", "needle", "nul.dat"}},
		{name: "null_no_final_terminator", args: []string{"-j1", "--null-data", "needle", "nofin.dat"}},
		{name: "null_empty_record", args: []string{"-j1", "-n", "--null-data", "needle", "empty.dat"}},
		{name: "null_record_spans_newline", args: []string{"-j1", "-n", "--null-data", "newline needle", "nlinside.dat"}},
		{name: "null_dollar_anchor", args: []string{"-j1", "--null-data", "needle$", "nul.dat"}},
		{name: "null_only_matching", args: []string{"-j1", "--null-data", "-o", "needle", "nul.dat"}},
		{name: "null_after_context", args: []string{"-j1", "--null-data", "-A1", "rec1", "nul.dat"}},
		{name: "null_binary_detection_off", args: []string{"-j1", "--null-data", "rec2", "nul.dat"}},
		{name: "null_count", args: []string{"-j1", "--null-data", "-c", "needle", "nul.dat"}},
		{name: "null_passthru", args: []string{"-j1", "--null-data", "--passthru", "rec2", "nul.dat"}},
		{name: "null_line_regexp", args: []string{"-j1", "--null-data", "-x", "rec2", "nul.dat"}},
		{name: "null_whole_newline_file_one_record", args: []string{"-j1", "-n", "--null-data", "foo", "crlf.txt"}},
		{name: "null_stats", args: []string{"-j1", "--null-data", "--stats", "needle", "nul.dat"}, stats: true},
		{name: "null_stats_anchored", args: []string{"-j1", "--null-data", "--stats", "needle$", "nul.dat"}, stats: true},
		{name: "null_count_matches_anchored", args: []string{"-j1", "--null-data", "--count-matches", "needle$", "nul.dat"}},
		{name: "null_column_byteoffset", args: []string{"-j1", "-b", "--column", "--null-data", "again", "nul.dat"}},
		{name: "null_invert", args: []string{"-j1", "--null-data", "-v", "-n", "needle", "multi.dat"}},
		{name: "null_max_count", args: []string{"-j1", "--null-data", "-m2", "-n", "needle", "multi.dat"}},
		{name: "null_null_flag_path_terminator", args: []string{"-j1", "-0", "--null-data", "-n", "-H", "needle", "multi.dat"}},
		{name: "null_no_crlf_stays_null", args: []string{"-j1", "--null-data", "--no-crlf", "needle", "nul.dat"}},
		{name: "null_crlf_no_crlf_plain", args: []string{"-j1", "-n", "--null-data", "--crlf", "--no-crlf", "needle", "nul.dat"}},
		{name: "null_crlf_lastwins_binary", args: []string{"-j1", "--null-data", "--crlf", "needle", "nul.dat"}},
		{name: "null_context_gap_separator", args: []string{"-j1", "--null-data", "-C1", "-e", "aaa", "-e", "ddd", "gap.dat"}},
		{name: "null_field_context_separator", args: []string{"-j1", "--null-data", "-C1", "--field-context-separator=@", "-n", "-e", "aaa", "-e", "ddd", "gap.dat"}},

		// --- null-data regex anchor / dot semantics (rg's (?m) behavior) ---
		{name: "null_dot_no_cross_newline", args: []string{"-j1", "--null-data", "-o", "foo.bar", "spananchor.dat"}},
		{name: "null_dollar_interior_newline", args: []string{"-j1", "--null-data", "-o", "foo$", "spananchor.dat"}},
		{name: "null_dollar_record_end", args: []string{"-j1", "--null-data", "-o", "bar$", "spananchor.dat"}},
		{name: "null_line_regexp_interior_line", args: []string{"-j1", "--null-data", "-n", "-x", "foo", "spanx.dat"}},

		// --- summary modes across files ---
		{name: "null_files_with_matches", args: []string{"-j1", "-l", "--null-data", "needle", "multi.dat", "nul.dat"}},
		{name: "null_count_multi_file", args: []string{"-j1", "-c", "--null-data", "needle", "multi.dat", "nul.dat"}},
		{name: "null_heading_multi_file", args: []string{"-j1", "--null-data", "--heading", "-n", "needle", "multi.dat", "nul.dat"}},

		// --- --files ignores the terminator (always '\n') ---
		{name: "files_crlf", args: []string{"-j1", "--files", "--crlf"}, walk: true},
		{name: "files_null_data", args: []string{"-j1", "--files", "--null-data"}, walk: true},

		// --- walk discovery under --null-data (binary detection off) ---
		{name: "null_walk_discovery", args: []string{"-j1", "--null-data", "needle"}, walk: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := ltFixture(t)
			rgOut, rgErr, rgCode := runInDir(t, "rg", dir, tc.args)
			ggOut, ggErr, ggCode := runInDir(t, ggBin, dir, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}

			want, got := rgOut, ggOut
			if tc.stats {
				var nrg, ngg int
				want, nrg = normStatsTiming(want)
				got, ngg = normStatsTiming(got)
				if nrg != 2 || ngg != 2 {
					t.Fatalf("expected 2 timing lines normalized in each (rg=%d gg=%d) -- stats block drifted", nrg, ngg)
				}
			}
			if tc.walk {
				// Walk order across multiple files is not contractual, so
				// compare the terminated records as a sorted multiset.
				want = sortRecords(want)
				got = sortRecords(got)
			}
			if !bytes.Equal(want, got) {
				t.Errorf("raw stdout mismatch\n--- rg (%d bytes) ---\n%q\n--- gg (%d bytes) ---\n%q\n--- rg stderr ---\n%s\n--- gg stderr ---\n%s",
					len(rgOut), rgOut, len(ggOut), ggOut, rgErr, ggErr)
			}
		})
	}
}

// sortRecords splits out on whichever terminator it carries (NUL if any NUL
// is present, else '\n'), sorts the resulting records, and rejoins them --
// making a byte comparison insensitive to multi-file walk order without
// losing the terminator bytes themselves (each record keeps its own).
func sortRecords(out []byte) []byte {
	if len(out) == 0 {
		return out
	}
	term := byte('\n')
	if bytes.IndexByte(out, 0) >= 0 {
		term = 0
	}
	var recs [][]byte
	for len(out) > 0 {
		i := bytes.IndexByte(out, term)
		if i < 0 {
			recs = append(recs, out)
			break
		}
		recs = append(recs, out[:i+1])
		out = out[i+1:]
	}
	sort.Slice(recs, func(i, j int) bool { return bytes.Compare(recs[i], recs[j]) < 0 })
	return bytes.Join(recs, nil)
}
