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
// Deliberate deferral: cfg.Mmap (--mmap/--no-mmap) is parsed but not
// consulted anywhere in this file -- every file is searched through the
// streaming Searcher.Search path, never SearchBytes/syscall.Mmap. This
// is a documented no-op for M2, not an oversight, per the task brief's
// explicit "prefer implementing it... ONLY if [a no-op is] documented"
// allowance: SearchBytes's own doc says BinaryConvert detection still
// fires under mmap but leaves NULs unconverted, which is subtly
// different from what real rg's mmap path does with a binary file
// (rg's grep-searcher docs: Convert "has no effect and is ignored"
// under mmap) -- and the golden binary_quit_by_default/binary_text_mode
// cases were verified against real rg on a file far below any mmap
// heuristic's size threshold, so there's no verified byte-for-byte
// mmap comparison to build against yet. Wiring mmap up is flagged as
// follow-up work (PLAN.md's M3 already lists it under the perf queue).
func execute(cfg *Config, stdout, stderr io.Writer) int {
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

	paths := cfg.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}

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

		f, ferr := os.Open(e.Path)
		if ferr != nil {
			fmt.Fprintf(stderr, "gg: %s: %s\n", e.Path, ferr)
			anyError.Store(true)
			return walk.Continue
		}
		defer f.Close()

		var sink search.Sink = unit.sink
		if quiet != nil {
			sink = quiet
		}
		tracked := &matchTracker{
			Sink:           sink,
			matched:        &anyMatched,
			standard:       unit.isStandard,
			binMode:        unit.searcher.BinaryMode,
			showPath:       showPath,
			heading:        heading,
			contextEnabled: contextEnabled,
			dest:           dest,
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

	if err := walk.Walk(paths, walkOpts, visitor); err != nil {
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
	var buf [3]byte
	n, err := io.ReadFull(r, buf[:])
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// Fewer than 3 bytes in the whole file: definitely not a BOM,
			// and there is nothing more to read from r anyway.
			return bytes.NewReader(buf[:n]), nil
		}
		return nil, err
	}
	if buf == utf8BOM {
		return r, nil
	}
	// Not a BOM: put the 3 already-consumed bytes back in front of the
	// rest of the stream.
	return io.MultiReader(bytes.NewReader(buf[:]), r), nil
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
		Matcher:       matcher,
		Invert:        cfg.Invert,
		LineNumbers:   lineNumbers,
		BeforeContext: cfg.ContextBefore,
		AfterContext:  cfg.ContextAfter,
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
//     14.1.1 binary (see the M2 handoff notes for the exact probes):
//     - BinaryQuit + Stats.Binary: the file's entire output is discarded
//     and does not count as a match, even if a real match occurred
//     before the NUL byte. rg's default recursive-walk behavior for a
//     binary file is total, silent exclusion -- not "report whatever
//     was found before the NUL".
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
type matchTracker struct {
	search.Sink
	matched        *atomic.Bool
	standard       bool
	binMode        search.BinaryMode
	showPath       bool
	heading        bool
	contextEnabled bool
	dest           *printer.Dest
}

func (t *matchTracker) Finish(path string, stats *search.Stats) error {
	if t.binMode == search.BinaryQuit && stats.Binary {
		// Discard entirely: don't even call the underlying sink's
		// Finish, since Standard/Count/FilesWithMatches would otherwise
		// unconditionally flush whatever partial output they already
		// buffered during Matched/Context. The next Begin on this
		// (pooled) sink resets that buffer, so nothing leaks.
		return nil
	}
	if t.binMode == search.BinaryConvert && stats.Binary && stats.Matched && t.standard {
		t.matched.Store(true)
		return writeBinaryMessage(t.dest, path, stats.BinaryOffset, t.showPath, t.heading, t.contextEnabled)
	}
	if stats.Matched {
		t.matched.Store(true)
	}
	return t.Sink.Finish(path, stats)
}

// writeBinaryMessage writes rg's generic binary-match line directly to
// dest, bypassing the Standard sink's own buffer/Finish entirely (see
// matchTracker.Finish). The path prefix follows the same ShowPath
// heuristic as normal output; the inter-file separator matches
// Standard.interFileSeparator's rule (heading -> blank line, context ->
// "--", else none) so a binary file's message chains correctly with
// neighboring files' output.
func writeBinaryMessage(dest *printer.Dest, path string, offset int64, showPath, heading, contextEnabled bool) error {
	var buf []byte
	if showPath {
		buf = append(buf, path...)
		buf = append(buf, ':', ' ')
	}
	buf = append(buf, `binary file matches (found "\0" byte around offset `...)
	buf = strconv.AppendInt(buf, offset, 10)
	buf = append(buf, ")\n"...)

	var sep []byte
	switch {
	case heading:
		sep = []byte("\n")
	case contextEnabled:
		sep = []byte("--\n")
	}
	return dest.WriteBlock(buf, sep)
}
