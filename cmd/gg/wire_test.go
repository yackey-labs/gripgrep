package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRunEmptyPatternFileIsNoMatchNotError covers execute's handling of
// a -f PATTERNFILE that resolves to zero total patterns: verified against
// the real rg binary (`rg -f empty.txt file` exits 1 with no output, no
// error message), this must NOT hit match.New's "at least one pattern"
// error path.
func TestRunEmptyPatternFileIsNoMatchNotError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyPats := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(emptyPats, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-f", emptyPats, dir}, nil, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (no match, not an error); stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestResolvePatternFiles exercises wire.go's -f/--file I/O directly
// (ParseArgs never touches the filesystem -- see Config.Patterns's doc),
// covering every behavior verified against the real rg 15.1.0 binary in
// the round-31 differential sweep.
func TestResolvePatternFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("one pattern per line", func(t *testing.T) {
		p := write("pats.txt", "apple\nbanana\n")
		cfg := &Config{PatternFiles: []string{p}}
		if err := resolvePatternFiles(cfg, nil); err != nil {
			t.Fatalf("resolvePatternFiles: %v", err)
		}
		if got, want := cfg.Patterns, []string{"apple", "banana"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Patterns = %v, want %v", got, want)
		}
	})

	t.Run("empty line becomes an empty pattern, not skipped", func(t *testing.T) {
		p := write("pats_blank.txt", "apple\n\nbanana\n")
		cfg := &Config{PatternFiles: []string{p}}
		if err := resolvePatternFiles(cfg, nil); err != nil {
			t.Fatalf("resolvePatternFiles: %v", err)
		}
		if got, want := cfg.Patterns, []string{"apple", "", "banana"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Patterns = %v, want %v", got, want)
		}
	})

	t.Run("CRLF line endings are stripped", func(t *testing.T) {
		p := write("pats_crlf.txt", "apple\r\nbanana\r\n")
		cfg := &Config{PatternFiles: []string{p}}
		if err := resolvePatternFiles(cfg, nil); err != nil {
			t.Fatalf("resolvePatternFiles: %v", err)
		}
		if got, want := cfg.Patterns, []string{"apple", "banana"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Patterns = %v, want %v (no stray \\r)", got, want)
		}
	})

	t.Run("empty pattern file contributes zero patterns, no error", func(t *testing.T) {
		p := write("empty.txt", "")
		cfg := &Config{PatternFiles: []string{p}}
		if err := resolvePatternFiles(cfg, nil); err != nil {
			t.Fatalf("resolvePatternFiles: unexpected error: %v", err)
		}
		if len(cfg.Patterns) != 0 {
			t.Errorf("Patterns = %v, want empty", cfg.Patterns)
		}
	})

	t.Run("nonexistent file is an error", func(t *testing.T) {
		cfg := &Config{PatternFiles: []string{filepath.Join(dir, "does-not-exist.txt")}}
		if err := resolvePatternFiles(cfg, nil); err == nil {
			t.Fatal("resolvePatternFiles: expected an error, got none")
		}
	})

	t.Run("multiple -f files concatenate in order", func(t *testing.T) {
		a := write("a.txt", "alpha\n")
		b := write("b.txt", "beta\n")
		cfg := &Config{PatternFiles: []string{a, b}}
		if err := resolvePatternFiles(cfg, nil); err != nil {
			t.Fatalf("resolvePatternFiles: %v", err)
		}
		if got, want := cfg.Patterns, []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Patterns = %v, want %v", got, want)
		}
	})

	t.Run("-f combines with pre-existing -e patterns", func(t *testing.T) {
		p := write("pats2.txt", "gamma\n")
		cfg := &Config{PatternFiles: []string{p}, Patterns: []string{"alpha"}}
		if err := resolvePatternFiles(cfg, nil); err != nil {
			t.Fatalf("resolvePatternFiles: %v", err)
		}
		if got, want := cfg.Patterns, []string{"alpha", "gamma"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Patterns = %v, want %v", got, want)
		}
	})

	t.Run("-f - reads from stdin", func(t *testing.T) {
		cfg := &Config{PatternFiles: []string{"-"}}
		if err := resolvePatternFiles(cfg, strings.NewReader("delta\nepsilon\n")); err != nil {
			t.Fatalf("resolvePatternFiles: %v", err)
		}
		if got, want := cfg.Patterns, []string{"delta", "epsilon"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Patterns = %v, want %v", got, want)
		}
	})

	t.Run("no trailing newline still reads the last line", func(t *testing.T) {
		p := write("no_trailing_nl.txt", "apple\nbanana")
		cfg := &Config{PatternFiles: []string{p}}
		if err := resolvePatternFiles(cfg, nil); err != nil {
			t.Fatalf("resolvePatternFiles: %v", err)
		}
		if got, want := cfg.Patterns, []string{"apple", "banana"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Patterns = %v, want %v", got, want)
		}
	})
}
