//go:build unix

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
//
// eofHint implements short-read-implies-EOF (M3 #28): on Linux, read(2)
// on a regular file returns fewer bytes than requested only at EOF --
// page I/O against local regular files is uninterruptible, so a signal
// arriving mid-transfer doesn't produce a short read the way it can for
// a pipe or socket. That means once a Read returns 0 < n < len(p), the
// very next Read is guaranteed to return (0, io.EOF); eofHint records
// that and the next call skips the read(2) syscall entirely rather than
// paying for a read that can only ever confirm what's already known. On
// the linux-tree benchmark corpus (~74.5k regular files searched) this
// removes one confirm-EOF read(2) per file -- strace showed gg issuing
// almost exactly content-read-count + file-count reads before this
// change.
//
// This is only sound for regular files, which is why the hint defaults
// to armed and callers that can't vouch for regularity must opt out via
// disableEOFHint: a pipe or socket can legitimately return a short read
// with more data still to come, and applying the hint there would
// silently truncate output.
//
// Every file cmd/gg's walk-discovered search path opens is verified
// regular before it ever reaches openRaw -- walk.fileTypeOf classifies
// TypeFile only from a dirent's d_type IsRegular() bit (see that func's
// doc) -- so the hint stays armed for the ~74.5k-file traversal path
// this was built for. Explicit CLI path arguments (walk.Entry.Depth==0)
// are a different story: walk.buildRootTask Lstat/Stat's them but never
// checks IsRegular, so `gg pat somefifo` or `gg pat <(cmd)` (process
// substitution) reach here too, and rg reads both of those to
// completion -- verified directly (`rg hello <(cat file)` matches; a
// closed-under-the-hint gg would not, since the hint's short-read
// assumption is false for a pipe). wire.go disables the hint for every
// explicit argument for exactly this reason, at zero cost to the
// traversal benchmark this exists for.
//
// Honest caveat: a file that grows between our short read and Close is
// a timing race, not a correctness guarantee either implementation
// makes -- rg reads until it observes n==0, so it can pick up an
// appended tail we won't if the append lands in that exact window;
// neither behavior is "more correct", just different, and both are
// racing a concurrent writer either way.
type rawFile struct {
	fd              int
	path            string
	eofHint         bool
	eofHintDisabled bool
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

// disableEOFHint opts this file out of the short-read-implies-EOF
// optimization (see eofHint's doc on rawFile). Must be called before the
// first Read; wire.go calls it for every explicit CLI path argument,
// since those aren't verified regular the way walk-discovered files are.
func (f *rawFile) disableEOFHint() {
	f.eofHintDisabled = true
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
//
// See eofHint's doc on rawFile for why a short read can stand in for
// the confirm-EOF read that would otherwise follow it.
func (f *rawFile) Read(p []byte) (int, error) {
	if f.eofHint {
		f.eofHint = false
		return 0, io.EOF
	}
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
		if n < len(p) && !f.eofHintDisabled {
			f.eofHint = true
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
