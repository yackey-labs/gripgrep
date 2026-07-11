package walk

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/yackey-labs/gripgrep/filetype"
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
	// The NoIgnore* fields decompose rg's five independent no-ignore-*
	// sub-flags, each gating one ignore source (crates/core/flags/
	// hiargs.rs walk_builder). --no-ignore is CLI sugar that sets the
	// first five (dot/exclude/global/parent/vcs) but not files; here they
	// are already resolved to independent bools by the caller.
	//
	//   NoIgnoreDot     kills .ignore and .rgignore (dot family)
	//   NoIgnoreExclude kills .git/info/exclude
	//   NoIgnoreParent  kills the entire parent-directory ignore chain
	//   NoIgnoreVcs     kills .gitignore, exclude, and the global matcher
	//   NoRequireGit    consults git/global matchers even outside a repo
	//
	// (--no-ignore-global is resolved at the engine boundary by simply not
	// passing a GlobalIgnore set, so it has no field here.)
	NoIgnoreDot     bool
	NoIgnoreExclude bool
	NoIgnoreParent  bool
	NoIgnoreVcs     bool
	NoRequireGit    bool
	// IgnoreCaseInsensitive is --ignore-file-case-insensitive: matches the
	// per-directory tree sources (.rgignore/.ignore/.gitignore/exclude)
	// case-insensitively. It deliberately does NOT reach GlobalIgnore or
	// ExplicitIgnore (probe F3): those are compiled case-sensitively by
	// the caller.
	IgnoreCaseInsensitive bool
	// ExplicitIgnore is every --ignore-file's patterns compiled into one
	// Set (in arg order) by the caller, or nil when none was given or
	// --no-ignore-files killed them. Matched against the display path,
	// never git-gated. See ignoreCtx.explicitSet.
	ExplicitIgnore *glob.Set
	// GlobalIgnore is the resolved global gitignore matcher (core.
	// excludesFile / XDG default), or nil when absent or killed by
	// --no-ignore-global/--no-ignore-vcs. Matched against the display
	// path, gated per-entry by anyGit. See ignoreCtx.globalSet.
	GlobalIgnore *glob.Set

	// FollowSymlinks follows symlinks during traversal (default: no).
	FollowSymlinks bool
	// MaxFileSize skips files larger than this many bytes; 0 = unlimited.
	MaxFileSize int64
	// MaxDepth is -d/--max-depth: nil = unlimited. A non-nil 0 is a real,
	// legal value (rg parity: `rg --max-depth 0 dir/` searches only the
	// roots themselves, descending nowhere) -- NOT the same as unset,
	// which is why this is a pointer rather than a plain int with a
	// 0-means-unlimited convention (mirrors cmd/gg's Config.MaxCount).
	// Depth is counted from each root independently: a root itself is
	// depth 0 (see Entry.Depth's doc), so MaxDepth bounds how many
	// directory levels below each root get visited/enqueued -- applied
	// only to entries discovered WITHIN a directory listing (see
	// worker.processDir), never to a root itself, which is always
	// visited regardless (verified against the real rg binary: `rg
	// --max-depth 0 pat file` still searches an explicitly named FILE
	// root, since roots are depth 0).
	MaxDepth *int
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
	// Types, if non-nil, is an additional file-name filter applied AFTER
	// ignore-file processing but BEFORE nothing else (e.g. -t/-T/
	// --type-add/--type-clear): rg's own precedence, verified against the
	// real binary (round #35) -- -g/--iglob (Globs above) always decides
	// first when it has an opinion; only when Globs and the ignore stack
	// both defer does Types get consulted. Never applied to directories
	// (see filetype.Matcher.Match's doc) or to anything when nil, which
	// costs one extra pointer check per entry -- the same pattern Globs
	// above already pays when unset. See worker.classify for the exact
	// precedence chain.
	Types *filetype.Matcher

	// ignoreActive and ictx are derived once at Walk start (not caller-set)
	// from the fields above: ignoreActive is whether ANY ignore source can
	// contribute (so the per-directory node chain and matched() are skipped
	// entirely otherwise, preserving --no-ignore's walk speed), and ictx is
	// the shared per-walk ignore context threaded into matched().
	ignoreActive bool
	ictx         *ignoreCtx
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

	// Derive the per-walk ignore state once. ignoreActive is false only
	// when nothing could ever match (both tree families off, no global, no
	// explicit) -- e.g. plain --no-ignore with no --ignore-file -- in which
	// case the per-directory node chain is skipped entirely, exactly as the
	// old single NoIgnore fast path did.
	opts.ignoreActive = !opts.NoIgnoreDot || !opts.NoIgnoreVcs ||
		opts.ExplicitIgnore != nil || opts.GlobalIgnore != nil
	// cwd re-anchors the global/explicit matchers for absolute walk-root
	// args (see ignoreCtx.cwd). Only needed when one of those matchers is
	// active; a Getwd error just disables the re-anchor (empty cwd).
	var cwd string
	if opts.GlobalIgnore != nil || opts.ExplicitIgnore != nil {
		cwd, _ = os.Getwd()
	}
	opts.ictx = &ignoreCtx{
		noIgnoreDot:     opts.NoIgnoreDot,
		noIgnoreVcs:     opts.NoIgnoreVcs,
		noIgnoreExclude: opts.NoIgnoreExclude,
		noRequireGit:    opts.NoRequireGit,
		cwd:             cwd,
		globalSet:       opts.GlobalIgnore,
		explicitSet:     opts.ExplicitIgnore,
	}

	queues := make([]*dirQueue, n)
	for i := range queues {
		queues[i] = &dirQueue{}
	}

	initial := make([]*dirTask, 0, len(roots))
	for _, r := range roots {
		initial = append(initial, buildRootTask(r, &opts))
	}
	// Push in reverse so each queue's LIFO pop() yields its assigned roots
	// in original argument order (push appends to the tail, pop removes
	// from the tail): the last root pushed to a given queue is the first
	// one popped from it. Iterating backwards while keeping the same
	// i%n assignment means, for any queue, the earliest-argument root
	// among those assigned to it is pushed last and therefore popped
	// first -- restoring argument order without touching the
	// round-robin assignment (queue load balancing for n>1 is
	// unaffected) or the depth-first LIFO semantics children rely on
	// (see dirQueue's doc; rg -j1 preserves explicit top-level argument
	// order, verified empirically -- round #37).
	for i := len(initial) - 1; i >= 0; i-- {
		queues[i%n].push(initial[i])
	}

	coord := newCoordinator()
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
		if opts.ignoreActive && !opts.NoIgnoreParent {
			t.ignore = buildParentChain(t.abs, opts)
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
