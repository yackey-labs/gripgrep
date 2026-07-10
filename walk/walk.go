package walk

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"

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
	// (min(runtime.GOMAXPROCS(0), 12), matching ripgrep's default cap).
	Threads int
	// Globs, if non-nil, is an additional include/exclude matcher
	// applied on top of ignore-file processing (e.g. -g/--glob).
	Globs *glob.Set
	// GlobsRequireMatch changes how a Globs NoMatch is treated: when
	// true, an entry that Globs doesn't match at all is excluded, not
	// just left to the ignore-file stack. This is what rg's -g override
	// semantics need — a plain -g pattern is really an *include* filter
	// ("only search files matching some -g"), so once at least one such
	// pattern exists, anything matching none of them should drop out
	// regardless of gitignore state. '!'-prefixed -g patterns still work
	// as ordinary excludes (glob.Ignored/Whitelisted are unaffected by
	// this flag; it only changes the glob.NoMatch case). The CLI is
	// responsible for setting this when it has built Globs from -g/-g'!'
	// flags; it should stay false for a Globs set built from some other
	// source that isn't override-shaped.
	GlobsRequireMatch bool
}

// Walk traverses roots in parallel per opts, calling visit for every
// matched file-system entry.
//
// A root of "" means "the current directory, because the caller had no
// PATH argument to substitute a default for" -- distinct from an
// explicit ".", which is a real, common CLI invocation of its own and
// must echo a "./" prefix on every discovered path to match rg exactly.
// See buildRootTask's doc for the verified-against-rg behavior this
// distinction preserves; callers defaulting an empty path list must pass
// "" here, not ".", or every discovered path will carry a "./" prefix
// rg's own default-directory behavior doesn't have.
//
// Walk distributes roots round-robin across a fixed pool of worker
// goroutines, each running a work-stealing loop over per-worker LIFO
// deques (see dirQueue): a worker processes directories depth-first from
// its own deque, and steals a batch from another worker's deque when its
// own runs dry. Every visit call may come from a different goroutine —
// see Visitor's doc.
//
// Walk returns once every worker has quiesced (no more work anywhere) or
// a Visitor returns Quit. Per-entry errors (a directory that couldn't be
// opened, a symlink that couldn't be stat'd, ...) are reported to visit
// via Entry.Err, not through Walk's return value; Walk itself only ever
// returns nil in the current implementation, but returns error to leave
// room for future fatal/setup-level failures.
func Walk(roots []string, opts Options, visit Visitor) error {
	if len(roots) == 0 {
		return nil
	}
	n := opts.Threads
	if n <= 0 {
		n = defaultThreads()
	}

	queues := make([]*dirQueue, n)
	for i := range queues {
		queues[i] = &dirQueue{}
	}

	initial := make([]*dirTask, 0, len(roots))
	for _, r := range roots {
		initial = append(initial, buildRootTask(r, &opts))
	}
	for i, t := range initial {
		queues[i%n].push(t)
	}

	coord := &coordinator{}
	coord.active.Store(int64(n))

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		w := &worker{idx: i, queues: queues, coord: coord, opts: &opts, visit: visit}
		go func() {
			defer wg.Done()
			w.run()
		}()
	}
	wg.Wait()
	return nil
}

// buildRootTask classifies one walk root. Explicit roots that are
// symlinks are always resolved regardless of Options.FollowSymlinks —
// matching the common CLI convention that an argument you named directly
// is always followed, even when discovered-during-traversal symlinks are
// left alone.
//
// root == "" is a special case, distinct from ".": it means "search the
// current directory because no PATH argument was given at all" -- as
// opposed to a user literally typing "." on the command line, which
// joinPath's doc explains DOES echo a "./" prefix on every discovered
// path (verified against real rg: `rg -n pat .` prints "./file", not
// "file"). Real rg's own behavior differs for the two cases: with no
// PATH argument at all, `rg` prints unprefixed relative paths ("file",
// not "./file") -- verified directly (`rg --files` vs `rg --files .`).
// Since both cases resolve to the identical string "." by the time a
// caller could pass it here, the caller (cmd/gg) must use "" to convey
// "this is a default, not something the user typed" -- real filesystem
// calls still need a real path, so "" is resolved to "." for those,
// while t.path (which every descendant's displayed Path is built from,
// via joinPath) keeps the caller's original "" -- see
// TestEmptyRootProducesUnprefixedPaths.
func buildRootTask(root string, opts *Options) *dirTask {
	fsRoot := root
	if fsRoot == "" {
		fsRoot = "."
	}
	t := &dirTask{path: root, depth: 0}

	info, err := os.Lstat(fsRoot)
	if err != nil {
		t.rootKind = rootInvalid
		t.rootErr = err
		return t
	}
	if abs, err := filepath.Abs(fsRoot); err == nil {
		t.abs = abs
	} else {
		t.abs = fsRoot
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Stat(fsRoot)
		if err != nil {
			t.rootKind = rootInvalid
			t.rootErr = err
			return t
		}
		info = target
	}

	if info.IsDir() {
		t.rootKind = rootDir
		if !opts.NoIgnore {
			t.ignore = buildParentChain(t.abs)
		}
		return t
	}
	t.rootKind = rootFile
	return t
}

// defaultThreads is min(GOMAXPROCS, 12) — ripgrep's 2016-era default cap;
// PLAN.md flags this as a sweep target for M3, not a settled constant.
func defaultThreads() int {
	n := runtime.GOMAXPROCS(0)
	if n > 12 {
		n = 12
	}
	if n < 1 {
		n = 1
	}
	return n
}
