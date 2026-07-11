//go:build e2e

// Round #35's file-type system (-t/-T/--type-add/--type-clear/--type-list)
// lives apart from e2e_test.go's golden matrix because its own oracle is
// unusual: --type-list's output must be BYTE-IDENTICAL to rg's (not just
// sort-normalized -- it's deterministic, sorted, single-shot output, so a
// byte diff is the stronger and simpler check), and it doubles as the
// drift test for rg's default type table (filetype/default_types.go) --
// if a future rg pin bump adds/removes/changes a default type, this test
// fails until the table is regenerated (see internal/typetable/extract).
package gripgrep_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGoldenVsRipgrep_TypeList is the file-type system's ultimate oracle
// (the brief): every type name and every glob, byte-for-byte
// against the real rg binary, with no corpus or PATTERN needed at all.
func TestGoldenVsRipgrep_TypeList(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	rgOut, rgErr, rgCode := run(t, "rg", []string{"--type-list"})
	ggOut, ggErr, ggCode := run(t, ggBin, []string{"--type-list"})

	if rgCode != ggCode {
		t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
	}
	if string(rgOut) != string(ggOut) {
		t.Errorf("--type-list output not byte-identical to rg\n--- rg ---\n%s\n--- gg ---\n%s", rgOut, ggOut)
	}
}

// TestGoldenVsRipgrep_TypeListIgnoresExtraArgs verifies --type-list's
// documented (and probed against the real binary) behavior: it ignores
// any PATTERN/PATH positionals given alongside it and is unaffected by
// -q, still printing the full table.
func TestGoldenVsRipgrep_TypeListIgnoresExtraArgs(t *testing.T) {
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
		{"extra_positionals", []string{"--type-list", "foo", "bar"}},
		{"quiet_does_not_suppress", []string{"-q", "--type-list"}},
		{"mode_precedence_files_then_type_list_wins", []string{"--files", "--type-list"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)
			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if string(rgOut) != string(ggOut) {
				t.Errorf("stdout not byte-identical to rg\n--- rg ---\n%s\n--- gg ---\n%s", rgOut, ggOut)
			}
		})
	}
}

// TestGoldenVsRipgrep_TypeListMutated covers --type-add/--type-clear's
// effect on --type-list: extending an existing type, composing via
// include, and clearing+rebuilding, each byte-diffed against rg.
func TestGoldenVsRipgrep_TypeListMutated(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	cases := []struct {
		name string
		args []string
	}{
		{"add_extends_existing", []string{"--type-add", "rust:*.rs2", "--type-list"}},
		{"add_new_type", []string{"--type-add", "foo:*.foo", "--type-list"}},
		{"add_include_directive", []string{"--type-add", "src:include:rust,c", "--type-list"}},
		{"clear_then_add_rebuild", []string{"--type-clear", "rust", "--type-add", "rust:*.rs2", "--type-list"}},
		{"clear_removes_type", []string{"--type-clear", "rust", "--type-list"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)
			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if string(rgOut) != string(ggOut) {
				t.Errorf("stdout not byte-identical to rg\n--- rg ---\n%s\n--- gg ---\n%s", rgOut, ggOut)
			}
		})
	}
}

// TestGoldenVsRipgrep_TypeErrors covers the error-shape probes
// against the real rg binary: exit code AND the exact stderr message
// (gg's convention allows extra detail, but here it deliberately matches
// rg's wording exactly -- see filetype.ErrInvalidDefinition's doc).
func TestGoldenVsRipgrep_TypeErrors(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	ggBin := buildGG(t, filepath.Dir(thisFile))

	cases := []struct {
		name string
		args []string
	}{
		{"unknown_type_select", []string{"-t", "bogus", "x", "."}},
		{"unknown_type_negate", []string{"-T", "bogus", "x", "."}},
		{"unknown_type_select_type_list", []string{"-t", "bogus", "--type-list"}},
		{"malformed_type_add_no_colon", []string{"--type-add", "foo", "x", "."}},
		{"malformed_type_add_bad_three_part", []string{"--type-add", "foo:bar:baz", "x", "."}},
		{"malformed_type_add_reserved_all", []string{"--type-add", "all:*.foo", "x", "."}},
		{"malformed_type_add_include_unknown", []string{"--type-add", "combo:include:rust,bogus", "x", "."}},
		{"clear_then_select_errors", []string{"--type-clear", "rust", "-t", "rust", "x", "."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgOut, rgErr, rgCode := run(t, "rg", tc.args)
			ggOut, ggErr, ggCode := run(t, ggBin, tc.args)
			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s", rgCode, ggCode, rgErr, ggErr)
			}
			if len(rgOut) != 0 || len(ggOut) != 0 {
				t.Errorf("expected no stdout on error: rg=%q gg=%q", rgOut, ggOut)
			}
			// rg's stderr is "rg: <message>\n"; gg's is "gg: <message>\n" --
			// only the binary-name prefix differs, so compare the message
			// itself rather than the raw bytes.
			rgMsg := stripBinaryPrefix(string(rgErr), "rg: ")
			ggMsg := stripBinaryPrefix(string(ggErr), "gg: ")
			if rgMsg != ggMsg {
				t.Errorf("stderr message mismatch:\nrg: %q\ngg: %q", rgMsg, ggMsg)
			}
		})
	}
}

func stripBinaryPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

// TestGoldenVsRipgrep_FileTypesFiltering exercises -t/-T selection,
// precedence against -g/--iglob, and the hidden-file whitelist override,
// on a small mixed-type fixture tree -- a differential sweep beyond the
// string-only --type-list oracle above.
func TestGoldenVsRipgrep_FileTypesFiltering(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"main.rs":     "PM_RESUME token\n",
		"main.c":      "PM_RESUME token\n",
		"note.txt":    "PM_RESUME token\n",
		"note.xyz":    "PM_RESUME token\n", // no default type
		".bashrc":     "PM_RESUME token\n", // sh type includes dotfiles
		"sub/deep.rs": "PM_RESUME token\n",
		"sub/deep.c":  "PM_RESUME token\n",
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
		{"select_rust", []string{"-l", "-t", "rust", "PM_RESUME", dir}},
		{"negate_rust", []string{"-l", "-T", "rust", "PM_RESUME", dir}},
		{"select_all", []string{"-l", "-t", "all", "PM_RESUME", dir}},
		{"negate_all", []string{"-l", "-T", "all", "PM_RESUME", dir}},
		{"select_then_negate_same_type_last_wins", []string{"-l", "-t", "rust", "-T", "rust", "PM_RESUME", dir}},
		{"negate_then_select_same_type_last_wins", []string{"-l", "-T", "rust", "-t", "rust", "PM_RESUME", dir}},
		{"select_rust_negate_c", []string{"-l", "-t", "rust", "-T", "c", "PM_RESUME", dir}},
		{"select_sh_shows_hidden", []string{"-l", "-t", "sh", "PM_RESUME", dir}},
		{"glob_excludes_outright", []string{"-l", "-t", "rust", "-g", "!*.rs", "PM_RESUME", dir}},
		{"positive_glob_overrides_type", []string{"-l", "-g", "*.c", "-t", "rust", "PM_RESUME", dir}},
		{"iglob_overrides_type", []string{"-l", "-t", "rust", "--iglob", "*.C", "PM_RESUME", dir}},
		{"type_add_custom_then_select", []string{"-l", "--type-add", "foo:*.xyz", "-t", "foo", "PM_RESUME", dir}},
		{"type_add_include_then_select", []string{"-l", "--type-add", "src:include:rust,c", "-t", "src", "PM_RESUME", dir}},
		{"files_mode_type_filter", []string{"--files", "-t", "rust", dir}},
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

// TestGoldenVsRipgrep_FileTypesOnLinuxTree runs -t/-T against
// benchmark-data/linux (skipped, not failed, when that gitignored corpus
// isn't checked out): -t c and -T c on real C/header-heavy source,
// -t make on the kernel's many differently-named Makefiles, and -t all
// --files as the full "does every default glob actually compile and
// match correctly against real filenames" oracle the type-system design
// review called for (--type-list's own oracle only proves the glob
// STRINGS match rg's, not that gg's glob engine compiles/matches every
// one of them the same way rg's does).
func TestGoldenVsRipgrep_FileTypesOnLinuxTree(t *testing.T) {
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
		{"select_c_files", []string{"--files", "-t", "c", linuxTree}},
		{"negate_c_files", []string{"--files", "-T", "c", linuxTree}},
		{"select_make_files", []string{"--files", "-t", "make", linuxTree}},
		{"select_all_files", []string{"--files", "-t", "all", linuxTree}},
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
