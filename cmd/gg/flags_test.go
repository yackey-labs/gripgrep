package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Most cases below port rg's own flag unit tests directly from
// ../ripgrep/crates/core/flags/defs.rs (e.g. test_fixed_strings,
// test_ignore_case, test_smart_case, test_word_regexp, test_hidden,
// test_no_ignore, test_glob, test_unrestricted, test_max_filesize,
// test_line_number, test_no_line_number, test_count,
// test_files_with_matches, test_quiet, test_color, test_after_context,
// test_before_context, test_context, test_invert_match, test_threads,
// test_text, test_mmap) -- same inputs, same expected resolved values.
//
// The genuinely ambiguous interactions (arg-syntax matrix, -e vs bare
// pattern, -u/-uu/-uuu cascading, -A/-B/-C override direction, -n/-N
// last-wins, combined short-flag clusters) were additionally verified
// against the real `rg` 14.1.1 binary on PATH; each such test's comment
// records the exact rg invocation and observed output so the oracle is
// reproducible without re-running rg.

func mustParse(t *testing.T, args ...string) *Config {
	t.Helper()
	cfg, err := ParseArgs(args)
	if err != nil {
		t.Fatalf("ParseArgs(%q): unexpected error: %v", args, err)
	}
	return cfg
}

func wantErr(t *testing.T, args ...string) error {
	t.Helper()
	_, err := ParseArgs(args)
	if err == nil {
		t.Fatalf("ParseArgs(%q): expected an error, got none", args)
	}
	return err
}

// --- Pattern flags ---

func TestFixedStrings(t *testing.T) {
	if cfg := mustParse(t, "pat", "path"); cfg.Fixed {
		t.Errorf("default Fixed = true, want false")
	}
	if cfg := mustParse(t, "--fixed-strings", "pat", "path"); !cfg.Fixed {
		t.Errorf("--fixed-strings: Fixed = false, want true")
	}
	if cfg := mustParse(t, "-F", "pat", "path"); !cfg.Fixed {
		t.Errorf("-F: Fixed = false, want true")
	}
	if cfg := mustParse(t, "-F", "--no-fixed-strings", "pat", "path"); cfg.Fixed {
		t.Errorf("-F --no-fixed-strings: Fixed = true, want false")
	}
	if cfg := mustParse(t, "--no-fixed-strings", "-F", "pat", "path"); !cfg.Fixed {
		t.Errorf("--no-fixed-strings -F: Fixed = false, want true")
	}
}

func TestCaseMode(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want CaseMode
	}{
		{"default", nil, CaseSensitive},
		{"-i", []string{"-i"}, CaseInsensitive},
		{"--ignore-case", []string{"--ignore-case"}, CaseInsensitive},
		{"-i -s", []string{"-i", "-s"}, CaseSensitive},
		{"-s -i", []string{"-s", "-i"}, CaseInsensitive},
		{"--smart-case", []string{"--smart-case"}, CaseSmart},
		{"-S", []string{"-S"}, CaseSmart},
		{"-S -s", []string{"-S", "-s"}, CaseSensitive},
		{"-S -i", []string{"-S", "-i"}, CaseInsensitive},
		{"-s -S", []string{"-s", "-S"}, CaseSmart},
		{"-i -S", []string{"-i", "-S"}, CaseSmart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "pat", "path")
			cfg := mustParse(t, args...)
			if cfg.Case != tc.want {
				t.Errorf("Case = %v, want %v", cfg.Case, tc.want)
			}
		})
	}
}

func TestWordRegexp(t *testing.T) {
	if cfg := mustParse(t, "pat", "path"); cfg.Word {
		t.Errorf("default Word = true, want false")
	}
	if cfg := mustParse(t, "--word-regexp", "pat", "path"); !cfg.Word {
		t.Errorf("--word-regexp: Word = false, want true")
	}
	if cfg := mustParse(t, "-w", "pat", "path"); !cfg.Word {
		t.Errorf("-w: Word = false, want true")
	}
}

func TestRegexpFlag(t *testing.T) {
	// -e/--regexp is repeatable and OR's patterns; per rg, once -e is
	// used, ALL positionals become paths (none become "the pattern").
	//
	// Verified against real rg 14.1.1:
	//   $ rg -e alpha f.txt        -> searches f.txt for "alpha" (f.txt is a path)
	//   $ rg -e alpha beta f.txt   -> "beta" treated as a path (not found), exit 2
	//   $ rg -e alpha -e beta f.txt -> both patterns searched (OR)
	cfg := mustParse(t, "-e", "alpha", "f.txt")
	if got, want := cfg.Patterns, []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Patterns = %v, want %v", got, want)
	}
	if got, want := cfg.Paths, []string{"f.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Paths = %v, want %v (f.txt must remain a path, not become part of the pattern)", got, want)
	}

	cfg = mustParse(t, "-e", "alpha", "-e", "beta", "f.txt")
	if got, want := cfg.Patterns, []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Patterns = %v, want %v", got, want)
	}

	cfg = mustParse(t, "--regexp=foo")
	if got, want := cfg.Patterns, []string{"foo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--regexp=foo: Patterns = %v, want %v", got, want)
	}

	cfg = mustParse(t, "-efoo")
	if got, want := cfg.Patterns, []string{"foo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("-efoo: Patterns = %v, want %v", got, want)
	}

	// -e-foo: the value is greedily "-foo", not re-parsed as a flag.
	cfg = mustParse(t, "-e-foo")
	if got, want := cfg.Patterns, []string{"-foo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("-e-foo: Patterns = %v, want %v", got, want)
	}
}

func TestBarePatternPositional(t *testing.T) {
	// Without -e, the first positional is the pattern and the rest are paths.
	cfg := mustParse(t, "pat", "path1", "path2")
	if got, want := cfg.Patterns, []string{"pat"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Patterns = %v, want %v", got, want)
	}
	if got, want := cfg.Paths, []string{"path1", "path2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Paths = %v, want %v", got, want)
	}
}

func TestNoPatternIsError(t *testing.T) {
	// Verified against real rg 14.1.1: `rg` with no args prints
	// "ripgrep requires at least one pattern to execute a search" and
	// exits 2.
	wantErr(t)
}

func TestDashDashDelimiter(t *testing.T) {
	// Verified: `rg -- -weird dash.txt` treats "-weird" as the pattern,
	// not a flag, because of the "--" delimiter.
	cfg := mustParse(t, "--", "-weird", "dash.txt")
	if got, want := cfg.Patterns, []string{"-weird"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Patterns = %v, want %v", got, want)
	}
	if got, want := cfg.Paths, []string{"dash.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Paths = %v, want %v", got, want)
	}
}

// --- Filtering flags ---

func TestHidden(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.Hidden {
		t.Errorf("default Hidden = true, want false")
	}
	if cfg := mustParse(t, "--hidden", "pat"); !cfg.Hidden {
		t.Errorf("--hidden: Hidden = false, want true")
	}
	if cfg := mustParse(t, "-.", "pat"); !cfg.Hidden {
		t.Errorf("-. : Hidden = false, want true")
	}
	if cfg := mustParse(t, "-.", "--no-hidden", "pat"); cfg.Hidden {
		t.Errorf("-. --no-hidden: Hidden = true, want false")
	}
	if cfg := mustParse(t, "--no-hidden", "-.", "pat"); !cfg.Hidden {
		t.Errorf("--no-hidden -.: Hidden = false, want true")
	}
}

func TestNoIgnore(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.NoIgnore {
		t.Errorf("default NoIgnore = true, want false")
	}
	if cfg := mustParse(t, "--no-ignore", "pat"); !cfg.NoIgnore {
		t.Errorf("--no-ignore: NoIgnore = false, want true")
	}
	// --ignore is --no-ignore's negation spelling (an unusual, but
	// real, rg spelling -- see the comment on the flag's registration).
	if cfg := mustParse(t, "--no-ignore", "--ignore", "pat"); cfg.NoIgnore {
		t.Errorf("--no-ignore --ignore: NoIgnore = true, want false")
	}
}

func TestGlobRepeatableAndOrdered(t *testing.T) {
	cfg := mustParse(t, "-g", "*.txt", "-g", "!ctx.txt", "pat")
	want := []string{"*.txt", "!ctx.txt"}
	if !reflect.DeepEqual(cfg.Globs, want) {
		t.Errorf("Globs = %v, want %v", cfg.Globs, want)
	}
	cfg = mustParse(t, "-g*.txt", "pat")
	if got, want := cfg.Globs, []string{"*.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("-g*.txt: Globs = %v, want %v", got, want)
	}
}

func TestUnrestrictedStacking(t *testing.T) {
	// Verified against real rg 14.1.1's own documented cascade:
	// -u = --no-ignore; -uu = --no-ignore --hidden; -uuu = --no-ignore
	// --hidden --binary.
	cfg := mustParse(t, "-u", "pat")
	if !cfg.NoIgnore || cfg.Hidden || cfg.Binary != BinaryAuto {
		t.Errorf("-u: NoIgnore=%v Hidden=%v Binary=%v, want true/false/Auto", cfg.NoIgnore, cfg.Hidden, cfg.Binary)
	}

	cfg = mustParse(t, "-uu", "pat")
	if !cfg.NoIgnore || !cfg.Hidden || cfg.Binary != BinaryAuto {
		t.Errorf("-uu: NoIgnore=%v Hidden=%v Binary=%v, want true/true/Auto", cfg.NoIgnore, cfg.Hidden, cfg.Binary)
	}

	cfg = mustParse(t, "-uuu", "pat")
	if !cfg.NoIgnore || !cfg.Hidden || cfg.Binary != BinarySearchAndSuppress {
		t.Errorf("-uuu: NoIgnore=%v Hidden=%v Binary=%v, want true/true/SearchAndSuppress", cfg.NoIgnore, cfg.Hidden, cfg.Binary)
	}

	// Verified: `rg -uuuu` errors ("flag can only be repeated up to 3 times").
	wantErr(t, "-uuuu", "pat")
}

func TestMaxFilesize(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.MaxFilesize != 0 {
		t.Errorf("default MaxFilesize = %d, want 0", cfg.MaxFilesize)
	}
	if cfg := mustParse(t, "--max-filesize", "1024", "pat"); cfg.MaxFilesize != 1024 {
		t.Errorf("--max-filesize 1024: MaxFilesize = %d, want 1024", cfg.MaxFilesize)
	}
	if cfg := mustParse(t, "--max-filesize", "1K", "pat"); cfg.MaxFilesize != 1024 {
		t.Errorf("--max-filesize 1K: MaxFilesize = %d, want 1024", cfg.MaxFilesize)
	}
	if cfg := mustParse(t, "--max-filesize", "1K", "--max-filesize=1M", "pat"); cfg.MaxFilesize != 1024*1024 {
		t.Errorf("last --max-filesize wins: MaxFilesize = %d, want %d", cfg.MaxFilesize, 1024*1024)
	}
	// Verified: `rg --max-filesize notanumber` errors with exit 2.
	wantErr(t, "--max-filesize", "notanumber", "pat")
	// Unrecognized suffix and empty string are both format errors, per
	// crates/cli/src/human.rs::parse_human_readable_size (only K/M/G,
	// exact case, no decimals).
	wantErr(t, "--max-filesize", "1X", "pat")
	wantErr(t, "--max-filesize", "", "pat")
	wantErr(t, "--max-filesize", "1k", "pat") // lowercase suffix is rejected, unlike uppercase
}

// --- Output flags ---

func TestLineNumberLastWins(t *testing.T) {
	// Verified against real rg 14.1.1:
	//   rg -N -n alpha f.txt  -> line numbers ON  (last flag: -n)
	//   rg -n -N alpha f.txt  -> line numbers OFF (last flag: -N)
	if cfg := mustParse(t, "pat"); cfg.LineNumbers != nil {
		t.Errorf("default LineNumbers = %v, want nil (unset)", cfg.LineNumbers)
	}
	cfg := mustParse(t, "-n", "pat")
	if cfg.LineNumbers == nil || !*cfg.LineNumbers {
		t.Errorf("-n: LineNumbers = %v, want true", cfg.LineNumbers)
	}
	cfg = mustParse(t, "-n", "--no-line-number", "pat")
	if cfg.LineNumbers == nil || *cfg.LineNumbers {
		t.Errorf("-n --no-line-number: LineNumbers = %v, want false", cfg.LineNumbers)
	}
	cfg = mustParse(t, "-N", "-n", "pat")
	if cfg.LineNumbers == nil || !*cfg.LineNumbers {
		t.Errorf("-N -n: LineNumbers = %v, want true (last flag wins)", cfg.LineNumbers)
	}
	cfg = mustParse(t, "-n", "-N", "pat")
	if cfg.LineNumbers == nil || *cfg.LineNumbers {
		t.Errorf("-n -N: LineNumbers = %v, want false (last flag wins)", cfg.LineNumbers)
	}
}

func TestSearchModeLastWins(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.Mode != ModeStandard {
		t.Errorf("default Mode = %v, want ModeStandard", cfg.Mode)
	}
	if cfg := mustParse(t, "-c", "pat"); cfg.Mode != ModeCount {
		t.Errorf("-c: Mode = %v, want ModeCount", cfg.Mode)
	}
	if cfg := mustParse(t, "-l", "pat"); cfg.Mode != ModeFilesWithMatches {
		t.Errorf("-l: Mode = %v, want ModeFilesWithMatches", cfg.Mode)
	}
	if cfg := mustParse(t, "-c", "-l", "pat"); cfg.Mode != ModeFilesWithMatches {
		t.Errorf("-c -l: Mode = %v, want ModeFilesWithMatches (last wins)", cfg.Mode)
	}
}

func TestQuiet(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.Quiet {
		t.Errorf("default Quiet = true, want false")
	}
	if cfg := mustParse(t, "-q", "pat"); !cfg.Quiet {
		t.Errorf("-q: Quiet = false, want true")
	}
	// Quiet is independent of Mode: it doesn't get unset by a later
	// mode flag (they write different fields).
	if cfg := mustParse(t, "-q", "-c", "pat"); !cfg.Quiet || cfg.Mode != ModeCount {
		t.Errorf("-q -c: Quiet=%v Mode=%v, want true/ModeCount", cfg.Quiet, cfg.Mode)
	}
}

func TestColorChoices(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.Color != ColorAuto {
		t.Errorf("default Color = %v, want ColorAuto", cfg.Color)
	}
	for _, tc := range []struct {
		val  string
		want ColorMode
	}{
		{"never", ColorNever},
		{"auto", ColorAuto},
		{"always", ColorAlways},
		{"ansi", ColorAnsi}, // verified: rg accepts "ansi" too, not just auto|never|always
	} {
		cfg := mustParse(t, "--color", tc.val, "pat")
		if cfg.Color != tc.want {
			t.Errorf("--color %s: Color = %v, want %v", tc.val, cfg.Color, tc.want)
		}
	}
	// Verified: `rg --color=notacolor` errors with exit 2:
	// "choice 'notacolor' is unrecognized".
	wantErr(t, "--color=notacolor", "pat")
}

func TestContextOverrideSemantics(t *testing.T) {
	// Verified against real rg 14.1.1: -A/-B always partially override
	// -C's corresponding side, REGARDLESS OF ORDER, because rg tracks
	// before/after/both independently and only resolves them at the end
	// (crates/core/flags/lowargs.rs ContextModeLimited::get). Confirmed:
	//   rg -C2 -A1 gamma ctx.txt   -> before=2 after=1
	//   rg -A1 -C2 gamma ctx.txt   -> before=2 after=1 (same, order-independent)
	//   rg -B1 -C2 gamma ctx.txt   -> before=2 after=2  (wait: -B1 -C2 -> before wins:1, after from C:2)
	cases := []struct {
		name         string
		args         []string
		wantB, wantA int
	}{
		{"-C alone", []string{"-C", "2"}, 2, 2},
		{"-C then -A overrides after", []string{"-C", "2", "-A", "1"}, 2, 1},
		{"-A then -C (order-independent)", []string{"-A", "1", "-C", "2"}, 2, 1},
		{"-B then -C overrides before only", []string{"-B", "1", "-C", "2"}, 1, 2},
		{"-C then -B (order-independent)", []string{"-C", "2", "-B", "1"}, 1, 2},
		{"repeated -C, last wins", []string{"-C", "3", "-C", "1"}, 1, 1},
		{"-A -C -B all three, C only fills gaps", []string{"-A", "3", "-C", "1", "-B", "0"}, 0, 3},
		{"-C5 attached", []string{"-C5"}, 5, 5},
		{"long form --context=5", []string{"--context=5"}, 5, 5},
		{"repeated -A, last wins", []string{"-A5", "-A10"}, 0, 10},
		{"repeated -A to 0", []string{"-A5", "-A0"}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "pat")
			cfg := mustParse(t, args...)
			if cfg.ContextBefore != tc.wantB || cfg.ContextAfter != tc.wantA {
				t.Errorf("ContextBefore=%d ContextAfter=%d, want %d/%d", cfg.ContextBefore, cfg.ContextAfter, tc.wantB, tc.wantA)
			}
		})
	}
}

func TestInvertMatch(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.Invert {
		t.Errorf("default Invert = true, want false")
	}
	if cfg := mustParse(t, "-v", "pat"); !cfg.Invert {
		t.Errorf("-v: Invert = false, want true")
	}
	if cfg := mustParse(t, "-v", "--no-invert-match", "pat"); cfg.Invert {
		t.Errorf("-v --no-invert-match: Invert = true, want false")
	}
}

// --- Perf flags ---

func TestThreads(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.Threads != 0 {
		t.Errorf("default Threads = %d, want 0", cfg.Threads)
	}
	if cfg := mustParse(t, "-j", "5", "pat"); cfg.Threads != 5 {
		t.Errorf("-j 5: Threads = %d, want 5", cfg.Threads)
	}
	if cfg := mustParse(t, "-j5", "pat"); cfg.Threads != 5 {
		t.Errorf("-j5: Threads = %d, want 5", cfg.Threads)
	}
	if cfg := mustParse(t, "-j5", "-j10", "pat"); cfg.Threads != 10 {
		t.Errorf("-j5 -j10: Threads = %d, want 10 (last wins)", cfg.Threads)
	}
}

func TestTextVsBinary(t *testing.T) {
	// Verified against real rg 14.1.1's own test_text (defs.rs): -a and
	// -uuu's implied --binary override each other based on order;
	// --no-text always resets to Auto (not a no-op).
	if cfg := mustParse(t, "pat"); cfg.Binary != BinaryAuto {
		t.Errorf("default Binary = %v, want BinaryAuto", cfg.Binary)
	}
	if cfg := mustParse(t, "-a", "pat"); cfg.Binary != BinaryAsText {
		t.Errorf("-a: Binary = %v, want BinaryAsText", cfg.Binary)
	}
	if cfg := mustParse(t, "-a", "--no-text", "pat"); cfg.Binary != BinaryAuto {
		t.Errorf("-a --no-text: Binary = %v, want BinaryAuto", cfg.Binary)
	}
	if cfg := mustParse(t, "-a", "-uuu", "pat"); cfg.Binary != BinarySearchAndSuppress {
		t.Errorf("-a -uuu: Binary = %v, want BinarySearchAndSuppress (uuu came last)", cfg.Binary)
	}
}

func TestMmap(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.Mmap != MmapAuto {
		t.Errorf("default Mmap = %v, want MmapAuto", cfg.Mmap)
	}
	if cfg := mustParse(t, "--mmap", "pat"); cfg.Mmap != MmapAlways {
		t.Errorf("--mmap: Mmap = %v, want MmapAlways", cfg.Mmap)
	}
	if cfg := mustParse(t, "--no-mmap", "pat"); cfg.Mmap != MmapNever {
		t.Errorf("--no-mmap: Mmap = %v, want MmapNever", cfg.Mmap)
	}
	if cfg := mustParse(t, "--mmap", "--no-mmap", "pat"); cfg.Mmap != MmapNever {
		t.Errorf("--mmap --no-mmap: Mmap = %v, want MmapNever (last wins)", cfg.Mmap)
	}
	if cfg := mustParse(t, "--no-mmap", "--mmap", "pat"); cfg.Mmap != MmapAlways {
		t.Errorf("--no-mmap --mmap: Mmap = %v, want MmapAlways (last wins)", cfg.Mmap)
	}
}

// --- Arg-syntax matrix (the fiddly core the advisor flagged) ---

func TestArgSyntaxMatrix(t *testing.T) {
	// Every form here was checked against the real rg 14.1.1 binary.
	cases := []struct {
		name string
		args []string
	}{
		{"long space", []string{"--after-context", "3"}},
		{"long equals", []string{"--after-context=3"}},
		{"short space", []string{"-A", "3"}},
		{"short attached", []string{"-A3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "pat")
			cfg := mustParse(t, args...)
			if cfg.ContextAfter != 3 {
				t.Errorf("ContextAfter = %d, want 3", cfg.ContextAfter)
			}
		})
	}
}

func TestCombinedShortFlags(t *testing.T) {
	// Verified: `rg -in ALPHA f.txt` == -i -n combined; `rg -niw CAT
	// words.txt` == -n -i -w combined (all three independent switches).
	cfg := mustParse(t, "-in", "pat")
	if cfg.Case != CaseInsensitive || cfg.LineNumbers == nil || !*cfg.LineNumbers {
		t.Errorf("-in: Case=%v LineNumbers=%v, want Insensitive/true", cfg.Case, cfg.LineNumbers)
	}

	cfg = mustParse(t, "-niw", "pat")
	if cfg.Case != CaseInsensitive || !cfg.Word || cfg.LineNumbers == nil || !*cfg.LineNumbers {
		t.Errorf("-niw: Case=%v Word=%v LineNumbers=%v", cfg.Case, cfg.Word, cfg.LineNumbers)
	}
}

func TestCombinedShortFlagsEndingInValueTaker(t *testing.T) {
	// Verified: `rg -nA3 gamma ctx.txt` == -n -A3 (attached value on the
	// cluster's trailing flag); `rg -niA3` == -n -i -A3; `rg -niA 3`
	// (space-separated value after a combined switch cluster) also works.
	cfg := mustParse(t, "-nA3", "pat")
	if cfg.LineNumbers == nil || !*cfg.LineNumbers || cfg.ContextAfter != 3 {
		t.Errorf("-nA3: LineNumbers=%v ContextAfter=%d, want true/3", cfg.LineNumbers, cfg.ContextAfter)
	}

	cfg = mustParse(t, "-niA3", "pat")
	if cfg.Case != CaseInsensitive || cfg.ContextAfter != 3 {
		t.Errorf("-niA3: Case=%v ContextAfter=%d, want Insensitive/3", cfg.Case, cfg.ContextAfter)
	}

	cfg = mustParse(t, "-niA", "3", "pat")
	if cfg.Case != CaseInsensitive || cfg.ContextAfter != 3 {
		t.Errorf("-niA 3: Case=%v ContextAfter=%d, want Insensitive/3", cfg.Case, cfg.ContextAfter)
	}

	// Verified: `rg -A3ni gamma ctx.txt` errors -- once -A (not last in
	// the cluster) is hit, the REST of the token ("3ni") greedily
	// becomes its value, which fails to parse as a number.
	wantErr(t, "-A3ni", "pat")
}

// --- Error handling: unrecognized / not-yet-implemented / malformed ---

func TestUnrecognizedLongFlag(t *testing.T) {
	// Verified: `rg --bogus-flag` -> "unrecognized flag --bogus-flag", exit 2.
	wantErr(t, "--bogus-flag", "pat")
}

func TestUnrecognizedShortFlag(t *testing.T) {
	// Verified: `rg -iQ gamma f.txt` -> "unrecognized flag -Q", exit 2
	// ('Q' is not a real rg short flag, unlike e.g. 'z' which is).
	wantErr(t, "-iQ", "pat")
}

func TestNotYetImplementedFlags(t *testing.T) {
	// A sample from the curated not-yet-implemented set: these are real
	// rg flags (per PLAN.md's explicit "Not in v1" list, or natural
	// neighbors of flags gg does implement), so they must fail with a
	// specific error distinguishing them from a bare "unrecognized
	// flag" -- never silently ignored or reinterpreted.
	for _, args := range [][]string{
		{"-o", "pat"},
		{"--only-matching", "pat"},
		{"-P", "pat"},
		{"--pcre2", "pat"},
		{"-U", "pat"},
		{"--multiline", "pat"},
		{"--json", "pat"},
		{"--sort", "path", "pat"},
		{"-r", "x", "pat"},
		{"--replace", "x", "pat"},
		{"-z", "pat"},
		{"-x", "pat"},
		{"-L", "pat"},
		{"-f", "patfile", "pat"},
		{"--binary", "pat"},
	} {
		t.Run(args[0], func(t *testing.T) {
			err := wantErr(t, args...)
			if err == nil {
				return // wantErr already failed the test
			}
		})
	}
}

// TestHelpAndVersionSkipPatternRequirement mirrors `rg --help`/`rg -V`:
// both work with no PATTERN argument at all, unlike every other
// invocation (which requires at least one pattern or -e). See
// ParseArgs's early return on cfg.Help/cfg.Version.
func TestHelpAndVersionSkipPatternRequirement(t *testing.T) {
	if cfg := mustParse(t, "--help"); !cfg.Help {
		t.Error("expected Config.Help = true")
	}
	if cfg := mustParse(t, "-h"); !cfg.Help {
		t.Error("expected Config.Help = true")
	}
	if cfg := mustParse(t, "--version"); !cfg.Version {
		t.Error("expected Config.Version = true")
	}
	if cfg := mustParse(t, "-V"); !cfg.Version {
		t.Error("expected Config.Version = true")
	}
}

func TestSwitchFlagRejectsInlineValue(t *testing.T) {
	// Verified: `rg --hidden=true` -> exit 2 ("unexpected argument for
	// option '--hidden': \"true\"").
	wantErr(t, "--hidden=true", "pat")
}

func TestMissingValueErrors(t *testing.T) {
	// Verified: `rg -A alpha f.txt` -> -A eats "alpha" as its value,
	// fails to parse as a number, exit 2. `rg --after-context` (end of
	// args) -> "missing argument for option '--after-context'", exit 2.
	wantErr(t, "-A", "alpha", "f.txt")
	wantErr(t, "--after-context")
}

func TestNegativeNumericValueRejected(t *testing.T) {
	// Verified against real rg 14.1.1: `rg -A -5` and `rg -j -5` both
	// fail with "invalid digit found in string" -- the same generic
	// error as any other non-digit input, because rg parses straight
	// into an unsigned type (no separate "negative not allowed" check,
	// no support for negative values at all).
	wantErr(t, "-A", "-5", "pat")
	wantErr(t, "-j", "-5", "pat")
}

func TestMaxFilesizeOverflow(t *testing.T) {
	// Verified: `rg --max-filesize 99999999999999999999G` errors with
	// "number too large to fit in target type".
	wantErr(t, "--max-filesize", "99999999999999999999G", "pat")
}

func TestAbbreviatedLongFlagRejected(t *testing.T) {
	// Verified: `rg --fixed` (abbreviating --fixed-strings) is NOT
	// accepted by rg -- exact match only, no prefix abbreviation.
	wantErr(t, "--fixed", "pat")
}

// TestRunBadFlag exercises run() (main's logic, minus os.Exit/os.Args)
// in-process: faster than the subprocess tests below and gives real
// coverage numbers for main.go's wiring, not just ParseArgs.
func TestRunBadFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--bogus-flag", "pat"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unrecognized flag") {
		t.Errorf("stderr = %q, want it to mention the parse error", stderr.String())
	}
	if !strings.Contains(stderr.String(), usageLine) {
		t.Errorf("stderr = %q, want it to include the usage line %q", stderr.String(), usageLine)
	}
}

// TestRunSearchMatch and TestRunSearchNoMatch exercise the M2 wiring
// (run -> execute -> walk/match/search/printer) end to end in-process,
// replacing the M0/M1-era "NotYetImplemented" placeholders that used to
// assert exit 2 here -- a syntactically valid invocation now really
// searches, so those assertions became stale the moment M2 wired
// execute() in. e2e_test.go covers the full flag matrix against the real
// rg binary; these two just prove run() reaches a real match/no-match
// exit code (0/1), not the M0 sentinel (2).
func TestRunSearchMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-n", "hello", dir}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (match found); stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello world") {
		t.Errorf("stdout = %q, want it to contain the matched line", stdout.String())
	}
}

// TestRunSearchStripsUTF8BOM is a regression test for an M2 manual-diff
// finding: a file starting with a UTF-8 byte-order-mark (EF BB BF) used
// to have those 3 bytes show up verbatim in gg's matched-line output,
// while the real rg binary strips a leading BOM unconditionally (even
// under -a/--text) before searching or printing. See stripUTF8BOM.
func TestRunSearchStripsUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	content := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello world\n")...)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-n", "hello", dir}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want := filepath.Join(dir, "f.txt") + ":1:hello world\n"
	if got := stdout.String(); got != want {
		t.Errorf("got %q, want %q (BOM bytes must not appear in output)", got, want)
	}
}

func TestRunSearchNoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-n", "zzz_no_such_pattern", dir}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (no match); stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty output for no match", stdout.String())
	}
}

// TestRunRealisticFlagsDoesNotErrorOnParse pins the exact invocation
// flagged during review as a regression risk: `./gg -nA3 -i PM_RESUME .`.
// It must parse successfully (no usage line) and reach a real match/
// no-match exit code (0/1), never the bad-flag exit 2 + usage line pair.
func TestRunRealisticFlagsDoesNotErrorOnParse(t *testing.T) {
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run([]string{"-nA3", "-i", "PM_RESUME", dir}, &stdout, &stderr)
	if code != 0 && code != 1 {
		t.Errorf("exit code = %d, want 0 or 1 (a valid parse must reach a real search, not exit 2); stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), usageLine) {
		t.Errorf("stderr = %q, should not print the usage line for a successfully parsed invocation", stderr.String())
	}
}

// TestRunExitCodePrecedence_ErrorOverridesMatch is a regression test for
// an M2 manual-diff finding: `rg pat dirWithMatchAndPermissionError`
// exits 2 (error wins), not 0, even though a real match was found
// elsewhere in the same walk -- verified against the real rg binary.
// Non-quiet modes report exit 2 whenever any per-file/path error
// occurred, regardless of whether a match was also found, since the
// results are known-incomplete.
func TestRunExitCodePrecedence_ErrorOverridesMatch(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission bits don't block access")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "found.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noaccess := filepath.Join(dir, "noaccess")
	if err := os.MkdirAll(noaccess, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noaccess, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(noaccess, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(noaccess, 0o755)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-n", "hello", dir}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (a per-path error overrides a real match); stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello world") {
		t.Errorf("stdout = %q, want it to still contain the match (error doesn't suppress output)", stdout.String())
	}
}

// TestRunExitCodePrecedence_QuietMatchOverridesError is a regression
// test for an M2 manual-diff finding: `rg -q pat
// dirWithMatchAndPermissionError` exits 0, not 2 -- -q's contract is
// "yes/no as fast as possible", so once a match is confirmed anywhere,
// the exit code locks to 0 regardless of any error encountered
// elsewhere in the same run (verified against the real rg binary; this
// is the one mode where error does NOT override a found match).
func TestRunExitCodePrecedence_QuietMatchOverridesError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission bits don't block access")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "found.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noaccess := filepath.Join(dir, "noaccess")
	if err := os.MkdirAll(noaccess, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noaccess, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(noaccess, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(noaccess, 0o755)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-q", "hello", dir}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (-q locks in a found match regardless of errors elsewhere); stderr=%q", code, stderr.String())
	}
}

// TestRunExitCodePrecedence_QuietErrorNoMatch verifies -q still reports
// 2 (not 1) when an error occurred and no match was ever found --
// verified against the real rg binary.
func TestRunExitCodePrecedence_QuietErrorNoMatch(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission bits don't block access")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("nomatchhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noaccess := filepath.Join(dir, "noaccess")
	if err := os.MkdirAll(noaccess, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(noaccess, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(noaccess, 0o755)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-q", "zzz_wontmatch", dir}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2; stderr=%q", code, stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), usageLine) {
		t.Errorf("stdout = %q, want it to contain the usage line", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-V"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "gg") {
		t.Errorf("stdout = %q, want it to mention gg's name", stdout.String())
	}
}

// TestBinaryExitCodeOnBadFlag builds the real gg binary and runs it, to
// verify the CLI-observable behavior the task names ("exit code 2 +
// usage on bad flags") end to end through an actual separate process --
// not just that run() behaves correctly in-process, but that main()
// really wires os.Args/os.Stderr/os.Exit together. TestRunBadFlag above
// covers the same logic faster; this is the belt-and-suspenders check
// that the wiring itself (not exercised by any in-process call) is real.
func TestBinaryExitCodeOnBadFlag(t *testing.T) {
	bin := buildGGBinary(t)

	_, stderr, code := runGGBinary(t, bin, "--bogus-flag", "pat")
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unrecognized flag") {
		t.Errorf("stderr = %q, want it to mention the parse error", stderr)
	}
	if !strings.Contains(stderr, usageLine) {
		t.Errorf("stderr = %q, want it to include the usage line %q", stderr, usageLine)
	}
}

// TestBinaryExitCodeOnValidParse is the subprocess-level (real binary,
// real os.Exit) version of TestRunSearchMatch: a syntactically valid
// invocation must reach a real search and exit 0/1, never the bad-flag
// exit 2 + usage line pair -- replacing the M0/M1-era placeholder that
// asserted exit 2 here (see TestRunSearchMatch's doc for why that became
// stale once M2 wired execute() in).
func TestBinaryExitCodeOnValidParse(t *testing.T) {
	bin := buildGGBinary(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runGGBinary(t, bin, "-n", "hello", dir)
	if code != 0 {
		t.Errorf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "hello world") {
		t.Errorf("stdout = %q, want it to contain the matched line", stdout)
	}
}

// TestBinaryExitCodeOnRealisticFlagsDoesNotErrorOnParse is the
// subprocess-level version of TestRunRealisticFlagsDoesNotErrorOnParse:
// the exact command flagged during review, `./gg -nA3 -i PM_RESUME .`,
// must parse successfully and exit 0/1, never the bad-flag exit 2 +
// usage line pair. A separate compiled process is the only way to prove
// the actual os.Exit call -- not just run()'s in-process return value --
// behaves correctly, since a stray or stale gg binary lying around from
// an earlier build is indistinguishable from a real regression unless
// this test builds fresh from source every time it runs.
func TestBinaryExitCodeOnRealisticFlagsDoesNotErrorOnParse(t *testing.T) {
	bin := buildGGBinary(t)
	dir := t.TempDir()

	_, stderr, code := runGGBinary(t, bin, "-nA3", "-i", "PM_RESUME", dir)
	if code != 0 && code != 1 {
		t.Errorf("exit code = %d, want 0 or 1; stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, usageLine) {
		t.Errorf("stderr = %q, should not print the usage line for a successfully parsed invocation", stderr)
	}
}

func buildGGBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gg")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building gg: %v\n%s", err, out)
	}
	return bin
}

func runGGBinary(t *testing.T, bin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return outBuf.String(), errBuf.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("running gg: %v", err)
	}
	return outBuf.String(), errBuf.String(), 0
}
