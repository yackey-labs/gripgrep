package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/yackey-labs/gripgrep/filetype"
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
	// --sort/--sortr created is rejected before ANY mode runs (rg checks
	// SortMode::supported() at argument-resolution time, so even --files and
	// --type-list error), because creation time is unavailable on Linux. One
	// stderr line, exit 2 -- matching rg's presence and exit code.
	if cfg.Sort.Kind == SortCreated {
		fmt.Fprintln(stderr, "gg: sorting by creation time isn't supported: creation time is not available on this platform currently")
		return 2
	}
	if cfg.Mode == ModeFiles {
		// --files skips the matcher/searcher pipeline entirely (see
		// executeFiles's doc) -- dispatched before buildMatcher runs
		// since --files needs no pattern at all. -f/--file is irrelevant
		// here too (--files takes no PATTERN of any kind), so
		// resolvePatternFiles is never called on this path.
		return executeFiles(cfg, stdout, stderr)
	}
	if cfg.Mode == ModeTypes {
		// --type-list skips the matcher/searcher/walk pipeline entirely
		// (see executeTypes' doc) -- dispatched before buildMatcher runs
		// since --type-list needs no pattern at all.
		return executeTypes(cfg, stdout, stderr)
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

	// --json is a printer MODE that only applies to the standard search
	// mode: the summary-mode flags (-c/-l/--count-matches/
	// --files-without-match) keep their own plain output (J6/J7/J9), and
	// --files/--type-list already returned above. -q is independent of Mode
	// (it stays ModeStandard), so -q --json still takes this path and emits
	// the summary-only stream (J8).
	useJSON := cfg.JSON && cfg.Mode == ModeStandard

	matcher, err := buildMatcher(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}

	isTTY := isTerminalWriter(stdout)
	color := cfg.Color == ColorAlways || cfg.Color == ColorAnsi || (cfg.Color == ColorAuto && isTTY)

	// column resolves rg's `column.unwrap_or(vimgrep)` (hiargs.rs): an
	// explicit --column/--no-column always wins; otherwise --vimgrep
	// implies it. See Config.Column's doc.
	column := cfg.Vimgrep
	if cfg.Column != nil {
		column = *cfg.Column
	}

	heading := isTTY
	if cfg.Heading != nil {
		heading = *cfg.Heading
	}
	if cfg.Vimgrep {
		// --vimgrep always disables heading, even overriding an explicit
		// --heading given after it (verified against the real rg binary:
		// `rg --vimgrep --heading` still prints the flat per-match format,
		// no heading). Mirrors hiargs.rs's `Some(true) => !low.vimgrep`.
		heading = false
	}

	// --column/--vimgrep default line numbers on, same as rg
	// (hiargs.rs's line_number default includes `|| column || vimgrep`),
	// but an explicit -n/-N still wins outright.
	lineNumbers := isTTY || column || cfg.Vimgrep
	if cfg.LineNumbers != nil {
		lineNumbers = *cfg.LineNumbers
	}
	if useJSON {
		// rg's JSON printer always reports line numbers (its match/context
		// messages always carry a line_number), regardless of TTY or -n/-N
		// -- verified against the answer key, where line_number is never
		// null. Force them on so the field is always a number.
		lineNumbers = true
	}
	econf.LineNumbers = lineNumbers

	statPaths, _ := engine.ResolvePaths(cfg.Paths)
	showPath := computeShowPath(statPaths)
	if cfg.Vimgrep {
		// --vimgrep always forces the path prefix, even for a single
		// explicit file (verified against the real rg binary), unless an
		// explicit -H/-I below overrides it. Mirrors hiargs.rs's
		// `with_filename.unwrap_or(vimgrep || !paths.is_one_file)`.
		showPath = true
	}
	if cfg.WithFilename != nil {
		showPath = *cfg.WithFilename
	}
	contextEnabled := cfg.ContextBefore > 0 || cfg.ContextAfter > 0

	// Output buffering (rg's --line-buffered/--block-buffered): block mode
	// wraps stdout in a buffer flushed once at the end; line mode (and the
	// tty default) writes through directly. Byte-invisible either way (see
	// resolveBlockBuffered) -- the deferred flush below guarantees the
	// buffered bytes reach stdout on every return path.
	out := io.Writer(stdout)
	flush := func() error { return nil }
	if resolveBlockBuffered(cfg.Buffer) {
		bw := bufio.NewWriterSize(stdout, 64*1024)
		out = bw
		flush = bw.Flush
	}

	// bytes-printed counter: installed between Dest and stdout only under
	// --stats in a line-displaying mode, so its total is exactly rg's
	// "bytes printed" (see countingWriter). standardDisplay excludes
	// -c/-l/--count-matches/--files-without-match (non-standard Mode) and
	// -q (no output), all of which rg reports as 0 bytes printed.
	standardDisplay := cfg.Mode == ModeStandard && !cfg.Quiet
	var counter *countingWriter
	destWriter := out
	if cfg.Stats && standardDisplay && !useJSON {
		counter = &countingWriter{w: out}
		destWriter = counter
	}

	dest := printer.NewDest(destWriter)

	// --json carries its own stats (in every end message and the trailing
	// summary), so --stats adds nothing under it (J11) and the text
	// StatsAccumulator/countingWriter machinery is bypassed entirely. The
	// JSON accumulator collects the run-level summary totals instead, folded
	// in by each worker's JSON sink at Finish and written once after the walk.
	var jsonAcc *printer.JSONAccumulator
	if useJSON {
		jsonAcc = printer.NewJSONAccumulator()
	}

	var statsAcc *engine.StatsAccumulator
	if cfg.Stats && !useJSON {
		statsAcc = engine.NewStatsAccumulator()
	}

	// -q's early-exit QuitSink is used only WITHOUT --stats. Under --stats,
	// rg keeps searching a -q run to completion to compute full counts, so
	// -q instead runs an ordinary full walk whose worker sink is the silent
	// discardSink (selected in buildCLISink) -- no QuitSink, no walk-level
	// early stop.
	var quiet *printer.Quiet
	if cfg.Quiet && !cfg.Stats && !useJSON {
		quiet = printer.NewQuiet()
	}

	newWorker := func() *engine.Worker {
		return newEngineWorker(cfg, econf, matcher, dest, jsonAcc, color, heading, showPath, column)
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

	started := time.Now()
	result, err := engine.Run(econf, newWorker, quietSink, nil, bm, statsAcc, stderr)
	if err != nil {
		// A fatal walk-setup error (bad glob) prints NO stats block, unlike
		// a per-file error, which still leaves result usable and prints the
		// (zeroed-or-partial) block below.
		flush()
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}

	// The stats block prints for every search mode that ran (regardless of
	// exit code -- a per-file error path still exits 2 WITH the block, and
	// a zero-match run exits 1 WITH it), snapshotting bytes printed before
	// writing so the block never counts its own bytes. Written to out,
	// above the counter; --files/--type-list never reach here (they return
	// from execute's dispatch above), matching rg's no-block behavior for
	// modes that run no search.
	if useJSON {
		// The "summary" message is always the last line of a --json run
		// (verified: it appears even for a zero-match run, J2, and a run with
		// a per-file error, both of which still exit non-zero below). Written
		// to out, after every per-file block; elapsed_total is the whole-run
		// wall clock.
		if werr := jsonAcc.WriteSummary(out, time.Since(started)); werr != nil {
			flush()
			fmt.Fprintf(stderr, "gg: %s\n", werr)
			return 2
		}
	} else if cfg.Stats {
		var bytesPrinted int64
		if counter != nil {
			bytesPrinted = counter.n
		}
		writeStatsBlock(out, statsAcc.Snapshot(), bytesPrinted, time.Since(started).Seconds())
	}
	if err := flush(); err != nil {
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
	// when no match was ever found. This -q precedence holds even under
	// --stats (verified: `rg -q --stats` over a matching file plus a
	// nonexistent path exits 0, not 2) -- there the fast QuitSink is off
	// (quiet is nil) so the confirmed-match signal is result.Matched from
	// the full walk instead of quiet.Found().
	if cfg.Quiet {
		matched := result.Matched
		if quiet != nil {
			matched = quiet.Found()
		}
		if matched {
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
		Patterns:              cfg.Patterns,
		Case:                  convertCaseMode(cfg.Case),
		Fixed:                 cfg.Fixed,
		Word:                  cfg.Word,
		LineRegexp:            cfg.LineRegexp,
		Paths:                 cfg.Paths,
		Hidden:                cfg.Hidden,
		NoIgnoreDot:           cfg.NoIgnoreDot,
		NoIgnoreExclude:       cfg.NoIgnoreExclude,
		NoIgnoreGlobal:        cfg.NoIgnoreGlobal,
		NoIgnoreParent:        cfg.NoIgnoreParent,
		NoIgnoreVcs:           cfg.NoIgnoreVcs,
		NoRequireGit:          cfg.NoRequireGit,
		NoIgnoreFiles:         cfg.NoIgnoreFiles,
		IgnoreFiles:           cfg.IgnoreFiles,
		IgnoreCaseInsensitive: cfg.IgnoreCaseInsensitive,
		Globs:                 cfg.Globs,
		IGlobs:                cfg.IGlobs,
		GlobCaseInsensitive:   cfg.GlobCaseInsensitive,
		TypeChanges:           convertTypeChanges(cfg.TypeChanges),
		MaxFilesize:           cfg.MaxFilesize,
		MaxDepth:              cfg.MaxDepth,
		FollowSymlinks:        cfg.FollowSymlinks,
		OneFileSystem:         cfg.OneFileSystem,
		NoMessages:            cfg.NoMessages,
		NoIgnoreMessages:      cfg.NoIgnoreMessages,
		Threads:               cfg.Threads,
		Binary:                convertBinaryMode(cfg.Binary),
		Mmap:                  convertMmapMode(cfg.Mmap),
		Invert:                cfg.Invert,
		BeforeContext:         cfg.ContextBefore,
		AfterContext:          cfg.ContextAfter,
		PassThru:              cfg.PassThru,
		MaxCount:              cfg.MaxCount,
		CRLF:                  cfg.CRLF,
		NullData:              cfg.NullData,
		SortKind:              convertSortKind(cfg.Sort.Kind),
		SortReverse:           cfg.Sort.Reverse,
	}
}

// convertSortKind maps cmd/gg's SortKind onto the engine's. SortCreated
// never reaches here -- execute rejects it before any engine.Config is
// built -- so it falls through to SortNone defensively.
func convertSortKind(k SortKind) engine.SortKind {
	switch k {
	case SortPath:
		return engine.SortPath
	case SortModified:
		return engine.SortModified
	case SortAccessed:
		return engine.SortAccessed
	default:
		return engine.SortNone
	}
}

// convertTypeChanges translates cfg.TypeChanges (cmd/gg's own, dependency-
// free TypeChange -- see its doc) into []filetype.Change, ORDER PRESERVED:
// -t/-T/--type-add/--type-clear precedence depends on their exact relative
// CLI order (see filetype.Builder's doc), which this is a 1:1 element-wise
// mapping of, never a regrouping.
func convertTypeChanges(changes []TypeChange) []filetype.Change {
	if len(changes) == 0 {
		return nil
	}
	out := make([]filetype.Change, len(changes))
	for i, c := range changes {
		out[i] = filetype.Change{Kind: convertTypeChangeKind(c.Kind), Arg: c.Arg}
	}
	return out
}

func convertTypeChangeKind(k TypeChangeKind) filetype.ChangeKind {
	switch k {
	case TypeNegate:
		return filetype.Negate
	case TypeAdd:
		return filetype.Add
	case TypeClear:
		return filetype.Clear
	default:
		return filetype.Select
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
func newEngineWorker(cfg *Config, econf engine.Config, matcher match.Matcher, dest *printer.Dest, jsonAcc *printer.JSONAccumulator, color, heading, showPath, column bool) *engine.Worker {
	searcher := engine.NewSearcher(econf, matcher)

	// --json replaces the standard sink entirely (jsonAcc non-nil only when
	// execute resolved useJSON). Its JSON flag tells matchTracker to leave
	// binary handling to the sink -- the JSON printer records binary_offset
	// and clamps bytes_searched itself, and must never emit the plain-text
	// "binary file matches" message (verified, J15) -- while still folding
	// every searched file (even a quarantined binary one) into the summary.
	if jsonAcc != nil {
		j := printer.NewJSON(dest, jsonAcc)
		j.Matcher = matcher
		j.Invert = cfg.Invert
		j.Quiet = cfg.Quiet
		return &engine.Worker{
			Searcher:          searcher,
			Sink:              j,
			Standard:          false,
			JSON:              true,
			InvertMatchSignal: false,
		}
	}

	sink, isStandard := buildCLISink(cfg, dest, matcher, color, heading, showPath, column)
	return &engine.Worker{
		Searcher:          searcher,
		Sink:              sink,
		Standard:          isStandard,
		InvertMatchSignal: cfg.Mode == ModeFilesWithoutMatch,
	}
}

// buildCLISink selects and configures the printer sink cfg.Mode calls
// for (Standard/Count/FilesWithMatches), applying the color/heading/
// showPath/column decisions execute already resolved once per invocation
// (column is passed in already resolved -- see execute's `column :=
// cfg.Vimgrep; if cfg.Column != nil {...}` -- since Config.Column is a
// pointer and its Vimgrep-implied default can't be read off cfg alone).
// This is purely a display concern -- which is why it lives in cmd/gg
// rather than internal/engine (see that package's doc): the root facade
// builds its own, unrelated Sink implementation instead.
func buildCLISink(cfg *Config, dest *printer.Dest, matcher match.Matcher, color, heading, showPath, column bool) (sink search.Sink, isStandard bool) {
	if cfg.Quiet {
		// -q produces no output regardless of Mode. Without --stats this
		// sink is unused (engine.Run swaps in its own early-exit QuitSink);
		// with --stats it IS the worker sink, running a silent full search
		// so the stats accumulator sees every match. isStandard=false keeps
		// matchTracker from doing any standard-mode display work (binary
		// messages, byte counting) on its behalf.
		return discardSink{}, false
	}
	switch cfg.Mode {
	case ModeCount:
		c := printer.NewCount(dest)
		c.Color = color
		c.ShowPath = showPath
		// -o's "count occurrences, not lines" effect on -c -- see
		// printer.Count.OnlyMatching's doc.
		c.OnlyMatching = cfg.OnlyMatching
		c.IncludeZero = cfg.IncludeZero
		c.Null = cfg.Null
		c.CRLF = cfg.CRLF
		c.NullData = cfg.NullData
		c.Matcher = matcher
		return c, false
	case ModeCountMatches:
		// --count-matches is Count with OnlyMatching forced true
		// UNCONDITIONALLY (unlike ModeCount, which only sets it from
		// cfg.OnlyMatching) -- see ModeCountMatches' doc.
		c := printer.NewCount(dest)
		c.Color = color
		c.ShowPath = showPath
		c.OnlyMatching = true
		c.IncludeZero = cfg.IncludeZero
		c.Null = cfg.Null
		c.CRLF = cfg.CRLF
		c.NullData = cfg.NullData
		c.Matcher = matcher
		return c, false
	case ModeFilesWithMatches:
		f := printer.NewFilesWithMatches(dest)
		f.Color = color
		f.Null = cfg.Null
		f.CRLF = cfg.CRLF
		f.NullData = cfg.NullData
		return f, false
	case ModeFilesWithoutMatch:
		f := printer.NewFilesWithoutMatch(dest)
		f.Color = color
		f.Null = cfg.Null
		f.CRLF = cfg.CRLF
		f.NullData = cfg.NullData
		return f, false
	default:
		s := printer.NewStandard(dest)
		s.Color = color
		s.Matcher = matcher
		s.Heading = heading
		s.ShowPath = showPath
		s.ContextEnabled = cfg.ContextBefore > 0 || cfg.ContextAfter > 0
		s.Column = column
		s.Vimgrep = cfg.Vimgrep
		s.ByteOffset = cfg.ByteOffset
		s.OnlyMatching = cfg.OnlyMatching
		s.MaxColumns = cfg.MaxColumns
		s.MaxColumnsPreview = cfg.MaxColumnsPreview
		s.Trim = cfg.Trim
		s.Null = cfg.Null
		s.CRLF = cfg.CRLF
		s.NullData = cfg.NullData
		s.MatchFieldSep = resolveFieldSep(cfg.FieldMatchSeparator, ":")
		s.ContextFieldSep = resolveFieldSep(cfg.FieldContextSeparator, "-")
		s.GapSeparator = resolveGapSeparator(cfg.ContextSeparator)
		return s, true
	}
}

// resolveFieldSep resolves cfg.FieldMatchSeparator/FieldContextSeparator
// (nil = --field-match-separator/--field-context-separator never given)
// into the []byte printer.Standard needs, falling back to rg's own
// default (":" or "-") when unset.
func resolveFieldSep(custom []byte, def string) []byte {
	if custom != nil {
		return custom
	}
	return []byte(def)
}

// resolveGapSeparator resolves cfg.ContextSeparator (nil = --context-
// separator/--no-context-separator never given) into the []byte
// printer.Standard.GapSeparator needs: rg's own default "--" when unset,
// nil (disabled) for --no-context-separator, or the explicit value
// otherwise -- see ContextSep's doc and GapSeparator's doc for the
// nil-means-disabled convention both share.
func resolveGapSeparator(cs *ContextSep) []byte {
	if cs == nil {
		return []byte("--")
	}
	if cs.Disabled {
		return nil
	}
	return cs.Value
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
