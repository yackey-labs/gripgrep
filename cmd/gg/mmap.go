package main

import (
	"os"
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

// mmappedFile (per-OS: mmap_unix.go, mmap_windows.go) is a read-only
// memory-mapped view of a file's contents. mmapOpen attempts to open and
// memory-map a path for reading, mirroring rg's own MmapChoice::open:
// any failure (open, stat, a zero-length file, or the mapping call
// itself) is reported as ok=false with a nil error, matching rg's
// silent-fallback-to-streaming behavior (rg logs such failures at debug
// level only; gg has no equivalent facility, and this is an internal
// performance choice invisible to correct output either way, not a
// user-facing error).

// stripUTF8BOMSlice is stripUTF8BOM's equivalent for a mmap'd (or any
// already-fully-in-memory) []byte: no reader state to preserve, so this
// is just a slice check and reslice.
func stripUTF8BOMSlice(data []byte) []byte {
	if len(data) >= len(utf8BOM) && [3]byte(data[:3]) == utf8BOM {
		return data[3:]
	}
	return data
}
