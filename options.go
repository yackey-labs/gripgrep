package gripgrep

import "github.com/yackey-labs/gripgrep/internal/engine"

// Match is one matched line, returned by Search/SearchStream. Every
// field is an independent copy -- see the package doc's memory-safety
// note -- so a Match is safe to keep around after the call that produced
// it returns.
type Match struct {
	// Path is the file this match came from, relative to the search
	// root(s) exactly like the CLI's own output paths.
	Path string
	// LineNumber is 1-based. It is 0 only if the searcher genuinely
	// couldn't attribute the match to a line (never happens via this
	// package's own calls, which always request line numbers; kept as a
	// documented possibility rather than a promise, since Match is also
	// what a future streaming context event might reuse).
	LineNumber int
	// Line is the matched line's text, without its trailing newline.
	Line string
	// Before holds up to Options.Before (or Options.Context) lines of
	// leading context, oldest first. Nil when no context was requested.
	Before []string
	// After holds up to Options.After (or Options.Context) lines of
	// trailing context, in file order. Nil when no context was
	// requested. Populated after Search/SearchStream has read enough
	// further lines to know it -- see SearchStream's doc for what that
	// means for early-stop timing.
	After []string
	// Column is the 1-based BYTE column (not rune column -- rg/gg count
	// bytes throughout, exactly like LineNumber counts lines, not
	// display rows) of the FIRST match on Line, mirroring the CLI's
	// --column semantics. It is computed by re-scanning Line through the
	// same matcher that found it in the first place, since the search
	// layer deliberately never carries match bounds through to a Sink
	// (see printer's findSpans doc for why: only callers that actually
	// need exact spans -- --column, --vimgrep, match coloring -- pay for
	// locating them, and this package is now one of those callers). 0
	// means "no column": either the line genuinely has no matchable span
	// -- which is exactly what happens for a line reported by an
	// Options.InvertMatch search (the pattern does NOT match such a
	// line, by definition, so there is nothing to report a column for) --
	// or LineNumber is also 0 for the same "couldn't attribute" reason.
	// This mirrors the CLI's own `--column -v`, which omits the column
	// field entirely.
	Column int
	// ByteOffset is the absolute byte offset of Line's FIRST byte within
	// its file, mirroring the CLI's plain -b/--byte-offset -- NOT -o -b's
	// per-OCCURRENCE offset. Match is inherently line-granular (one Match
	// per matched line, however many times the pattern occurs on it), so
	// there is no second, occurrence-level offset to report here; a
	// caller that wants one needs Column (the first occurrence only) or
	// the low-level search/printer packages directly.
	ByteOffset int64
}

// Options controls a search's behavior, mirroring gg's/rg's own CLI
// flags in name and default: the zero value is exactly what the CLI
// does with no flags at all (recursive, gitignore-aware, case-sensitive,
// binary-file filtering, no context, auto worker count).
type Options struct {
	IgnoreCase   bool // -i/--ignore-case
	SmartCase    bool // -S/--smart-case; wins over IgnoreCase if both are set (rg's own -i -S ordering can't be expressed by two independent bools, so this struct picks the more specific flag deterministically rather than reproducing "last one wins")
	Word         bool // -w/--word-regexp; see LineRegexp's doc for the tie-break if both are set
	FixedStrings bool // -F/--fixed-strings

	// LineRegexp is -x/--line-regexp. Word and LineRegexp mirror the
	// engine's single shared boundary mode (match.Config's doc: callers
	// must never set both) -- the CLI resolves this from -w/-x order
	// (last one given wins), which two independent bools can't
	// reproduce, so -- same tie-break style as SmartCase-vs-IgnoreCase
	// above -- LineRegexp wins if both are set.
	LineRegexp bool

	Hidden   bool     // --hidden
	NoIgnore bool     // --no-ignore
	Globs    []string // -g/--glob, repeatable; a leading '!' negates, exactly like the CLI
	// IGlobs is --iglob, repeatable; same verbatim/negation convention
	// as Globs but always matched case-insensitively regardless of
	// GlobCaseInsensitive, exactly like the CLI (see
	// internal/engine.Config.IGlobs' doc for the combined-ordering
	// rule with Globs).
	IGlobs []string
	// GlobCaseInsensitive is --glob-case-insensitive: makes every Globs
	// pattern (not IGlobs, already always case-insensitive) match
	// case-insensitively.
	GlobCaseInsensitive bool

	// Context sets both Before and After at once, like -C. Before/After
	// each independently override their side when non-zero, like -B/-A
	// -- but unlike the CLI's pointer-tracked flags, this struct can't
	// tell "explicitly set to 0" apart from "unset"; a 0 always means
	// "use Context for this side," which only matters if you want one
	// side to be 0 while Context is non-zero, in which case set the
	// other side's field directly and leave this one at a positive
	// value covering just the side you want (see resolveContext).
	Context int
	Before  int
	After   int

	InvertMatch bool // -v/--invert-match

	// MaxCount is -m/--max-count: 0 = unlimited (CLI default). The
	// engine's own field is *int (nil = unlimited, a non-nil 0 is a
	// legal "match nothing" limit -- see internal/engine.Config.MaxCount's
	// doc), but Options stays a plain int per this package's
	// zero-value-means-default policy: a non-zero value here converts to
	// a pointer at the engine boundary (see toEngineConfig), and the
	// CLI's `-m 0` ("match nothing") is deliberately inexpressible --
	// callers wanting that don't call Search.
	MaxCount int
	// MaxDepth is -d/--max-depth: 0 = unlimited (CLI default), same
	// pointer-conversion rationale as MaxCount above. The CLI's `-d 0`
	// (roots only) is not expressible; pass explicit file paths instead
	// of a directory root if that's what you need.
	MaxDepth int

	MaxFilesize int64 // 0 = unlimited, like the CLI's default
	Workers     int   // -j/--threads; 0 = auto, like the CLI's default
}

// resolveContext mirrors cmd/gg's resolveContext (flags.go) as closely
// as a plain-int Options struct allows: Before/After each override
// Context on their own side when set. See Options.Context's doc for the
// one CLI behavior this can't reproduce (explicitly zeroing one side
// while Context is positive).
func resolveContext(o Options) (before, after int) {
	before, after = o.Context, o.Context
	if o.Before != 0 {
		before = o.Before
	}
	if o.After != 0 {
		after = o.After
	}
	return before, after
}

// caseMode resolves IgnoreCase/SmartCase into engine.CaseMode -- see
// SmartCase's doc for the deterministic tie-break this struct uses in
// place of the CLI's order-dependent last-flag-wins.
func (o Options) caseMode() engine.CaseMode {
	switch {
	case o.SmartCase:
		return engine.CaseSmart
	case o.IgnoreCase:
		return engine.CaseInsensitive
	default:
		return engine.CaseSensitive
	}
}

// intPtrIfSet converts n to *int, following the engine's nil-means-
// unlimited convention (see Options.MaxCount/MaxDepth's docs): a zero
// Options field -- "unset" in this package's zero-value-means-default
// policy -- becomes nil rather than a pointer to 0.
func intPtrIfSet(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// boundaryMode resolves Word/LineRegexp into the (word, lineRegexp) pair
// the engine's shared boundary field allows -- see LineRegexp's doc for
// the deterministic tie-break this struct uses in place of the CLI's
// order-dependent last-flag-wins.
func (o Options) boundaryMode() (word, lineRegexp bool) {
	if o.LineRegexp {
		return false, true
	}
	return o.Word, false
}

// toEngineConfig translates o plus a pattern/path list into an
// engine.Config, the facade's half of the same config->engine boundary
// cmd/gg's own toEngineConfig implements (see internal/engine's doc).
// Binary/Mmap are always left at their CLI-default "Auto" policies --
// this package has no flag surface for -a/--text, -uuu, or --mmap, so
// there is nothing to translate; LineNumbers is unconditionally true,
// since every Match this package returns always carries one (unlike the
// CLI, which only computes them when isatty(stdout) or -n asks for it).
func (o Options) toEngineConfig(pattern string, paths []string) engine.Config {
	before, after := resolveContext(o)
	word, lineRegexp := o.boundaryMode()
	return engine.Config{
		Patterns:            []string{pattern},
		Case:                o.caseMode(),
		Fixed:               o.FixedStrings,
		Word:                word,
		LineRegexp:          lineRegexp,
		Paths:               paths,
		Hidden:              o.Hidden,
		NoIgnore:            o.NoIgnore,
		Globs:               o.Globs,
		IGlobs:              o.IGlobs,
		GlobCaseInsensitive: o.GlobCaseInsensitive,
		MaxFilesize:         o.MaxFilesize,
		MaxDepth:            intPtrIfSet(o.MaxDepth),
		Threads:             o.Workers,
		Binary:              engine.BinaryAuto,
		Mmap:                engine.MmapAuto,
		Invert:              o.InvertMatch,
		LineNumbers:         true,
		BeforeContext:       before,
		AfterContext:        after,
		MaxCount:            intPtrIfSet(o.MaxCount),
	}
}
