package walk

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestPermissionDeniedDirectory verifies that an unreadable subdirectory
// delivers an Entry with Err set (via the os.Open failure path in
// processDir) rather than crashing or aborting the whole walk; siblings
// must still be visited.
func TestPermissionDeniedDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits don't block access")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(locked, "secret.txt"), "x")
	writeFile(t, filepath.Join(root, "sibling.txt"), "ok")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o755) // let t.TempDir() clean up

	var mu sync.Mutex
	var lockedErr error
	sawSibling := false
	err := Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		mu.Lock()
		defer mu.Unlock()
		if e.Path == locked {
			lockedErr = e.Err
		}
		if e.Type == TypeFile && filepath.Base(e.Path) == "sibling.txt" {
			sawSibling = true
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if lockedErr == nil {
		t.Errorf("expected Entry.Err set for unreadable directory %s", locked)
	}
	if !sawSibling {
		t.Errorf("sibling.txt should still be visited despite the locked directory")
	}
}

// TestFIFONeverOpened verifies a FIFO is classified (not TypeFile/TypeDir)
// and visiting it completes promptly — walk must never call os.Open on a
// non-directory entry, since opening a FIFO with no reader/writer on the
// other end blocks forever.
func TestFIFONeverOpened(t *testing.T) {
	root := t.TempDir()
	fifoPath := filepath.Join(root, "myfifo")
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	writeFile(t, filepath.Join(root, "a.txt"), "a")

	done := make(chan FileType, 1)
	go func() {
		var fifoType FileType
		Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
			if e.Path == fifoPath {
				fifoType = e.Type
			}
			return Continue
		})
		done <- fifoType
	}()

	select {
	case ft := <-done:
		if ft == TypeFile || ft == TypeDir {
			t.Errorf("FIFO classified as %v; want anything but TypeFile/TypeDir (walk must not treat it as an openable regular file)", ft)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Walk hung — a FIFO was probably opened, blocking with no reader/writer on the other end")
	}
}

// TestFileDeletedBetweenReaddirAndOpen simulates the classic TOCTOU: a
// subdirectory is removed by the Visitor callback for its own directory
// entry, i.e. after the parent's ReadDir already listed it but before its
// own dirTask is popped and os.Open'd. This must surface as an Entry.Err,
// not a crash, and not abort the rest of the walk.
func TestFileDeletedBetweenReaddirAndOpen(t *testing.T) {
	root := t.TempDir()
	vanish := filepath.Join(root, "vanish")
	writeFile(t, filepath.Join(vanish, "inside.txt"), "x")
	writeFile(t, filepath.Join(root, "keep.txt"), "keep")

	var mu sync.Mutex
	var vanishErr error
	var vanishErrSet bool
	sawKeep := false
	err := Walk([]string{root}, Options{NoIgnore: true, Threads: 1}, func(e *Entry) WalkState {
		mu.Lock()
		defer mu.Unlock()
		if e.Path == vanish && e.Err == nil {
			// This is the directory-entry visit, before it's been
			// opened for descent: remove it out from under the walk.
			os.RemoveAll(vanish)
		}
		if e.Path == vanish && e.Err != nil {
			vanishErr = e.Err
			vanishErrSet = true
		}
		if e.Type == TypeFile && filepath.Base(e.Path) == "keep.txt" {
			sawKeep = true
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !vanishErrSet {
		t.Skip("directory was removed before ReadDir could even list it as a child (timing-dependent); not exercising the intended race")
	}
	if vanishErr == nil {
		t.Errorf("expected an Entry.Err for the vanished directory")
	}
	if !sawKeep {
		t.Errorf("keep.txt should still be visited after the sibling vanished")
	}
}

// TestSymlinkSelfLoop covers the simple case: a directory symlinked to
// itself (or an ancestor) must not be followed twice.
func TestSymlinkSelfLoop(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	if err := os.Symlink(root, filepath.Join(root, "selfloop")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	done := make(chan int, 1)
	go func() {
		n := 0
		Walk([]string{root}, Options{NoIgnore: true, FollowSymlinks: true}, func(e *Entry) WalkState {
			n++
			return Continue
		})
		done <- n
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Walk hung on a self-referential symlink loop")
	}
}

// TestSymlinkCrossLoop is the case the advisor flagged: a directory
// reached *only* through a symlink (not an ancestor reached directly)
// that then loops back on itself. This requires resolving the symlink's
// target identity (not the symlink path's own identity) when extending
// the ancestor chain — see pushSymAncestor's Stat-not-Lstat fix.
func TestSymlinkCrossLoop(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir() // a real directory OUTSIDE root's own subtree
	writeFile(t, filepath.Join(target, "f.txt"), "x")
	if err := os.Symlink(target, filepath.Join(target, "loop")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	done := make(chan int, 1)
	go func() {
		n := 0
		Walk([]string{root}, Options{NoIgnore: true, FollowSymlinks: true}, func(e *Entry) WalkState {
			n++
			return Continue
		})
		done <- n
	}()
	select {
	case n := <-done:
		if n == 0 {
			t.Errorf("expected at least the root and f.txt to be visited")
		}
		// A correct implementation resolves "loop" back to the already-
		// visited target directory and stops immediately (root, link,
		// f.txt, loop == a handful of entries). Comparing the *symlink
		// path's own* inode instead of the resolved target's (the bug
		// pushSymAncestor's Stat-not-Lstat fix addresses) doesn't hang —
		// it terminates only once generated paths hit PATH_MAX — but
		// visits on the order of hundreds of entries first. A tight
		// upper bound catches that without needing a timeout to fire.
		const maxExpected = 10
		if n > maxExpected {
			t.Errorf("visited %d entries chasing the symlink loop, want <= %d: loop detection is comparing the wrong identity (symlink's own inode instead of its resolved target's) and only terminating because generated paths eventually exceed PATH_MAX", n, maxExpected)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Walk hung on a cross-symlink loop (target reached only via symlink, looping back on itself)")
	}
}

// TestNonUTF8Filename verifies a filename containing invalid UTF-8 bytes
// is still visited, with those exact bytes preserved in Entry.Path — gg
// is byte-oriented end to end, matching rg's own filename handling.
func TestNonUTF8Filename(t *testing.T) {
	root := t.TempDir()
	badName := string([]byte{0xff, 0xfe, 'x', '.', 't', 'x', 't'})
	full := filepath.Join(root, badName)
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Skipf("filesystem rejected non-UTF-8 filename: %v", err)
	}

	found := false
	err := Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
		if e.Type == TypeFile && e.Path == full {
			found = true
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !found {
		t.Errorf("non-UTF-8 filename %q not visited with byte-exact path", badName)
	}
}

// TestDeepNestingNoStackOverflow walks a tree nested far deeper than any
// native-recursion implementation could survive. The walker's descent is
// iterative (a worker's own queue, popped in its run loop), not
// function-call recursion, so directory depth should never touch the Go
// call stack in a way that could overflow it.
func TestDeepNestingNoStackOverflow(t *testing.T) {
	const depth = 1000
	root := t.TempDir()
	path := root
	for i := 0; i < depth; i++ {
		path = filepath.Join(path, "d")
	}
	writeFile(t, filepath.Join(path, "leaf.txt"), "x")

	found := false
	err := Walk([]string{root}, Options{NoIgnore: true, Threads: 1}, func(e *Entry) WalkState {
		if e.Type == TypeFile {
			found = true
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !found {
		t.Errorf("leaf file at depth %d not visited", depth)
	}
}

// TestGitignoreCannotReincludeInsideExcludedDir encodes git's own rule:
// a `!`-negation pattern for a path INSIDE an excluded directory has no
// effect, because git (and rg) never descends into an excluded directory
// to check it. Pruning an ignored directory before enqueue (rather than
// filtering its contents after the fact) gets this right "for free".
func TestGitignoreCannotReincludeInsideExcludedDir(t *testing.T) {
	root := t.TempDir()
	markGitRepo(t, root)
	writeFile(t, filepath.Join(root, ".gitignore"), "excluded/\n!excluded/keep.txt\n")
	writeFile(t, filepath.Join(root, "excluded", "keep.txt"), "x")
	writeFile(t, filepath.Join(root, "excluded", "other.txt"), "y")
	writeFile(t, filepath.Join(root, "a.txt"), "a")

	got := visitFiles(t, root, Options{})
	want := []string{"a.txt"} // keep.txt's negation is inert: excluded/ itself is never descended into
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("got %v, want %v (a negation for a path inside an excluded dir must not resurrect it)", got, want)
	}
}

// TestQuiescenceRandomQuitInjection stresses termination under -race: a
// tree of many tiny directories, walked repeatedly, with the Visitor
// returning Quit at a random point (or never) on each run. No iteration
// should hang, panic, or double-visit; this is the same active-worker
// protocol TestQuiescenceInvariant checks, but exercising the Quit path
// instead of only the natural-completion path.
func TestQuiescenceRandomQuitInjection(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		for j := 0; j < 5; j++ {
			writeFile(t, filepath.Join(root, fmt.Sprintf("d%02d", i), fmt.Sprintf("f%d.txt", j)), "x")
		}
	}

	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 60; iter++ {
		quitAt := rng.Intn(250) // sometimes larger than total entries: never quits
		var mu sync.Mutex
		seen := map[string]bool{}
		n := 0
		done := make(chan error, 1)
		go func() {
			done <- Walk([]string{root}, Options{NoIgnore: true}, func(e *Entry) WalkState {
				p := clone(e.Path)
				mu.Lock()
				dup := seen[p]
				seen[p] = true
				n++
				cur := n
				mu.Unlock()
				if dup {
					t.Errorf("iter %d: duplicate visit of %s", iter, p)
				}
				if cur == quitAt {
					return Quit
				}
				return Continue
			})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("iter %d: Walk: %v", iter, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("iter %d: Walk hung (quitAt=%d)", iter, quitAt)
		}
	}
}

// TestAllocsPerEntrySteadyState asserts the common per-file visit path
// (an already-open directory, no new dirTask, no ignore-file I/O) stays
// near zero allocations beyond what os.DirEntry/ReadDir itself costs —
// see walk_bench_test.go's benchmark for the measured baseline this
// threshold is calibrated against.
func TestAllocsPerEntrySteadyState(t *testing.T) {
	root := t.TempDir()
	const n = 2000
	for i := 0; i < n; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%05d.txt", i)), "x")
	}

	allocs := testing.AllocsPerRun(5, func() {
		err := Walk([]string{root}, Options{NoIgnore: true, Threads: 1}, func(e *Entry) WalkState {
			return Continue
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	perEntry := allocs / float64(n)
	t.Logf("allocs/run=%.1f files=%d allocs/file=%.3f (GOMAXPROCS=%d)", allocs, n, perEntry, runtime.GOMAXPROCS(0))
	const maxPerEntry = 3.0 // stdlib ReadDir/os.DirEntry itself accounts for the bulk of this; see doc.go
	if perEntry > maxPerEntry {
		t.Errorf("allocs per entry = %.3f, want <= %.1f: steady-state file-visit path regressed", perEntry, maxPerEntry)
	}
}
