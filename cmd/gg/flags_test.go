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

// TestLineRegexp covers -x/--line-regexp, including its interaction with
// -w: rg's own LineRegexp/WordRegexp both write ONE shared BoundaryMode
// field (lowargs.rs), so whichever of -x/-w is given LAST wins outright.
// Verified against the real rg 15.1.0 binary:
//
//	$ rg -x -w apple f.txt   -> behaves as plain -w (word-boundary matches,
//	                            including "apple pie")
//	$ rg -w -x apple f.txt   -> behaves as plain -x (whole-line matches only)
func TestLineRegexp(t *testing.T) {
	if cfg := mustParse(t, "pat", "path"); cfg.LineRegexp {
		t.Errorf("default LineRegexp = true, want false")
	}
	if cfg := mustParse(t, "--line-regexp", "pat", "path"); !cfg.LineRegexp {
		t.Errorf("--line-regexp: LineRegexp = false, want true")
	}
	if cfg := mustParse(t, "-x", "pat", "path"); !cfg.LineRegexp {
		t.Errorf("-x: LineRegexp = false, want true")
	}

	if cfg := mustParse(t, "-x", "-w", "pat", "path"); cfg.LineRegexp || !cfg.Word {
		t.Errorf("-x -w: LineRegexp=%v Word=%v, want LineRegexp=false Word=true (last flag wins)", cfg.LineRegexp, cfg.Word)
	}
	if cfg := mustParse(t, "-w", "-x", "pat", "path"); !cfg.LineRegexp || cfg.Word {
		t.Errorf("-w -x: LineRegexp=%v Word=%v, want LineRegexp=true Word=false (last flag wins)", cfg.LineRegexp, cfg.Word)
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

// TestFileFlag covers -f/--file at the ParseArgs level: this parser only
// records the raw PATTERNFILE argument(s) (repeatable) and, like -e,
// routes every positional to Paths -- actually reading/line-splitting
// pattern files is wire.go's resolvePatternFiles, exercised separately
// (this file does no I/O -- see its top doc comment).
//
// Verified against the real rg 15.1.0 binary:
//
//	$ rg -f pats.txt f.txt        -> f.txt is a PATH, pats.txt supplies the pattern(s)
//	$ rg -f pats.txt -e alpha f.txt -> both -f and -e patterns are searched (OR)
func TestFileFlag(t *testing.T) {
	cfg := mustParse(t, "-f", "pats.txt", "f.txt")
	if got, want := cfg.PatternFiles, []string{"pats.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PatternFiles = %v, want %v", got, want)
	}
	if got, want := cfg.Paths, []string{"f.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Paths = %v, want %v (f.txt must remain a path, not become part of the pattern)", got, want)
	}
	if len(cfg.Patterns) != 0 {
		t.Errorf("Patterns = %v, want empty (nothing read from disk by ParseArgs)", cfg.Patterns)
	}

	cfg = mustParse(t, "-f", "a.txt", "-f", "b.txt", "f.txt")
	if got, want := cfg.PatternFiles, []string{"a.txt", "b.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("repeated -f: PatternFiles = %v, want %v", got, want)
	}

	cfg = mustParse(t, "-f", "pats.txt", "-e", "alpha", "f.txt")
	if got, want := cfg.PatternFiles, []string{"pats.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("-f + -e: PatternFiles = %v, want %v", got, want)
	}
	if got, want := cfg.Patterns, []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Errorf("-f + -e: Patterns = %v, want %v", got, want)
	}

	cfg = mustParse(t, "--file=pats.txt", "f.txt")
	if got, want := cfg.PatternFiles, []string{"pats.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--file=pats.txt: PatternFiles = %v, want %v", got, want)
	}

	cfg = mustParse(t, "-fpats.txt", "f.txt")
	if got, want := cfg.PatternFiles, []string{"pats.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("-fpats.txt: PatternFiles = %v, want %v", got, want)
	}

	// -f - is just an ordinary PatternFiles entry at the parse level;
	// stdin handling lives in resolvePatternFiles.
	cfg = mustParse(t, "-f", "-", "f.txt")
	if got, want := cfg.PatternFiles, []string{"-"}; !reflect.DeepEqual(got, want) {
		t.Errorf("-f -: PatternFiles = %v, want %v", got, want)
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

// sugarAllSet reports whether all five --no-ignore sub-flags are set (the
// state --no-ignore/-u sugar produces).
func sugarAllSet(cfg *Config) bool {
	return cfg.NoIgnoreDot && cfg.NoIgnoreExclude && cfg.NoIgnoreGlobal &&
		cfg.NoIgnoreParent && cfg.NoIgnoreVcs
}

// sugarAnySet reports whether any of the five sub-flags is set.
func sugarAnySet(cfg *Config) bool {
	return cfg.NoIgnoreDot || cfg.NoIgnoreExclude || cfg.NoIgnoreGlobal ||
		cfg.NoIgnoreParent || cfg.NoIgnoreVcs
}

func TestNoIgnore(t *testing.T) {
	if cfg := mustParse(t, "pat"); sugarAnySet(cfg) {
		t.Errorf("default: a no-ignore sub-flag is set, want none")
	}
	// --no-ignore is sugar: it sets the five sub-flags dot/exclude/global/
	// parent/vcs together, but NOT no-ignore-files (probe B8).
	if cfg := mustParse(t, "--no-ignore", "pat"); !sugarAllSet(cfg) || cfg.NoIgnoreFiles {
		t.Errorf("--no-ignore: allSet=%v NoIgnoreFiles=%v, want true/false", sugarAllSet(cfg), cfg.NoIgnoreFiles)
	}
	// --ignore is --no-ignore's negation spelling (an unusual, but
	// real, rg spelling -- see the comment on the flag's registration):
	// it resets all five back on (probe A8).
	if cfg := mustParse(t, "--no-ignore", "--ignore", "pat"); sugarAnySet(cfg) {
		t.Errorf("--no-ignore --ignore: a sub-flag is still set, want none")
	}
	// Each sub-flag is independent and last-wins: --no-ignore then
	// --ignore-dot re-enables only the dot family (probe A7).
	cfg := mustParse(t, "--no-ignore", "--ignore-dot", "pat")
	if cfg.NoIgnoreDot {
		t.Errorf("--no-ignore --ignore-dot: NoIgnoreDot = true, want false")
	}
	if !cfg.NoIgnoreVcs || !cfg.NoIgnoreExclude || !cfg.NoIgnoreGlobal || !cfg.NoIgnoreParent {
		t.Errorf("--no-ignore --ignore-dot: other sub-flags should stay set")
	}
}

func TestIgnoreClusterFlags(t *testing.T) {
	// --ignore-file is a repeatable value flag with no negation.
	cfg := mustParse(t, "--ignore-file", "a", "--ignore-file", "b", "pat")
	if want := []string{"a", "b"}; !reflect.DeepEqual(cfg.IgnoreFiles, want) {
		t.Errorf("IgnoreFiles = %v, want %v", cfg.IgnoreFiles, want)
	}
	// --no-ignore-files kills --ignore-file position-independently (probe
	// B6): parsing just records both; the kill happens at wire time.
	cfg = mustParse(t, "--no-ignore-files", "--ignore-file", "a", "pat")
	if !cfg.NoIgnoreFiles || len(cfg.IgnoreFiles) != 1 {
		t.Errorf("--no-ignore-files --ignore-file a: NoIgnoreFiles=%v IgnoreFiles=%v", cfg.NoIgnoreFiles, cfg.IgnoreFiles)
	}
	// --ignore-files restores (probe B7).
	cfg = mustParse(t, "--no-ignore-files", "--ignore-files", "pat")
	if cfg.NoIgnoreFiles {
		t.Errorf("--no-ignore-files --ignore-files: NoIgnoreFiles = true, want false")
	}
	// --ignore-file-case-insensitive and its negation (probe F4).
	cfg = mustParse(t, "--ignore-file-case-insensitive", "pat")
	if !cfg.IgnoreCaseInsensitive {
		t.Errorf("--ignore-file-case-insensitive: IgnoreCaseInsensitive = false, want true")
	}
	cfg = mustParse(t, "--ignore-file-case-insensitive", "--no-ignore-file-case-insensitive", "pat")
	if cfg.IgnoreCaseInsensitive {
		t.Errorf("case-insensitive then negation: IgnoreCaseInsensitive = true, want false")
	}
	// Each individual sub-flag and its negation.
	for _, tc := range []struct {
		flag, neg string
		get       func(*Config) bool
	}{
		{"--no-ignore-dot", "--ignore-dot", func(c *Config) bool { return c.NoIgnoreDot }},
		{"--no-ignore-exclude", "--ignore-exclude", func(c *Config) bool { return c.NoIgnoreExclude }},
		{"--no-ignore-global", "--ignore-global", func(c *Config) bool { return c.NoIgnoreGlobal }},
		{"--no-ignore-parent", "--ignore-parent", func(c *Config) bool { return c.NoIgnoreParent }},
		{"--no-ignore-vcs", "--ignore-vcs", func(c *Config) bool { return c.NoIgnoreVcs }},
		{"--no-require-git", "--require-git", func(c *Config) bool { return c.NoRequireGit }},
	} {
		if cfg := mustParse(t, tc.flag, "pat"); !tc.get(cfg) {
			t.Errorf("%s: field = false, want true", tc.flag)
		}
		if cfg := mustParse(t, tc.flag, tc.neg, "pat"); tc.get(cfg) {
			t.Errorf("%s %s: field = true, want false", tc.flag, tc.neg)
		}
	}
}

// TestWiringQuartetFlags covers -L/--follow, --one-file-system,
// --no-messages, and --no-ignore-messages: each field defaults false, the
// primary spelling sets it, and the negation clears it (last-wins).
func TestWiringQuartetFlags(t *testing.T) {
	for _, tc := range []struct {
		flag, neg string
		get       func(*Config) bool
	}{
		{"--follow", "--no-follow", func(c *Config) bool { return c.FollowSymlinks }},
		{"--one-file-system", "--no-one-file-system", func(c *Config) bool { return c.OneFileSystem }},
		{"--no-messages", "--messages", func(c *Config) bool { return c.NoMessages }},
		{"--no-ignore-messages", "--ignore-messages", func(c *Config) bool { return c.NoIgnoreMessages }},
	} {
		if cfg := mustParse(t, "pat"); tc.get(cfg) {
			t.Errorf("%s: field defaults true, want false", tc.flag)
		}
		if cfg := mustParse(t, tc.flag, "pat"); !tc.get(cfg) {
			t.Errorf("%s: field = false, want true", tc.flag)
		}
		if cfg := mustParse(t, tc.flag, tc.neg, "pat"); tc.get(cfg) {
			t.Errorf("%s %s: field = true, want false", tc.flag, tc.neg)
		}
	}
	// -L is the short form of --follow.
	if cfg := mustParse(t, "-L", "pat"); !cfg.FollowSymlinks {
		t.Errorf("-L: FollowSymlinks = false, want true")
	}
	// --follow is no longer a not-implemented flag (removed from the list).
	if _, ok := notImplLongIndex["follow"]; ok {
		t.Error("follow still present in notImplementedFlags")
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
	if !sugarAllSet(cfg) || cfg.Hidden || cfg.Binary != BinaryAuto {
		t.Errorf("-u: noIgnoreAll=%v Hidden=%v Binary=%v, want true/false/Auto", sugarAllSet(cfg), cfg.Hidden, cfg.Binary)
	}

	cfg = mustParse(t, "-uu", "pat")
	if !sugarAllSet(cfg) || !cfg.Hidden || cfg.Binary != BinaryAuto {
		t.Errorf("-uu: noIgnoreAll=%v Hidden=%v Binary=%v, want true/true/Auto", sugarAllSet(cfg), cfg.Hidden, cfg.Binary)
	}

	cfg = mustParse(t, "-uuu", "pat")
	if !sugarAllSet(cfg) || !cfg.Hidden || cfg.Binary != BinarySearchAndSuppress {
		t.Errorf("-uuu: noIgnoreAll=%v Hidden=%v Binary=%v, want true/true/SearchAndSuppress", sugarAllSet(cfg), cfg.Hidden, cfg.Binary)
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

func TestMaxDepthFlag(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.MaxDepth != nil {
		t.Errorf("default MaxDepth = %v, want nil (unset/unlimited)", *cfg.MaxDepth)
	}
	if cfg := mustParse(t, "-d", "1", "pat"); cfg.MaxDepth == nil || *cfg.MaxDepth != 1 {
		t.Errorf("-d 1: MaxDepth = %v, want 1", cfg.MaxDepth)
	}
	if cfg := mustParse(t, "--max-depth", "0", "pat"); cfg.MaxDepth == nil || *cfg.MaxDepth != 0 {
		t.Errorf("--max-depth 0: MaxDepth = %v, want a non-nil 0, not unset", cfg.MaxDepth)
	}
	if cfg := mustParse(t, "--max-depth=5", "pat"); cfg.MaxDepth == nil || *cfg.MaxDepth != 5 {
		t.Errorf("--max-depth=5: MaxDepth = %v, want 5", cfg.MaxDepth)
	}
	// rg's --maxdepth alias (defs.rs's Flag::aliases()).
	if cfg := mustParse(t, "--maxdepth", "3", "pat"); cfg.MaxDepth == nil || *cfg.MaxDepth != 3 {
		t.Errorf("--maxdepth 3 (alias): MaxDepth = %v, want 3", cfg.MaxDepth)
	}
	if cfg := mustParse(t, "-d", "1", "-d", "2", "pat"); cfg.MaxDepth == nil || *cfg.MaxDepth != 2 {
		t.Errorf("-d 1 -d 2: MaxDepth = %v, want 2 (last wins)", cfg.MaxDepth)
	}
	// Verified against the real rg binary: `rg -d x` fails with
	// "value is not a valid number: invalid digit found in string",
	// same message/exit-2 plumbing as -m (parseNonNegInt).
	wantErr(t, "-d", "x", "pat")
	wantErr(t, "-d", "-1", "pat")
}

func TestIGlobRepeatableAndOrdered(t *testing.T) {
	cfg := mustParse(t, "--iglob", "*.txt", "--iglob", "!ctx.txt", "pat")
	want := []string{"*.txt", "!ctx.txt"}
	if !reflect.DeepEqual(cfg.IGlobs, want) {
		t.Errorf("IGlobs = %v, want %v", cfg.IGlobs, want)
	}
	// -g and --iglob are independent lists; giving both must not merge or
	// clobber either (buildGlobs, not the parser, is what orders them
	// together later -- see its doc).
	cfg = mustParse(t, "-g", "*.go", "--iglob", "*.txt", "pat")
	if got, want := cfg.Globs, []string{"*.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Globs = %v, want %v", got, want)
	}
	if got, want := cfg.IGlobs, []string{"*.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("IGlobs = %v, want %v", got, want)
	}
}

func TestGlobCaseInsensitiveFlag(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.GlobCaseInsensitive {
		t.Error("default GlobCaseInsensitive = true, want false")
	}
	if cfg := mustParse(t, "--glob-case-insensitive", "pat"); !cfg.GlobCaseInsensitive {
		t.Error("--glob-case-insensitive: GlobCaseInsensitive = false, want true")
	}
	if cfg := mustParse(t, "--glob-case-insensitive", "--no-glob-case-insensitive", "pat"); cfg.GlobCaseInsensitive {
		t.Error("--glob-case-insensitive --no-glob-case-insensitive: GlobCaseInsensitive = true, want false")
	}
	if cfg := mustParse(t, "--no-glob-case-insensitive", "--glob-case-insensitive", "pat"); !cfg.GlobCaseInsensitive {
		t.Error("--no-glob-case-insensitive --glob-case-insensitive: GlobCaseInsensitive = false, want true")
	}
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

func TestWithFilenameLastWins(t *testing.T) {
	// Verified against the real rg binary:
	//   rg -I -H alpha f.txt  -> path shown    (last flag: -H)
	//   rg -H -I alpha f.txt  -> path suppressed (last flag: -I)
	if cfg := mustParse(t, "pat"); cfg.WithFilename != nil {
		t.Errorf("default WithFilename = %v, want nil (unset)", cfg.WithFilename)
	}
	cfg := mustParse(t, "-H", "pat")
	if cfg.WithFilename == nil || !*cfg.WithFilename {
		t.Errorf("-H: WithFilename = %v, want true", cfg.WithFilename)
	}
	cfg = mustParse(t, "--no-filename", "pat")
	if cfg.WithFilename == nil || *cfg.WithFilename {
		t.Errorf("--no-filename: WithFilename = %v, want false", cfg.WithFilename)
	}
	cfg = mustParse(t, "-I", "-H", "pat")
	if cfg.WithFilename == nil || !*cfg.WithFilename {
		t.Errorf("-I -H: WithFilename = %v, want true (last flag wins)", cfg.WithFilename)
	}
	cfg = mustParse(t, "-H", "-I", "pat")
	if cfg.WithFilename == nil || *cfg.WithFilename {
		t.Errorf("-H -I: WithFilename = %v, want false (last flag wins)", cfg.WithFilename)
	}
}

func TestHeadingFlag(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.Heading != nil {
		t.Errorf("default Heading = %v, want nil (unset)", cfg.Heading)
	}
	cfg := mustParse(t, "--heading", "pat")
	if cfg.Heading == nil || !*cfg.Heading {
		t.Errorf("--heading: Heading = %v, want true", cfg.Heading)
	}
	cfg = mustParse(t, "--no-heading", "pat")
	if cfg.Heading == nil || *cfg.Heading {
		t.Errorf("--no-heading: Heading = %v, want false", cfg.Heading)
	}
	cfg = mustParse(t, "--heading", "--no-heading", "pat")
	if cfg.Heading == nil || *cfg.Heading {
		t.Errorf("--heading --no-heading: Heading = %v, want false (last wins)", cfg.Heading)
	}
	cfg = mustParse(t, "--no-heading", "--heading", "pat")
	if cfg.Heading == nil || !*cfg.Heading {
		t.Errorf("--no-heading --heading: Heading = %v, want true (last wins)", cfg.Heading)
	}
}

func TestColumnFlag(t *testing.T) {
	// Verified against the real rg 15.1.0 binary:
	//   rg --column cat f.txt        -> Column resolves true (implies -n)
	//   rg --no-column cat f.txt     -> Column resolves false
	//   rg --column --no-column ...  -> false (last wins)
	//   rg --no-column --column ...  -> true (last wins)
	if cfg := mustParse(t, "pat"); cfg.Column != nil {
		t.Errorf("default Column = %v, want nil (unset)", cfg.Column)
	}
	cfg := mustParse(t, "--column", "pat")
	if cfg.Column == nil || !*cfg.Column {
		t.Errorf("--column: Column = %v, want true", cfg.Column)
	}
	cfg = mustParse(t, "--no-column", "pat")
	if cfg.Column == nil || *cfg.Column {
		t.Errorf("--no-column: Column = %v, want false", cfg.Column)
	}
	cfg = mustParse(t, "--column", "--no-column", "pat")
	if cfg.Column == nil || *cfg.Column {
		t.Errorf("--column --no-column: Column = %v, want false (last wins)", cfg.Column)
	}
	cfg = mustParse(t, "--no-column", "--column", "pat")
	if cfg.Column == nil || !*cfg.Column {
		t.Errorf("--no-column --column: Column = %v, want true (last wins)", cfg.Column)
	}
}

func TestVimgrepFlag(t *testing.T) {
	// --vimgrep has no negation (defs.rs's VimGrep::update asserts this);
	// repeating it is a harmless no-op.
	if cfg := mustParse(t, "pat"); cfg.Vimgrep {
		t.Errorf("default Vimgrep = true, want false")
	}
	if cfg := mustParse(t, "--vimgrep", "pat"); !cfg.Vimgrep {
		t.Errorf("--vimgrep: Vimgrep = false, want true")
	}
	if cfg := mustParse(t, "--vimgrep", "--vimgrep", "pat"); !cfg.Vimgrep {
		t.Errorf("--vimgrep --vimgrep: Vimgrep = false, want true")
	}
	wantErr(t, "--no-vimgrep", "pat")
}

func TestByteOffsetFlag(t *testing.T) {
	// Verified against the real rg binary:
	//   rg -b cat f.txt                 -> ByteOffset on
	//   rg -b --no-byte-offset cat f.txt -> off (last wins)
	if cfg := mustParse(t, "pat"); cfg.ByteOffset {
		t.Errorf("default ByteOffset = true, want false")
	}
	if cfg := mustParse(t, "-b", "pat"); !cfg.ByteOffset {
		t.Errorf("-b: ByteOffset = false, want true")
	}
	if cfg := mustParse(t, "--byte-offset", "pat"); !cfg.ByteOffset {
		t.Errorf("--byte-offset: ByteOffset = false, want true")
	}
	cfg := mustParse(t, "-b", "--no-byte-offset", "pat")
	if cfg.ByteOffset {
		t.Errorf("-b --no-byte-offset: ByteOffset = true, want false (last wins)")
	}
	cfg = mustParse(t, "--no-byte-offset", "-b", "pat")
	if !cfg.ByteOffset {
		t.Errorf("--no-byte-offset -b: ByteOffset = false, want true (last wins)")
	}
}

func TestOnlyMatchingFlag(t *testing.T) {
	// -o/--only-matching has no negation (defs.rs's OnlyMatching::update
	// asserts this); repeating it is a harmless no-op.
	if cfg := mustParse(t, "pat"); cfg.OnlyMatching {
		t.Errorf("default OnlyMatching = true, want false")
	}
	if cfg := mustParse(t, "-o", "pat"); !cfg.OnlyMatching {
		t.Errorf("-o: OnlyMatching = false, want true")
	}
	if cfg := mustParse(t, "--only-matching", "pat"); !cfg.OnlyMatching {
		t.Errorf("--only-matching: OnlyMatching = false, want true")
	}
	if cfg := mustParse(t, "-o", "-o", "pat"); !cfg.OnlyMatching {
		t.Errorf("-o -o: OnlyMatching = false, want true")
	}
	wantErr(t, "--no-only-matching", "pat")
}

func TestMaxColumnsFlag(t *testing.T) {
	// Verified against the real rg binary:
	//   rg -M 5 pat f.txt       -> MaxColumns = 5
	//   rg -M5 pat f.txt        -> MaxColumns = 5 (attached short value)
	//   rg --max-columns 5 ...  -> MaxColumns = 5
	//   rg --max-columns 5 -M0  -> MaxColumns = 0 (0 means unlimited)
	if cfg := mustParse(t, "pat"); cfg.MaxColumns != 0 {
		t.Errorf("default MaxColumns = %d, want 0 (unlimited)", cfg.MaxColumns)
	}
	if cfg := mustParse(t, "-M", "5", "pat"); cfg.MaxColumns != 5 {
		t.Errorf("-M 5: MaxColumns = %d, want 5", cfg.MaxColumns)
	}
	if cfg := mustParse(t, "-M5", "pat"); cfg.MaxColumns != 5 {
		t.Errorf("-M5: MaxColumns = %d, want 5", cfg.MaxColumns)
	}
	if cfg := mustParse(t, "--max-columns", "5", "pat"); cfg.MaxColumns != 5 {
		t.Errorf("--max-columns 5: MaxColumns = %d, want 5", cfg.MaxColumns)
	}
	if cfg := mustParse(t, "--max-columns", "5", "-M0", "pat"); cfg.MaxColumns != 0 {
		t.Errorf("--max-columns 5 -M0: MaxColumns = %d, want 0", cfg.MaxColumns)
	}
	wantErr(t, "-M", "-5", "pat")
}

func TestMaxColumnsPreviewFlag(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.MaxColumnsPreview {
		t.Errorf("default MaxColumnsPreview = true, want false")
	}
	if cfg := mustParse(t, "--max-columns-preview", "pat"); !cfg.MaxColumnsPreview {
		t.Errorf("--max-columns-preview: MaxColumnsPreview = false, want true")
	}
	cfg := mustParse(t, "--max-columns-preview", "--no-max-columns-preview", "pat")
	if cfg.MaxColumnsPreview {
		t.Errorf("--max-columns-preview --no-max-columns-preview: MaxColumnsPreview = true, want false (last wins)")
	}
}

func TestTrimFlag(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.Trim {
		t.Errorf("default Trim = true, want false")
	}
	if cfg := mustParse(t, "--trim", "pat"); !cfg.Trim {
		t.Errorf("--trim: Trim = false, want true")
	}
	cfg := mustParse(t, "--trim", "--no-trim", "pat")
	if cfg.Trim {
		t.Errorf("--trim --no-trim: Trim = true, want false (last wins)")
	}
	cfg = mustParse(t, "--no-trim", "--trim", "pat")
	if !cfg.Trim {
		t.Errorf("--no-trim --trim: Trim = false, want true (last wins)")
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

// TestFilesMode covers --files's parsing-level contract (M3 #25): no
// PATTERN is required, every positional becomes a Path, and its
// mode-precedence interaction with -c/-l is plain last-flag-wins, same
// as -c vs -l themselves -- verified against the real rg binary directly
// (see the SearchMode doc comment): rg's own claimed "search modes can't
// override non-search modes" doesn't hold in the actual binary, so
// gg intentionally does NOT special-case this.
func TestFilesMode(t *testing.T) {
	// No PATTERN needed at all: bare --files with zero positionals must
	// not trip the "at least one pattern" requirement.
	cfg := mustParse(t, "--files")
	if cfg.Mode != ModeFiles {
		t.Errorf("--files: Mode = %v, want ModeFiles", cfg.Mode)
	}
	if len(cfg.Paths) != 0 {
		t.Errorf("--files with no positionals: Paths = %v, want empty", cfg.Paths)
	}

	// Every positional becomes a Path, never a pattern.
	cfg = mustParse(t, "--files", "path1", "path2")
	if got, want := cfg.Paths, []string{"path1", "path2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--files path1 path2: Paths = %v, want %v", got, want)
	}
	if len(cfg.Patterns) != 0 {
		t.Errorf("--files path1 path2: Patterns = %v, want empty", cfg.Patterns)
	}

	// -e still populates Patterns (harmless, just unused downstream in
	// Files mode -- verified against real rg: `rg --files -e pat` lists
	// every file, ignoring "pat" entirely), but must not consume any
	// positional as a pattern.
	cfg = mustParse(t, "--files", "-e", "pat", "path1")
	if got, want := cfg.Paths, []string{"path1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--files -e pat path1: Paths = %v, want %v", got, want)
	}

	// Last-flag-wins mode precedence, both directions -- verified against
	// real rg: `rg --files -l` (with zero positionals) errors ("requires
	// at least one pattern"), since after -l wins, we're in
	// ModeFilesWithMatches with no pattern given; `rg -l --files` lists
	// files instead, since --files wins there. gg's plain "last write
	// wins" SearchMode field assignment already produces exactly this
	// without any special-casing.
	wantErr(t, "--files", "-l")
	wantErr(t, "--files", "-c")
	if cfg := mustParse(t, "-l", "--files"); cfg.Mode != ModeFiles {
		t.Errorf("-l --files: Mode = %v, want ModeFiles (last wins)", cfg.Mode)
	}
	if cfg := mustParse(t, "-c", "--files"); cfg.Mode != ModeFiles {
		t.Errorf("-c --files: Mode = %v, want ModeFiles (last wins)", cfg.Mode)
	}

	// --files -l with an actual pattern positional: since -l is what's in
	// effect after last-wins, that positional IS the pattern (Files mode
	// is not in effect here) -- verified against real rg:
	// `rg --files -l somepattern` searches for "somepattern" in
	// FilesWithMatches mode, treating "somepattern" as the pattern, not a
	// path.
	cfg = mustParse(t, "--files", "-l", "somepattern")
	if got, want := cfg.Patterns, []string{"somepattern"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--files -l somepattern: Patterns = %v, want %v (Mode ended as ModeFilesWithMatches, not ModeFiles)", got, want)
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

// TestPassThruContextInteraction covers --passthru: it is
// mutually exclusive with -A/-B/-C in the sense that rg's own
// ContextMode is ONE mutable value, not independent fields -- whichever
// of --passthru or an -A/-B/-C flag came LAST wins outright, discarding
// whatever state came before it. Verified against the real rg binary:
// `rg -A5 --passthru` is full passthru (5 is thrown away entirely, not
// "passthru with 5 lines of after-context" -- no such mode exists);
// `rg --passthru -A5` is PLAIN -A5, passthru is entirely gone.
func TestPassThruContextInteraction(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		wantPassThru bool
		wantB, wantA int
	}{
		{"passthru alone", []string{"--passthru"}, true, 0, 0},
		{"passthrough alias", []string{"--passthrough"}, true, 0, 0},
		{"-A5 then passthru: passthru wins outright", []string{"-A5", "--passthru"}, true, 0, 0},
		{"passthru then -A5: -A5 wins, passthru gone", []string{"--passthru", "-A5"}, false, 0, 5},
		{"passthru then -B5: -B5 wins, passthru gone", []string{"--passthru", "-B5"}, false, 5, 0},
		{"passthru then -C5: -C5 wins, passthru gone", []string{"--passthru", "-C5"}, false, 5, 5},
		{"-C2 -A1 passthru: passthru wins, discards both", []string{"-C2", "-A1", "--passthru"}, true, 0, 0},
		{"passthru -A1 -B2: -A1/-B2 apply normally, no C fallback", []string{"--passthru", "-A1", "-B2"}, false, 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "pat")
			cfg := mustParse(t, args...)
			if cfg.PassThru != tc.wantPassThru {
				t.Errorf("PassThru = %v, want %v", cfg.PassThru, tc.wantPassThru)
			}
			if cfg.ContextBefore != tc.wantB || cfg.ContextAfter != tc.wantA {
				t.Errorf("ContextBefore=%d ContextAfter=%d, want %d/%d", cfg.ContextBefore, cfg.ContextAfter, tc.wantB, tc.wantA)
			}
		})
	}
}

// TestStatsFlag covers --stats/--no-stats last-flag-wins parsing.
func TestStatsFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"absent", []string{"pat"}, false},
		{"stats", []string{"--stats", "pat"}, true},
		{"no-stats", []string{"--no-stats", "pat"}, false},
		{"stats then no-stats", []string{"--stats", "--no-stats", "pat"}, false},
		{"no-stats then stats", []string{"--no-stats", "--stats", "pat"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustParse(t, tc.args...).Stats; got != tc.want {
				t.Errorf("Stats = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBufferFlags covers --line-buffered/--block-buffered (and their
// negations) resolving into the single Buffer field, last flag winning --
// mirroring rg's one BufferMode value. The negations restore auto.
func TestBufferFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want BufferMode
	}{
		{"default auto", []string{"pat"}, BufferAuto},
		{"line", []string{"--line-buffered", "pat"}, BufferLine},
		{"block", []string{"--block-buffered", "pat"}, BufferBlock},
		{"line then block: block wins", []string{"--line-buffered", "--block-buffered", "pat"}, BufferBlock},
		{"block then line: line wins", []string{"--block-buffered", "--line-buffered", "pat"}, BufferLine},
		{"line then no-line: auto", []string{"--line-buffered", "--no-line-buffered", "pat"}, BufferAuto},
		{"block then no-block: auto", []string{"--block-buffered", "--no-block-buffered", "pat"}, BufferAuto},
		{"no-line alone: auto", []string{"--no-line-buffered", "pat"}, BufferAuto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustParse(t, tc.args...).Buffer; got != tc.want {
				t.Errorf("Buffer = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSortFlags covers --sort/--sortr kind parsing, the shared last-wins
// slot (each flag overrides the other entirely -- kind AND direction), and
// the equals form. Behavior against real rg is covered end-to-end in
// e2e_sort_test.go; this pins the pure flag-resolution contract.
func TestSortFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want SortSpec
	}{
		{"default none", []string{"pat"}, SortSpec{Kind: SortNone}},
		{"sort path", []string{"--sort", "path", "pat"}, SortSpec{Kind: SortPath}},
		{"sort modified", []string{"--sort", "modified", "pat"}, SortSpec{Kind: SortModified}},
		{"sort accessed", []string{"--sort", "accessed", "pat"}, SortSpec{Kind: SortAccessed}},
		{"sort none explicit", []string{"--sort", "none", "pat"}, SortSpec{Kind: SortNone}},
		{"sort created kept for later rejection", []string{"--sort", "created", "pat"}, SortSpec{Kind: SortCreated}},
		{"sortr path is reversed", []string{"--sortr", "path", "pat"}, SortSpec{Kind: SortPath, Reverse: true}},
		{"equals form", []string{"--sort=modified", "pat"}, SortSpec{Kind: SortModified}},
		// Shared slot: the LAST of --sort/--sortr wins entirely.
		{"sort then sortr: sortr wins", []string{"--sort", "path", "--sortr", "modified", "pat"}, SortSpec{Kind: SortModified, Reverse: true}},
		{"sortr then sort: sort wins", []string{"--sortr", "modified", "--sort", "path", "pat"}, SortSpec{Kind: SortPath, Reverse: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustParse(t, tc.args...).Sort; got != tc.want {
				t.Errorf("Sort = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSortBadKind pins that an empty or unrecognized KIND is a parse error
// (exit 2), never silently treated as "none" -- matching rg's choice
// validation (`--sort '' -> choice '' is unrecognized`).
func TestSortBadKind(t *testing.T) {
	for _, args := range [][]string{
		{"--sort", "", "pat"},
		{"--sort", "bogus", "pat"},
		{"--sortr", "sideways", "pat"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			wantErr(t, args...)
		})
	}
}

// TestUnescapeSeparator ports rg's own bstr::ByteVec::unescape_bytes
// test table directly (the crate --context-separator/--field-match-
// separator/--field-context-separator all funnel through -- see
// unescapeSeparator's doc), since gg's separator flags must reproduce
// exactly the same unescaping rules, byte for byte.
func TestUnescapeSeparator(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{`\`, `\`},
		{`\\`, `\`},
		{`\0`, "\x00"},
		{`\x00`, "\x00"},
		{`\n`, "\n"},
		{`\r`, "\r"},
		{`\t`, "\t"},
		{`\a`, `\a`},   // not a recognized escape: literal
		{`\\a`, `\a`},  // \\ -> one backslash, then plain "a"
		{`\x`, `\x`},   // incomplete hex escape: literal
		{`\\x`, `\x`},  // \\ -> one backslash, then plain "x"
		{`\xz`, `\xz`}, // invalid hex digit: literal
		{`\xzz`, `\xzz`},
		{`\xFF`, "\xff"},
		{`\xf0`, "\xf0"}, // lowercase hex digits too
		{`\x61`, "a"},
		{`\\x61`, `\x61`},        // the FIRST \\ consumes to one backslash; "x61" is then plain text, NOT re-interpreted as a hex escape
		{`\xA`, `\xA`},           // only one hex digit before end of string: literal
		{`\u{2603}`, `\u{2603}`}, // rg has no \u escape at all: literal
		{"XYZ", "XYZ"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := string(unescapeSeparator(tc.in)); got != tc.want {
				t.Errorf("unescapeSeparator(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFieldSeparatorFlags(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.FieldMatchSeparator != nil || cfg.FieldContextSeparator != nil {
		t.Errorf("default FieldMatchSeparator/FieldContextSeparator = %q/%q, want nil/nil (unset)", cfg.FieldMatchSeparator, cfg.FieldContextSeparator)
	}
	cfg := mustParse(t, "--field-match-separator", "|", "--field-context-separator", `\t`, "pat")
	if string(cfg.FieldMatchSeparator) != "|" {
		t.Errorf("FieldMatchSeparator = %q, want %q", cfg.FieldMatchSeparator, "|")
	}
	if string(cfg.FieldContextSeparator) != "\t" {
		t.Errorf("FieldContextSeparator = %q, want tab", cfg.FieldContextSeparator)
	}
	// Empty is legal and distinct from unset (rg's own doc: "any number
	// of bytes, including zero").
	cfg = mustParse(t, "--field-match-separator=", "pat")
	if cfg.FieldMatchSeparator == nil || len(cfg.FieldMatchSeparator) != 0 {
		t.Errorf("--field-match-separator=: FieldMatchSeparator = %#v, want non-nil empty slice", cfg.FieldMatchSeparator)
	}
}

func TestContextSeparatorFlag(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.ContextSeparator != nil {
		t.Errorf("default ContextSeparator = %+v, want nil (unset)", cfg.ContextSeparator)
	}
	cfg := mustParse(t, "--context-separator", "SEP", "pat")
	if cfg.ContextSeparator == nil || cfg.ContextSeparator.Disabled || string(cfg.ContextSeparator.Value) != "SEP" {
		t.Errorf("--context-separator SEP: ContextSeparator = %+v, want {Disabled:false Value:SEP}", cfg.ContextSeparator)
	}
	cfg = mustParse(t, "--no-context-separator", "pat")
	if cfg.ContextSeparator == nil || !cfg.ContextSeparator.Disabled {
		t.Errorf("--no-context-separator: ContextSeparator = %+v, want {Disabled:true}", cfg.ContextSeparator)
	}
	// Last-wins, either order (verified against the real rg binary).
	cfg = mustParse(t, "--context-separator", "SEP", "--no-context-separator", "pat")
	if cfg.ContextSeparator == nil || !cfg.ContextSeparator.Disabled {
		t.Errorf("--context-separator SEP --no-context-separator: ContextSeparator = %+v, want {Disabled:true}", cfg.ContextSeparator)
	}
	cfg = mustParse(t, "--no-context-separator", "--context-separator", "SEP", "pat")
	if cfg.ContextSeparator == nil || cfg.ContextSeparator.Disabled || string(cfg.ContextSeparator.Value) != "SEP" {
		t.Errorf("--no-context-separator --context-separator SEP: ContextSeparator = %+v, want {Disabled:false Value:SEP}", cfg.ContextSeparator)
	}
	// --no-context-separator takes no value (a switch, unlike its
	// primary spelling) -- see applyNegatedSwitch's doc.
	wantErr(t, "--no-context-separator=x", "pat")
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

// TestMaxCountFlag covers -m/--max-count's parsing: repeatable
// (last-wins, a plain overwrite of the same field), and 0 is a legal,
// distinct-from-unset value (verified against the real rg 15.1.0
// binary: `rg -m 0 pat file` searches nothing and exits 1) -- hence
// Config.MaxCount is *int, not plain int.
func TestMaxCountFlag(t *testing.T) {
	if cfg := mustParse(t, "pat"); cfg.MaxCount != nil {
		t.Errorf("default MaxCount = %v, want nil (unset/unlimited)", *cfg.MaxCount)
	}
	if cfg := mustParse(t, "-m", "5", "pat"); cfg.MaxCount == nil || *cfg.MaxCount != 5 {
		t.Errorf("-m 5: MaxCount = %v, want 5", cfg.MaxCount)
	}
	if cfg := mustParse(t, "--max-count", "5", "pat"); cfg.MaxCount == nil || *cfg.MaxCount != 5 {
		t.Errorf("--max-count 5: MaxCount = %v, want 5", cfg.MaxCount)
	}
	if cfg := mustParse(t, "--max-count=5", "pat"); cfg.MaxCount == nil || *cfg.MaxCount != 5 {
		t.Errorf("--max-count=5: MaxCount = %v, want 5", cfg.MaxCount)
	}
	if cfg := mustParse(t, "-m5", "pat"); cfg.MaxCount == nil || *cfg.MaxCount != 5 {
		t.Errorf("-m5: MaxCount = %v, want 5", cfg.MaxCount)
	}
	if cfg := mustParse(t, "-m", "0", "pat"); cfg.MaxCount == nil || *cfg.MaxCount != 0 {
		t.Errorf("-m 0: MaxCount = %v, want a non-nil 0, not unset", cfg.MaxCount)
	}
	if cfg := mustParse(t, "-m", "1", "-m", "2", "pat"); cfg.MaxCount == nil || *cfg.MaxCount != 2 {
		t.Errorf("-m 1 -m 2: MaxCount = %v, want 2 (last wins)", cfg.MaxCount)
	}
	// Verified against the real rg binary: a negative NUM fails with
	// "value is not a valid number: invalid digit found in string"
	// (parses directly into an unsigned type, same as -A/-B/-C).
	wantErr(t, "-m", "-1", "pat")
	wantErr(t, "-m", "notanumber", "pat")
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
		{"-P", "pat"},
		{"--pcre2", "pat"},
		{"-U", "pat"},
		{"--multiline", "pat"},
		{"-r", "x", "pat"},
		{"--replace", "x", "pat"},
		{"-z", "pat"},
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
	code := run([]string{"--bogus-flag", "pat"}, nil, &stdout, &stderr)
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
	code := run([]string{"-n", "hello", dir}, nil, &stdout, &stderr)
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
	code := run([]string{"-n", "hello", dir}, nil, &stdout, &stderr)
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
	code := run([]string{"-n", "zzz_no_such_pattern", dir}, nil, &stdout, &stderr)
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
	code := run([]string{"-nA3", "-i", "PM_RESUME", dir}, nil, &stdout, &stderr)
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
	code := run([]string{"-n", "hello", dir}, nil, &stdout, &stderr)
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
	code := run([]string{"-q", "hello", dir}, nil, &stdout, &stderr)
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
	code := run([]string{"-q", "zzz_wontmatch", dir}, nil, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2; stderr=%q", code, stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--help"}, nil, &stdout, &stderr)
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
	code := run([]string{"-V"}, nil, &stdout, &stderr)
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
