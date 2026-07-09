package walk

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/yackey-labs/gripgrep/glob"
)

// visitFiles walks root with opts and returns the sorted base names of
// every visited TypeFile entry. Paths are cloned before being retained
// (see the clone doc in walk_test.go).
func visitFiles(t *testing.T, root string, opts Options) []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	err := Walk([]string{root}, opts, func(e *Entry) WalkState {
		if e.Type == TypeFile {
			base := clone(filepath.Base(e.Path))
			mu.Lock()
			got = append(got, base)
			mu.Unlock()
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	sort.Strings(got)
	return got
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func markGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
}

func TestGitignoreRespectedInsideRepo(t *testing.T) {
	root := t.TempDir()
	markGitRepo(t, root)
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.log"), "b")

	got := visitFiles(t, root, Options{})
	want := []string{"a.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGitignoreNotAppliedOutsideRepo(t *testing.T) {
	root := t.TempDir() // no .git anywhere
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.log"), "b")

	got := visitFiles(t, root, Options{})
	want := []string{"a.txt", "b.log"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (gitignore should be inert with no .git anywhere)", got, want)
	}
}

func TestDotIgnoreAppliesWithoutGitRepo(t *testing.T) {
	root := t.TempDir() // no .git anywhere
	writeFile(t, filepath.Join(root, ".ignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.log"), "b")

	got := visitFiles(t, root, Options{})
	want := []string{"a.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (.ignore is not git-gated)", got, want)
	}
}

func TestNestedGitignoreWhitelistReinclusion(t *testing.T) {
	root := t.TempDir()
	markGitRepo(t, root)
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "sub", ".gitignore"), "!keep.log\n")
	writeFile(t, filepath.Join(root, "sub", "keep.log"), "keep")
	writeFile(t, filepath.Join(root, "sub", "other.log"), "other")
	writeFile(t, filepath.Join(root, "sub", "a.txt"), "a")

	got := visitFiles(t, root, Options{})
	want := []string{"a.txt", "keep.log"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGitExcludeRespected(t *testing.T) {
	root := t.TempDir()
	markGitRepo(t, root)
	writeFile(t, filepath.Join(root, ".git", "info", "exclude"), "*.tmp\n")
	writeFile(t, filepath.Join(root, "keep.txt"), "keep")
	writeFile(t, filepath.Join(root, "temp.tmp"), "temp")

	got := visitFiles(t, root, Options{})
	want := []string{"keep.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestIgnoreFileTakesPrecedenceOverGitignore(t *testing.T) {
	root := t.TempDir()
	markGitRepo(t, root)
	// .gitignore whitelists it, .ignore plainly ignores it: .ignore wins
	// (.ignore > .gitignore > exclude).
	writeFile(t, filepath.Join(root, ".gitignore"), "!important.log\n")
	writeFile(t, filepath.Join(root, ".ignore"), "important.log\n")
	writeFile(t, filepath.Join(root, "important.log"), "x")
	writeFile(t, filepath.Join(root, "a.txt"), "a")

	got := visitFiles(t, root, Options{})
	want := []string{"a.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (.ignore should override .gitignore's whitelist)", got, want)
	}
}

func TestGitignoreDirectoryOnlyPattern(t *testing.T) {
	root := t.TempDir()
	markGitRepo(t, root)
	writeFile(t, filepath.Join(root, ".gitignore"), "build/\n")
	writeFile(t, filepath.Join(root, "build", "out.txt"), "out")
	writeFile(t, filepath.Join(root, "notbuild.txt"), "x")
	// A *file* literally named "build" should not be excluded by a
	// directory-only pattern.
	writeFile(t, filepath.Join(root, "src", "build"), "not-a-dir")

	got := visitFiles(t, root, Options{})
	want := []string{"build", "notbuild.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParentGitignoreAppliesAboveWalkRoot(t *testing.T) {
	repo := t.TempDir()
	markGitRepo(t, repo)
	writeFile(t, filepath.Join(repo, ".gitignore"), "*.log\n")
	sub := filepath.Join(repo, "src")
	writeFile(t, filepath.Join(sub, "a.txt"), "a")
	writeFile(t, filepath.Join(sub, "b.log"), "b")

	// Walk root is the SUBDIRECTORY, not the repo root: the repo root's
	// .gitignore must still apply (buildParentChain).
	got := visitFiles(t, sub, Options{})
	want := []string{"a.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (parent .gitignore above walk root should still apply)", got, want)
	}
}

func TestNoIgnoreDisablesAllIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	markGitRepo(t, root)
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, ".ignore"), "*.txt\n")
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.log"), "b")

	got := visitFiles(t, root, Options{NoIgnore: true})
	// NoIgnore disables ignore-*file processing* only; the dotfiles
	// themselves are still hidden by the (independent) hidden-file rule.
	want := []string{"a.txt", "b.log"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGlobsOverrideTakesPrecedenceOverIgnore(t *testing.T) {
	root := t.TempDir()
	markGitRepo(t, root)
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "keep.log"), "x")
	writeFile(t, filepath.Join(root, "a.txt"), "a")

	var b glob.Builder
	b.Add("keep.log") // -g whitelist-capable override forcing inclusion
	set, err := b.Build()
	if err != nil {
		t.Fatalf("glob build: %v", err)
	}

	got := visitFiles(t, root, Options{Globs: set})
	// keep.log's Set.Match here returns Ignored (a plain, non-'!'
	// pattern) which per shouldSkip's precedence *excludes* it (Globs
	// highest precedence, and a plain match is exclusionary just like an
	// ignore file). This test documents that behavior rather than -g's
	// CLI-level include semantics, which GlobsRequireMatch covers
	// separately (see TestGlobsRequireMatch).
	want := []string{"a.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGlobsRequireMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "a")
	writeFile(t, filepath.Join(root, "b.txt"), "b")

	var b glob.Builder
	// Per task #12's resolution, the CLI encodes a plain `-g '*.go'`
	// include as a whitelist entry (flipped polarity) so that a matching
	// path bypasses exclusion, while GlobsRequireMatch handles the "no
	// override glob matched at all" exclusion case below.
	b.Add("!*.go")
	set, err := b.Build()
	if err != nil {
		t.Fatalf("glob build: %v", err)
	}

	got := visitFiles(t, root, Options{NoIgnore: true, Globs: set, GlobsRequireMatch: true})
	want := []string{"a.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (b.txt matches no override glob and should be excluded)", got, want)
	}
}

// TestGlobsRequireMatchDoesNotPruneDirs is a regression test for an M2
// integration finding: `gg -g '*.rs' pat .` over the real ripgrep
// checkout (a deeply nested tree) only found matches in root-level .rs
// files, missing every .rs file under a subdirectory -- because
// GlobsRequireMatch's "no override glob matched at all -> exclude" rule
// was being applied to directories too. A directory like "crates" or
// "sub" doesn't itself match "*.go" (almost no directory name ends in
// ".go"), so the whole subtree was pruned before any file inside it got
// a chance to match. GlobsRequireMatch must only ever exclude files;
// directories always need to be descended into so their contents can be
// individually filtered. See classify's doc for the verified rg
// comparison.
func TestGlobsRequireMatchDoesNotPruneDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "a")
	writeFile(t, filepath.Join(root, "sub", "nested.go"), "b")
	writeFile(t, filepath.Join(root, "sub", "deeper", "more.go"), "c")
	writeFile(t, filepath.Join(root, "sub", "skip.txt"), "d")

	var b glob.Builder
	b.Add("!*.go") // flipped-polarity whitelist, per task #12's CLI encoding
	set, err := b.Build()
	if err != nil {
		t.Fatalf("glob build: %v", err)
	}

	got := visitFiles(t, root, Options{NoIgnore: true, Globs: set, GlobsRequireMatch: true})
	want := []string{"a.go", "more.go", "nested.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (every .go file at every depth should match; skip.txt should not; subdirectories must never be pruned just for failing to match *.go themselves)", got, want)
	}
}
