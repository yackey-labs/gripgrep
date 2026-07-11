package engine

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yackey-labs/gripgrep/printer"
	"github.com/yackey-labs/gripgrep/search"
	"github.com/yackey-labs/gripgrep/walk"
)

// Worker bundles one search.Searcher with the search.Sink it feeds, so
// Run's internal pool can hand out matched pairs to concurrently-active
// walk goroutines -- exactly the sharing unit sync.Pool exists for (a
// Searcher's pooled rolling buffer and scratch Match/Ctx values, and most
// Sink implementations' own per-file buffers, are single-goroutine only).
// Standard marks whether Sink is a line-displaying sink (as opposed to a
// count/paths-only/collecting one); Run consults it, together with the
// per-file BinaryMode, to apply rg's binary suppression rules before any
// line reaches Sink -- see matchTracker's doc.
//
// InvertMatchSignal marks a Sink whose exit-code contribution is the
// COMPLEMENT of search.Stats.Matched -- currently only cmd/gg's
// --files-without-match (printer.FilesWithoutMatch), whose whole purpose
// is to report files where NOTHING matched, and whose real-rg-verified
// exit code is 0 iff at least one such file was found, not iff any file
// had a real match. See matchTracker's doc for where this is consulted.
type Worker struct {
	Searcher          *search.Searcher
	Sink              search.Sink
	Standard          bool
	InvertMatchSignal bool
}

// NewWorkerFunc builds one Worker. Called by Run's own sync.Pool, so it
// runs at most once per concurrently-active walk goroutine, not once per
// file -- callers building it from cmd/gg's printer sinks or the root
// facade's collecting/streaming sink pay that construction cost at the
// same rate either way.
type NewWorkerFunc func() *Worker

// QuitSink is an optional search.Sink that also reports whether it has
// already found what it's looking for. When supplied to Run, it replaces
// every worker's own Sink for the rest of the walk (forcing Standard's
// binary-suppression semantics off, matching rg's -q, which shows no
// output at all) and Run consults Found() before every visited file,
// stopping the walk immediately once true -- rg's own -q contract:
// "yes/no as fast as possible." cmd/gg's -q supplies *printer.Quiet here
// (which already implements both halves); pass nil for a normal,
// exhaustive search.
type QuitSink interface {
	search.Sink
	Found() bool
}

// BinaryMessaging carries the display parameters matchTracker needs to
// write rg's binary-file messages ("binary file matches...", "WARNING:
// stopped searching...") through Dest, matching whatever a Run caller's
// own sinks are already doing with the same values -- see
// matchTracker's doc. cmd/gg supplies its real stdout-backed Dest plus
// the showPath/heading/contextEnabled it already computed for its
// printer sinks. A caller with no textual output stream of its own (the
// root facade) supplies a Dest wrapping io.Discard and leaves the rest
// false: the suppression/drop decisions that change what reaches Sink
// don't depend on these fields, only the (then-discarded) message text
// does.
type BinaryMessaging struct {
	Dest           *printer.Dest
	ShowPath       bool
	Heading        bool
	ContextEnabled bool
}

// Result reports the outcome of a Run or Files call that a caller needs
// to compute its own exit code (cmd/gg) or return value (the facade).
type Result struct {
	// Matched is true if any file, anywhere in the walk, produced at
	// least one match (Run) or was listed (Files).
	Matched bool
	// AnyError is true if any per-file/per-path error was encountered
	// (permission denied, a file deleted between readdir and open, ...).
	// Per-file errors are also written to stderr as they occur; this
	// only tells the caller whether any happened at all.
	AnyError bool
}

// Run walks cfg.Paths (or "." when empty, per ResolvePaths), searching
// every discovered/named file and driving results through newWorker()'s
// Sink. This is the one engine both cmd/gg and the root facade drive --
// see the package doc -- so binary-detection policy (resolveBinaryMode),
// mmap eligibility, BOM stripping, and rg's binary-message suppression
// rules (matchTracker) are never forked between the CLI and the library.
//
// quiet and bm are documented on their own types; stderr receives one
// line per per-file/per-path error encountered (never a fatal abort --
// see Result.AnyError). A non-nil error return means walk setup itself
// failed (a bad -g/--glob pattern, or walk.Walk's own setup error);
// per-file errors during a successful walk are reported via Result and
// stderr instead.
//
// stop, if non-nil, is checked before every visited file exactly like
// quiet.Found() (and combines with it via OR) but WITHOUT quiet's sink
// override: quiet always replaces every worker's own Sink (rg's -q shows
// no output at all, ever), whereas a caller that wants early-exit while
// still keeping its real per-worker sinks -- and their per-file state,
// such as the root facade's SearchStream grouping context lines -- needs
// the stop check on its own. cmd/gg's -q passes stop=nil (quiet already
// covers it); the facade's SearchStream passes quiet=nil and its own
// stop func backed by the same flag its sinks set when the caller's
// early-stop callback returns false.
// stats, when non-nil, is the shared --stats accumulator every per-file
// matchTracker adds into (see StatsAccumulator). nil disables all stats
// bookkeeping, keeping the no-stats path cost-free. Callers with no --stats
// need (the root facade) pass nil.
func Run(cfg Config, newWorker NewWorkerFunc, quiet QuitSink, stop func() bool, bm BinaryMessaging, stats *StatsAccumulator, stderr io.Writer) (Result, error) {
	typesMatcher, err := buildTypes(cfg.TypeChanges)
	if err != nil {
		return Result{}, err
	}
	globSet, globsRequireMatch, err := buildGlobs(cfg.Globs, cfg.IGlobs, cfg.GlobCaseInsensitive)
	if err != nil {
		return Result{}, err
	}

	statPaths, walkRoots := ResolvePaths(cfg.Paths)

	// mmapOK is a once-per-invocation decision (mirroring rg exactly --
	// see mmapEligible's doc), not a per-file heuristic: it's computed
	// once, from the full path list, before any walking starts.
	mmapOK := mmapEligible(cfg.Mmap, statPaths)

	explicitIgnore, globalIgnore := buildIgnoreSources(cfg, stderr)
	walkOpts := ignoreWalkOptions(cfg, explicitIgnore, globalIgnore)
	walkOpts.Hidden = cfg.Hidden
	walkOpts.MaxFileSize = cfg.MaxFilesize
	walkOpts.MaxDepth = cfg.MaxDepth
	walkOpts.Threads = cfg.Threads
	walkOpts.Globs = globSet
	walkOpts.GlobsRequireMatch = globsRequireMatch
	walkOpts.Types = typesMatcher
	walkOpts.FollowSymlinks = cfg.FollowSymlinks
	walkOpts.OneFileSystem = cfg.OneFileSystem
	walkOpts.Sort = sortConfig(cfg)

	var anyMatched, anyError atomic.Bool
	// reportErr writes one stderr line per per-file/per-path error and flips
	// the error-exit signal. --no-messages gates ONLY the print: the
	// AnyError store always happens, so an error still forces exit 2 with or
	// without the flag (rg parity -- see Config.NoMessages).
	reportErr := func(path string, err error) {
		if !cfg.NoMessages {
			fmt.Fprintf(stderr, "gg: %s: %s\n", path, err)
		}
		anyError.Store(true)
	}
	pool := &sync.Pool{
		New: func() any { return newWorker() },
	}

	visitor := func(e *walk.Entry) walk.WalkState {
		if (quiet != nil && quiet.Found()) || (stop != nil && stop()) {
			return walk.Quit
		}
		if e.Err != nil {
			reportErr(e.Path, e.Err)
			return walk.Continue
		}
		if e.Type != walk.TypeFile {
			// Directories recurse internally (nothing to do here). Followed
			// symlinks (-L) never reach here AS symlinks -- the walker
			// resolves them to a TypeFile/TypeDir target first (or a
			// TypeSymlink WITH Err for a broken link/loop, handled above);
			// an UNfollowed TypeSymlink carries no content to search.
			// TypeUnknown covers FIFO/socket/device entries, which must
			// never be opened (opening a FIFO blocks forever) -- skip
			// unconditionally.
			return walk.Continue
		}

		explicit := e.Depth == 0
		w := pool.Get().(*Worker)
		defer pool.Put(w)
		w.Searcher.BinaryMode = resolveBinaryMode(cfg.Binary, explicit)
		if cfg.NullData {
			// --null-data disables binary detection entirely: a NUL is the
			// record terminator here, not a binary marker (rg's own
			// `none = AsText || null_data`). Overrides the -a/-uuu/explicit
			// resolution above for both walk-discovered and explicit files.
			w.Searcher.BinaryMode = search.BinaryNone
		}

		sink := w.Sink
		standard := w.Standard
		invertMatchSignal := w.InvertMatchSignal
		if quiet != nil {
			// -q always overrides Mode (Config.Quiet's doc: "independent
			// of Mode"), so the binary-message branches below must never
			// fire under -q even if Mode happens to be ModeStandard --
			// quiet writes nothing, ever, and matchTracker must not
			// write to Dest on its behalf. cmd/gg's execute() also never
			// reads Result.Matched under -q (it branches on quiet.Found()
			// instead), so invertMatchSignal is moot here either way --
			// forced off anyway, for the same "quiet wins outright"
			// reason standard is.
			sink = quiet
			standard = false
			invertMatchSignal = false
		}
		tracked := &matchTracker{
			Sink:              sink,
			matched:           &anyMatched,
			standard:          standard,
			invertMatchSignal: invertMatchSignal,
			binMode:           w.Searcher.BinaryMode,
			showPath:          bm.ShowPath,
			heading:           bm.Heading,
			contextEnabled:    bm.ContextEnabled,
			dest:              bm.Dest,
			searcher:          w.Searcher,
			stats:             stats,
		}
		if stats != nil {
			tracked.statsStart = time.Now()
		}

		if mmapOK {
			if mf, ok := mmapOpen(e.Path); ok {
				data := stripUTF8BOMSlice(mf.data)
				serr := w.Searcher.SearchBytes(e.Path, data, tracked)
				mf.Close()
				if serr != nil {
					reportErr(e.Path, serr)
				}
				return walk.Continue
			}
			// mmapOpen failing (any reason -- open error, zero-length
			// file, mmap(2) itself failing) falls through to the
			// streaming path below, exactly like rg's MmapChoice::open
			// returning None: no user-visible error, just an internal
			// fallback.
		}

		f, ferr := openRaw(e.Path)
		if ferr != nil {
			reportErr(e.Path, ferr)
			return walk.Continue
		}
		defer f.Close()
		if explicit {
			// An explicit CLI path argument isn't verified regular the
			// way a walk-discovered TypeFile is (walk.buildRootTask
			// stats it but doesn't check IsRegular) -- process
			// substitution and FIFOs reach here this way, and rg reads
			// them to completion, so the short-read-implies-EOF hint
			// (see rawfile_unix.go's doc) must stay off. See
			// disableEOFHint's doc.
			f.disableEOFHint()
		}

		reader, rerr := stripUTF8BOM(f)
		if rerr != nil {
			reportErr(e.Path, rerr)
			return walk.Continue
		}

		if serr := w.Searcher.Search(e.Path, reader, tracked); serr != nil {
			reportErr(e.Path, serr)
		}
		return walk.Continue
	}

	if err := walk.Walk(walkRoots, walkOpts, visitor); err != nil {
		return Result{}, err
	}

	return Result{Matched: anyMatched.Load(), AnyError: anyError.Load()}, nil
}

// sortConfig translates cfg's engine-level SortKind/SortReverse into the
// walk.SortConfig the traversal consumes. SortNone maps to walk.SortNone
// (the parallel fast path); the created kind never reaches here (cmd/gg
// rejects it before building an engine.Config, per rg's pre-search
// supported() check).
func sortConfig(cfg Config) walk.SortConfig {
	var kind walk.SortKind
	switch cfg.SortKind {
	case SortPath:
		kind = walk.SortPath
	case SortModified:
		kind = walk.SortModified
	case SortAccessed:
		kind = walk.SortAccessed
	default:
		kind = walk.SortNone
	}
	return walk.SortConfig{Kind: kind, Reverse: cfg.SortReverse}
}

// FilesVisit is called once per file Files would search (but doesn't --
// see Files' doc), in nondeterministic (parallel) order. path is a view
// valid only for the duration of the call (mirrors walk.Entry.Path);
// callers needing it to outlive must copy.
type FilesVisit func(path string)

// Files walks cfg's paths/filters WITHOUT searching (rg's --files, per
// PLAN.md's Output row: "--files mode skips matcher/searcher entirely").
// Every discovered regular file is reported via visit. Files composes
// with everything that shapes the walked file set (Hidden, NoIgnore,
// Globs, TypeChanges, MaxFilesize, Threads) exactly like Run, via the
// same walk.Options construction; it never touches mmap, the matcher, or
// any other search-specific concern, since there is nothing to search.
func Files(cfg Config, visit FilesVisit, stderr io.Writer) (Result, error) {
	typesMatcher, err := buildTypes(cfg.TypeChanges)
	if err != nil {
		return Result{}, err
	}
	globSet, globsRequireMatch, err := buildGlobs(cfg.Globs, cfg.IGlobs, cfg.GlobCaseInsensitive)
	if err != nil {
		return Result{}, err
	}

	// Files never calls os.Stat on a path itself (no computeShowPath/
	// mmapEligible equivalent here), so only walkRoots (the "" vs "."
	// distinction ResolvePaths exists for -- see its doc) matters; the
	// stat-able form is discarded.
	_, walkRoots := ResolvePaths(cfg.Paths)

	explicitIgnore, globalIgnore := buildIgnoreSources(cfg, stderr)
	walkOpts := ignoreWalkOptions(cfg, explicitIgnore, globalIgnore)
	walkOpts.Hidden = cfg.Hidden
	walkOpts.MaxFileSize = cfg.MaxFilesize
	walkOpts.MaxDepth = cfg.MaxDepth
	walkOpts.Threads = cfg.Threads
	walkOpts.Globs = globSet
	walkOpts.GlobsRequireMatch = globsRequireMatch
	walkOpts.Types = typesMatcher
	walkOpts.FollowSymlinks = cfg.FollowSymlinks
	walkOpts.OneFileSystem = cfg.OneFileSystem
	walkOpts.Sort = sortConfig(cfg)

	var anyMatched, anyError atomic.Bool
	visitor := func(e *walk.Entry) walk.WalkState {
		if e.Err != nil {
			// --no-messages gates the print only; the error still forces
			// exit 2 (rg parity -- see Config.NoMessages). --files never
			// reads file content, so the only errors reaching here are walk
			// errors (unreadable directory, symlink loop / broken link
			// under -L).
			if !cfg.NoMessages {
				fmt.Fprintf(stderr, "gg: %s: %s\n", e.Path, e.Err)
			}
			anyError.Store(true)
			return walk.Continue
		}
		if e.Type != walk.TypeFile {
			// Directories recurse internally. A followed symlink (-L) is
			// resolved to its TypeFile/TypeDir target before reaching here
			// (a broken link / loop arrives as TypeSymlink WITH Err, handled
			// above); an UNfollowed symlink carries no listable content --
			// matches real rg's own --files output, verified directly.
			// TypeUnknown (FIFO/socket/device) is excluded the same way Run
			// already excludes it.
			return walk.Continue
		}
		anyMatched.Store(true)
		visit(e.Path)
		return walk.Continue
	}

	if err := walk.Walk(walkRoots, walkOpts, visitor); err != nil {
		return Result{}, err
	}

	return Result{Matched: anyMatched.Load(), AnyError: anyError.Load()}, nil
}
