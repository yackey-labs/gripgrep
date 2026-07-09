package main

import (
	"io"
	"io/fs"
	"syscall"
)

// rawFile is a minimal io.ReadCloser backed by a bare file descriptor,
// opened and read via direct syscalls instead of os.Open/os.File.
//
// Why this exists (M3 profiling, linux-tree corpus, ~79k files): every
// os.Open of a regular file pays for os.newFile's runtime-poller
// registration -- an extra fcntl(F_GETFL)/(F_SETFL) round trip so the
// os.File *could* support Read deadlines and cancellation. gg's search
// path never uses either on a regular file (no per-file timeouts, no
// concurrent Close-to-unblock-a-Read), so that machinery is pure
// overhead at gg's open rate: a CPU profile of `gg --no-ignore` on the
// linux-tree corpus showed 64% of total samples in
// internal/runtime/syscall/linux.Syscall6 with os.newFile/syscall.fcntl
// as the dominant callers -- i.e. the poller setup, not the actual
// read()s, was the largest single cost in the whole search. rg's own
// file-open path is a bare open()/read()/close() with no equivalent
// step, which this matches.
//
// Only Read and Close are implemented -- callers needing Stat, Fd, or
// deadline support must use os.Open instead; gg's per-file search path
// (cmd/gg/wire.go) needs neither.
type rawFile struct {
	fd   int
	path string
}

// openRaw opens path for reading via a direct open(2) syscall, skipping
// os.File's poller registration. The returned error matches os.Open's
// shape (*fs.PathError) so existing "gg: %s: %s" error reporting is
// unaffected.
func openRaw(path string) (*rawFile, error) {
	var fd int
	var err error
	for {
		fd, err = syscall.Open(path, syscall.O_RDONLY, 0)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return &rawFile{fd: fd, path: path}, nil
}

// Read implements io.Reader via a direct read(2) syscall. read(2)
// signals EOF as a zero-length, error-free return; Read translates
// that into io.EOF itself, since raw syscall.Read does not.
//
// A bare read(2) can return EINTR if a signal interrupts it before any
// data is transferred; os.File's Read retries this internally (via
// poll.FD, part of the machinery this type deliberately bypasses), so
// this loop reproduces that one behavior explicitly rather than
// surfacing EINTR as an error to the caller.
func (f *rawFile) Read(p []byte) (int, error) {
	for {
		n, err := syscall.Read(f.fd, p)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return n, &fs.PathError{Op: "read", Path: f.path, Err: err}
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}

// Close implements io.Closer via a direct close(2) syscall. Unlike Read
// and openRaw, this deliberately does not retry on EINTR: on Linux the
// file descriptor is always released even when close(2) reports EINTR,
// so retrying risks closing an unrelated fd that has since been reused
// with the same number (the standard close-on-EINTR pitfall).
func (f *rawFile) Close() error {
	return syscall.Close(f.fd)
}
