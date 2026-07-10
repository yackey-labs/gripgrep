package walk

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// clone copies s's bytes into a fresh allocation. Entry.Path (and any
// substring derived from it, e.g. via filepath.Base) is documented as
// valid only for the duration of a Visitor call — the worker that
// produced it reuses the same backing buffer on its very next entry.
// Every test below that retains a path past its callback must clone it
// first; skipping this produces silent, hard-to-diagnose aliasing
// corruption (two stored "paths" quietly become the same string once the
// producing worker moves on), not a crash or a race-detector hit.
func clone(s string) string { return strings.Clone(s) }

// buildTree creates a small directory tree under a temp dir per spec:
// spec[path] = "" for a directory, or file contents for a file. Parent
// directories are created implicitly.
func buildTree(t *testing.T, spec map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range spec {
		full := filepath.Join(root, rel)
		if content == "" && (rel == "" || rel[len(rel)-1] == '/') {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", full, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir parent of %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return root
}

func TestWalkVisitsAllFilesExactlyOnce(t *testing.T) {
	spec := map[string]string{
		"a.txt":               "a",
		"dir1/b.txt":          "b",
		"dir1/c.txt":          "c",
		"dir1/sub/d.txt":      "d",
		"dir2/e.txt":          "e",
		"dir2/sub/f.txt":      "f",
		"dir2/sub/sub2/g.txt": "g",
	}
	root := buildTree(t, spec)

	var mu sync.Mutex
	seen := map[string]int{}
	err := Walk([]string{root}, Options{NoIgnore: true, Hidden: true}, func(e *Entry) WalkState {
		mu.Lock()
		seen[clone(e.Path)]++
		mu.Unlock()
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	wantFiles := []string{"a.txt", "dir1/b.txt", "dir1/c.txt", "dir1/sub/d.txt", "dir2/e.txt", "dir2/sub/f.txt", "dir2/sub/sub2/g.txt"}
	for _, f := range wantFiles {
		full := filepath.Join(root, f)
		if n := seen[full]; n != 1 {
			t.Errorf("file %s visited %d times, want 1", full, n)
		}
	}
	wantDirs := []string{root, filepath.Join(root, "dir1"), filepath.Join(root, "dir1", "sub"), filepath.Join(root, "dir2"), filepath.Join(root, "dir2", "sub"), filepath.Join(root, "dir2", "sub", "sub2")}
	for _, d := range wantDirs {
		if n := seen[d]; n != 1 {
			t.Errorf("dir %s visited %d times, want 1", d, n)
		}
	}
	if len(seen) != len(wantFiles)+len(wantDirs) {
		t.Errorf("visited %d distinct paths, want %d", len(seen), len(wantFiles)+len(wantDirs))
	}
}

func TestSkipDirPrunesSubtree(t *testing.T) {
	spec := map[string]string{
		"keep/a.txt":         "a",
		"prune/b.txt":        "b",
		"prune/sub/c.txt":    "c",
		"prune/sub/deep.txt": "deep",
	}
	root := buildTree(t, spec)
	pruneDir := filepath.Join(root, "prune")

	var mu sync.Mutex
	var visited []string
	err := Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		isPruneDir := e.Path == pruneDir // compared while e.Path is still valid
		mu.Lock()
		visited = append(visited, clone(e.Path))
		mu.Unlock()
		if isPruneDir {
			return SkipDir
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	for _, p := range visited {
		if p != pruneDir && filepath_HasPrefixDir(p, pruneDir) {
			t.Errorf("entry %s under pruned dir %s was visited", p, pruneDir)
		}
	}
	// keep/a.txt must still show up.
	found := false
	for _, p := range visited {
		if p == filepath.Join(root, "keep", "a.txt") {
			found = true
		}
	}
	if !found {
		t.Errorf("keep/a.txt not visited; visited=%v", visited)
	}
}

func filepath_HasPrefixDir(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[0] != '.'
}

func TestSkipDirOnFileIsNoop(t *testing.T) {
	spec := map[string]string{"a.txt": "a", "b.txt": "b"}
	root := buildTree(t, spec)

	var mu sync.Mutex
	count := 0
	err := Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		if e.Type == TypeFile {
			mu.Lock()
			count++
			mu.Unlock()
			return SkipDir // per doc: behaves like Continue on a file entry
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if count != 2 {
		t.Errorf("visited %d files, want 2 (SkipDir on a file must not stop the walk)", count)
	}
}

func TestQuitStopsPromptly(t *testing.T) {
	spec := map[string]string{}
	for i := 0; i < 50; i++ {
		spec[fmt.Sprintf("f%03d.txt", i)] = "x"
	}
	root := buildTree(t, spec)

	// Single worker: deterministic, so Quit-on-first-visit means exactly
	// one Visitor call total.
	var mu sync.Mutex
	count := 0
	err := Walk([]string{root}, Options{NoIgnore: true, Threads: 1}, func(e *Entry) WalkState {
		mu.Lock()
		count++
		mu.Unlock()
		return Quit
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if count != 1 {
		t.Errorf("Quit on first visit: got %d total visits, want exactly 1", count)
	}
}

func TestQuitFromDeepWorkerStopsAllWorkers(t *testing.T) {
	spec := map[string]string{}
	for i := 0; i < 2000; i++ {
		spec[fmt.Sprintf("d%03d/f%03d.txt", i%20, i)] = "x"
	}
	root := buildTree(t, spec)

	var mu sync.Mutex
	count := 0
	err := Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		mu.Lock()
		count++
		n := count
		mu.Unlock()
		if n == 5 {
			return Quit
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	total := 2000 + 20 + 1 // files + subdirs + root
	if count >= total {
		t.Errorf("Quit did not stop the walk early: visited %d of %d", count, total)
	}
}

func TestDeepAndWideTree(t *testing.T) {
	spec := map[string]string{}
	const width, depth = 4, 4 // a file at every level: 4+16+64+256 = 340 files, deep+wide enough to exercise steal fan-out without a multi-second fixture build
	var buildPath func(prefix string, d int)
	fileN := 0
	buildPath = func(prefix string, d int) {
		if d == 0 {
			return
		}
		for i := 0; i < width; i++ {
			sub := fmt.Sprintf("%s/n%d", prefix, i)
			spec[sub+fmt.Sprintf("/leaf%d.txt", fileN)] = "x"
			fileN++
			buildPath(sub, d-1)
		}
	}
	buildPath("top", depth)
	root := buildTree(t, spec)

	var mu sync.Mutex
	files := map[string]bool{}
	err := Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		if e.Type == TypeFile {
			mu.Lock()
			files[clone(e.Path)] = true
			mu.Unlock()
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(files) != fileN {
		t.Errorf("visited %d files, want %d", len(files), fileN)
	}
}

func TestConcurrentVisitorSafety(t *testing.T) {
	spec := map[string]string{}
	for i := 0; i < 500; i++ {
		spec[fmt.Sprintf("d%02d/f%03d.txt", i%10, i)] = "x"
	}
	root := buildTree(t, spec)

	var mu sync.Mutex
	seen := map[string]bool{}
	err := Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		p := clone(e.Path)
		mu.Lock()
		if seen[p] {
			t.Errorf("duplicate visit: %s", p)
		}
		seen[p] = true
		mu.Unlock()
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
}

// TestQuiescenceInvariant runs the walker repeatedly over the same tree
// and asserts the visited-entry count never varies — a flaky count here
// would indicate a termination race (early quit or a hang masked by
// t.Fatal in some other goroutine).
func TestQuiescenceInvariant(t *testing.T) {
	spec := map[string]string{}
	for i := 0; i < 300; i++ {
		spec[fmt.Sprintf("d%02d/sub%d/f%03d.txt", i%7, i%3, i)] = "x"
	}
	root := buildTree(t, spec)

	var want int
	for iter := 0; iter < 100; iter++ {
		var mu sync.Mutex
		count := 0
		err := Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
			mu.Lock()
			count++
			mu.Unlock()
			return Continue
		})
		if err != nil {
			t.Fatalf("iter %d: Walk: %v", iter, err)
		}
		if iter == 0 {
			want = count
		} else if count != want {
			t.Fatalf("iter %d: got %d visits, want %d (matches iter 0) — termination race", iter, count, want)
		}
	}
}

func TestHiddenExcludedByDefault(t *testing.T) {
	spec := map[string]string{
		"visible.txt":      "v",
		".hidden.txt":      "h",
		".hiddendir/x.txt": "x",
	}
	root := buildTree(t, spec)

	check := func(hidden bool) map[string]bool {
		got := map[string]bool{}
		var mu sync.Mutex
		err := Walk([]string{root}, Options{NoIgnore: true, Hidden: hidden}, func(e *Entry) WalkState {
			if e.Type == TypeFile {
				base := clone(filepath.Base(e.Path))
				mu.Lock()
				got[base] = true
				mu.Unlock()
			}
			return Continue
		})
		if err != nil {
			t.Fatalf("Walk: %v", err)
		}
		return got
	}

	def := check(false)
	if def["visible.txt"] != true || def[".hidden.txt"] || def["x.txt"] {
		t.Errorf("default (Hidden=false) result wrong: %v", def)
	}
	withHidden := check(true)
	if !withHidden["visible.txt"] || !withHidden[".hidden.txt"] || !withHidden["x.txt"] {
		t.Errorf("Hidden=true result wrong: %v", withHidden)
	}
}

func TestMaxFileSize(t *testing.T) {
	root := buildTree(t, map[string]string{
		"small.txt": "12345",
		"big.txt":   "1234567890",
	})

	got := map[string]bool{}
	var mu sync.Mutex
	err := Walk([]string{root}, Options{NoIgnore: true, MaxFileSize: 6}, func(e *Entry) WalkState {
		if e.Type == TypeFile {
			base := clone(filepath.Base(e.Path))
			mu.Lock()
			got[base] = true
			mu.Unlock()
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !got["small.txt"] {
		t.Errorf("small.txt (5 bytes) should pass MaxFileSize=6")
	}
	if got["big.txt"] {
		t.Errorf("big.txt (10 bytes) should be skipped by MaxFileSize=6")
	}
}

func TestSingleFileRoot(t *testing.T) {
	root := buildTree(t, map[string]string{"only.txt": "x"})
	filePath := filepath.Join(root, "only.txt")

	var got []Entry
	err := Walk([]string{filePath}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		got = append(got, *e)
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(got) != 1 || got[0].Path != filePath || got[0].Type != TypeFile {
		t.Errorf("single-file root: got %+v, want one TypeFile entry for %s", got, filePath)
	}
}

// TestDotRootPreservesPrefix is a regression test for an M2 integration
// finding: `gg -n pat .` used to print paths like "crates/foo.rs" while
// the real rg binary prints "./crates/foo.rs" for the exact same
// invocation (root="." is gg's default and rg's most common invocation
// shape, so this divergence would have shown up in essentially every
// bare `gg pattern` run against the current directory). joinPath used to
// special-case away a "." dir component the same way filepath.Join does;
// real rg does not. See joinPath's doc for the verified rg comparison.
func TestDotRootPreservesPrefix(t *testing.T) {
	root := buildTree(t, map[string]string{"sub/file.txt": "x"})

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	var got []string
	err = Walk([]string{"."}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		if e.Type == TypeFile {
			got = append(got, clone(e.Path))
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	want := []string{"./sub/file.txt"}
	if !slicesEqualStrings(got, want) {
		t.Errorf("got %v, want %v (rg preserves the \"./\" prefix from a \".\" root)", got, want)
	}
}

// TestEmptyRootProducesUnprefixedPaths is the companion to
// TestDotRootPreservesPrefix, covering the other real-rg-verified half
// of the same distinction: a caller with no PATH argument at all must
// pass "" (not ".") as the root, or every discovered path wrongly gets a
// "./" prefix real rg's own default-directory invocation doesn't have
// (verified directly: `rg --files` prints "sub/file.txt", while
// `rg --files .` prints "./sub/file.txt", for the identical directory).
// See buildRootTask's doc for the "" convention this exercises.
func TestEmptyRootProducesUnprefixedPaths(t *testing.T) {
	root := buildTree(t, map[string]string{"sub/file.txt": "x"})

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	var got []string
	err = Walk([]string{""}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		if e.Type == TypeFile {
			got = append(got, clone(e.Path))
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	want := []string{"sub/file.txt"}
	if !slicesEqualStrings(got, want) {
		t.Errorf("got %v, want %v (an empty root, unlike an explicit \".\", must not add a \"./\" prefix)", got, want)
	}
}

func slicesEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestInvalidRootReportsErr(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")

	var got *Entry
	err := Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		cp := *e
		got = &cp
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got == nil || got.Err == nil {
		t.Errorf("expected an Entry with Err set for missing root, got %+v", got)
	}
}

func TestEmptyRootsIsNoop(t *testing.T) {
	called := false
	err := Walk(nil, Options{}, func(e *Entry) WalkState {
		called = true
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk(nil): %v", err)
	}
	if called {
		t.Errorf("visitor should not be called for an empty roots slice")
	}
}

func TestMultipleRootsRoundRobin(t *testing.T) {
	r1 := buildTree(t, map[string]string{"a.txt": "a"})
	r2 := buildTree(t, map[string]string{"b.txt": "b"})

	var mu sync.Mutex
	var got []string
	err := Walk([]string{r1, r2}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		if e.Type == TypeFile {
			p := clone(e.Path)
			mu.Lock()
			got = append(got, p)
			mu.Unlock()
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	sort.Strings(got)
	want := []string{filepath.Join(r1, "a.txt"), filepath.Join(r2, "b.txt")}
	sort.Strings(want)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}
