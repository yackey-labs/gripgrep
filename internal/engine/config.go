// Package engine holds the search/walk orchestration shared by cmd/gg and
// the root gripgrep facade -- the "engine" PLAN.md's architecture section
// describes as sitting between the low-level packages (glob/walk/match/
// search/printer) and any presentation layer. It lives under internal/ so
// that neither the CLI's flag surface nor its printer-formatting concerns
// leak into it, and so the exported API stays exactly what the facade in
// the root package chooses to re-expose (see PLAN.md's "library first, CLI
// on top" architecture and Task #30's move of cmd/gg/wire.go's engine
// pieces out of package main).
//
// Package main (cmd/gg) and the root gripgrep package are different Go
// packages -- unexported symbols in one are unreachable from the other --
// so this shared machinery has to live somewhere both can import; internal/
// is the standard Go answer, and it also keeps the engine's driver API
// (Run/Files/BuildMatcher/NewSearcher, all sink-driven) out of the public
// surface the facade curates. This is a deliberate deviation from "moved
// into the root package" in Task #30's literal wording, for that reason.
package engine

import (
	"github.com/yackey-labs/gripgrep/filetype"
	"github.com/yackey-labs/gripgrep/search"
)

// CaseMode mirrors cmd/gg's CaseMode (itself mirroring rg's): last of
// -i/-s/-S wins. A separate type from cmd/gg's own CaseMode, translated at
// the cmd/gg -> engine boundary (see cmd/gg's convertCaseMode-equivalent
// call site) -- and likewise translated by the root facade from its own
// Options -- so this package has no dependency on either caller's flag or
// option surface.
type CaseMode int

const (
	CaseSensitive CaseMode = iota
	CaseInsensitive
	CaseSmart
)

// BinaryMode mirrors cmd/gg's BinaryMode (rg's --binary/-a/-uuu ladder,
// already resolved to one of these three by the caller): BinaryAuto lets
// Run/Files pick per-file (Quit for walk-discovered, Convert for
// explicitly named -- see resolveBinaryMode), BinarySearchAndSuppress is
// rg's --binary (always Convert), BinaryAsText is -a/--text (detection
// off).
type BinaryMode int

const (
	BinaryAuto BinaryMode = iota
	BinarySearchAndSuppress
	BinaryAsText
)

// MmapMode mirrors cmd/gg's MmapMode (rg's --mmap/--no-mmap).
type MmapMode int

const (
	MmapAuto MmapMode = iota
	MmapAlways
	MmapNever
)

// Config is the plain-data input to BuildMatcher, NewSearcher, Run, and
// Files. Every field here is engine-level (no CLI flags, no printer/
// display concerns) so both cmd/gg and the root facade can populate one
// from their own, differently-shaped configuration without either
// depending on the other -- see Config's doc on the package as a whole.
// Not every function reads every field (Run, for instance, never touches
// Patterns/Case/Fixed/Word: those only matter to BuildMatcher), mirroring
// how cmd/gg's own single Config was already threaded through multiple
// functions before this move.
type Config struct {
	// Patterns are OR'd together; consumed only by BuildMatcher.
	Patterns []string
	Case     CaseMode
	Fixed    bool // -F/--fixed-strings
	// Word and LineRegexp mirror cmd/gg's own mutually-exclusive pair
	// (rg's shared BoundaryMode field): a caller must never set both --
	// cmd/gg's -w/-x flagSpecs guarantee this via last-flag-wins before
	// translating into this struct.
	Word       bool // -w/--word-regexp
	LineRegexp bool // -x/--line-regexp

	// Paths are the files/directories to search (or list, for Files);
	// empty means "search/list the current directory" (see ResolvePaths).
	Paths       []string
	Hidden      bool     // --hidden
	NoIgnore    bool     // --no-ignore
	Globs       []string // -g/--glob, verbatim (leading '!' is glob polarity, see buildGlobs)
	// IGlobs are --iglob GLOB, verbatim (same '!' polarity as Globs).
	// Always matched case-insensitively, regardless of
	// GlobCaseInsensitive (rg parity: verified against the real rg
	// binary -- an --iglob pattern is case-insensitive even when
	// --glob-case-insensitive was never given). Combined with Globs by
	// buildGlobs into one ordered pattern list with Globs always FIRST
	// and IGlobs always LAST for last-match-wins precedence, regardless
	// of the two flags' actual relative order on the command line --
	// verified against the real rg binary (crates/core/flags/hiargs.rs's
	// globs(): every -g pattern is added to the override builder before
	// any --iglob pattern, unconditionally).
	IGlobs []string
	// GlobCaseInsensitive is --glob-case-insensitive: makes every Globs
	// pattern (not IGlobs, which is already always case-insensitive)
	// match case-insensitively, equivalent to typing each -g pattern as
	// --iglob instead (rg's own doc for this flag says exactly that).
	GlobCaseInsensitive bool
	MaxFilesize         int64 // 0 = unlimited
	// MaxDepth is -d/--max-depth: nil = unlimited, passed straight
	// through to walk.Options.MaxDepth (see its doc for the pointer
	// rationale, identical to MaxCount below).
	MaxDepth *int

	// TypeChanges are -t/-T/--type-add/--type-clear, in exact CLI order
	// (order matters -- see filetype.Builder's doc). Empty means no
	// -t/-T/--type-add/--type-clear at all, which buildTypes fast-paths
	// to a nil *filetype.Matcher (walk.Options.Types stays nil, costing
	// nothing per entry -- see walk.Options.Types' doc).
	TypeChanges []filetype.Change

	Threads int        // -j/--threads; 0 = auto
	Binary  BinaryMode // resolved binary-detection policy
	Mmap    MmapMode   // --mmap/--no-mmap

	// Invert/LineNumbers/BeforeContext/AfterContext are Searcher
	// construction inputs, consumed only by NewSearcher. LineNumbers is
	// already resolved to a concrete bool by the caller (cmd/gg derives
	// it from isatty(stdout) and -n/-N; the facade always wants it true)
	// -- this package has no TTY-detection concern of its own.
	Invert        bool
	LineNumbers   bool
	BeforeContext int
	AfterContext  int
	// MaxCount is -m/--max-count, passed straight through to
	// search.Searcher.MaxCount (see its doc): nil means unlimited; a
	// non-nil 0 is a legitimate "match nothing" limit, which is why this
	// isn't a plain int with the 0-means-unlimited convention
	// BeforeContext/AfterContext use above.
	MaxCount *int
}

// defaultParallelWorkers is the intra-file parallel-search worker count
// used when -j/--threads was left at its auto default (Config.Threads ==
// 0). Deliberately NOT derived from GOMAXPROCS/NumCPU the way walk's own
// thread count is: an explicit 2/3/4/6/8-worker sweep on the benchmark box
// (single 830MB file, mmap'd) found wall-clock time plateaus around 3-4
// workers, with 6-8 giving no further measurable gain -- so defaulting to
// walk.defaultThreads()'s min(GOMAXPROCS,12) would spin up workers well
// past the point they help.
//
// Why the plateau isn't fully explained yet, and shouldn't be
// over-claimed: self-speedup at 4 workers landed at ~1.9x on the mmap'd
// benchmark file, short of a naive 4x. Isolating the cause (same
// SearchBytes call, same corpus, read into a plain heap []byte via
// os.ReadFile instead of mmap'd) raised self-speedup to ~2.3x, which shows
// mmap page-fault handling (concurrent workers first-touching different
// pages of one shared mapping) is a real contributor -- but doesn't fully
// close the gap to 4x on its own, so something else also caps it (possibly
// still memory bandwidth, possibly this box's scheduler/cache behavior;
// not conclusively isolated). MAP_POPULATE (pre-faulting the whole mapping
// up front) was tried as the obvious mitigation and made things WORSE, not
// better, on this box -- shifting all the fault-in cost into one serial
// pass before any worker starts is a net loss compared to letting workers
// fault their own chunks in concurrently, even with contention. A real fix
// here (concurrent madvise/prefetch, or per-worker sub-range mmaps) is a
// follow-up, not something this change attempts.
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

// resolveBinaryMode maps Config.Binary (already resolved from -a/--text
// and the -uuu ladder by the caller) plus whether this entry was named
// explicitly on the command line, onto search.BinaryMode. Per PLAN.md's
// binary-detection design row: walk-discovered files default to Quit,
// explicitly-named files default to Convert; BinaryAsText disables
// detection entirely regardless of how the file was reached;
// BinarySearchAndSuppress (rg's --binary) searches past NUL bytes
// everywhere, matching rg's -uuu.
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
