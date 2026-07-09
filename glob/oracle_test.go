package glob

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file differentially tests Set against the real `git check-ignore`
// command: same .gitignore content, same path, verdicts must agree. Any
// mismatch here is either a real bug in the parser/regex translation, or
// a gitignore subtlety this package hasn't accounted for.
//
// One subtlety needed careful handling: git enforces "you can't
// re-include a path if a parent directory of that path is excluded" —
// e.g. `foo/` + `!foo/bar` still ignores `foo/bar`, because `foo` itself
// is pruned. Set.Match is intentionally flat (mirrors rg's
// Gitignore::matched_stripped exactly: one glob-set, one path, no
// ancestor awareness) — that rule is enforced by the *walker* pruning a
// directory before ever descending into it and asking Set.Match about
// its contents, not by Set itself. So the oracle doesn't call
// Set.Match(path, isDir) directly; it calls ignoredLikeWalk, which
// simulates the walker's top-down prefix pruning against the same Set.
// That's the behavior a correct walk integration must reproduce — see
// the coordination note in this package's handoff to "walk" and "main".
//
// A second subtlety, discovered empirically while building this harness:
// git determines a checked path's file-vs-directory type by *stat*ing it
// on disk (falling back to a trailing '/' in the argument only when
// nothing exists there). A synthetic nonexistent path with a
// hand-appended trailing slash does NOT always behave like a real
// directory — e.g. `a/**` (contents-only ignore) against the literal
// argument "a/" reports ignored, but against a real, existing, empty
// directory "a" it does not (matching rg's own gitignore.rs test
// expectations). So this harness creates real files/directories in a
// throwaway repo for every case rather than faking type via string
// tricks, which is what `git check-ignore` itself is sensitive to.
func ignoredLikeWalk(s *Set, path string, isDir bool) bool {
	comps := strings.Split(path, "/")
	prefix := ""
	for i := 0; i < len(comps)-1; i++ {
		if prefix == "" {
			prefix = comps[i]
		} else {
			prefix = prefix + "/" + comps[i]
		}
		if s.Match([]byte(prefix), true) == Ignored {
			return true
		}
	}
	return s.Match([]byte(path), isDir) == Ignored
}

func requireGit(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping oracle test")
	}
}

// isolatedGitEnv strips out any system/global/user git config so a
// stray core.excludesFile or similar on the host can't poison verdicts.
func isolatedGitEnv(homeDir string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + homeDir,
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
}

func initGitRepo(t testing.TB, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", ".")
	cmd.Dir = dir
	cmd.Env = isolatedGitEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func writeGitignore(t testing.TB, dir string, patterns []string) {
	t.Helper()
	content := strings.Join(patterns, "\n")
	if len(patterns) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// createFSPath creates a real file or directory at path (relative to
// repoDir, '/'-separated) so git's own stat-based type detection sees
// exactly what the oracle comparison intends by isDir. All intermediate
// directories are created too.
func createFSPath(t testing.TB, repoDir, path string, isDir bool) {
	t.Helper()
	full := filepath.Join(repoDir, filepath.FromSlash(path))
	if isDir {
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", full, err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir parent of %q: %v", full, err)
	}
	if err := os.WriteFile(full, nil, 0o644); err != nil {
		t.Fatalf("create file %q: %v", full, err)
	}
}

// gitCheckIgnore reports whether git considers path (relative to
// repoDir, must already exist on disk via createFSPath) ignored.
func gitCheckIgnore(t testing.TB, repoDir, path string) bool {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "-q", "--", path)
	cmd.Dir = repoDir
	cmd.Env = isolatedGitEnv(repoDir)
	err := cmd.Run()
	if err == nil {
		return true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore -q -- %q: %v", path, err)
	return false
}

// compareOracle builds a fresh throwaway repo, writes patterns to its
// .gitignore, creates path for real on disk (so git's type detection
// matches isDir), and asserts ignoredLikeWalk(Set, ...) agrees with git
// check-ignore. Each case gets its own repo so unrelated cases can't
// leave conflicting files/directories behind for each other (e.g. one
// case wanting "foo" as a file, another wanting it as a directory).
func compareOracle(t *testing.T, patterns []string, path string, isDir bool) {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeGitignore(t, dir, patterns)

	var b Builder
	for _, p := range patterns {
		b.Add(p)
	}
	s, err := b.Build()
	if err != nil {
		t.Fatalf("Build(%q): %v", patterns, err)
	}

	ours := ignoredLikeWalk(s, path, isDir)

	createFSPath(t, dir, path, isDir)
	want := gitCheckIgnore(t, dir, path)

	if ours != want {
		t.Errorf("oracle mismatch: patterns=%q path=%q isDir=%v: ours=%v git=%v", patterns, path, isDir, ours, want)
	}
}

// TestGitCheckIgnoreOracle is the table half of the differential oracle:
// each case is adjudicated by real git, not a hand-derived expectation.
// It specifically covers every named edge case from PLAN.md's test
// coverage requirements: `!` re-include (including the
// can't-re-include-under-an-excluded-ancestor rule), trailing-space
// handling (both trimmed and backslash-escaped), `\#`/`\!` escapes,
// `a/**`, `**/a`, `a/**/b`, and a bare `/`.
func TestGitCheckIgnoreOracle(t *testing.T) {
	requireGit(t)

	cases := []struct {
		name     string
		patterns []string
		path     string
		isDir    bool
	}{
		// `!` re-include, ordinary (same directory level).
		{"reinclude-basic", []string{"*.rs", "!keep.rs"}, "keep.rs", false},
		{"reinclude-other-still-ignored", []string{"*.rs", "!keep.rs"}, "drop.rs", false},
		{"reinclude-reverse-order-loses", []string{"!keep.rs", "*.rs"}, "keep.rs", false},

		// `!` re-include blocked by an excluded ancestor directory,
		// versus a whitelist at the *same* level (not blocked).
		{"reinclude-blocked-by-ancestor-dir", []string{"foo/", "!foo/bar"}, "foo/bar", false},
		{"reinclude-blocked-by-ancestor-dir-deep", []string{"foo/", "!foo/bar/baz"}, "foo/bar/baz", false},
		{"reinclude-same-level-not-blocked", []string{"foo/", "!foo"}, "foo", true},

		// Trailing-space handling.
		{"trailing-space-trimmed", []string{"node_modules/ "}, "node_modules", true},
		{"trailing-space-trimmed-file", []string{"foo.txt   "}, "foo.txt", false},
		{"trailing-space-escaped", []string{`foo\ `}, "foo ", false},

		// `\#`/`\!` escapes (a literal leading '#' or '!' in a name).
		{"escape-hash", []string{`\#foo`}, "#foo", false},
		{"escape-hash-not-comment", []string{`\#foo`}, "foo", false},
		{"escape-bang", []string{`\!bar`}, "!bar", false},
		{"escape-bang-not-whitelist", []string{`\!bar`}, "bar", false},

		// `a/**`: contents ignored, directory itself is not.
		{"a-doublestar-self", []string{"a/**"}, "a", true},
		{"a-doublestar-child", []string{"a/**"}, "a/x", false},
		{"a-doublestar-grandchild", []string{"a/**"}, "a/x/y", false},

		// `**/a`: basename match at any depth, respecting component
		// boundaries (no partial-name matches).
		{"doublestar-a-top", []string{"**/a"}, "a", false},
		{"doublestar-a-nested", []string{"**/a"}, "x/y/a", false},
		{"doublestar-a-component-boundary", []string{"**/a"}, "xa", false},
		{"doublestar-a-component-boundary2", []string{"**/a"}, "ax", false},

		// `a/**/b`: zero, one, and two intervening directories.
		{"a-doublestar-b-zero", []string{"a/**/b"}, "a/b", false},
		{"a-doublestar-b-one", []string{"a/**/b"}, "a/x/b", false},
		{"a-doublestar-b-two", []string{"a/**/b"}, "a/x/y/b", false},
		{"a-doublestar-b-notb", []string{"a/**/b"}, "a/x/notb", false},

		// A bare `/` pattern (degenerate: matches nothing).
		{"bare-slash-file", []string{"/"}, "foo", false},
		{"bare-slash-dir", []string{"/"}, "foo", true},
		{"bare-slash-nested", []string{"/"}, "foo/bar", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			compareOracle(t, c.patterns, c.path, c.isDir)
		})
	}
}

// --- Fuzz variant: a valid-pattern generator ---------------------------

// fuzzCursor turns a fuzzer-supplied byte slice into a deterministic
// stream of small choices, so the same input always generates the same
// pattern/path pair (required for the fuzzer's minimization/replay to
// make sense).
type fuzzCursor struct {
	data []byte
	pos  int
}

func (c *fuzzCursor) next() byte {
	if c.pos >= len(c.data) {
		return 0
	}
	b := c.data[c.pos]
	c.pos++
	return b
}

func (c *fuzzCursor) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(c.next()) % n
}

func (c *fuzzCursor) bool() bool {
	return c.next()&1 == 1
}

var (
	fuzzSegments  = []string{"a", "b", "c", "foo", "bar", "baz", "dir1", "dir2"}
	fuzzExts      = []string{".go", ".rs", ".txt", ".log", ".o", ".md"}
	fuzzWildcards = []string{"*", "?", "[abc]", "[a-z]", "[!xyz]"}
)

// genGlobComponent produces one slash-free glob path component: a plain
// literal, an extension glob, a character class/wildcard, or a
// literal+extension combo — the shapes that dominate real gitignore
// files.
func genGlobComponent(c *fuzzCursor) string {
	switch c.intn(4) {
	case 0:
		return fuzzSegments[c.intn(len(fuzzSegments))]
	case 1:
		return "*" + fuzzExts[c.intn(len(fuzzExts))]
	case 2:
		return fuzzWildcards[c.intn(len(fuzzWildcards))]
	default:
		return fuzzSegments[c.intn(len(fuzzSegments))] + fuzzExts[c.intn(len(fuzzExts))]
	}
}

// genPattern produces a syntactically valid gitignore pattern line:
// optional `!`, optional leading `/`, 1-3 components (occasionally a
// bare `**` component), optional trailing `/`.
func genPattern(c *fuzzCursor) string {
	var sb strings.Builder
	if c.bool() {
		sb.WriteByte('!')
	}
	if c.bool() {
		sb.WriteByte('/')
	}
	n := 1 + c.intn(3)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte('/')
		}
		if c.intn(6) == 0 {
			sb.WriteString("**")
		} else {
			sb.WriteString(genGlobComponent(c))
		}
	}
	if c.bool() {
		sb.WriteByte('/')
	}
	return sb.String()
}

// genPath produces a candidate path of 1-4 components, mixing names
// that share vocabulary with genGlobComponent (so patterns actually
// have a chance to hit) with names that don't (so they don't).
func genPath(c *fuzzCursor) (string, bool) {
	n := 1 + c.intn(4)
	parts := make([]string, n)
	for i := range parts {
		switch c.intn(3) {
		case 0:
			parts[i] = fuzzSegments[c.intn(len(fuzzSegments))]
		case 1:
			parts[i] = fuzzSegments[c.intn(len(fuzzSegments))] + fuzzExts[c.intn(len(fuzzExts))]
		default:
			parts[i] = "x" + string(rune('0'+c.intn(5)))
		}
	}
	return strings.Join(parts, "/"), c.bool()
}

// FuzzGitignoreOracle runs the same differential check as
// TestGitCheckIgnoreOracle over generator-produced pattern/path pairs.
// Under plain `go test`, only the seed corpus below runs (satisfying
// "regular test run, not just a benchmark eyeball"); run with
// `go test -fuzz=FuzzGitignoreOracle` for open-ended exploration. Each
// fuzz execution gets its own throwaway repo (matching compareOracle)
// since a shared repo across generated pattern/path pairs would risk one
// iteration's file colliding with another's directory at the same name.
func FuzzGitignoreOracle(f *testing.F) {
	requireGit(f)
	seeds := [][]byte{
		{0, 1, 2, 3, 4, 5, 6, 7},
		{1, 1, 1, 1, 1, 1, 1, 1},
		{5, 10, 15, 20, 25, 30, 35, 40},
		{2, 4, 6, 8, 10, 12, 14, 16, 18},
		{0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{255, 254, 253, 252, 251, 250, 249},
		{3, 1, 4, 1, 5, 9, 2, 6, 5, 3, 5},
		{1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			t.Skip("empty input")
		}
		c := &fuzzCursor{data: data}
		pattern := genPattern(c)
		path, isDir := genPath(c)
		if pattern == "" || pattern == "!" {
			t.Skip("degenerate pattern")
		}

		var b Builder
		b.Add(pattern)
		s, err := b.Build()
		if err != nil {
			// Not a pattern our compiler accepts (e.g. an invalid
			// character range) — nothing to compare against git.
			t.Skip("pattern rejected by Build: " + err.Error())
		}

		dir := t.TempDir()
		initGitRepo(t, dir)
		writeGitignore(t, dir, []string{pattern})

		ours := ignoredLikeWalk(s, path, isDir)

		createFSPath(t, dir, path, isDir)
		want := gitCheckIgnore(t, dir, path)

		if ours != want {
			t.Fatalf("oracle mismatch: pattern=%q path=%q isDir=%v ours=%v git=%v", pattern, path, isDir, ours, want)
		}
	})
}
