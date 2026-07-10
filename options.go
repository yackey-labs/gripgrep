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
}

// Options controls a search's behavior, mirroring gg's/rg's own CLI
// flags in name and default: the zero value is exactly what the CLI
// does with no flags at all (recursive, gitignore-aware, case-sensitive,
// binary-file filtering, no context, auto worker count).
type Options struct {
	IgnoreCase   bool // -i/--ignore-case
	SmartCase    bool // -S/--smart-case; wins over IgnoreCase if both are set (rg's own -i -S ordering can't be expressed by two independent bools, so this struct picks the more specific flag deterministically rather than reproducing "last one wins")
	Word         bool // -w/--word-regexp
	FixedStrings bool // -F/--fixed-strings

	Hidden   bool     // --hidden
	NoIgnore bool     // --no-ignore
	Globs    []string // -g/--glob, repeatable; a leading '!' negates, exactly like the CLI

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
	return engine.Config{
		Patterns:      []string{pattern},
		Case:          o.caseMode(),
		Fixed:         o.FixedStrings,
		Word:          o.Word,
		Paths:         paths,
		Hidden:        o.Hidden,
		NoIgnore:      o.NoIgnore,
		Globs:         o.Globs,
		MaxFilesize:   o.MaxFilesize,
		Threads:       o.Workers,
		Binary:        engine.BinaryAuto,
		Mmap:          engine.MmapAuto,
		Invert:        o.InvertMatch,
		LineNumbers:   true,
		BeforeContext: before,
		AfterContext:  after,
	}
}
