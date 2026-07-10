package gripgrep

import (
	"bufio"
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// buildGGForFacadeTest builds the gg CLI once per test run, mirroring
// e2e_test.go's own buildGG (a separate, e2e-tagged file this package
// can't import helpers from) -- kept deliberately minimal since this
// file's only use for the binary is parsing its -c/-l output as an
// oracle for the facade's CountMatches/FilesWithMatch (gate 6: "facade
// correctness... vs parsing the CLI's own output").
func buildGGForFacadeTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Dir(thisFile)
	bin := filepath.Join(t.TempDir(), "gg")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/gg")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building gg: %v\n%s", err, out)
	}
	return bin
}

func runGG(t *testing.T, bin, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}
	// Exit 1 (no match) is a legitimate outcome for some cases below, so
	// only a real launch failure (not an *exec.ExitError) is fatal here.
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("running gg %v: %v", args, err)
		}
	}
	return out.String()
}

// parseCLICounts parses `gg -c PATTERN PATH` output ("path:count\n" per
// line, showPath always on since every case here searches a directory)
// into the same map[string]int shape CountMatches returns.
func parseCLICounts(t *testing.T, out string) map[string]int {
	t.Helper()
	got := make(map[string]int)
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		i := strings.LastIndexByte(line, ':')
		if i < 0 {
			t.Fatalf("unparseable -c line: %q", line)
		}
		n, err := strconv.Atoi(line[i+1:])
		if err != nil {
			t.Fatalf("unparseable -c count in %q: %v", line, err)
		}
		got[line[:i]] = n
	}
	return got
}

func parseCLIPaths(out string) []string {
	var got []string
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			got = append(got, line)
		}
	}
	sort.Strings(got)
	return got
}

// TestFacadeVsCLI is round #30's gate 6: gripgrep.CountMatches and
// gripgrep.FilesWithMatch must agree exactly with the CLI's own -c/-l
// output (counts + sorted file lists identical) on both testdata/corpus
// and the full benchmark-data/linux tree -- the latter specifically
// because it contains real binary files (vmlinux, System.map,
// Module.symvers) that exercise internal/engine's rg-verified binary-
// suppression rules (see internal/engine's matchTracker doc): if the
// facade forked that logic instead of sharing it, this is where it would
// show up as a count/file-list mismatch, not as a crash.
func TestFacadeVsCLI(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Dir(thisFile)
	bin := buildGGForFacadeTest(t)

	cases := []struct {
		name    string
		pattern string
		relPath string
	}{
		{"corpus_hello", "hello", "testdata/corpus"},
		{"linux_pm_resume", "PM_RESUME", "benchmark-data/linux"},
		{"linux_pm_resume_ci", "pm_resume", "benchmark-data/linux"}, // paired with -i below
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			absPath := filepath.Join(root, tc.relPath)
			if _, err := os.Stat(absPath); err != nil {
				// benchmark-data/ is gitignored and only exists on boxes
				// provisioned for benchmarking (internal/bench/setup.sh);
				// CI runners and fresh clones don't have it.
				t.Skipf("corpus %s not present: %v", tc.relPath, err)
			}
			args := []string{"-c", tc.pattern, tc.relPath}
			opts := Options{}
			if tc.name == "linux_pm_resume_ci" {
				args = []string{"-c", "-i", tc.pattern, tc.relPath}
				opts.IgnoreCase = true
			}

			cliCountOut := runGG(t, bin, root, args...)
			wantCounts := parseCLICounts(t, cliCountOut)
			gotCounts, err := opts.CountMatches(tc.pattern, absPath)
			if err != nil {
				t.Fatalf("CountMatches: %v", err)
			}
			gotCountsRel := relKeys(t, root, gotCounts)
			if !mapsEqual(wantCounts, gotCountsRel) {
				t.Errorf("CountMatches mismatch:\nCLI:    %v\nfacade: %v", wantCounts, gotCountsRel)
			}

			lArgs := append([]string{"-l"}, args[1:]...)
			cliFilesOut := runGG(t, bin, root, lArgs...)
			wantFiles := parseCLIPaths(cliFilesOut)
			gotFiles, err := opts.FilesWithMatch(tc.pattern, absPath)
			if err != nil {
				t.Fatalf("FilesWithMatch: %v", err)
			}
			gotFilesRel := relPaths(t, root, gotFiles)
			sort.Strings(gotFilesRel)
			if !slicesEq(wantFiles, gotFilesRel) {
				t.Errorf("FilesWithMatch mismatch:\nCLI:    %v\nfacade: %v", wantFiles, gotFilesRel)
			}

			nArgs := append([]string{"-n"}, args[1:]...)
			cliMatchOut := runGG(t, bin, root, nArgs...)
			wantLines := sortedNonEmptyLines(cliMatchOut)
			gotMatches, err := opts.Search(tc.pattern, absPath)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			gotLines := formatMatchesAsGGLines(t, root, gotMatches)
			if !slicesEq(wantLines, gotLines) {
				t.Errorf("Search mismatch:\nCLI:    %v\nfacade: %v", wantLines, gotLines)
			}
		})
	}
}

// formatMatchesAsGGLines renders facade Matches in gg -n's own
// "path:lineno:line\n" shape (root-relativized to match the CLI's
// relative-path args) so they can be sort-normalized and diffed against
// real CLI output line-for-line -- the same oracle strategy e2e_test.go
// uses for the CLI itself, applied one layer up.
func formatMatchesAsGGLines(t *testing.T, root string, matches []Match) []string {
	t.Helper()
	out := make([]string, len(matches))
	for i, m := range matches {
		rel, err := filepath.Rel(root, m.Path)
		if err != nil {
			t.Fatalf("relativizing %q: %v", m.Path, err)
		}
		out[i] = rel + ":" + strconv.Itoa(m.LineNumber) + ":" + m.Line
	}
	sort.Strings(out)
	return out
}

func sortedNonEmptyLines(out string) []string {
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	return lines
}

func relKeys(t *testing.T, root string, m map[string]int) map[string]int {
	t.Helper()
	out := make(map[string]int, len(m))
	for k, v := range m {
		rel, err := filepath.Rel(root, k)
		if err != nil {
			t.Fatalf("relativizing %q: %v", k, err)
		}
		out[rel] = v
	}
	return out
}

func relPaths(t *testing.T, root string, paths []string) []string {
	t.Helper()
	out := make([]string, len(paths))
	for i, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatalf("relativizing %q: %v", p, err)
		}
		out[i] = rel
	}
	return out
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func slicesEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
