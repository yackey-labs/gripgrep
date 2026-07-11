//go:build e2e

// Golden e2e coverage for round #41's ignore-control cluster (--ignore-file,
// --ignore-file-case-insensitive, and the --no-ignore-dot/exclude/files/
// global/parent/vcs + --no-require-git sub-flags). Every case runs BOTH the
// pinned rg 15.1.0 binary and gg with -j1, an isolated HOME/XDG_CONFIG_HOME
// (t.TempDir), and a specific cwd, then compares stdout byte-for-byte and
// exit codes exactly. stderr text may be gg-flavored, so only its
// presence/absence is asserted (the contractual part). Ported from the
// lead's answer-key probes (probe41.sh, answer-key-41.txt) -- see round #41's
// brief. exec.Command with a nil Stdin reads /dev/null on Unix, so the
// search-mode G-block never blocks reading stdin.
package gripgrep_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// ignoreEnv is one isolated environment: a fake HOME and XDG_CONFIG_HOME,
// the two knobs global-gitignore resolution reads. The dev box has a real
// ~/.gitconfig with a global excludesFile that would otherwise contaminate
// every case, so isolation here is mandatory, not hygiene.
type ignoreEnv struct {
	home string
	xdg  string
}

func newIgnoreEnv(t *testing.T) ignoreEnv {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	xdg := filepath.Join(base, "xdg")
	mkdirAll(t, home)
	mkdirAll(t, filepath.Join(xdg, "git"))
	return ignoreEnv{home: home, xdg: xdg}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		writeFile(t, p, "")
	}
}

// runIgnore executes bin in cwd with only PATH/HOME/XDG_CONFIG_HOME set
// (nothing inherited), stdin from /dev/null. It returns stdout, stderr, and
// the exit code separately.
func runIgnore(t *testing.T, bin, cwd string, env ignoreEnv, args []string) (stdout, stderr []byte, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = []string{
		"PATH=/usr/bin:/bin:" + filepath.Dir(bin),
		"HOME=" + env.home,
		"XDG_CONFIG_HOME=" + env.xdg,
	}
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

// cmpIgnore runs one case against both tools and asserts stdout+exit-code
// equality and stderr presence parity.
func cmpIgnore(t *testing.T, ggBin, cwd string, env ignoreEnv, args []string) {
	t.Helper()
	rgOut, rgErr, rgCode := runIgnore(t, "rg", cwd, env, args)
	ggOut, ggErr, ggCode := runIgnore(t, ggBin, cwd, env, args)
	if rgCode != ggCode {
		t.Errorf("exit code mismatch (args %v): rg=%d gg=%d\nrg stderr: %s\ngg stderr: %s",
			args, rgCode, ggCode, rgErr, ggErr)
	}
	if !bytes.Equal(rgOut, ggOut) {
		t.Errorf("stdout mismatch (args %v):\n--- rg ---\n%s\n--- gg ---\n%s", args, rgOut, ggOut)
	}
	if (len(rgErr) > 0) != (len(ggErr) > 0) {
		t.Errorf("stderr presence mismatch (args %v): rg=%q gg=%q", args, rgErr, ggErr)
	}
}

func e2eRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	return filepath.Dir(thisFile)
}

// mkStdRepo builds the standard fixture used by the A/B blocks: a repo where
// each ignore source excludes one distinct file.
func mkStdRepo(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, ".gitignore"), "git.txt\n")
	writeFile(t, filepath.Join(dir, ".ignore"), "dot.txt\n")
	writeFile(t, filepath.Join(dir, ".rgignore"), "rgi.txt\n")
	writeFile(t, filepath.Join(dir, ".git", "info", "exclude"), "excl.txt\n")
	touch(t,
		filepath.Join(dir, "keep.txt"), filepath.Join(dir, "git.txt"),
		filepath.Join(dir, "dot.txt"), filepath.Join(dir, "rgi.txt"),
		filepath.Join(dir, "excl.txt"), filepath.Join(dir, "glob.txt"),
		filepath.Join(dir, "sub", "keep2.txt"),
	)
}

func TestGoldenIgnoreClusterA(t *testing.T) {
	ggBin := buildGG(t, e2eRoot(t))
	env := newIgnoreEnv(t)
	writeFile(t, filepath.Join(env.xdg, "git", "ignore"), "glob.txt\n")
	repo := t.TempDir()
	mkStdRepo(t, repo)

	files := func(extra ...string) []string { return append([]string{"--files", "-j1"}, extra...) }
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"A1_baseline", files()},
		{"A2_no_ignore", files("--no-ignore")},
		{"A3_no_ignore_dot", files("--no-ignore-dot")},
		{"A4_no_ignore_vcs", files("--no-ignore-vcs")},
		{"A5_no_ignore_exclude", files("--no-ignore-exclude")},
		{"A6_no_ignore_global", files("--no-ignore-global")},
		{"A7_no_ignore_then_ignore_dot", files("--no-ignore", "--ignore-dot")},
		{"A8_no_ignore_dot_then_ignore", files("--no-ignore-dot", "--ignore")},
		{"A9_u", files("-u")},
		{"A10_ignore_vcs", files("--ignore-vcs")},
	} {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, repo, env, tc.args) })
	}
}

func TestGoldenIgnoreClusterB(t *testing.T) {
	ggBin := buildGG(t, e2eRoot(t))
	env := newIgnoreEnv(t)
	writeFile(t, filepath.Join(env.xdg, "git", "ignore"), "glob.txt\n")
	repo := t.TempDir()
	mkStdRepo(t, repo)
	touch(t, filepath.Join(repo, "extra.txt"), filepath.Join(repo, "x1.txt"), filepath.Join(repo, "x2.txt"))
	touch(t, filepath.Join(repo, "anch", "az.txt"))

	ig := t.TempDir()
	writeFile(t, filepath.Join(ig, "ig1"), "extra.txt\n")
	writeFile(t, filepath.Join(ig, "ig2"), "x1.txt\nx2.txt\n")
	writeFile(t, filepath.Join(ig, "ig3"), "!x2.txt\n")
	writeFile(t, filepath.Join(ig, "ig-wl"), "!git.txt\n")
	writeFile(t, filepath.Join(ig, "ig-anch"), "/anch/az.txt\n")
	// Absolute-path pattern for the cwd="/" case (B15): from root, rg
	// strips only the leading "/", so the candidate becomes the full
	// path minus its leading slash -- which this absolute pattern (also
	// minus its leading slash once compiled) matches.
	writeFile(t, filepath.Join(ig, "ig-abs"), filepath.Join(repo, "anch", "az.txt")+"\n")
	missing := filepath.Join(ig, "nonexistent")

	files := func(extra ...string) []string { return append([]string{"--files", "-j1"}, extra...) }
	cases := []struct {
		name string
		cwd  string
		args []string
	}{
		{"B1_single", repo, files("--ignore-file", filepath.Join(ig, "ig1"))},
		{"B2_later_wins", repo, files("--ignore-file", filepath.Join(ig, "ig2"), "--ignore-file", filepath.Join(ig, "ig3"))},
		{"B3_reversed", repo, files("--ignore-file", filepath.Join(ig, "ig3"), "--ignore-file", filepath.Join(ig, "ig2"))},
		{"B4_explicit_wl_cannot_resurrect", repo, files("--ignore-file", filepath.Join(ig, "ig-wl"))},
		{"B5_missing", repo, files("--ignore-file", missing)},
		{"B6_no_ignore_files_kills", repo, files("--no-ignore-files", "--ignore-file", filepath.Join(ig, "ig1"))},
		{"B7_no_ignore_files_then_ignore_files", repo, files("--no-ignore-files", "--ignore-files", "--ignore-file", filepath.Join(ig, "ig1"))},
		{"B8_no_ignore_keeps_ignore_file", repo, files("--no-ignore", "--ignore-file", filepath.Join(ig, "ig1"))},
		{"B9_anchored_whole_tree", repo, files("--ignore-file", filepath.Join(ig, "ig-anch"))},
		{"B10_anchored_subdir_arg", repo, files("--ignore-file", filepath.Join(ig, "ig-anch"), "anch")},
		{"B11_cwd_inside_anch", filepath.Join(repo, "anch"), files("--ignore-file", filepath.Join(ig, "ig-anch"))},
		// B12: an ABSOLUTE walk-root arg under CWD -- rg strips the CWD
		// prefix off the absolute display path before matching the
		// CWD-anchored explicit pattern, so /anch/az.txt still lines up and
		// az.txt is ignored (exit 1, no output).
		{"B12_absolute_arg_under_cwd", repo, files("--ignore-file", filepath.Join(ig, "ig-anch"), filepath.Join(repo, "anch"))},
		// B13: absolute walk-root arg from the PARENT cwd -- the display
		// path strips to `<repo-base>/anch/az.txt`, which the anchored
		// pattern does NOT match, so az.txt survives.
		{"B13_absolute_arg_from_parent", filepath.Dir(repo), files("--ignore-file", filepath.Join(ig, "ig-anch"), filepath.Join(repo, "anch"))},
		// B14: absolute walk-root arg NOT under CWD -- rg leaves the full
		// absolute path in place (strip fails), so the anchored pattern
		// misses and az.txt survives.
		{"B14_absolute_arg_not_under_cwd", ig, files("--ignore-file", filepath.Join(ig, "ig-anch"), filepath.Join(repo, "anch"))},
		// B15: CWD is "/" -- rg strips only the leading separator, so an
		// absolute pattern still lines up and az.txt is ignored. Guards the
		// anchorToCwd cwd=="/" boundary case.
		{"B15_root_cwd", "/", files("--ignore-file", filepath.Join(ig, "ig-abs"), filepath.Join(repo, "anch"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, tc.cwd, env, tc.args) })
	}
}

func TestGoldenIgnoreClusterC(t *testing.T) {
	ggBin := buildGG(t, e2eRoot(t))
	env := newIgnoreEnv(t)
	files := func(extra ...string) []string { return append([]string{"--files", "-j1"}, extra...) }

	norepo := t.TempDir()
	writeFile(t, filepath.Join(norepo, ".gitignore"), "git.txt\n")
	touch(t, filepath.Join(norepo, "git.txt"), filepath.Join(norepo, "keep.txt"))

	jj := t.TempDir()
	mkdirAll(t, filepath.Join(jj, ".jj"))
	writeFile(t, filepath.Join(jj, ".gitignore"), "git.txt\n")
	touch(t, filepath.Join(jj, "git.txt"), filepath.Join(jj, "keep.txt"))

	outer := t.TempDir()
	mkdirAll(t, filepath.Join(outer, "repo", ".git"))
	writeFile(t, filepath.Join(outer, ".gitignore"), "foo.txt\n")
	touch(t, filepath.Join(outer, "repo", "foo.txt"), filepath.Join(outer, "repo", "keep.txt"))
	outerRepo := filepath.Join(outer, "repo")

	for _, tc := range []struct {
		name string
		cwd  string
		args []string
	}{
		{"C1_no_git_default", norepo, files()},
		{"C2_no_require_git", norepo, files("--no-require-git")},
		{"C3_no_require_git_no_vcs", norepo, files("--no-require-git", "--no-ignore-vcs")},
		{"C4_jj_marker", jj, files()},
		{"C5_outer_gitignore_ignored", outerRepo, files()},
		{"C6_outer_gitignore_no_require_git", outerRepo, files("--no-require-git")},
	} {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, tc.cwd, env, tc.args) })
	}
}

func TestGoldenIgnoreClusterD(t *testing.T) {
	ggBin := buildGG(t, e2eRoot(t))
	env := newIgnoreEnv(t)
	files := func(extra ...string) []string { return append([]string{"--files", "-j1"}, extra...) }

	prec := t.TempDir()
	mkdirAll(t, filepath.Join(prec, ".git"))
	writeFile(t, filepath.Join(prec, ".ignore"), "!a.txt\n")
	writeFile(t, filepath.Join(prec, ".rgignore"), "a.txt\n")
	touch(t, filepath.Join(prec, "a.txt"), filepath.Join(prec, "keep.txt"))

	// Parent-chain fixture: ignore sources ABOVE the repo root.
	pchain := t.TempDir()
	repo := filepath.Join(pchain, "repo")
	deep := filepath.Join(repo, "deep")
	mkdirAll(t, filepath.Join(repo, ".git", "info"))
	mkdirAll(t, deep)
	writeFile(t, filepath.Join(pchain, ".ignore"), "pi.txt\n")
	writeFile(t, filepath.Join(pchain, ".rgignore"), "pr.txt\n")
	writeFile(t, filepath.Join(repo, ".gitignore"), "pg.txt\n")
	touch(t,
		filepath.Join(deep, "pi.txt"), filepath.Join(deep, "pr.txt"),
		filepath.Join(deep, "pg.txt"), filepath.Join(deep, "keep.txt"),
	)

	for _, tc := range []struct {
		name string
		cwd  string
		args []string
	}{
		{"D1_custom_decisive_first", prec, files()},
		{"D2_parent_chain_applies", deep, files()},
		{"D3_no_ignore_parent", deep, files("--no-ignore-parent")},
		{"D4_no_ignore_dot_keeps_parent_gitignore", deep, files("--no-ignore-dot")},
		// D5r: walk root is the subdir arg `deep` given from the repo cwd.
		// Every parent source (the .ignore/.rgignore above the repo root and
		// the repo root's own .gitignore) still applies to that walk -> only
		// deep/keep.txt survives.
		{"D5_subdir_arg_from_repo", repo, files("deep")},
		// "Parent" is above the WALK ROOT, not cwd: from repo, walking
		// deep, --no-ignore-parent kills repo's own .gitignore (probe D6).
		{"D6_no_ignore_parent_from_repo", repo, files("--no-ignore-parent", "deep")},
	} {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, tc.cwd, env, tc.args) })
	}
}

func TestGoldenIgnoreClusterE(t *testing.T) {
	ggBin := buildGG(t, e2eRoot(t))
	files := func(extra ...string) []string { return append([]string{"--files", "-j1"}, extra...) }

	// E1-E3: global (XDG default path) inside a repo.
	env := newIgnoreEnv(t)
	writeFile(t, filepath.Join(env.xdg, "git", "ignore"), "glob.txt\n")
	globRepo := t.TempDir()
	mkdirAll(t, filepath.Join(globRepo, ".git"))
	touch(t, filepath.Join(globRepo, "glob.txt"), filepath.Join(globRepo, "keep.txt"))

	// E4-E5: same global, but outside any repo.
	globNoRepo := t.TempDir()
	touch(t, filepath.Join(globNoRepo, "glob.txt"), filepath.Join(globNoRepo, "keep.txt"))

	for _, tc := range []struct {
		name string
		cwd  string
		args []string
	}{
		{"E1_global_inside_repo", globRepo, files()},
		{"E2_no_ignore_global", globRepo, files("--no-ignore-global")},
		{"E3_no_ignore_vcs_kills_global", globRepo, files("--no-ignore-vcs")},
		{"E4_global_not_outside_repo", globNoRepo, files()},
		{"E5_global_no_require_git_outside", globNoRepo, files("--no-require-git")},
	} {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, tc.cwd, env, tc.args) })
	}

	// E6: excludesFile in HOME/.gitconfig replaces the default XDG path.
	t.Run("E6_excludesfile_home_gitconfig", func(t *testing.T) {
		env := newIgnoreEnv(t)
		writeFile(t, filepath.Join(env.xdg, "git", "ignore"), "glob.txt\n")
		custom := filepath.Join(t.TempDir(), "customglobal")
		writeFile(t, custom, "gcfg.txt\n")
		writeFile(t, filepath.Join(env.home, ".gitconfig"), "[core]\n\texcludesfile = "+custom+"\n")
		repo := t.TempDir()
		mkdirAll(t, filepath.Join(repo, ".git"))
		touch(t, filepath.Join(repo, "gcfg.txt"), filepath.Join(repo, "glob.txt"), filepath.Join(repo, "keep.txt"))
		cmpIgnore(t, ggBin, repo, env, files())
	})

	// E7: excludesFile in XDG git/config (no HOME/.gitconfig).
	t.Run("E7_excludesfile_xdg_config", func(t *testing.T) {
		env := newIgnoreEnv(t)
		writeFile(t, filepath.Join(env.xdg, "git", "ignore"), "glob.txt\n")
		custom := filepath.Join(t.TempDir(), "customglobal")
		writeFile(t, custom, "gcfg.txt\n")
		writeFile(t, filepath.Join(env.xdg, "git", "config"), "[core]\n\texcludesfile = "+custom+"\n")
		repo := t.TempDir()
		mkdirAll(t, filepath.Join(repo, ".git"))
		touch(t, filepath.Join(repo, "gcfg.txt"), filepath.Join(repo, "glob.txt"), filepath.Join(repo, "keep.txt"))
		cmpIgnore(t, ggBin, repo, env, files())
	})

	// E-anchored: a rooted (/foo.txt) pattern in the global ignore file
	// anchors at the walk position -- only the top-level foo.txt is
	// ignored, sub/foo.txt survives (the brief's "verify /anchored global"
	// check).
	t.Run("E_anchored_global_pattern", func(t *testing.T) {
		env := newIgnoreEnv(t)
		writeFile(t, filepath.Join(env.xdg, "git", "ignore"), "/foo.txt\n")
		repo := t.TempDir()
		mkdirAll(t, filepath.Join(repo, ".git"))
		touch(t, filepath.Join(repo, "foo.txt"), filepath.Join(repo, "sub", "foo.txt"), filepath.Join(repo, "keep.txt"))
		cmpIgnore(t, ggBin, repo, env, files())
	})

	// E8: an exclude whitelist beats the global ignore (exclude > global).
	t.Run("E8_exclude_whitelist_beats_global", func(t *testing.T) {
		env := newIgnoreEnv(t)
		writeFile(t, filepath.Join(env.xdg, "git", "ignore"), "glob.txt\n")
		repo := t.TempDir()
		mkdirAll(t, filepath.Join(repo, ".git", "info"))
		writeFile(t, filepath.Join(repo, ".git", "info", "exclude"), "!glob.txt\n")
		touch(t, filepath.Join(repo, "glob.txt"), filepath.Join(repo, "keep.txt"))
		cmpIgnore(t, ggBin, repo, env, files())
	})
}

func TestGoldenIgnoreClusterF(t *testing.T) {
	ggBin := buildGG(t, e2eRoot(t))
	env := newIgnoreEnv(t)
	files := func(extra ...string) []string { return append([]string{"--files", "-j1"}, extra...) }

	ci := t.TempDir()
	mkdirAll(t, filepath.Join(ci, ".git"))
	writeFile(t, filepath.Join(ci, ".gitignore"), "foo.txt\n")
	touch(t, filepath.Join(ci, "FOO.TXT"), filepath.Join(ci, "keep.txt"), filepath.Join(ci, "BAR.TXT"))
	igci := filepath.Join(t.TempDir(), "ig-ci")
	writeFile(t, igci, "bar.txt\n")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"F1_case_default_survives", files()},
		{"F2_case_insensitive_gitignore", files("--ignore-file-case-insensitive")},
		{"F3_case_flag_not_explicit", files("--ignore-file-case-insensitive", "--ignore-file", igci)},
		{"F4_negation_restores", files("--ignore-file-case-insensitive", "--no-ignore-file-case-insensitive")},
	} {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, ci, env, tc.args) })
	}

	// F5: --ignore-file-case-insensitive DOES fold the global matcher
	// (unlike the explicit --ignore-file in F3) -- a global pattern cig.txt
	// ignores CIG.TXT under the flag.
	t.Run("F5_case_insensitive_global", func(t *testing.T) {
		env := newIgnoreEnv(t)
		writeFile(t, filepath.Join(env.xdg, "git", "ignore"), "cig.txt\n")
		repo := t.TempDir()
		mkdirAll(t, filepath.Join(repo, ".git"))
		touch(t, filepath.Join(repo, "CIG.TXT"), filepath.Join(repo, "keep.txt"))
		cmpIgnore(t, ggBin, repo, env, files("--ignore-file-case-insensitive"))
	})
}

func TestGoldenIgnoreClusterG(t *testing.T) {
	ggBin := buildGG(t, e2eRoot(t))
	env := newIgnoreEnv(t)
	writeFile(t, filepath.Join(env.xdg, "git", "ignore"), "glob.txt\n")

	repo := t.TempDir()
	mkStdRepo(t, repo)
	// keep.txt carries matchable content so the exit-code discrimination
	// below is meaningful.
	writeFile(t, filepath.Join(repo, "keep.txt"), "keepmatch\n")
	// A file matched only inside a gitignored file: not searched.
	writeFile(t, filepath.Join(repo, "hidden-by-git.txt"), "onlyinignored\n")
	writeFile(t, filepath.Join(repo, ".gitignore"), "git.txt\nhidden-by-git.txt\n")
	missing := filepath.Join(t.TempDir(), "nonexistent")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"G2_gitignored_not_searched", []string{"-j1", "-l", "onlyinignored"}},
		{"G3_no_ignore_finds_it", []string{"-j1", "-l", "--no-ignore", "onlyinignored"}},
		// G4r/G5r: a missing --ignore-file warns to stderr but the run
		// otherwise succeeds -- a real match still gives exit 0 (the load
		// error never sets anyError -> never exit 2).
		{"G4_missing_ignore_file_real_match", []string{"-j1", "-l", "--ignore-file", missing, "keepmatch"}},
		{"G5_missing_ignore_file_quiet_match", []string{"-j1", "-q", "--ignore-file", missing, "keepmatch"}},
		// G6: missing --ignore-file + NO match -> exit 1 (warning present,
		// but still not exit 2), proving the warning is orthogonal to the
		// match-driven exit code.
		{"G6_missing_ignore_file_no_match", []string{"-j1", "-l", "--ignore-file", missing, "zzznomatch"}},
	} {
		t.Run(tc.name, func(t *testing.T) { cmpIgnore(t, ggBin, repo, env, tc.args) })
	}

	// G7: an unreadable (EACCES) --ignore-file behaves exactly like a
	// missing one -- warning, exit 0 with a real match -- since both hit
	// the same os.ReadFile error path in LoadExplicitIgnore. Skipped under
	// root, where chmod 000 does not actually block reads.
	t.Run("G7_unreadable_ignore_file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("chmod 000 does not block root")
		}
		noread := filepath.Join(t.TempDir(), "noread.ign")
		writeFile(t, noread, "x\n")
		if err := os.Chmod(noread, 0o000); err != nil {
			t.Fatal(err)
		}
		cmpIgnore(t, ggBin, repo, env, []string{"-j1", "-l", "--ignore-file", noread, "keepmatch"})
	})
}
