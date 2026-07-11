package walk

import (
	"os"
	"path/filepath"
	"reflect"

	"github.com/yackey-labs/gripgrep/glob"
	"testing"
)

// mkGitDir creates dir/.git/info so a directory counts as a repo root and
// can carry an info/exclude file.
func mkGitDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestIgnorePrecedenceOrder checks the full source precedence chain
// .rgignore > .ignore > .gitignore > exclude: each file is ignored by a
// higher-precedence source and whitelisted by the next one down, so only
// the higher verdict may win.
func TestIgnorePrecedenceOrder(t *testing.T) {
	root := t.TempDir()
	mkGitDir(t, root)
	writeFile(t, filepath.Join(root, ".rgignore"), "w.txt\n")
	writeFile(t, filepath.Join(root, ".ignore"), "!w.txt\nx.txt\n")
	writeFile(t, filepath.Join(root, ".gitignore"), "!x.txt\ny.txt\n")
	writeFile(t, filepath.Join(root, ".git", "info", "exclude"), "!y.txt\nz.txt\n")
	for _, f := range []string{"w.txt", "x.txt", "y.txt", "z.txt", "keep.txt"} {
		writeFile(t, filepath.Join(root, f), "")
	}

	got := visitFiles(t, root, Options{})
	if want := []string{"keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("precedence: got %v, want %v", got, want)
	}
}

// TestSawGitNestedRepo checks that a .gitignore ABOVE a repo root does not
// apply inside the repo (saw_git stops the ascent), but --no-require-git
// makes it apply (saw_git never trips).
func TestSawGitNestedRepo(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, ".gitignore"), "foo.txt\n")
	repo := filepath.Join(base, "repo")
	mkGitDir(t, repo)
	writeFile(t, filepath.Join(repo, "foo.txt"), "")
	writeFile(t, filepath.Join(repo, "keep.txt"), "")

	got := visitFiles(t, repo, Options{})
	if want := []string{"foo.txt", "keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("default: got %v, want %v (outer .gitignore must not apply inside repo)", got, want)
	}

	got = visitFiles(t, repo, Options{NoRequireGit: true})
	if want := []string{"keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--no-require-git: got %v, want %v (outer .gitignore should apply)", got, want)
	}
}

// TestParentChainToFSRoot checks that non-git ignore files above the repo
// root apply to a walk of a subdirectory, and that --no-ignore-parent
// disables the whole chain.
func TestParentChainToFSRoot(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, ".ignore"), "pi.txt\n")
	writeFile(t, filepath.Join(base, ".rgignore"), "pr.txt\n")
	repo := filepath.Join(base, "repo")
	mkGitDir(t, repo)
	writeFile(t, filepath.Join(repo, ".gitignore"), "pg.txt\n")
	deep := filepath.Join(repo, "deep")
	for _, f := range []string{"pi.txt", "pr.txt", "pg.txt", "keep.txt"} {
		writeFile(t, filepath.Join(deep, f), "")
	}

	got := visitFiles(t, deep, Options{})
	if want := []string{"keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("parent chain: got %v, want %v", got, want)
	}

	// --no-ignore-parent disables the entire parent chain, including the
	// repo root's own .gitignore (a parent of deep), so all four files
	// survive (probe D3).
	got = visitFiles(t, deep, Options{NoIgnoreParent: true})
	if want := []string{"keep.txt", "pg.txt", "pi.txt", "pr.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--no-ignore-parent: got %v, want %v", got, want)
	}

	// --no-ignore-dot drops parent .ignore/.rgignore but keeps the repo
	// root's .gitignore.
	got = visitFiles(t, deep, Options{NoIgnoreDot: true})
	if want := []string{"keep.txt", "pi.txt", "pr.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--no-ignore-dot: got %v, want %v", got, want)
	}
}

// TestGlobalMatcherAnyGitGate checks that the global matcher only applies
// inside a repo (anyGit), and that --no-require-git opens the gate.
func TestGlobalMatcherAnyGitGate(t *testing.T) {
	global := compileGlobData([]byte("glob.txt\n"), false)
	if global == nil {
		t.Fatal("global set should compile")
	}

	repo := t.TempDir()
	mkGitDir(t, repo)
	writeFile(t, filepath.Join(repo, "glob.txt"), "")
	writeFile(t, filepath.Join(repo, "keep.txt"), "")
	got := visitFiles(t, repo, Options{GlobalIgnore: global})
	if want := []string{"keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("global inside repo: got %v, want %v", got, want)
	}

	norepo := t.TempDir()
	writeFile(t, filepath.Join(norepo, "glob.txt"), "")
	writeFile(t, filepath.Join(norepo, "keep.txt"), "")
	got = visitFiles(t, norepo, Options{GlobalIgnore: global})
	if want := []string{"glob.txt", "keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("global outside repo: got %v, want %v (anyGit gate should keep it inert)", got, want)
	}
	got = visitFiles(t, norepo, Options{GlobalIgnore: global, NoRequireGit: true})
	if want := []string{"keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("global outside repo --no-require-git: got %v, want %v", got, want)
	}
}

// TestCaseInsensitiveGating checks that --ignore-file-case-insensitive
// folds the per-directory tree sources but NOT the explicit matcher.
func TestCaseInsensitiveGating(t *testing.T) {
	root := t.TempDir()
	mkGitDir(t, root)
	writeFile(t, filepath.Join(root, ".gitignore"), "foo.txt\n")
	writeFile(t, filepath.Join(root, "FOO.TXT"), "")
	writeFile(t, filepath.Join(root, "BAR.TXT"), "")
	writeFile(t, filepath.Join(root, "keep.txt"), "")
	explicit := compileGlobData([]byte("bar.txt\n"), false)

	// Case-sensitive default: FOO.TXT and BAR.TXT both survive.
	got := visitFiles(t, root, Options{ExplicitIgnore: explicit})
	if want := []string{"BAR.TXT", "FOO.TXT", "keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("case-sensitive: got %v, want %v", got, want)
	}

	// Case-insensitive: the tree .gitignore now matches FOO.TXT, but the
	// explicit matcher (bar.txt) stays case-sensitive, so BAR.TXT survives.
	got = visitFiles(t, root, Options{ExplicitIgnore: explicit, IgnoreCaseInsensitive: true})
	if want := []string{"BAR.TXT", "keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("case-insensitive: got %v, want %v", got, want)
	}
}

// TestAnchorToCwd covers the CWD prefix strip the global/explicit matchers
// apply to absolute candidate paths (rg's Gitignore root=CWD strip).
func TestAnchorToCwd(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		cwd  string
		want string
	}{
		{"under_cwd", "/home/u/proj/a.txt", "/home/u/proj", "a.txt"},
		{"under_cwd_nested", "/home/u/proj/sub/a.txt", "/home/u/proj", "sub/a.txt"},
		{"root_cwd", "/tmp/x/az.txt", "/", "tmp/x/az.txt"},
		{"not_under_cwd", "/other/a.txt", "/home/u/proj", "/other/a.txt"},
		{"sibling_prefix_not_boundary", "/home/u/project2/a.txt", "/home/u/proj", "/home/u/project2/a.txt"},
		{"relative_path", "sub/a.txt", "/home/u/proj", "sub/a.txt"},
		{"empty_cwd", "/home/u/a.txt", "", "/home/u/a.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := string(anchorToCwd([]byte(tc.path), tc.cwd))
			if got != tc.want {
				t.Errorf("anchorToCwd(%q, %q) = %q, want %q", tc.path, tc.cwd, got, tc.want)
			}
		})
	}
}

// TestLoadGlobalIgnoreResolution checks the env-driven resolution order:
// core.excludesFile in a git config replaces the default XDG ignore path.
func TestLoadGlobalIgnoreResolution(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	xdg := filepath.Join(base, "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(xdg, "git", "ignore"), "default.txt\n")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	// Default path used when no config names an excludesFile.
	set := LoadGlobalIgnore(false)
	if set == nil || set.Match([]byte("default.txt"), false) != glob.Ignored {
		t.Fatalf("default path: expected default.txt ignored, set=%v", set != nil)
	}

	// core.excludesFile in HOME/.gitconfig replaces the default path.
	custom := filepath.Join(base, "custom-ignore")
	writeFile(t, custom, "custom.txt\n")
	writeFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\texcludesfile = "+custom+"\n")
	set = LoadGlobalIgnore(false)
	if set == nil {
		t.Fatal("excludesFile path: set is nil")
	}
	if set.Match([]byte("custom.txt"), false) != glob.Ignored {
		t.Error("excludesFile: custom.txt should be ignored")
	}
	if set.Match([]byte("default.txt"), false) == glob.Ignored {
		t.Error("excludesFile: default.txt should NOT be ignored (default path replaced)")
	}
}
