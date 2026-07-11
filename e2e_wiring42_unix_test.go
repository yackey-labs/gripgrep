//go:build e2e && unix

// --one-file-system e2e (Unix only -- needs a real cross-device mount
// boundary, and device-number comparison via syscall.Stat_t). See
// e2e_wiring42_test.go for the shared L/M coverage.
package gripgrep_test

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"
)

// sameDevice reports whether two FileInfos live on the same file system.
func sameDevice(a, b os.FileInfo) bool {
	sa, oka := a.Sys().(*syscall.Stat_t)
	sb, okb := b.Sys().(*syscall.Stat_t)
	if !oka || !okb {
		return true // can't tell -> treat as same, so the test skips
	}
	return uint64(sa.Dev) == uint64(sb.Dev)
}

// sortLines returns out with its newline-separated lines sorted, for
// comparing walks over trees whose readdir order isn't ours to freeze.
func sortLines(out []byte) []byte {
	lines := bytes.Split(bytes.TrimRight(out, "\n"), []byte("\n"))
	sort.Slice(lines, func(i, j int) bool { return bytes.Compare(lines[i], lines[j]) < 0 })
	return bytes.Join(lines, []byte("\n"))
}

// cmpSorted is cmpIgnore with stdout compared order-insensitively.
func cmpSorted(t *testing.T, ggBin, cwd string, env ignoreEnv, args []string) {
	t.Helper()
	rgOut, rgErr, rgCode := runIgnore(t, "rg", cwd, env, args)
	ggOut, ggErr, ggCode := runIgnore(t, ggBin, cwd, env, args)
	if rgCode != ggCode {
		t.Errorf("exit code mismatch (args %v): rg=%d gg=%d", args, rgCode, ggCode)
	}
	if !bytes.Equal(sortLines(rgOut), sortLines(ggOut)) {
		t.Errorf("stdout (sorted) mismatch (args %v):\nrg lines=%d gg lines=%d",
			args, bytes.Count(rgOut, []byte("\n")), bytes.Count(ggOut, []byte("\n")))
	}
	if (len(rgErr) > 0) != (len(ggErr) > 0) {
		t.Errorf("stderr presence mismatch (args %v): rg=%q gg=%q", args, rgErr, ggErr)
	}
}

// TestGoldenOneFileSystem exercises --one-file-system against a real mount
// boundary: /sys/fs contains cgroup2/pstore/bpf submounts on a different
// device than sysfs itself. If no such boundary exists (device numbers
// equal, or /sys/fs absent), the test skips. GitHub's ubuntu runners have
// cgroup2 mounted, so CI exercises this.
func TestGoldenOneFileSystem(t *testing.T) {
	const outer, inner = "/sys/fs", "/sys/fs/cgroup"
	outerInfo, err1 := os.Stat(outer)
	innerInfo, err2 := os.Stat(inner)
	if err1 != nil || err2 != nil || !outerInfo.IsDir() || !innerInfo.IsDir() {
		t.Skip("no /sys/fs + /sys/fs/cgroup mount pair on this host")
	}
	if sameDevice(outerInfo, innerInfo) {
		t.Skip("/sys/fs and /sys/fs/cgroup share a device -- no boundary to test")
	}

	ggBin := buildGG(t, e2eRoot(t))
	env := newIgnoreEnv(t)

	// The boundary prune itself: --files /sys/fs with --one-file-system keeps
	// only sysfs-native files, dropping the cross-device submounts.
	cmpSorted(t, ggBin, "/", env, []string{"--files", "-j1", "--one-file-system", outer})
	// Per-root: an explicit root that IS a submount is searched fully.
	cmpSorted(t, ggBin, "/", env, []string{"--files", "-j1", "--one-file-system", inner})
	// Negation restores the full cross-device walk.
	cmpSorted(t, ggBin, "/", env, []string{"--files", "-j1", "--one-file-system", "--no-one-file-system", outer})

	// Composition with -L: a symlink to a cross-device dir is pruned, leaving
	// only the same-device file. -L alone follows it (searches through).
	fixture := t.TempDir()
	writeFile(t, filepath.Join(fixture, "here.txt"), "hi\n")
	symlink(t, inner, filepath.Join(fixture, "clink"))
	cmpSorted(t, ggBin, "/", env, []string{"--files", "-j1", "-L", fixture})
	cmpSorted(t, ggBin, "/", env, []string{"--files", "-j1", "-L", "--one-file-system", fixture})
}
