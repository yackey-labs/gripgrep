package main

import (
	"os"
	"syscall"
)

// mmapEligible reports whether the given --mmap/--no-mmap mode and path
// list mean gg should attempt to memory-map files at all this
// invocation, mirroring rg's own once-per-run policy exactly
// (crates/core/flags/hiargs.rs's mmap_choice construction, verified
// against the real rg source -- not the size-threshold heuristic this
// package originally assumed, which was wrong):
//
//   - MmapNever: never.
//   - MmapAlways: always, unconditionally (rg's AlwaysTryMmap).
//   - MmapAuto (the default): only when 10 or fewer paths were given and
//     EVERY one of them stats as a regular file. Naming even one
//     directory disables mmap for the ENTIRE invocation, not just that
//     path -- rg computes this decision exactly once, from the
//     command-line path list alone, and then applies the same yes/no
//     answer uniformly to every file it ends up searching (including
//     ones discovered by walking a directory, on the rare path where
//     that's even possible: MmapAlways can still force mmap attempts
//     on directory-walked files; MmapAuto never reaches that case
//     because naming a directory alone already fails the "every path is
//     a file" check).
//
// This is deliberately a single, one-time, whole-invocation decision
// (matching rg), not a per-file heuristic -- see PLAN.md's I/O design
// row: "mmap only for <=10 explicitly named files (never during
// walks)".
func mmapEligible(mode MmapMode, paths []string) bool {
	switch mode {
	case MmapNever:
		return false
	case MmapAlways:
		return true
	}
	if len(paths) > 10 {
		return false
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// mmappedFile is a read-only memory-mapped view of a file's contents,
// backed directly by syscall.Mmap over a bare file descriptor (no
// os.File involved, consistent with rawfile.go's rationale for avoiding
// os.File's runtime-poller overhead -- though here the bigger win is
// skipping read(2) entirely).
type mmappedFile struct {
	data []byte
	fd   int
}

// mmapOpen attempts to open and memory-map path for reading, mirroring
// rg's own MmapChoice::open: any failure (open, fstat, a zero-length
// file, or the mmap(2) call itself) is reported as ok=false with a nil
// error, matching rg's silent-fallback-to-streaming behavior (rg logs
// such failures at debug level only; gg has no equivalent facility, and
// this is an internal performance choice invisible to correct output
// either way, not a user-facing error). A real error is returned only
// if it's useful to distinguish from "just fall back" -- in practice
// this never happens on the success path callers care about, since
// every failure mode above is folded into ok=false.
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

// stripUTF8BOMSlice is stripUTF8BOM's equivalent for a mmap'd (or any
// already-fully-in-memory) []byte: no reader state to preserve, so this
// is just a slice check and reslice.
func stripUTF8BOMSlice(data []byte) []byte {
	if len(data) >= len(utf8BOM) && [3]byte(data[:3]) == utf8BOM {
		return data[3:]
	}
	return data
}
