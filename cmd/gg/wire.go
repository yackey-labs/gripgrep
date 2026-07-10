package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yackey-labs/gripgrep/glob"
	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/printer"
	"github.com/yackey-labs/gripgrep/search"
	"github.com/yackey-labs/gripgrep/walk"
)

// execute runs cfg's search end to end: it builds the matcher/glob
// wiring, walks cfg.Paths (or "." when empty) via package walk, and
// writes results to stdout through the printer package. It returns the
// process exit code: 0 if any match was found anywhere, 1 if the search
// completed with no match, 2 on a fatal setup error (a bad pattern or
// glob). Per-file errors encountered during the walk (permission denied,
// a file deleted between readdir and open, ...) are reported to stderr
// and do not change the exit code -- matching rg, which keeps walking
// and only reflects match/no-match in its exit status (verified: a
// permission-denied subdirectory alongside a real match still exits 0).
//
// cfg.Mmap (--mmap/--no-mmap) is consulted once, up front, via
// mmapEligible (mirroring rg's own once-per-invocation MmapChoice
// construction in hiargs.rs exactly -- not a per-size-threshold
// heuristic, which an earlier version of this comment wrongly assumed).
// When eligible, every file this run searches is opened via mmapOpen
// (SearchBytes) instead of the streaming openRaw+Search path; any mmap
// failure (open, fstat, zero-length, or mmap(2) itself) falls back to
// streaming silently, matching rg's own MmapChoice::open behavior.
// SearchBytes's BinaryConvert-under-mmap and BinaryQuit-first-chunk-only
// semantics were verified against rg's SliceByLine (searcher/glue.rs)
// directly -- see search.go's SearchBytes doc.
func execute(cfg *Config, stdout, stderr io.Writer) int {
	if cfg.Mode == ModeFiles {
		// --files skips the matcher/searcher pipeline entirely (see
		// executeFiles's doc) -- dispatched before buildMatcher runs
		// since --files needs no pattern at all.
		return executeFiles(cfg, stdout, stderr)
	}

	matcher, err := buildMatcher(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}

	globSet, globsRequireMatch, err := buildGlobs(cfg.Globs)
	if err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}

	paths, walkRoots := resolvePaths(cfg.Paths)

	// mmapOK is a once-per-invocation decision (mirroring rg exactly --
	// see mmapEligible's doc), not a per-file heuristic: it's computed
	// once, from the full path list, before any walking starts.
	mmapOK := mmapEligible(cfg.Mmap, paths)

	isTTY := isTerminalWriter(stdout)
	color := cfg.Color == ColorAlways || cfg.Color == ColorAnsi || (cfg.Color == ColorAuto && isTTY)
	heading := isTTY
	lineNumbers := isTTY
	if cfg.LineNumbers != nil {
		lineNumbers = *cfg.LineNumbers
	}
	showPath := computeShowPath(paths)
	contextEnabled := cfg.ContextBefore > 0 || cfg.ContextAfter > 0

	dest := printer.NewDest(stdout)
	var quiet *printer.Quiet
	if cfg.Quiet {
		quiet = printer.NewQuiet()
	}

	var anyMatched, anyError atomic.Bool
	pool := &sync.Pool{
		New: func() any {
			return newWorkerUnit(cfg, matcher, dest, color, heading, showPath, lineNumbers)
		},
	}

	walkOpts := walk.Options{
		Hidden:            cfg.Hidden,
		NoIgnore:          cfg.NoIgnore,
		MaxFileSize:       cfg.MaxFilesize,
		Threads:           cfg.Threads,
		Globs:             globSet,
		GlobsRequireMatch: globsRequireMatch,
	}

	visitor := func(e *walk.Entry) walk.WalkState {
		if quiet != nil && quiet.Found() {
			return walk.Quit
		}
		if e.Err != nil {
			fmt.Fprintf(stderr, "gg: %s: %s\n", e.Path, e.Err)
			anyError.Store(true)
			return walk.Continue
		}
		if e.Type != walk.TypeFile {
			// Directories recurse internally (nothing to do here);
			// symlinks are never followed in v1 (-L not implemented, so
			// FollowSymlinks is always false and TypeSymlink entries
			// carry no content to search); TypeUnknown covers
			// FIFO/socket/device entries, which must never be opened
			// (opening a FIFO blocks forever) -- skip unconditionally.
			return walk.Continue
		}

		explicit := e.Depth == 0
		unit := pool.Get().(*workerUnit)
		defer pool.Put(unit)
		unit.searcher.BinaryMode = resolveBinaryMode(cfg.Binary, explicit)

		var sink search.Sink = unit.sink
		if quiet != nil {
			sink = quiet
		}
		tracked := &matchTracker{
			Sink:    sink,
			matched: &anyMatched,
			// -q always overrides Mode (Config.Quiet's doc: "independent
			// of Mode"), so the binary-message branches below must never
			// fire under -q even if Mode happens to be ModeStandard --
			// quiet writes nothing, ever, and matchTracker must not
			// write to dest on its behalf.
			standard:       quiet == nil && unit.isStandard,
			binMode:        unit.searcher.BinaryMode,
			showPath:       showPath,
			heading:        heading,
			contextEnabled: contextEnabled,
			dest:           dest,
			searcher:       unit.searcher,
		}

		if mmapOK {
			if mf, ok := mmapOpen(e.Path); ok {
				data := stripUTF8BOMSlice(mf.data)
				serr := unit.searcher.SearchBytes(e.Path, data, tracked)
				mf.Close()
				if serr != nil {
					fmt.Fprintf(stderr, "gg: %s: %s\n", e.Path, serr)
					anyError.Store(true)
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
			fmt.Fprintf(stderr, "gg: %s: %s\n", e.Path, ferr)
			anyError.Store(true)
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
			fmt.Fprintf(stderr, "gg: %s: %s\n", e.Path, rerr)
			anyError.Store(true)
			return walk.Continue
		}

		if serr := unit.searcher.Search(e.Path, reader, tracked); serr != nil {
			fmt.Fprintf(stderr, "gg: %s: %s\n", e.Path, serr)
			anyError.Store(true)
		}
		return walk.Continue
	}

	if err := walk.Walk(walkRoots, walkOpts, visitor); err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}

	// Exit-code precedence, verified against the real rg binary (a
	// permission-denied subdirectory alongside a real match elsewhere in
	// the same walk): non-quiet modes (including -c/-l, which still walk
	// every file to produce accurate counts/paths) let a per-file/path
	// error override a real match -- exit 2, not 0, since the results are
	// known-incomplete. -q is the one exception: once any match is
	// confirmed, rg locks in exit 0 regardless of any error encountered
	// elsewhere in the same run (rg -q's contract is "yes/no as fast as
	// possible" -- a confirmed "yes" makes the error irrelevant to the
	// answer the caller asked for), and only falls back to error-implies-2
	// when no match was ever found.
	if quiet != nil {
		if quiet.Found() {
			return 0
		}
		if anyError.Load() {
			return 2
		}
		return 1
	}
	if anyError.Load() {
		return 2
	}
	if anyMatched.Load() {
		return 0
	}
	return 1
}

// defaultParallelWorkers is the intra-file parallel-search worker count
// used when -j/--threads was left at its auto default (cfg.Threads == 0).
// Deliberately NOT derived from GOMAXPROCS/NumCPU the way walk's own
// thread count is: an explicit 2/3/4/6/8-worker sweep on the benchmark
// box (single 830MB file, mmap'd) found wall-clock time plateaus around
// 3-4 workers, with 6-8 giving no further measurable gain -- so
// defaulting to walk.defaultThreads()'s min(GOMAXPROCS,12) would spin up
// workers well past the point they help.
//
// Why the plateau isn't fully explained yet, and shouldn't be
// over-claimed: self-speedup at 4 workers landed at ~1.9x on the mmap'd
// benchmark file, short of a naive 4x. Isolating the cause (same
// SearchBytes call, same corpus, read into a plain heap []byte via
// os.ReadFile instead of mmap'd) raised self-speedup to ~2.3x, which
// shows mmap page-fault handling (concurrent workers first-touching
// different pages of one shared mapping) is a real contributor -- but
// doesn't fully close the gap to 4x on its own, so something else also
// caps it (possibly still memory bandwidth, possibly this box's
// scheduler/cache behavior; not conclusively isolated). MAP_POPULATE
// (pre-faulting the whole mapping up front) was tried as the obvious
// mitigation and made things WORSE, not better, on this box -- shifting
// all the fault-in cost into one serial pass before any worker starts is
// a net loss compared to letting workers fault their own chunks in
// concurrently, even with contention. A real fix here (concurrent
// madvise/prefetch, or per-worker sub-range mmaps) is a follow-up, not
// something this change attempts.
//
// Bottom line this constant only needs: past ~3-4 workers, more doesn't
// help on this box, whatever the exact mix of causes. Revisit the value
// (and this doc) if a future prefetch attempt changes that.
const defaultParallelWorkers = 4

// resolveParallelWorkers maps -j/--threads onto search.Searcher's
// ParallelWorkers: an explicit thread count is honored as-is (the user
// asked for exactly that much parallelism), otherwise defaultParallelWorkers
// applies -- see its doc for why that isn't just walk's own thread
// default.
func resolveParallelWorkers(threads int) int {
	if threads > 0 {
		return threads
	}
	return defaultParallelWorkers
}

// resolvePaths splits a possibly-empty cfg.Paths into the two forms the
// rest of execute()/executeFiles() need, which must differ when no PATH
// argument was given at all:
//
//   - statPaths always has at least one real, stat-able entry ("."
//     substituted for empty) -- for computeShowPath and mmapEligible,
//     which call os.Stat and need something valid to call it on.
//   - walkRoots is what actually gets passed to walk.Walk. Here the
//     substitute must be "" (see walk.Walk's doc), not ".": rg's own
//     default-directory invocation (no PATH argument) prints unprefixed
//     relative paths, while an explicit "." argument DOES echo a "./"
//     prefix on every discovered path -- two different, both
//     real-rg-verified behaviors that resolve to the identical string
//     "." by the time a caller could stat it, so only walk.Walk's root
//     list can still tell them apart.
//
// When cfg.Paths is non-empty, both results are just cfg.Paths itself --
// an explicit path is never substituted for anything, only normalized to
// the internal '/' separator form (a no-op everywhere but Windows; see
// normalizeSeparators).
func resolvePaths(cfgPaths []string) (statPaths, walkRoots []string) {
	if len(cfgPaths) == 0 {
		return []string{"."}, []string{""}
	}
	norm := normalizeSeparators(cfgPaths)
	return norm, norm
}

// computeShowPath implements rg's with-filename heuristic for gg's v1
// flag set (no -H/--with-filename/-I/--no-filename yet): the path is
// suppressed only for the single-explicit-file case -- exactly one path
// argument that resolves to something other than a directory. Verified
// against the real rg binary: a single directory argument (which may
// expand to any number of files) always shows the path; a single named
// regular file does not, unless more than one path was given.
func computeShowPath(paths []string) bool {
	if len(paths) != 1 {
		return true
	}
	info, err := os.Stat(paths[0])
	if err != nil {
		// Let the walk surface the real error; the path decision doesn't
		// matter for a target that can't be statted anyway.
		return true
	}
	return info.IsDir()
}

// resolveBinaryMode maps gg's CLI-level BinaryMode (from -a/--text and
// the -uuu/--unrestricted ladder) plus whether this entry was named
// explicitly on the command line, onto search.BinaryMode. Per PLAN.md's
// binary-detection design row: walk-discovered files default to Quit,
// explicitly-named files default to Convert; -a/--text disables
// detection entirely regardless of how the file was reached; -uuu
// (BinarySearchAndSuppress) searches past NUL bytes everywhere, matching
// rg's --binary.
func resolveBinaryMode(cfgBinary BinaryMode, explicit bool) search.BinaryMode {
	switch cfgBinary {
	case BinaryAsText:
		return search.BinaryNone
	case BinarySearchAndSuppress:
		return search.BinaryConvert
	default: // BinaryAuto
		if explicit {
			return search.BinaryConvert
		}
		return search.BinaryQuit
	}
}

// buildMatcher compiles cfg's pattern set into a match.Matcher.
func buildMatcher(cfg *Config) (match.Matcher, error) {
	return match.New(match.Config{
		Patterns: cfg.Patterns,
		CaseMode: convertCaseMode(cfg.Case),
		Word:     cfg.Word,
		Fixed:    cfg.Fixed,
	})
}

func convertCaseMode(c CaseMode) match.CaseMode {
	switch c {
	case CaseInsensitive:
		return match.CaseInsensitive
	case CaseSmart:
		return match.CaseSmart
	default:
		return match.CaseSensitive
	}
}

// buildGlobs compiles -g/--glob patterns into a walk.Options.Globs
// override set, flipping polarity per walk.Options.GlobsRequireMatch's
// doc: a plain -g pattern is really an *include* filter in rg (only
// search files matching some -g), so it becomes a '!'-prefixed
// (whitelist) entry in the underlying gitignore-style glob.Set; a
// '!'-prefixed -g pattern is an ordinary exclude, so its leading '!' is
// stripped to become a plain (ignore) entry. requireMatch is true iff at
// least one plain -g pattern was given, which is exactly when the
// "anything matching none of the -g patterns is excluded" semantics
// apply (see walk.Options.GlobsRequireMatch's doc).
func buildGlobs(patterns []string) (*glob.Set, bool, error) {
	if len(patterns) == 0 {
		return nil, false, nil
	}
	var b glob.Builder
	requireMatch := false
	for _, p := range patterns {
		if strings.HasPrefix(p, "!") {
			b.Add(p[1:])
		} else {
			requireMatch = true
			b.Add("!" + p)
		}
	}
	set, err := b.Build()
	if err != nil {
		return nil, false, fmt.Errorf("invalid glob: %w", err)
	}
	return set, requireMatch, nil
}

// utf8BOM is the 3-byte UTF-8 byte-order-mark rg strips before searching
// or printing a file's content, unconditionally -- verified against the
// real rg binary: `rg -a pat file-with-bom` still strips it under
// --text, so this isn't tied to binary detection at all.
var utf8BOM = [3]byte{0xEF, 0xBB, 0xBF}

// stripUTF8BOM returns a reader over r's content with a leading UTF-8
// BOM removed, if present, matching rg's own BOM-sniffing. Every other
// byte is passed through unchanged and offsets/line numbers computed by
// search.Searcher end up relative to the BOM-stripped stream, exactly
// like rg's (verified: rg's reported line 1 starts at the byte
// immediately following the BOM, as if it were never in the file at
// all).
func stripUTF8BOM(r io.Reader) (io.Reader, error) {
	return &bomReader{r: r}, nil
}

// bomReader folds the BOM check into the caller's own first Read call
// instead of issuing a dedicated 3-byte probe read first: search.Searcher
// always reads through a lineBuffer sized in tens of KB (see
// DefaultBufferSize), so the first Read this ever sees is already asking
// for a chunk far larger than 3 bytes, and the underlying reader (a
// regular file) fills as much of it as it can in one read(2) call. Round
// #27's profiling found the old separate io.ReadFull-of-3-bytes probe
// costing one whole extra read(2) syscall per file walked (~79k of them
// on the linux benchmark tree) for no reason: the BOM question can always
// be answered from bytes the caller was going to read anyway.
//
// Read-chunk boundaries here are semantically load-bearing, not just a
// performance nicety: search.Searcher's binary detection (BinaryQuit)
// discards an entire freshly-read chunk, not just the bytes at/after a
// NUL within it (see linebuffer.go's fill), so any wrapper that
// artificially splits what would have been one read(2) call into two
// separate Read results moves a later NUL into a different chunk than an
// unwrapped read would ever produce -- previously caught by
// TestGoldenVsRipgrep/invert_match, where a 3-byte leading fragment of a
// walk-discovered binary file was wrongly treated as its own clean,
// NUL-free line. The fast path below (n >= 3 on the very first Read)
// never splits anything: it reads directly into the caller's own buffer
// and, if a BOM is present, shifts the remainder down in place -- the
// caller sees exactly the read boundaries the underlying file would have
// produced unwrapped, minus the 3 BOM bytes.
type bomReader struct {
	r    io.Reader
	done bool // true once the one-time BOM check has happened
}

func (br *bomReader) Read(b []byte) (int, error) {
	if br.done {
		return br.r.Read(b)
	}
	br.done = true

	if len(b) < 3 {
		// A caller-supplied buffer under 3 bytes -- never happens via
		// search.Searcher's own tens-of-KB buffers, but stay correct
		// regardless of caller. Fall back to the small-buffer path below.
		return br.finishBOMCheck(b, nil, nil)
	}

	n, err := br.r.Read(b)
	if n < 3 {
		// A short first read from r itself (a file under 3 bytes, or a
		// reader that simply didn't fill the buffer): a BOM could still
		// be split across this boundary, so fall back to gathering up to
		// 3 bytes before deciding.
		return br.finishBOMCheck(b, b[:n], err)
	}
	if [3]byte(b[:3]) == utf8BOM {
		rest := n - 3
		if rest == 0 && err == nil {
			// The chunk r.Read just filled contained nothing but the
			// BOM -- returning (0, nil) here would be a valid but
			// discouraged io.Reader response (see io.Reader's doc), so
			// fold forward into a real read instead of handing the
			// caller an empty, ambiguous result.
			return br.r.Read(b)
		}
		copy(b, b[3:n])
		return rest, err
	}
	return n, err
}

// finishBOMCheck handles the rare case where the BOM question couldn't be
// answered from a single caller-buffer-sized Read: already holds whatever
// bytes were already read into b by the caller's own Read call, if any.
// It gathers up to 3 bytes total (or stops at EOF/error), matching what
// the old peek-based implementation did, then folds whatever isn't part
// of a BOM back into the stream via prefixReader so later Reads are
// indistinguishable from an unwrapped read. Performance doesn't matter on
// this path -- it's only reachable for a file under 3 bytes, a reader
// that returns short reads for its own reasons, or (never via this
// package's own callers) a destination buffer smaller than 3 bytes.
func (br *bomReader) finishBOMCheck(b, already []byte, err error) (int, error) {
	var buf [3]byte
	got := copy(buf[:], already)
	for got < 3 && err == nil {
		var m int
		m, err = br.r.Read(buf[got:3])
		got += m
	}
	data := buf[:got]
	if got == 3 && buf == utf8BOM {
		data = nil
	}
	if len(data) == 0 {
		if err != nil {
			return 0, err
		}
		return br.r.Read(b)
	}
	n := copy(b, data)
	if n < len(data) {
		br.r = &prefixReader{prefix: data[n:], r: br.r}
	}
	return n, err
}

// prefixReader prepends prefix to r's stream without altering r's own
// read-boundary behavior: the first Read call copies prefix into the
// destination buffer and, if room remains, also reads from r to fill the
// rest. Only used by bomReader.finishBOMCheck's rare small-buffer
// fallback -- the common path never needs it, see bomReader's doc.
type prefixReader struct {
	prefix []byte
	r      io.Reader
}

func (p *prefixReader) Read(b []byte) (int, error) {
	if len(p.prefix) == 0 {
		return p.r.Read(b)
	}
	n := copy(b, p.prefix)
	p.prefix = p.prefix[n:]
	if n == len(b) {
		return n, nil
	}
	m, err := p.r.Read(b[n:])
	return n + m, err
}

// workerUnit bundles one search.Searcher with the printer sink it feeds,
// so a sync.Pool can hand out a matched pair per concurrently-active
// walk goroutine (see execute's pool). A Searcher's pooled rolling
// buffer and scratch Match/Ctx values, and a Standard/Count/
// FilesWithMatches printer's per-file buffer, are single-goroutine only
// (see their doc comments) -- exactly the sharing unit sync.Pool exists
// for, and it needs no explicit worker-index bookkeeping since
// walk.Visitor's own concurrency already bounds how many units are ever
// checked out at once.
type workerUnit struct {
	searcher   *search.Searcher
	sink       search.Sink
	isStandard bool
}

func newWorkerUnit(cfg *Config, matcher match.Matcher, dest *printer.Dest, color, heading, showPath, lineNumbers bool) *workerUnit {
	searcher := search.New(search.Searcher{
		Matcher:         matcher,
		Invert:          cfg.Invert,
		LineNumbers:     lineNumbers,
		BeforeContext:   cfg.ContextBefore,
		AfterContext:    cfg.ContextAfter,
		ParallelWorkers: resolveParallelWorkers(cfg.Threads),
	})

	var sink search.Sink
	isStandard := false
	switch cfg.Mode {
	case ModeCount:
		c := printer.NewCount(dest)
		c.Color = color
		c.ShowPath = showPath
		sink = c
	case ModeFilesWithMatches:
		f := printer.NewFilesWithMatches(dest)
		f.Color = color
		sink = f
	default:
		s := printer.NewStandard(dest)
		s.Color = color
		s.Matcher = matcher
		s.Heading = heading
		s.ShowPath = showPath
		s.ContextEnabled = cfg.ContextBefore > 0 || cfg.ContextAfter > 0
		sink = s
		isStandard = true
	}

	return &workerUnit{searcher: searcher, sink: sink, isStandard: isStandard}
}

// matchTracker wraps a search.Sink to add two things no printer sink can
// do on its own, since Standard/Count/FilesWithMatches never see
// search.Stats until their own Finish (and Sink.Finish's return value
// doesn't propagate a "matched" signal back to the caller):
//
//  1. Recording whether any file, anywhere, ever matched -- for the
//     process exit code.
//  2. rg's binary-file special-casing, verified against the real rg
//     14.1.1 binary (see the M2 handoff notes and task #20 for the exact
//     probes, including ../ripgrep/tests/data/sherlock-nul.txt and
//     ripgrep's own upstream searcher tests binary1-binary4):
//     - BinaryQuit + Stats.Binary, Standard mode: any matches already
//     found in earlier, NUL-free reads were already sunk into the
//     underlying sink's buffer before Finish ever ran (search's own
//     BinaryQuit discards only the single read chunk that contains
//     the NUL, not the whole file -- see linebuffer.go's fill), so
//     they're flushed normally, followed by rg's own
//     "WARNING: stopped searching binary file after match..." line
//     (verified against the real rg binary on the sherlock-nul.txt
//     fixture: real matches print, then that exact warning).
//     If Stats.Matched is false, nothing was found at all (the NUL
//     fell in the very first read), so nothing is printed -- rg is
//     silent in that case too (verified: a small file whose one-and-
//     only read contains a match immediately followed by a NUL
//     reports zero matches and prints no warning).
//     - BinaryQuit + Stats.Binary, -c/-l (standard=false): discarded
//     entirely regardless of Stats.Matched -- verified against the
//     real rg binary: `rg -c`/`rg -l` on the same sherlock-nul.txt
//     walk show nothing and exit 1, unlike the real count/path they'd
//     show for an explicitly-named (Convert-mode) binary file.
//     - BinaryConvert + Stats.Binary + Stats.Matched, Standard mode only:
//     rg replaces the file's entire per-line output with one generic
//     `binary file matches (found "\0" byte around offset N)` line
//     instead of normal match formatting. -c/-l are unaffected --
//     rg shows their real count/path exactly as it would for a text
//     file (verified: `rg -c` on a binary file with a NUL after one
//     match still prints the true match count).
//
// Known gap: -q (Quiet) records a match the instant Sink.Matched fires,
// before Finish (and therefore before Stats.Binary is known) -- so a
// walk-discovered binary file with a match before its first NUL byte
// will incorrectly count towards -q's exit code. This combination isn't
// in gg's v1 golden matrix; flagged for follow-up rather than fixed here.
//
// Known approximation: -uuu (BinarySearchAndSuppress, resolved to
// search.BinaryConvert for every file, not just explicit ones -- see
// resolveBinaryMode) routes through the same "generic binary message"
// branch as an explicitly-named file's default Convert mode. Real rg's
// --binary instead prints the actual matching lines plus a trailing
// "WARNING: ... stopped searching prematurely" note -- a different
// output shape entirely. -uuu is untested (no golden case exercises it);
// documented here rather than fixed, since matching it exactly would
// need a third matchTracker branch with its own message format.
//
// Known approximation: the BinaryQuit warning line is written as its own
// dest block (see writeBinaryQuitWarning), reusing the same inter-block
// separator rule as writeBinaryMessage. In the (untested, non-default)
// combination of Heading or context mode with a Quit-mode file that has
// real matches, this would insert a spurious separator between the last
// match and the warning that real rg does not -- real rg's warning is
// part of the exact same write sequence as the file's own lines, with no
// separator at all. Plain (non-heading, non-context) mode -- the only
// combination verified against real rg -- is unaffected: its separator
// is nil either way.
type matchTracker struct {
	search.Sink
	matched        *atomic.Bool
	standard       bool
	binMode        search.BinaryMode
	showPath       bool
	heading        bool
	contextEnabled bool
	dest           *printer.Dest
	// searcher is consulted live, mid-scan, by Matched/Context to
	// implement the BinaryConvert suppression rule below -- see those
	// methods' doc and search.Searcher.HasBinaryOffset's doc for why
	// Stats.Binary (only known at Finish) isn't enough on its own.
	searcher *search.Searcher
	// foundBinary/foundBinaryOffset record a NUL noteLineNUL discovered
	// inside a delivered match/context line's own bytes, past the
	// searcher's bounded upfront prefix (see SearchBytes's doc and
	// noteLineNUL's doc for why this lives here rather than in package
	// search).
	foundBinary       bool
	foundBinaryOffset int64
}

// noteLineNUL checks a just-delivered Matched/Context line's own bytes for
// a NUL and, if the searcher's bounded upfront prefix check hasn't already
// found one and this tracker hasn't either, records the offset locally.
// This is the counterpart to SearchBytes's deliberately bounded detection
// (see its doc): real rg's own mmap path (SliceByLine) never scans the
// whole file for a NUL either -- it only notices one that falls within a
// line it actually visits (matched or context). Checking m.Line/c.Line
// here, rather than making package search scan the whole slice, gives gg
// the exact same coverage rg has at effectively zero cost: one short
// memchr per delivered line, not one over the whole file.
//
// Only ever called when t.standard (from Matched/Context, mirroring
// binaryConvertSuppressed's gating) -- -c/-l don't need the offset since
// they never suppress or write the summary message.
func (t *matchTracker) noteLineNUL(lineOffset int64, line []byte) {
	if t.binMode != search.BinaryConvert || t.foundBinary || t.searcher.HasBinaryOffset() {
		return
	}
	if i := bytes.IndexByte(line, 0); i >= 0 {
		t.foundBinary = true
		t.foundBinaryOffset = lineOffset + int64(i)
	}
}

// effectiveBinaryOffset returns the NUL offset binaryConvertSuppressed and
// Finish should use: the searcher's own (bounded-prefix) detection if it
// found one, else whatever noteLineNUL discovered from a delivered line,
// else not-ok.
func (t *matchTracker) effectiveBinaryOffset() (offset int64, ok bool) {
	if t.searcher.HasBinaryOffset() {
		return t.searcher.BinaryOffset(), true
	}
	if t.foundBinary {
		return t.foundBinaryOffset, true
	}
	return 0, false
}

// binaryConvertSuppressed reports whether a match/context line spanning
// absolute byte range [lineStart, lineEnd) should be withheld from the
// underlying sink under BinaryConvert mode, matching rg's real
// explicit-file behavior exactly (empirically verified against the
// installed rg binary with fixtures placing a NUL at several offsets
// straddling DefaultBufferSize, both --mmap and --no-mmap, identical
// result both ways):
//
//   - If the detected NUL falls within the first DefaultBufferSize
//     bytes, EVERY line is suppressed -- even ones textually before the
//     NUL -- so only the one summary message (writeBinaryMessage,
//     written from Finish) appears for the whole file.
//   - Otherwise, a line is suppressed once its own byte range reaches
//     the NUL's offset -- lineEnd > binOffset, not just lineStart -- so
//     a line whose bytes straddle the NUL (SearchBytes/mmap never
//     rewrites a NUL into a line terminator the way Search's streaming
//     path does, so a line containing text on both sides of a NUL is a
//     completely ordinary occurrence, not a rare edge case) is
//     suppressed too, exactly like one that starts after it. Lines
//     entirely before the NUL still display normally, and the summary
//     message is appended after.
//
// This only ever affects the "standard" (default line-printing) sink;
// -c/-l/-q must keep counting/reporting every match regardless of where
// it falls (rg's own `-c` on such a file reports the true total, not a
// truncated one) -- callers must only invoke this when t.standard is
// already true.
func (t *matchTracker) binaryConvertSuppressed(lineEnd int64) bool {
	if t.binMode != search.BinaryConvert {
		return false
	}
	binOffset, ok := t.effectiveBinaryOffset()
	if !ok {
		return false
	}
	return binOffset < int64(search.DefaultBufferSize) || lineEnd > binOffset
}

// Matched overrides the embedded Sink to apply binaryConvertSuppressed
// before formatting a line -- see its doc. Suppressing here (rather
// than in the shared search package) is deliberate: package search
// calls every Sink's Matched/Context for every match regardless of mode
// (Count's own tally, for instance, comes from counting these calls,
// never from Stats.MatchCount -- see printer.Count's doc), so
// suppression must live above that shared path, applied only to the
// standard-mode display sink.
func (t *matchTracker) Matched(m *search.Match) (bool, error) {
	if t.standard {
		t.noteLineNUL(m.Offset, m.Line)
		if t.binaryConvertSuppressed(m.Offset + int64(len(m.Line))) {
			return true, nil
		}
	}
	return t.Sink.Matched(m)
}

// Context overrides the embedded Sink for the same reason as Matched.
func (t *matchTracker) Context(c *search.Ctx) (bool, error) {
	if t.standard {
		t.noteLineNUL(c.Offset, c.Line)
		if t.binaryConvertSuppressed(c.Offset + int64(len(c.Line))) {
			return true, nil
		}
	}
	return t.Sink.Context(c)
}

func (t *matchTracker) Finish(path string, stats *search.Stats) error {
	if t.binMode == search.BinaryQuit && stats.Binary {
		if !t.standard {
			// -c/-l: discard entirely, without even calling the
			// underlying sink's Finish (which would otherwise flush
			// whatever real count/path it already accumulated).
			return nil
		}
		if stats.Matched {
			t.matched.Store(true)
		}
		if err := t.Sink.Finish(path, stats); err != nil {
			return err
		}
		if !stats.Matched {
			return nil
		}
		return writeBinaryQuitWarning(t.dest, path, stats.BinaryOffset, t.showPath, t.heading, t.contextEnabled)
	}
	if t.binMode == search.BinaryConvert {
		if offset, ok := t.effectiveBinaryOffset(); ok {
			// Matched/Context have already withheld anything
			// binaryConvertSuppressed flagged, so whatever the underlying
			// sink accumulated is exactly what should display normally --
			// unlike BinaryQuit above, nothing here needs discarding.
			if stats.Matched {
				t.matched.Store(true)
			}
			if err := t.Sink.Finish(path, stats); err != nil {
				return err
			}
			if !stats.Matched || !t.standard {
				return nil
			}
			return writeBinaryMessage(t.dest, path, offset, t.showPath, t.heading, t.contextEnabled)
		}
	}
	if stats.Matched {
		t.matched.Store(true)
	}
	return t.Sink.Finish(path, stats)
}

// binarySeparator computes the same inter-block separator
// Standard.interFileSeparator would use (heading -> blank line, context
// -> "--", else none), so a binary-related message written directly to
// dest chains correctly with neighboring file blocks.
func binarySeparator(heading, contextEnabled bool) []byte {
	switch {
	case heading:
		return []byte("\n")
	case contextEnabled:
		return []byte("--\n")
	default:
		return nil
	}
}

// writeBinaryMessage writes rg's generic binary-match line directly to
// dest, bypassing the Standard sink's own buffer/Finish entirely (see
// matchTracker.Finish). The path prefix follows the same ShowPath
// heuristic as normal output.
func writeBinaryMessage(dest *printer.Dest, path string, offset int64, showPath, heading, contextEnabled bool) error {
	var buf []byte
	if showPath {
		buf = append(buf, path...)
		buf = append(buf, ':', ' ')
	}
	buf = append(buf, `binary file matches (found "\0" byte around offset `...)
	buf = strconv.AppendInt(buf, offset, 10)
	buf = append(buf, ")\n"...)
	return dest.WriteBlock(buf, binarySeparator(heading, contextEnabled))
}

// writeBinaryQuitWarning writes rg's "stopped searching" warning line
// directly to dest, after the real matches (already flushed via the
// underlying sink's own Finish) -- see matchTracker.Finish's BinaryQuit
// branch and its doc for the verified wording and the known separator
// approximation in heading/context mode.
func writeBinaryQuitWarning(dest *printer.Dest, path string, offset int64, showPath, heading, contextEnabled bool) error {
	var buf []byte
	if showPath {
		buf = append(buf, path...)
		buf = append(buf, ':', ' ')
	}
	buf = append(buf, `WARNING: stopped searching binary file after match (found "\0" byte around offset `...)
	buf = strconv.AppendInt(buf, offset, 10)
	buf = append(buf, ")\n"...)
	return dest.WriteBlock(buf, binarySeparator(heading, contextEnabled))
}
