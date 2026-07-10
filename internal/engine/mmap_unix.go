//go:build unix

package engine

import (
	"syscall"
)

// mmappedFile is a read-only memory-mapped view of a file's contents,
// backed directly by syscall.Mmap over a bare file descriptor (no
// os.File involved, consistent with rawfile_unix.go's rationale for
// avoiding os.File's runtime-poller overhead -- though here the bigger
// win is skipping read(2) entirely).
type mmappedFile struct {
	data []byte
	fd   int
}

// mmapOpen attempts to open and memory-map path for reading; see
// mmap.go's package-level contract for the shared ok=false fallback
// semantics. A real error is returned only if it's useful to distinguish
// from "just fall back" -- in practice this never happens on the success
// path callers care about, since every failure mode above is folded into
// ok=false.
func mmapOpen(path string) (mf *mmappedFile, ok bool) {
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, false
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return nil, false
	}
	if st.Size <= 0 {
		// mmap(2) of a zero-length region is either an error or
		// meaningless; the streaming path already handles empty files
		// correctly, so just decline.
		syscall.Close(fd)
		return nil, false
	}
	data, err := syscall.Mmap(fd, 0, int(st.Size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		syscall.Close(fd)
		return nil, false
	}
	return &mmappedFile{data: data, fd: fd}, true
}

// Close unmaps the memory region and closes the underlying descriptor.
func (mf *mmappedFile) Close() error {
	err := syscall.Munmap(mf.data)
	if cerr := syscall.Close(mf.fd); err == nil {
		err = cerr
	}
	return err
}
