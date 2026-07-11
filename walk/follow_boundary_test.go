package walk

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestFollowSymlinkLoopEmitsError verifies that following a symlink back to
// an ancestor is reported as a per-entry error (errSymlinkLoop), not
// silently skipped -- the walk-level half of the -L loop contract
// (the engine turns that error into exit 2 + a stderr line). The non-loop
// file must still be visited.
func TestFollowSymlinkLoopEmitsError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "f.txt"), "x")
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a/b/back -> a: following it revisits the ancestor a (a loop).
	if err := os.Symlink("../../a", filepath.Join(root, "a", "b", "back")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	var mu sync.Mutex
	var loopErrs int
	var sawFile bool
	err := Walk([]string{root}, Options{NoIgnoreDot: true, NoIgnoreVcs: true, FollowSymlinks: true}, func(e *Entry) WalkState {
		mu.Lock()
		defer mu.Unlock()
		if e.Err != nil {
			if errors.Is(e.Err, errSymlinkLoop) {
				loopErrs++
			}
			return Continue
		}
		if e.Type == TypeFile && filepath.Base(e.Path) == "f.txt" {
			sawFile = true
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if loopErrs != 1 {
		t.Errorf("errSymlinkLoop count = %d, want exactly 1", loopErrs)
	}
	if !sawFile {
		t.Error("f.txt outside the loop was not visited")
	}
}

// TestOneFileSystemSameDeviceKeepsAll is the negative control: with every
// directory on ONE device, --one-file-system prunes nothing, so the full
// tree is still visited. It also proves the flag doesn't over-prune (a
// same-device subdir must survive).
func TestOneFileSystemSameDeviceKeepsAll(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "top.txt"), "x")
	writeFile(t, filepath.Join(root, "sub", "deep.txt"), "x")
	writeFile(t, filepath.Join(root, "sub", "nested", "leaf.txt"), "x")

	opts := Options{NoIgnoreDot: true, NoIgnoreVcs: true, OneFileSystem: true}
	got := visitFiles(t, root, opts)
	want := []string{"deep.txt", "leaf.txt", "top.txt"}
	if len(got) != len(want) {
		t.Fatalf("visited %v, want %v (one-file-system must not prune same-device dirs)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visited %v, want %v", got, want)
		}
	}
}

// TestOneFileSystemPrunesCrossDevice exercises the real prune against a
// mount boundary: /sys/fs holds cross-device submounts (cgroup2/pstore/bpf
// on Linux). With the flag on, those submounts' files must disappear while
// the sysfs-native files remain. Skips when no boundary exists.
func TestOneFileSystemPrunesCrossDevice(t *testing.T) {
	const outer, inner = "/sys/fs", "/sys/fs/cgroup"
	oi, err1 := os.Stat(outer)
	ii, err2 := os.Stat(inner)
	if err1 != nil || err2 != nil || !oi.IsDir() || !ii.IsDir() {
		t.Skip("no /sys/fs + /sys/fs/cgroup mount pair on this host")
	}
	od, _, ok1 := devIno(oi)
	id, _, ok2 := devIno(ii)
	if !ok1 || !ok2 || od == id {
		t.Skip("/sys/fs and /sys/fs/cgroup share a device -- no boundary")
	}

	opts := Options{NoIgnoreDot: true, NoIgnoreVcs: true, Hidden: true, Threads: 1}
	full := countFiles(t, outer, opts)
	opts.OneFileSystem = true
	pruned := countFiles(t, outer, opts)
	if pruned >= full {
		t.Errorf("one-file-system did not prune: full=%d pruned=%d", full, pruned)
	}
	if pruned == 0 {
		t.Errorf("one-file-system pruned everything (want sysfs-native files kept): pruned=%d", pruned)
	}
	// Per-root: the submount searched directly (its own root) is unpruned.
	if got := countFiles(t, inner, opts); got == 0 {
		t.Error("explicit cross-device root was pruned; per-root device must apply")
	}
}

func countFiles(t *testing.T, root string, opts Options) int {
	t.Helper()
	var n int64
	err := Walk([]string{root}, opts, func(e *Entry) WalkState {
		if e.Type == TypeFile {
			atomic.AddInt64(&n, 1)
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return int(n)
}
