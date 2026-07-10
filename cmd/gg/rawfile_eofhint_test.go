//go:build unix

package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The short-read-implies-EOF hint (M3 #28) is a unix-rawFile mechanism:
// the Windows rawFile wraps os.File and never arms a hint (its
// disableEOFHint is a no-op), so these tests -- which prove syscall
// behavior by poking f.eofHint and closing f.fd out from under the
// reader -- are unix-only by construction.

// TestEofHintSkipsSyscall verifies M3 #28's short-read-implies-EOF hint:
// after a Read returns fewer bytes than requested, the next Read must
// return (0, io.EOF) without issuing another read(2). Proven by closing
// the raw fd out from under rawFile between the two Read calls -- if the
// second Read attempted a real syscall, it would see EBADF (wrapped as
// *fs.PathError), not io.EOF.
func TestEofHintSkipsSyscall(t *testing.T) {
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

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if n != len(data) || err != nil {
		t.Fatalf("first Read = (%d, %v), want (%d, nil)", n, err, len(data))
	}
	if !f.eofHint {
		t.Fatal("eofHint not set after a short read on a regular file")
	}

	if err := syscall.Close(f.fd); err != nil {
		t.Fatalf("Close(fd) for the no-syscall proof: %v", err)
	}

	n, err = f.Read(buf)
	if n != 0 || err != io.EOF {
		t.Fatalf("second Read = (%d, %v), want (0, io.EOF) with zero syscalls (fd was already closed)", n, err)
	}
}

// TestDisableEOFHintForcesRealRead verifies wire.go's opt-out for
// explicit CLI path arguments (see disableEOFHint's doc): after a short
// read on a disabled rawFile, the hint must never arm, so the next Read
// still issues a real read(2) -- proven the same way as
// TestEofHintNotSetOnExactBuffer, by closing the fd first and checking
// the next Read surfaces a real syscall error instead of a
// hint-shortcut io.EOF.
func TestDisableEOFHintForcesRealRead(t *testing.T) {
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
	f.disableEOFHint()

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if n != len(data) || err != nil {
		t.Fatalf("first Read = (%d, %v), want (%d, nil)", n, err, len(data))
	}
	if f.eofHint {
		t.Fatal("eofHint set despite disableEOFHint -- explicit-arg opt-out must be unconditional")
	}

	if err := syscall.Close(f.fd); err != nil {
		t.Fatalf("Close(fd) for the real-syscall proof: %v", err)
	}

	n, err = f.Read(buf)
	if err == nil {
		t.Fatalf("second Read = (%d, nil), want a real syscall error -- disableEOFHint should have forced an actual read(2) on the closed fd", n)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("second Read error = %v, want a real syscall error (*fs.PathError from the closed fd)", err)
	}
}

// TestEofHintNotSetOnExactBuffer verifies the hint's other half: a Read
// that exactly fills the destination buffer says nothing about EOF (the
// file could be an exact multiple of the buffer size), so eofHint must
// stay false and the confirm read must still happen for real. Proven the
// same way as TestEofHintSkipsSyscall, but inverted: closing the fd
// before the second Read must now produce a real error, not io.EOF.
func TestEofHintNotSetOnExactBuffer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	data := bytes.Repeat([]byte("x"), 8)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, len(data))
	n, err := f.Read(buf)
	if n != len(data) || err != nil {
		t.Fatalf("first Read = (%d, %v), want (%d, nil)", n, err, len(data))
	}
	if f.eofHint {
		t.Fatal("eofHint set after a full read that exactly filled the buffer -- must stay false")
	}

	if err := syscall.Close(f.fd); err != nil {
		t.Fatalf("Close(fd) for the real-syscall proof: %v", err)
	}

	n, err = f.Read(buf)
	if err == nil {
		t.Fatalf("second Read = (%d, nil), want an error -- the confirm read must have been skipped by a wrongly-set hint", n)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("second Read error = %v, want a real syscall error (*fs.PathError from the closed fd)", err)
	}
}

// TestEofHintEmptyFile checks the unchanged n==0 case still returns
// io.EOF directly on the first Read, without ever setting eofHint (the
// hint only ever follows a *short*, non-zero read).
func TestEofHintEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if n != 0 || err != io.EOF {
		t.Fatalf("Read(empty file) = (%d, %v), want (0, io.EOF)", n, err)
	}
	if f.eofHint {
		t.Error("eofHint set on a zero-byte read -- should only follow a short, non-zero read")
	}
}

// TestEofHintOneByteFile is the minimal short-read case: a 1-byte file
// read into an oversized buffer, checked end to end through io.ReadAll
// (which drives the hint-then-EOF sequence exactly as search's
// lineBuffer.fill does).
func TestEofHintOneByteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "x" {
		t.Errorf("content = %q, want %q", got, "x")
	}
}

// TestEofHintComposesWithBOMReader checks the hint through bomReader,
// which is the actual wrapper cmd/gg's streaming search path uses (see
// wire.go's stripUTF8BOM). A BOM-only file and a BOM-plus-one-byte file
// both exercise bomReader's fast path (n >= 3 on the first Read) folding
// straight into the hint on the very next Read.
func TestEofHintComposesWithBOMReader(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"bom-only", []byte{0xEF, 0xBB, 0xBF}, ""},
		{"bom-plus-one-byte", []byte{0xEF, 0xBB, 0xBF, 'x'}, "x"},
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
				t.Fatal(err)
			}
			defer f.Close()

			r, err := stripUTF8BOM(f)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != c.want {
				t.Errorf("content = %q, want %q", got, c.want)
			}
		})
	}
}
