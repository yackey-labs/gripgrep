//go:build e2e

// This file is only compiled with -tags e2e; it drives both gg and rg
// over testdata/corpus and diffs their output. Every case is t.Skip'd
// until M2 wires cmd/gg's real flag matrix — see PLAN.md's "Definition
// of done (v1)": byte-identical output to rg on this golden matrix.
package gripgrep_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGoldenVsRipgrep(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Dir(thisFile)
	corpus := filepath.Join(root, "testdata", "corpus")

	ggBin := buildGG(t, root)

	cases := []struct {
		name string
		args []string
	}{
		{"literal", []string{"-n", "hello", corpus}},
		{"case_insensitive", []string{"-n", "-i", "HELLO", corpus}},
		{"regex_alternation", []string{"-n", "hello|needle", corpus}},
		{"word_boundary", []string{"-n", "-w", "cat", corpus}},
		{"invert_match", []string{"-n", "-v", "hello", corpus}},
		{"count_mode", []string{"-c", "hello", corpus}},
		{"files_with_matches", []string{"-l", "hello", corpus}},
		{"hidden_excluded_by_default", []string{"-n", "secret", corpus}},
		{"hidden_included", []string{"-n", "--hidden", "secret", corpus}},
		{"gitignore_respected", []string{"-n", "secret", corpus}},
		{"no_ignore", []string{"-n", "--no-ignore", "secret", corpus}},
		{"context", []string{"-n", "-C", "1", "hello", corpus}},
		{"binary_quit_by_default", []string{"-n", "needle", filepath.Join(corpus, "binary.bin")}},
		{"binary_text_mode", []string{"-n", "-a", "needle", filepath.Join(corpus, "binary.bin")}},
		{"unicode_content", []string{"-n", "Привет", corpus}},
		{"long_line_over_64kb", []string{"-n", "needle", filepath.Join(corpus, "longline.txt")}},
		{"crlf_line_endings", []string{"-n", "needle", filepath.Join(corpus, "crlf.txt")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Skip("TODO(M2): enable once cmd/gg implements the v1 flag matrix")

			rgOut, rgCode := run(t, "rg", tc.args)
			ggOut, ggCode := run(t, ggBin, tc.args)

			if rgCode != ggCode {
				t.Errorf("exit code mismatch: rg=%d gg=%d", rgCode, ggCode)
			}
			if !bytes.Equal(rgOut, ggOut) {
				t.Errorf("output mismatch:\n--- rg ---\n%s\n--- gg ---\n%s", rgOut, ggOut)
			}
		})
	}
}

func buildGG(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gg")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/gg")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building gg: %v\n%s", err, out)
	}
	return bin
}

func run(t *testing.T, bin string, args []string) ([]byte, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running %s: %v", bin, err)
	}
	return out.Bytes(), code
}
