package engine

import (
	"os"
	"syscall"
	"unsafe"
)

// mmappedFile is a read-only memory-mapped view of a file's contents,
// backed by CreateFileMapping/MapViewOfFile -- the Windows spelling of
// the unix mmap(2) path (see mmap_unix.go). The os.File is kept open for
// the life of the view (Windows requires the file handle to outlive the
// mapping's creation, and holding it also keeps Close symmetrical); the
// unix implementation's bare-fd rationale (runtime-poller overhead) is a
// Linux profiling result that doesn't transfer here, so plain os.Open is
// fine.
type mmappedFile struct {
	data []byte
	f    *os.File
}

// mmapOpen attempts to open and memory-map path for reading; see
// mmap.go's package-level contract for the shared ok=false fallback
// semantics (any failure, including a zero-length file or a file too
// large to map into this process's address space, silently declines to
// the streaming path).
func mmapOpen(path string) (mf *mmappedFile, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	fi, err := f.Stat()
	if err != nil || fi.Size() <= 0 || fi.Size() != int64(int(fi.Size())) {
		f.Close()
		return nil, false
	}
	size := fi.Size()
	h, err := syscall.CreateFileMapping(syscall.Handle(f.Fd()), nil, syscall.PAGE_READONLY, uint32(size>>32), uint32(size), nil)
	if err != nil {
		f.Close()
		return nil, false
	}
	addr, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_READ, 0, 0, uintptr(size))
	// The view holds its own reference to the mapping object; the
	// mapping handle itself can (and per the CreateFileMapping docs,
	// should) be closed immediately.
	syscall.CloseHandle(h)
	if err != nil {
		f.Close()
		return nil, false
	}
	// vet flags this uintptr->unsafe.Pointer conversion ("possible
	// misuse"), but addr is a view address minted by the kernel via
	// MapViewOfFile, not a uintptr laundered from a Go pointer -- the
	// GC-safety hazard vet's unsafeptr heuristic guards against can't
	// arise. This is the standard pattern (see golang.org/x/exp/mmap's
	// windows implementation).
	return &mmappedFile{data: unsafe.Slice((*byte)(unsafe.Pointer(addr)), int(size)), f: f}, true
}

// Close unmaps the view and closes the underlying file.
func (mf *mmappedFile) Close() error {
	err := syscall.UnmapViewOfFile(uintptr(unsafe.Pointer(unsafe.SliceData(mf.data))))
	if cerr := mf.f.Close(); err == nil {
		err = cerr
	}
	return err
}
