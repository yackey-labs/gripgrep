package walk

import (
	"errors"

	"github.com/yackey-labs/gripgrep/glob"
)

// WalkState is returned by a Visitor to control traversal.
type WalkState uint8

const (
	// Continue proceeds with the walk as normal.
	Continue WalkState = iota
	// SkipDir, returned for a directory entry, prunes that whole
	// subtree without descending into it. Returned for a file entry, it
	// behaves like Continue.
	SkipDir
	// Quit aborts the entire walk as soon as possible, across all
	// worker goroutines.
	Quit
)

// FileType is a cheap classification of a directory entry, derived from
// readdir on Unix (no stat syscall per entry).
type FileType uint8

const (
	TypeUnknown FileType = iota
	TypeFile
	TypeDir
	TypeSymlink
)

// Entry describes one file-system entry delivered to a Visitor. Path is
// a view valid only for the duration of the Visitor call; a Visitor that
// needs to retain it must copy the string.
type Entry struct {
	// Path is the entry's path as reached from the walk root (root-relative
	// join, not necessarily cleaned beyond filepath.Join semantics).
	Path string
	Type FileType
	// Depth is the number of directory levels below the walk root (root
	// entries are depth 0).
	Depth int
	// Err is non-nil if this entry could not be read (e.g. a permission
	// error surfaced by ReadDir); Path/Type may be zero-valued in that case.
	Err error
}

// Visitor is called once per file-system entry from worker goroutines.
// It must be safe for concurrent use: the walker calls it from multiple
// goroutines with no external synchronization.
type Visitor func(e *Entry) WalkState

// Options configures a parallel walk.
type Options struct {
	// Hidden includes dot-files/dot-directories (default: excluded).
	Hidden bool
	// NoIgnore disables all .gitignore/.ignore/exclude processing.
	NoIgnore bool
	// FollowSymlinks follows symlinks during traversal (default: no).
	FollowSymlinks bool
	// MaxFileSize skips files larger than this many bytes; 0 = unlimited.
	MaxFileSize int64
	// Threads is the worker count; 0 selects the runtime default
	// (min(runtime.NumCPU(), 12), matching ripgrep's default).
	Threads int
	// Globs, if non-nil, is an additional include/exclude matcher
	// applied on top of ignore-file processing (e.g. -g/--glob).
	Globs *glob.Set
}

// ErrNotImplemented is returned by the M0 stub Walk. It will be removed
// once M1-walk lands a real implementation.
var ErrNotImplemented = errors.New("walk: not implemented (TODO M1-walk)")

// Walk traverses roots in parallel per opts, calling visit for every
// matched file-system entry.
//
// TODO(M1-walk): work-stealing deque (crossbeam_deque analogue), the
// immutable per-directory ignore-matcher stack (five matcher slots,
// compiled once per directory, shared by Arc-style reference with
// children), quiescence detection, hidden/max-filesize/symlink handling.
// The M0 stub always returns ErrNotImplemented without calling visit.
func Walk(roots []string, opts Options, visit Visitor) error {
	return ErrNotImplemented
}
