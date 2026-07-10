package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/yackey-labs/gripgrep/internal/engine"
	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/printer"
	"github.com/yackey-labs/gripgrep/search"
)

// execute runs cfg's search end to end: it builds the matcher and an
// engine.Config from cfg, then drives internal/engine.Run -- which owns
// the walk/mmap/binary-detection/matching machinery shared with the root
// gripgrep facade (see PLAN.md's "library first, CLI on top" and task
// #30's engine move) -- feeding results to stdout through the printer
// package. It returns the process exit code: 0 if any match was found
// anywhere, 1 if the search completed with no match, 2 on a fatal setup
// error (a bad pattern or glob). Per-file errors encountered during the
// walk (permission denied, a file deleted between readdir and open, ...)
// are reported to stderr and do not change the exit code -- matching rg,
// which keeps walking and only reflects match/no-match in its exit status
// (verified: a permission-denied subdirectory alongside a real match
// still exits 0).
func execute(cfg *Config, stdin io.Reader, stdout, stderr io.Writer) int {
	if cfg.Mode == ModeFiles {
		// --files skips the matcher/searcher pipeline entirely (see
		// executeFiles's doc) -- dispatched before buildMatcher runs
		// since --files needs no pattern at all. -f/--file is irrelevant
		// here too (--files takes no PATTERN of any kind), so
		// resolvePatternFiles is never called on this path.
		return executeFiles(cfg, stdout, stderr)
	}

	if err := resolvePatternFiles(cfg, stdin); err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}
	if len(cfg.Patterns) == 0 {
		// -f/--file was given but every PATTERNFILE turned out empty (and
		// no -e/positional pattern existed either): rg's own behavior
		// here (verified against the real binary: `rg -f empty.txt
		// file` exits 1 with no output and no error) is NOT an error --
		// the pattern set is known, in advance, to match nothing, so the
		// search completes immediately rather than erroring out of
		// match.New's normal "at least one pattern" requirement.
		return 1
	}

	econf := toEngineConfig(cfg)

	matcher, err := buildMatcher(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}

	isTTY := isTerminalWriter(stdout)
	color := cfg.Color == ColorAlways || cfg.Color == ColorAnsi || (cfg.Color == ColorAuto && isTTY)
	heading := isTTY
	if cfg.Heading != nil {
		heading = *cfg.Heading
	}
	lineNumbers := isTTY
	if cfg.LineNumbers != nil {
		lineNumbers = *cfg.LineNumbers
	}
	econf.LineNumbers = lineNumbers

	statPaths, _ := engine.ResolvePaths(cfg.Paths)
	showPath := computeShowPath(statPaths)
	if cfg.WithFilename != nil {
		showPath = *cfg.WithFilename
	}
	contextEnabled := cfg.ContextBefore > 0 || cfg.ContextAfter > 0

	dest := printer.NewDest(stdout)
	var quiet *printer.Quiet
	if cfg.Quiet {
		quiet = printer.NewQuiet()
	}

	newWorker := func() *engine.Worker {
		return newEngineWorker(cfg, econf, matcher, dest, color, heading, showPath)
	}

	// quietSink is passed to Run as an engine.QuitSink only when -q was
	// given; a nil interface value (not a nil *printer.Quiet stored in a
	// non-nil interface) matters here, since Run's own quiet != nil check
	// gates -q's whole-walk early exit and Sink-override behavior -- see
	// Run's doc.
	var quietSink engine.QuitSink
	if quiet != nil {
		quietSink = quiet
	}

	bm := engine.BinaryMessaging{
		Dest:           dest,
		ShowPath:       showPath,
		Heading:        heading,
		ContextEnabled: contextEnabled,
	}

	result, err := engine.Run(econf, newWorker, quietSink, nil, bm, stderr)
	if err != nil {
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
		if result.AnyError {
			return 2
		}
		return 1
	}
	if result.AnyError {
		return 2
	}
	if result.Matched {
		return 0
	}
	return 1
}

// toEngineConfig translates cfg's CLI-level fields into an engine.Config,
// per the config->engine boundary cmd/gg owns (see PLAN.md: "cmd/gg
// contains no logic beyond flags->config"). LineNumbers is left at its
// zero value here -- execute sets it separately once isTTY/-n/-N are
// resolved, since that resolution needs cfg.LineNumbers *and* stdout,
// neither of which this helper has reason to take.
func toEngineConfig(cfg *Config) engine.Config {
	return engine.Config{
		Patterns:            cfg.Patterns,
		Case:                convertCaseMode(cfg.Case),
		Fixed:               cfg.Fixed,
		Word:                cfg.Word,
		LineRegexp:          cfg.LineRegexp,
		Paths:               cfg.Paths,
		Hidden:              cfg.Hidden,
		NoIgnore:            cfg.NoIgnore,
		Globs:               cfg.Globs,
		IGlobs:              cfg.IGlobs,
		GlobCaseInsensitive: cfg.GlobCaseInsensitive,
		MaxFilesize:         cfg.MaxFilesize,
		MaxDepth:            cfg.MaxDepth,
		Threads:             cfg.Threads,
		Binary:              convertBinaryMode(cfg.Binary),
		Mmap:                convertMmapMode(cfg.Mmap),
		Invert:              cfg.Invert,
		BeforeContext:       cfg.ContextBefore,
		AfterContext:        cfg.ContextAfter,
		MaxCount:            cfg.MaxCount,
	}
}

func convertCaseMode(c CaseMode) engine.CaseMode {
	switch c {
	case CaseInsensitive:
		return engine.CaseInsensitive
	case CaseSmart:
		return engine.CaseSmart
	default:
		return engine.CaseSensitive
	}
}

func convertBinaryMode(b BinaryMode) engine.BinaryMode {
	switch b {
	case BinarySearchAndSuppress:
		return engine.BinarySearchAndSuppress
	case BinaryAsText:
		return engine.BinaryAsText
	default:
		return engine.BinaryAuto
	}
}

func convertMmapMode(m MmapMode) engine.MmapMode {
	switch m {
	case MmapAlways:
		return engine.MmapAlways
	case MmapNever:
		return engine.MmapNever
	default:
		return engine.MmapAuto
	}
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

// buildMatcher compiles cfg's pattern set into a match.Matcher via
// internal/engine.BuildMatcher, translating cfg's CLI-level CaseMode
// first.
func buildMatcher(cfg *Config) (match.Matcher, error) {
	return engine.BuildMatcher(toEngineConfig(cfg))
}

// newEngineWorker builds one engine.Worker: a search.Searcher (via
// internal/engine.NewSearcher) paired with the printer sink cfg.Mode
// selects, so engine.Run's own sync.Pool can hand out matched pairs to
// concurrently-active walk goroutines. A Searcher's pooled rolling
// buffer and scratch Match/Ctx values, and a Standard/Count/
// FilesWithMatches printer's per-file buffer, are single-goroutine only
// (see their doc comments) -- exactly the sharing unit sync.Pool exists
// for.
func newEngineWorker(cfg *Config, econf engine.Config, matcher match.Matcher, dest *printer.Dest, color, heading, showPath bool) *engine.Worker {
	searcher := engine.NewSearcher(econf, matcher)

	sink, isStandard := buildCLISink(cfg, dest, matcher, color, heading, showPath)
	return &engine.Worker{Searcher: searcher, Sink: sink, Standard: isStandard}
}

// buildCLISink selects and configures the printer sink cfg.Mode calls
// for (Standard/Count/FilesWithMatches), applying the color/heading/
// showPath decisions execute already resolved once per invocation. This
// is purely a display concern -- which is why it lives in cmd/gg rather
// than internal/engine (see that package's doc): the root facade builds
// its own, unrelated Sink implementation instead.
func buildCLISink(cfg *Config, dest *printer.Dest, matcher match.Matcher, color, heading, showPath bool) (sink search.Sink, isStandard bool) {
	switch cfg.Mode {
	case ModeCount:
		c := printer.NewCount(dest)
		c.Color = color
		c.ShowPath = showPath
		return c, false
	case ModeFilesWithMatches:
		f := printer.NewFilesWithMatches(dest)
		f.Color = color
		return f, false
	default:
		s := printer.NewStandard(dest)
		s.Color = color
		s.Matcher = matcher
		s.Heading = heading
		s.ShowPath = showPath
		s.ContextEnabled = cfg.ContextBefore > 0 || cfg.ContextAfter > 0
		return s, true
	}
}

// resolvePatternFiles implements -f/--file's actual I/O: ParseArgs only
// ever records the raw PATTERNFILE path(s) in cfg.PatternFiles (see
// Config.Patterns's doc for why -- flags.go has no I/O of its own), so
// this reads each one, in order, and appends every line as a pattern.
// Every behavior below is verified against the real rg 15.1.0 binary
// (see the round-31 differential sweep):
//
//   - One pattern per line; a trailing '\r' (CRLF pattern files) is
//     stripped by bufio.Scanner's default split func, matching rg's own
//     line splitting.
//   - An EMPTY line becomes an empty-string pattern (matches every line,
//     same as an empty -e value) -- never skipped.
//   - "-" reads patterns from stdin. Unlike a PATH positional of "-"
//     (which gg does not special-case as "search stdin" -- no v1 flag
//     reaches that path), this never collides with searching stdin:
//     rg's own resolution (verified) is that -f - consumes stdin ONLY
//     for patterns, and a bare invocation with no PATH argument falls
//     back to searching the current directory exactly as it would
//     without -f, never stdin.
//   - A PATTERNFILE that can't be opened is a fatal setup error, handled
//     by execute's existing "gg: %s\n" + exit 2 path, matching rg's own
//     "<path>: No such file or directory" + exit 2.
//   - An entirely empty (zero-line) pattern file contributes zero
//     patterns -- not an error on its own; execute's caller checks
//     whether the FINAL combined pattern set ends up empty.
func resolvePatternFiles(cfg *Config, stdin io.Reader) error {
	for _, path := range cfg.PatternFiles {
		r, closer, err := openPatternFile(path, stdin)
		if err != nil {
			return err
		}
		if r == nil {
			// "-" with a nil stdin (e.g. a test harness that doesn't wire
			// one up): treat as contributing zero patterns rather than
			// panicking on a nil Reader.
			continue
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			cfg.Patterns = append(cfg.Patterns, sc.Text())
		}
		serr := sc.Err()
		if closer != nil {
			closer.Close()
		}
		if serr != nil {
			return fmt.Errorf("%s: %w", path, serr)
		}
	}
	return nil
}

// openPatternFile resolves one -f/--file PATTERNFILE argument to a
// Reader: "-" is stdin (closer is nil -- stdin is never closed here),
// anything else is opened from disk (closer is the same *os.File,
// returned separately from r so callers can defer-free error handling
// without an io.Reader-to-io.Closer type assertion).
func openPatternFile(path string, stdin io.Reader) (r io.Reader, closer io.Closer, err error) {
	if path == "-" {
		return stdin, nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, f, nil
}
