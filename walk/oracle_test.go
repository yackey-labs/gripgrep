package walk

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// TestOracleRgFiles compares gg's walker against `rg --files` (sorted,
// since both tools complete in nondeterministic parallel order) on two
// real trees: this repo's own testdata/corpus, and the ripgrep checkout
// itself (a much larger, real-world gitignored tree with nested
// .gitignore files, a .git directory, etc). Skips gracefully if rg isn't
// on PATH.
func TestOracleRgFiles(t *testing.T) {
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("rg not found on PATH, skipping oracle test")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	walkDir := filepath.Dir(thisFile)
	repoRoot := filepath.Dir(walkDir)
	corpus := filepath.Join(repoRoot, "testdata", "corpus")
	ripgrepCheckout := filepath.Join(filepath.Dir(repoRoot), "ripgrep")

	dirs := []struct {
		name string
		path string
	}{
		{"testdata_corpus", corpus},
		{"ripgrep_checkout", ripgrepCheckout},
	}

	for _, d := range dirs {
		t.Run(d.name, func(t *testing.T) {
			if _, err := os.Stat(d.path); err != nil {
				t.Skipf("%s not present, skipping: %v", d.path, err)
			}
			rgFiles := runRgFiles(t, rgPath, d.path)
			ggFiles := runGgFiles(t, d.path)

			match, onlyRg, onlyGg := diffSets(rgFiles, ggFiles)
			t.Logf("%s: rg=%d gg=%d match=%d only-rg=%d only-gg=%d", d.name, len(rgFiles), len(ggFiles), match, len(onlyRg), len(onlyGg))

			const showLimit = 20
			if len(onlyRg) > 0 {
				t.Errorf("%d files rg found but gg missed (showing up to %d): %v", len(onlyRg), showLimit, capList(onlyRg, showLimit))
			}
			if len(onlyGg) > 0 {
				t.Errorf("%d files gg found but rg missed (showing up to %d): %v", len(onlyGg), showLimit, capList(onlyGg, showLimit))
			}
		})
	}
}

func runRgFiles(t *testing.T, rgPath, dir string) []string {
	t.Helper()
	cmd := exec.Command(rgPath, "--files", dir)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("rg --files %s: %v\nstderr: %s", dir, err, ee.Stderr)
		}
		t.Fatalf("rg --files %s: %v", dir, err)
	}
	return sortedNonEmptyLines(string(out))
}

func runGgFiles(t *testing.T, dir string) []string {
	t.Helper()
	var mu sync.Mutex
	var files []string
	err := Walk([]string{dir}, Options{}, func(e *Entry) WalkState {
		if e.Err != nil {
			return Continue
		}
		if e.Type == TypeFile {
			mu.Lock()
			files = append(files, clone(e.Path))
			mu.Unlock()
		}
		return Continue
	})
	if err != nil {
		t.Fatalf("Walk(%s): %v", dir, err)
	}
	sort.Strings(files)
	return files
}

func sortedNonEmptyLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	sort.Strings(lines)
	return lines
}

// diffSets returns the count of lines present in both sorted inputs, and
// the (sorted) lines unique to each side.
func diffSets(a, b []string) (match int, onlyA, onlyB []string) {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			match++
			i++
			j++
		case a[i] < b[j]:
			onlyA = append(onlyA, a[i])
			i++
		default:
			onlyB = append(onlyB, b[j])
			j++
		}
	}
	onlyA = append(onlyA, a[i:]...)
	onlyB = append(onlyB, b[j:]...)
	return match, onlyA, onlyB
}

func capList(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
