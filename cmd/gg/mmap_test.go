package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMmapEligible covers mmapEligible's policy directly (M3 mmap
// wiring): it must mirror rg's own once-per-invocation MmapChoice
// construction (crates/core/flags/hiargs.rs) exactly -- see mmap.go's
// doc comment for the verified-against-source rule this codifies.
func TestMmapEligible(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "a.txt")
	file2 := filepath.Join(dir, "b.txt")
	subdir := filepath.Join(dir, "sub")
	if err := os.WriteFile(file1, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	var manyFiles []string
	for i := 0; i < 11; i++ {
		p := filepath.Join(dir, "many", string(rune('a'+i))+".txt")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		manyFiles = append(manyFiles, p)
	}

	cases := []struct {
		name  string
		mode  MmapMode
		paths []string
		want  bool
	}{
		{"never overrides everything", MmapNever, []string{file1}, false},
		{"never overrides even a single file", MmapNever, []string{file1}, false},
		{"always overrides a directory", MmapAlways, []string{subdir}, true},
		{"always overrides more than 10 paths", MmapAlways, manyFiles, true},
		{"auto: single regular file", MmapAuto, []string{file1}, true},
		{"auto: several regular files under the limit", MmapAuto, []string{file1, file2}, true},
		{"auto: a directory disables it entirely", MmapAuto, []string{subdir}, false},
		{"auto: one directory among files disables all of them", MmapAuto, []string{file1, subdir, file2}, false},
		{"auto: more than 10 paths disables it", MmapAuto, manyFiles, false},
		{"auto: a nonexistent path disables it", MmapAuto, []string{filepath.Join(dir, "missing")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mmapEligible(c.mode, c.paths); got != c.want {
				t.Errorf("mmapEligible(%v, %v) = %v, want %v", c.mode, c.paths, got, c.want)
			}
		})
	}

	// Exactly 10 paths (the boundary itself) must still qualify -- only
	// exceeding it disables mmap.
	t.Run("auto: exactly 10 paths qualifies", func(t *testing.T) {
		if got := mmapEligible(MmapAuto, manyFiles[:10]); !got {
			t.Errorf("mmapEligible(Auto, 10 paths) = false, want true")
		}
	})
}

// TestMmapOpen covers mmapOpen's success and fallback (ok=false) paths
// directly.
func TestMmapOpen(t *testing.T) {
	dir := t.TempDir()

	t.Run("regular file maps successfully", func(t *testing.T) {
		path := filepath.Join(dir, "content.txt")
		want := []byte("hello, mmap\n")
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		mf, ok := mmapOpen(path)
		if !ok {
			t.Fatal("mmapOpen = false, want true")
		}
		defer mf.Close()
		if string(mf.data) != string(want) {
			t.Errorf("mmapOpen data = %q, want %q", mf.data, want)
		}
	})

	t.Run("nonexistent path falls back", func(t *testing.T) {
		if _, ok := mmapOpen(filepath.Join(dir, "missing")); ok {
			t.Error("mmapOpen(missing) = true, want false")
		}
	})

	t.Run("zero-length file falls back", func(t *testing.T) {
		path := filepath.Join(dir, "empty.txt")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := mmapOpen(path); ok {
			t.Error("mmapOpen(empty file) = true, want false (streaming path handles this instead)")
		}
	})
}

func TestStripUTF8BOMSlice(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"no BOM", []byte("hello"), []byte("hello")},
		{"BOM present", append(append([]byte{}, utf8BOM[:]...), "hello"...), []byte("hello")},
		{"BOM only", utf8BOM[:], nil},
		{"shorter than BOM", []byte{0xEF}, []byte{0xEF}},
		{"empty", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripUTF8BOMSlice(c.in)
			if string(got) != string(c.want) {
				t.Errorf("stripUTF8BOMSlice(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
