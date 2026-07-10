package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenRawReadsMatchOsOpen is a differential test against os.Open/
// io.ReadAll: openRaw exists purely as a lower-overhead substitute for
// os.Open on the per-file search path (see rawfile.go's doc comment for
// why -- os.newFile's runtime-poller registration was ~30% of total CPU
// time profiling gg against real rg on the linux kernel tree), so its
// Read/Close behavior must be indistinguishable from the os.File it
// replaces for every content shape callers actually feed it.
func TestOpenRawReadsMatchOsOpen(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"small", []byte("hello\n")},
		{"exactly-one-buffer", bytes.Repeat([]byte("x"), 4096)},
		{"multi-buffer", bytes.Repeat([]byte("abcdefgh"), 100000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "f")
			if err := os.WriteFile(path, c.data, 0o644); err != nil {
				t.Fatal(err)
			}

			f, err := openRaw(path)
			if err != nil {
				t.Fatalf("openRaw: %v", err)
			}
			got, err := io.ReadAll(f)
			if err != nil {
				t.Fatalf("ReadAll(openRaw): %v", err)
			}
			if err := f.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
			if !bytes.Equal(got, c.data) {
				t.Errorf("openRaw content = %q, want %q", got, c.data)
			}

			// Cross-check against the os.Open/io.ReadAll path it replaces.
			of, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			want, err := io.ReadAll(of)
			of.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("openRaw content diverges from os.Open: got %q, want %q", got, want)
			}
		})
	}
}

// TestOpenRawMissingFile checks that a nonexistent path produces an
// error shaped like os.Open's (*fs.PathError, os.IsNotExist-detectable),
// since cmd/gg's error reporting (execute's visitor) formats it as
// "gg: <path>: <err>" and existing behavior/tests assume that shape.
func TestOpenRawMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	_, err := openRaw(path)
	if err == nil {
		t.Fatal("openRaw(missing file) = nil error, want an error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("openRaw(missing file) error = %v, want fs.ErrNotExist", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != path {
		t.Errorf("openRaw(missing file) error = %#v, want *fs.PathError with Path %q", err, path)
	}
}

// TestOpenRawMultipleSmallReads exercises repeated short Read calls
// (below the destination buffer's capacity), the shape stripUTF8BOM's
// 3-byte probe read uses -- io.ReadFull calling Read potentially several
// times to fill a small buffer, which stresses openRaw's io.EOF
// translation on the final zero-byte read at true EOF.
func TestOpenRawMultipleSmallReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	data := []byte("ab")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var buf [3]byte
	n, err := io.ReadFull(f, buf[:])
	if n != 2 || err != io.ErrUnexpectedEOF {
		t.Fatalf("ReadFull = (%d, %v), want (2, io.ErrUnexpectedEOF)", n, err)
	}
	if !bytes.Equal(buf[:2], data) {
		t.Errorf("read %q, want %q", buf[:2], data)
	}
}
