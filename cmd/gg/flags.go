package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file implements gg's command-line flag parser. It has no
// dependency on any other gripgrep package (glob/walk/match/search/
// printer) -- it only produces a plain Config value. M2 is responsible
// for translating a Config into calls against those packages.
//
// Per PLAN.md's "1:1 rg compatibility is the contract" addendum, every
// flag gg implements must match ripgrep's naming, short forms, negation
// spelling, argument syntax, and override semantics exactly. Names,
// short flags, negations, and update semantics below were read directly
// from ../ripgrep/crates/core/flags/defs.rs and lowargs.rs (the
// authoritative source named in PLAN.md), then spot-checked against the
// real `rg` 14.1.1 binary on PATH for the ambiguous interactions (see
// flags_test.go doc comments for the exact probe commands and output).
//
// A flag outside gg's v1 scope must never be silently ignored or
// reinterpreted. Two buckets exist for that:
//   - notImplementedFlags: a curated set of real rg flags that are
//     particularly likely to be typed (PLAN.md's explicit "Not in v1"
//     list, plus a few natural neighbors of flags gg does implement).
//     These produce a clear "not yet implemented" error.
//   - anything else unrecognized: a generic "unrecognized flag" error.
// Both are exit 2, matching rg's own convention; gg cannot be
// byte-identical to rg for a flag it refuses in either case, so the
// distinction only changes the error message, not the exit code.

// CaseMode mirrors rg's CaseMode (lowargs.rs): last of -i/-s/-S wins,
// regardless of order, because each directly overwrites the same field.
type CaseMode int

const (
	CaseSensitive CaseMode = iota
	CaseInsensitive
	CaseSmart
)

// SearchMode mirrors the subset of rg's SearchMode/Mode that gg's v1
// scope implements. Like rg's Mode::update, any later mode flag
// overwrites an earlier one -- verified against the real rg binary for
// the case that matters most here: rg's own Mode::update doc comment
// claims a search mode (like -c/-l) can never override a non-search mode
// (like --files), but that is NOT what the binary actually does --
// `rg --files -l` errors ("requires at least one pattern"), while
// `rg -l --files` lists files. Whichever mode-setting flag appears LAST
// wins, full stop, regardless of "search" vs "non-search" -- exactly the
// plain last-write-wins gg already does below for -c/-l, so ModeFiles
// needs no special-case override logic of its own.
type SearchMode int

const (
	ModeStandard SearchMode = iota
	ModeCount
	ModeFilesWithMatches
	// ModeCountMatches is --count-matches: like ModeCount, but always
	// counts OCCURRENCES rather than matched lines -- i.e. the printer
	// behaves as if -o/--only-matching had ALSO been given, unconditionally
	// (not just when cfg.OnlyMatching happens to be set), matching rg's own
	// doc ("ripgrep behaves as if --count-matches was given" is the OTHER
	// direction of this same rule for -o -c -- see round 36's
	// printer.Count.OnlyMatching). Verified against the real rg binary:
	// `--count-matches -v` still falls back to counting LINES, exactly
	// like plain `-c -v` (Count.OnlyMatching's existing max(1,spanCount)
	// fallback already handles this for free -- see its doc).
	ModeCountMatches
	// ModeFilesWithoutMatch is --files-without-match: the exact complement
	// of ModeFilesWithMatches -- lists files with ZERO matches instead of
	// at least one. Exit code polarity is INVERTED from every other mode
	// (verified against the real rg binary: exit 0 iff at least one file
	// was printed, exit 1 iff every file had a real match and nothing was
	// printed) -- see internal/engine's matchTracker, which needs to know
	// about this mode specifically to flip its match-signal aggregation.
	ModeFilesWithoutMatch
	// ModeFiles is --files: list every file that would be searched,
	// without searching it. See the SearchMode doc above for its
	// last-flag-wins interaction with -c/-l, and ParseArgs for how it
	// exempts positionals from the "at least one pattern" requirement.
	ModeFiles
	// ModeTypes is --type-list: print rg's file-type table and exit,
	// without searching or walking anything. Same last-flag-wins and
	// no-pattern-required treatment as ModeFiles (see both docs above);
	// -t/-T/--type-add/--type-clear are FILTER flags (Config.TypeChanges),
	// not mode flags, so combining them with --type-list is legal and
	// still fails closed on a bad -t/--type-add (verified against the
	// real rg binary: `rg -t bogus --type-list` still exits 2 with
	// "unrecognized file type: bogus" -- rg builds its Types matcher
	// unconditionally, before dispatching on Mode).
	ModeTypes
)

// BinaryMode mirrors rg's BinaryMode.
type BinaryMode int

const (
	// BinaryAuto is rg's default: skip-on-NUL for walk-discovered files,
	// convert-on-NUL for explicitly named files. Which of those applies
	// is decided downstream (by search.Searcher), not by this parser.
	BinaryAuto BinaryMode = iota
	// BinarySearchAndSuppress is rg's --binary behavior, reached in v1
	// scope only via the third -u/--unrestricted level (-uuu).
	BinarySearchAndSuppress
	// BinaryAsText is -a/--text: binary detection fully disabled.
	BinaryAsText
)

// ColorMode mirrors rg's --color choices exactly, including "ansi".
// PLAN.md's v1-scope line says "auto|never|always", but the later "1:1
// rg compatibility" addendum is the controlling requirement, and rg
// itself accepts "ansi" as a fourth choice (defs.rs Color::doc_choices).
type ColorMode int

const (
	ColorAuto ColorMode = iota
	ColorNever
	ColorAlways
	ColorAnsi
)

// MmapMode mirrors rg's MmapMode.
type MmapMode int

const (
	MmapAuto MmapMode = iota
	MmapAlways
	MmapNever
)

// BufferMode mirrors rg's --line-buffered/--block-buffered choice
// (flags.rs/hiargs.rs): the default is auto -- line-buffered when stdout
// is a terminal, block-buffered otherwise -- while --line-buffered and
// --block-buffered force one or the other. Both flags (and their
// negations) resolve into this single field, so the LAST one on the
// command line wins, exactly as rg's own single BufferMode value does.
// The choice is byte-invisible in captured output (it only changes flush
// cadence to a live consumer), so it exists purely to be accepted with rg
// parity and to drive gg's honest flush policy -- see cmd/gg's execute.
type BufferMode int

const (
	// BufferAuto is the default and the state both negations restore:
	// line-buffered to a tty, block-buffered to a pipe/file.
	BufferAuto BufferMode = iota
	// BufferLine forces line buffering (--line-buffered).
	BufferLine
	// BufferBlock forces block buffering (--block-buffered).
	BufferBlock
)

// TypeChangeKind identifies which of -t/-T/--type-add/--type-clear one
// TypeChange represents -- mirrors rg's own TypeChange enum (lowargs.rs).
// Defined here, not in package filetype, so this file keeps its "no
// dependency on any other gripgrep package" property (see the top doc
// comment): wire.go translates a []TypeChange into []filetype.Change at
// the cmd/gg -> engine boundary, the same pattern convertCaseMode/
// convertBinaryMode already use for CaseMode/BinaryMode.
type TypeChangeKind int

const (
	// TypeSelect is -t/--type NAME.
	TypeSelect TypeChangeKind = iota
	// TypeNegate is -T/--type-not NAME.
	TypeNegate
	// TypeAdd is --type-add TYPESPEC.
	TypeAdd
	// TypeClear is --type-clear NAME.
	TypeClear
)

// TypeChange is one -t/-T/--type-add/--type-clear flag occurrence, in the
// exact order it appeared on the command line -- order is significant
// (see filetype.Builder's doc, which Config.TypeChanges is threaded into
// unchanged via wire.go).
type TypeChange struct {
	Kind TypeChangeKind
	// Arg is the type NAME for TypeSelect/TypeNegate/TypeClear, or the
	// raw TYPESPEC string for TypeAdd.
	Arg string
}

// ContextSep is the resolved state of one --context-separator/
// --no-context-separator write: Disabled true means --no-context-
// separator (no separator line at all, ever); Disabled false means an
// explicit --context-separator value (Value is already unescaped by
// unescapeSeparator, and may legitimately be empty -- rg's own doc: "an
// empty string still inserts a line break", distinct from Disabled's "no
// line at all"). Plain last-write-wins between the two flags, like every
// other switch/value pair in this file -- whichever's applyX ran last
// simply overwrites Config.ContextSeparator wholesale.
type ContextSep struct {
	Disabled bool
	Value    []byte
}

// Config is the parsed, plain-data result of ParseArgs. It has no
// dependency on any gripgrep library package.
type Config struct {
	// Patterns are OR'd together: either the single first positional
	// (when neither -e/--regexp nor -f/--file was ever given) or every
	// -e/--regexp value plus every line read from every -f/--file
	// PATTERNFILE (in which case ALL positionals become Paths -- see
	// ParseArgs). ParseArgs itself only ever populates Patterns from -e;
	// PatternFiles' contents are read later (I/O, see resolvePatternFiles
	// in wire.go) and appended to Patterns before the matcher is built --
	// this file has no dependency on any other gripgrep package, and no
	// I/O of its own, per its top doc comment.
	Patterns []string
	// PatternFiles are -f/--file PATTERNFILE arguments, in the order
	// given (repeatable); "-" means read from stdin. See Patterns' doc.
	PatternFiles []string
	// Paths are the files/directories to search.
	Paths []string

	Case  CaseMode
	Fixed bool // -F/--fixed-strings
	// Word and LineRegexp mirror rg's single shared "boundary" field
	// (lowargs.rs's BoundaryMode): -w/--word-regexp and -x/--line-regexp
	// each set ONE of these and clear the other, so whichever of -w/-x
	// was given LAST wins outright -- verified against the real rg
	// binary (`rg -x -w` behaves as plain -w; `rg -w -x` behaves as
	// plain -x), not "both apply". See their flagSpecs below.
	Word       bool // -w/--word-regexp
	LineRegexp bool // -x/--line-regexp

	Hidden bool // --hidden
	// The ignore-control cluster. --no-ignore and the first -u level are
	// sugar that set the first five NoIgnore* fields together (see their
	// flagSpecs); each sub-flag is otherwise an independent last-wins bool,
	// mirroring rg's LowArgs exactly.
	NoIgnoreDot     bool // --no-ignore-dot / --ignore-dot
	NoIgnoreExclude bool // --no-ignore-exclude / --ignore-exclude
	NoIgnoreGlobal  bool // --no-ignore-global / --ignore-global
	NoIgnoreParent  bool // --no-ignore-parent / --ignore-parent
	NoIgnoreVcs     bool // --no-ignore-vcs / --ignore-vcs
	NoRequireGit    bool // --no-require-git / --require-git
	// NoIgnoreFiles is --no-ignore-files / --ignore-files: kills every
	// --ignore-file, even ones given after it (position-independent, probe
	// B6). NOT set by --no-ignore (probe B8).
	NoIgnoreFiles bool
	// IgnoreFiles are --ignore-file PATH, repeatable, in the order given.
	IgnoreFiles []string
	// IgnoreCaseInsensitive is --ignore-file-case-insensitive /
	// --no-ignore-file-case-insensitive: matches the per-directory tree
	// ignore sources case-insensitively (not the global or explicit
	// matchers -- probe F3).
	IgnoreCaseInsensitive bool
	Globs                 []string // -g/--glob, in the order given verbatim (leading '!' negation is glob syntax, handled by package glob, not here)
	// IGlobs are --iglob GLOB, repeatable, same verbatim/negation
	// convention as Globs but always matched case-insensitively -- see
	// internal/engine.Config.IGlobs' doc for the combined-ordering rule
	// (Globs always precede IGlobs regardless of CLI order).
	IGlobs []string
	// GlobCaseInsensitive is --glob-case-insensitive/--no-glob-case-
	// insensitive: makes every Globs (not IGlobs, already always
	// case-insensitive) pattern match case-insensitively.
	GlobCaseInsensitive bool
	// TypeChanges are -t/--type, -T/--type-not, --type-add, and
	// --type-clear, each APPENDED in the exact order given on the command
	// line (never grouped by flag) -- see TypeChange's doc for why order
	// is significant. wire.go translates this verbatim into
	// []filetype.Change.
	TypeChanges  []TypeChange
	Unrestricted int   // 0-3: the -u/-uu/-uuu level actually given, kept for diagnostics even though NoIgnore/Hidden/Binary already reflect its effect
	MaxFilesize  int64 // 0 = unlimited (rg's None)
	// MaxDepth is -d/--max-depth: nil = unset/unlimited. A non-nil 0 is a
	// real, legal value (rg parity, verified against the real rg binary:
	// `rg --max-depth 0 dir/` searches nothing) -- NOT the same as
	// unset, mirroring MaxCount's pointer rationale below.
	MaxDepth *int
	// FollowSymlinks is -L/--follow / --no-follow: follow symbolic links
	// discovered during traversal (default off). Explicit symlink PATH
	// arguments are always followed regardless (walk.buildRootTask). Under
	// -L, rg reports symlink loops and broken links as errors (exit 2);
	// --no-messages suppresses those messages without changing the exit.
	FollowSymlinks bool
	// OneFileSystem is --one-file-system / --no-one-file-system: don't
	// cross a file-system boundary while traversing each path's tree
	// (find -xdev). Applies per root argument -- a second root on another
	// file system is still searched fully.
	OneFileSystem bool
	// NoMessages is --no-messages / --messages: suppress per-file/per-path
	// error messages (failed open/read, unreadable directories, symlink
	// loops and broken links under -L) AND ignore-file load warnings. It
	// never changes the exit code -- an error still forces exit 2. Last of
	// --no-messages/--messages wins.
	NoMessages bool
	// NoIgnoreMessages is --no-ignore-messages / --ignore-messages:
	// suppress ignore-file load/parse warnings only (not regular file
	// errors). Either this or --no-messages silences those warnings.
	NoIgnoreMessages bool

	// LineNumbers is nil when neither -n nor -N was given: rg decides
	// the default from isatty(stdout) at runtime, which is an M2/cmd
	// concern, not this parser's.
	LineNumbers *bool
	// WithFilename is nil when neither -H nor -I was given: like
	// LineNumbers, rg decides the default (single-explicit-file vs.
	// everything else) at runtime, an M2/cmd concern -- see wire.go's
	// computeShowPath. -H and -I are TWO SEPARATE rg flags (each its own
	// struct in defs.rs, same shape as -n/-N), not one flag with a
	// negated spelling, so whichever was given LAST simply overwrites
	// this field -- verified against the real rg binary: `rg -I -H`
	// behaves as plain -H, `rg -H -I` behaves as plain -I.
	WithFilename *bool
	// Heading is nil when neither --heading nor --no-heading was given:
	// rg's own default is isatty(stdout), which wire.go already computes
	// (heading := isTTY) before this field can override it.
	Heading *bool
	// Column is nil when neither --column nor --no-column was given: rg's
	// own default is Vimgrep (rg: `low.column.unwrap_or(low.vimgrep)`),
	// which wire.go resolves -- an explicit --column/--no-column always
	// wins over Vimgrep regardless of order (verified against the real rg
	// binary: `rg --vimgrep --no-column` still prints one row per match
	// occurrence, just without the column field).
	Column *bool
	// Vimgrep is --vimgrep: no negation exists in rg (defs.rs asserts
	// "--vimgrep has no negation" in its own update fn), so this is a
	// plain bool, not a pointer -- same shape as Fixed/Invert above. It
	// implies Column (see Column's doc), forces WithFilename true unless
	// explicitly overridden, and forces Heading false unconditionally
	// (even overriding an explicit --heading given after it) -- all
	// resolved in wire.go, not here, matching how LineNumbers/WithFilename/
	// Heading's own isTTY-dependent defaults are resolved outside this
	// dependency-free parser.
	Vimgrep bool
	// ByteOffset is -b/--byte-offset: prints the absolute byte offset of
	// each matched/context line (or, under Vimgrep, of each individual
	// match occurrence -- see printer.Standard.Matched's doc). Negated by
	// --no-byte-offset, matching rg's own plain bool field (unlike Column,
	// there is no Vimgrep-driven default to resolve: byte_offset defaults
	// to false regardless of any other flag).
	ByteOffset bool
	// OnlyMatching is -o/--only-matching: prints only the matched
	// (non-empty... actually empty matches DO print, as a blank row --
	// see printer.Standard.Matched's doc) text, one row per occurrence,
	// instead of the whole line. No negation exists in rg (defs.rs's
	// OnlyMatching::update asserts this), same shape as Vimgrep. Verified
	// against the real rg binary: has no effect at all once --vimgrep is
	// also given (vimgrep already implies "one row per occurrence" and
	// wins outright, regardless of flag order); does not error under -v
	// (rg: inversion has no matches for -o to narrow to, so the whole
	// non-matching line prints, same as without -o); changes -c to count
	// OCCURRENCES rather than matched LINES (rg's docs: "when --count is
	// combined with --only-matching, ripgrep behaves as if --count-matches
	// was given") EXCEPT under -v, where -c still counts lines (verified:
	// `rg -o -c -v` prints line count, not 0, matching plain `-c -v`); has
	// no effect on -l or -q.
	OnlyMatching bool
	// MaxColumns is -M/--max-columns: 0 means unlimited, whether because
	// the flag was never given or because it was explicitly given as
	// "-M0" -- rg folds both into the same None (defs.rs's MaxColumns::
	// update: "when max is 0 ... None"), so a single int with a
	// 0-means-unlimited convention is exact, unlike MaxCount/MaxDepth
	// above (where 0 is a distinct, meaningful value from "unset").
	MaxColumns int
	// MaxColumnsPreview is --max-columns-preview/--no-max-columns-preview:
	// changes an omitted over-long line from a fixed placeholder message
	// to a truncated preview of its own content. Has no effect unless
	// MaxColumns is also set (rg's own doc: "If the max-columns flag is
	// not set, then this has no effect").
	MaxColumnsPreview bool
	// Trim is --trim/--no-trim: strips leading ASCII whitespace (space,
	// tab, and -- per rg's own trim_ascii_prefix -- \n, \v, \f, \r, though
	// gg's line-terminator handling means only tab/space are ever
	// actually reachable at a printed line's start) from every printed
	// line, matched or context alike. Verified against the real rg
	// binary: applies BEFORE the MaxColumns length check (a line trimmed
	// under its limit is no longer omitted), but any --column/-b field
	// still reports the position in the UNTRIMMED line -- trimming only
	// ever changes what TEXT gets printed, never the numbers around it.
	// See printer.Standard.Trim's doc for the full breakdown.
	Trim bool
	Mode SearchMode
	// IncludeZero is rg's --include-zero: makes -c/--count-matches print
	// "path:0" for a file with no matches instead of skipping it. No
	// effect on any other Mode (verified against the real rg binary:
	// silently ignored under -l/--files-without-match) and never changes
	// the exit code -- see printer.Count.IncludeZero's doc.
	IncludeZero bool
	// Null is rg's -0/--null: terminates each path with a NUL byte
	// instead of whatever character would normally immediately follow it
	// (the ':'/'-' prelude separator in Standard mode, or the trailing
	// '\n' itself for -l/--files-without-match/--files, where the path is
	// the only field) -- see printer.Standard.Null's doc for the exact
	// per-mode rule, verified against the real rg binary. No negation in
	// rg (defs.rs's Null::update asserts "--null has no negation").
	Null bool
	// FieldMatchSeparator is rg's --field-match-separator: replaces EVERY
	// ':' field separator on a matched line. nil means unset (rg's own
	// default, ":"); a non-nil, possibly EMPTY slice is the user's
	// explicit choice (--field-match-separator='' is legal: rg's own doc
	// says "may be any number of bytes, including zero"). Escape
	// sequences (\t, \xZZ, ...) are already unescaped by the flag's
	// applyValue -- see unescapeSeparator.
	FieldMatchSeparator []byte
	// FieldContextSeparator is rg's --field-context-separator: the same
	// idea as FieldMatchSeparator, but for context lines ('-'). nil
	// means unset (rg's default, "-").
	FieldContextSeparator []byte
	// ContextSeparator is rg's --context-separator/--no-context-separator:
	// nil means unset (rg's own default, "--"); non-nil represents an
	// explicit choice -- see ContextSep's doc for the Disabled/Value
	// split, and printer.Standard.GapSeparator's doc for how this
	// resolves into what actually gets written.
	ContextSeparator *ContextSep
	Quiet            bool // -q/--quiet; independent of Mode, matches rg (quiet suppresses output regardless of search mode)
	Color            ColorMode
	// ContextBefore/ContextAfter are already resolved from rg's
	// independently-tracked -A/-B/-C (see resolveContext); -A/-B always
	// partially override -C's corresponding side, regardless of order.
	// Both are guaranteed 0 whenever PassThru is set (see PassThru's doc
	// and resolveContext).
	ContextBefore int
	ContextAfter  int
	// PassThru is rg's --passthru/--passthrough: mutually exclusive with
	// -A/-B/-C, in the specific sense that whichever of --passthru or an
	// -A/-B/-C flag appears LAST on the command line wins outright,
	// discarding whatever context state came before it -- mirrors rg's
	// own ContextMode enum exactly (crates/core/flags/lowargs.rs:
	// ContextMode::Passthru vs ::Limited are one mutable value, and
	// set_before/set_after/set_both each construct a FRESH Limited value
	// with only their own side set when transitioning away from
	// Passthru, discarding any Limited state that existed before
	// --passthru was given). See resolveContext for the exact state
	// machine, verified against the real rg binary (`-A5 --passthru`:
	// full passthru, prints every line; `--passthru -A5`: PLAIN -A5,
	// passthru is entirely gone, not "passthru with 5 lines of after-
	// context" -- there is no such combined mode in rg).
	PassThru bool
	Invert   bool // -v/--invert-match
	// MaxCount is -m/--max-count: nil = unset/unlimited. A non-nil 0 is
	// a real, legal value (rg parity, verified against the real binary:
	// `rg -m 0 pat file` searches nothing and reports no match) -- NOT
	// the same as unset, which is why this is a pointer rather than a
	// plain int with a 0-means-unlimited convention.
	MaxCount *int

	// Stats is rg's --stats/--no-stats: after the normal results, print
	// an aggregate summary block (matches, matched lines, files, bytes,
	// timing). Last flag wins (--stats then --no-stats -> off). The block
	// is emitted for every search mode (standard, -c/-l/--count-matches,
	// -q, ...) but NOT for --files/--type-list, which never run a search
	// -- see execute. No pluralization is ever applied ("1 matched
	// lines"), matching rg's fixed format. See engine.StatsAccumulator.
	Stats bool
	// Buffer is rg's --line-buffered/--block-buffered choice -- see
	// BufferMode. Byte-invisible; drives execute's stdout flush policy.
	Buffer BufferMode

	Threads int        // -j/--threads; 0 = auto (rg's None)
	Binary  BinaryMode // resolved from -a/--text and -uuu; last one processed wins
	Mmap    MmapMode   // --mmap/--no-mmap

	// Help/Version are -h/--help and -V/--version: when either is set,
	// ParseArgs returns immediately (skipping the "at least one pattern"
	// requirement below) so `gg --help` and `gg -V` work with no
	// pattern argument, matching rg. The caller (run) checks these
	// before doing anything else.
	Help    bool
	Version bool
}

// parseState holds parser-transient data that isn't part of the final
// Config (either because it's resolved away, like context, or because
// it's only needed to decide how to resolve positionals).
type parseState struct {
	positionals []string
	sawPattern  bool // true once -e/--regexp has been seen at least once
	uLevel      int  // -u/--unrestricted repeat count so far

	// before/after/both mirror rg's ContextModeLimited exactly: each
	// -A/-B/-C write is tracked independently and only resolved into
	// Config.ContextBefore/After at the very end (see resolveContext).
	before, after, both *int
	// passthru mirrors rg's ContextMode::Passthru variant: true exactly
	// when --passthru is the LAST of {--passthru, -A, -B, -C} given so
	// far. Set true by --passthru's own applySwitch (which also clears
	// before/after/both, discarding any prior -A/-B/-C state -- ContextMode
	// is one mutable value in rg, not independent fields); set back to
	// false by -A/-B/-C's own applyValue when it was true (which ALSO
	// clears the other two of before/after/both, since transitioning out
	// of Passthru constructs a fresh ContextModeLimited with only the
	// triggering side set -- see Config.PassThru's doc). See
	// resolveContext for the final read of this flag.
	passthru bool
}

type flagKind int

const (
	kindSwitch flagKind = iota
	kindValue
)

type flagSpec struct {
	long    string
	short   byte   // 0 = no short form
	negated string // "" = no negation
	kind    flagKind
	// aliases are additional long spellings rg accepts for this flag
	// (defs.rs's Flag::aliases()), e.g. "maxdepth" for --max-depth. Each
	// resolves to this spec exactly like long does (never negated -- no
	// flag using aliases in gg's v1 scope has a negation to alias too).
	aliases []string

	// exactly one of these is set, matching kind
	applySwitch func(cfg *Config, ps *parseState, on bool) error
	applyValue  func(cfg *Config, ps *parseState, val string) error
	// applyNegatedSwitch, when set, handles this flagSpec's negated
	// spelling as a plain SWITCH (no value consumed) instead of the
	// ordinary value-flag path -- for the rare case where a value flag's
	// negation has a DIFFERENT shape than its primary (rg's own
	// crates/core/flags/defs.rs: ContextSeparator::update handles
	// FlagValue::Switch(false) as a distinct case, not a second value
	// flag -- --context-separator=SEP takes a value, --no-context-
	// separator takes none). Only meaningful when kind == kindValue and
	// negated != ""; nil for every other flag, including every kindSwitch
	// flag (which already has its own on/off dispatch via applySwitch).
	applyNegatedSwitch func(cfg *Config, ps *parseState) error
}

// v1Flags is the registry of every flag gg actually implements in v1.
// Names/short-forms/negations are transcribed from
// ../ripgrep/crates/core/flags/defs.rs (see the doc comment at the top
// of this file).
var v1Flags = buildV1Flags()

func buildV1Flags() []*flagSpec {
	return []*flagSpec{
		// --- Pattern (PLAN.md v1 scope: regex default, -F, -i, -S, -w, -e) ---
		{
			long: "fixed-strings", short: 'F', negated: "no-fixed-strings", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Fixed = on
				return nil
			},
		},
		{
			// -i/--ignore-case. No negation of its own; -s/--case-sensitive
			// is the way back per defs.rs (each of -i/-s/-S sets the same
			// CaseMode field directly, so plain last-write-wins applies).
			long: "ignore-case", short: 'i', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Case = CaseInsensitive
				return nil
			},
		},
		{
			// -s/--case-sensitive is not in PLAN.md's literal v1 flag
			// list, but it is the direct complement of -i/-S (all three
			// write the same CaseMode field in rg) and omitting it would
			// break last-wins semantics for anyone combining -i/-S with
			// -s. Included deliberately; flagged in the M0 handoff.
			long: "case-sensitive", short: 's', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Case = CaseSensitive
				return nil
			},
		},
		{
			long: "smart-case", short: 'S', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Case = CaseSmart
				return nil
			},
		},
		{
			// -w and -x share rg's one BoundaryMode field: whichever is
			// given last wins outright, so each clears the other here.
			long: "word-regexp", short: 'w', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Word = true
				cfg.LineRegexp = false
				return nil
			},
		},
		{
			// -x/--line-regexp has no negation of its own (rg's own
			// LineRegexp::update asserts "has no negation"); repeating it
			// is a harmless no-op, matching -i's pattern. See -w above for
			// the shared-field override this mirrors.
			long: "line-regexp", short: 'x', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.LineRegexp = true
				cfg.Word = false
				return nil
			},
		},
		{
			long: "regexp", short: 'e', kind: kindValue,
			applyValue: func(cfg *Config, ps *parseState, val string) error {
				cfg.Patterns = append(cfg.Patterns, val)
				ps.sawPattern = true
				return nil
			},
		},
		{
			// -f/--file PATTERNFILE: repeatable, combines with -e. Per rg
			// (File's doc comment: "When --file or --regexp is used, then
			// ripgrep treats all positional arguments as files or
			// directories to search"), -f sets sawPattern exactly like -e
			// does, so ParseArgs's positional-resolution step already
			// routes every positional to Paths -- no separate handling
			// needed here. This parser only records the raw file path
			// (or "-" for stdin); actually reading and line-splitting the
			// file happens later, in wire.go's resolvePatternFiles, since
			// this file does no I/O of its own (see the top doc comment).
			long: "file", short: 'f', kind: kindValue,
			applyValue: func(cfg *Config, ps *parseState, val string) error {
				cfg.PatternFiles = append(cfg.PatternFiles, val)
				ps.sawPattern = true
				return nil
			},
		},

		// --- Filtering ---
		{
			long: "hidden", short: '.', negated: "no-hidden", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Hidden = on
				return nil
			},
		},
		{
			// rg's primary flag name really is "no-ignore"; its negation
			// is the (rarely typed) "--ignore", used to turn a preceding
			// --no-ignore back off. Do not "fix" this spelling.
			//
			// --no-ignore is SUGAR: it sets the five no-ignore-* sub-flags
			// dot/exclude/global/parent/vcs together, but NOT no-ignore-
			// files (probe B8). Because each sub-flag is an independent
			// last-wins field, a later --ignore-dot re-enables just the dot
			// family (probe A7), and a bare --ignore (on=false here) resets
			// all five back on (probe A8). This mirrors rg's LowArgs update
			// order exactly -- order-sensitivity comes for free.
			long: "no-ignore", negated: "ignore", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoIgnoreDot = on
				cfg.NoIgnoreExclude = on
				cfg.NoIgnoreGlobal = on
				cfg.NoIgnoreParent = on
				cfg.NoIgnoreVcs = on
				return nil
			},
		},
		{
			// --no-ignore-dot / --ignore-dot: kills .ignore AND .rgignore
			// (probe A3). Parent .ignore/.rgignore too, but keeps parent
			// .gitignore (probe D4).
			long: "no-ignore-dot", negated: "ignore-dot", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoIgnoreDot = on
				return nil
			},
		},
		{
			// --no-ignore-exclude / --ignore-exclude: kills .git/info/exclude.
			long: "no-ignore-exclude", negated: "ignore-exclude", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoIgnoreExclude = on
				return nil
			},
		},
		{
			// --no-ignore-files / --ignore-files: kills every --ignore-file,
			// position-independently (probe B6); negation restores (B7). NOT
			// implied by --no-ignore (B8).
			long: "no-ignore-files", negated: "ignore-files", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoIgnoreFiles = on
				return nil
			},
		},
		{
			// --no-ignore-global / --ignore-global: kills the global
			// (core.excludesFile / XDG) matcher.
			long: "no-ignore-global", negated: "ignore-global", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoIgnoreGlobal = on
				return nil
			},
		},
		{
			// --no-ignore-parent / --ignore-parent: kills the entire
			// parent-directory ignore chain (probe D3). "Parent" is above
			// the WALK ROOT, not CWD (probe D6).
			long: "no-ignore-parent", negated: "ignore-parent", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoIgnoreParent = on
				return nil
			},
		},
		{
			// --no-ignore-vcs / --ignore-vcs: kills .gitignore, exclude, AND
			// the global matcher (probe A4).
			long: "no-ignore-vcs", negated: "ignore-vcs", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoIgnoreVcs = on
				return nil
			},
		},
		{
			// --no-require-git / --require-git: when set, git ignore sources
			// (.gitignore/exclude/global) apply even outside a git repo
			// (probes C2/E5). --no-ignore-vcs still wins over it (probe C3).
			long: "no-require-git", negated: "require-git", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoRequireGit = on
				return nil
			},
		},
		{
			// -L/--follow / --no-follow: follow symlinks during traversal.
			// Explicit symlink PATH args are always followed regardless
			// (walk.buildRootTask). Under -L, symlink loops and broken links
			// are reported as errors (exit 2) -- suppressible via
			// --no-messages -- verified against the real rg binary (round
			// #42 L-block).
			long: "follow", short: 'L', negated: "no-follow", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.FollowSymlinks = on
				return nil
			},
		},
		{
			// --one-file-system / --no-one-file-system: don't cross a
			// file-system boundary relative to each root (find -xdev).
			// Per-root: a second root on another file system is still
			// searched fully (--one-file-system facts).
			long: "one-file-system", negated: "no-one-file-system", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.OneFileSystem = on
				return nil
			},
		},
		{
			// --no-messages / --messages: rg's primary spelling is
			// "no-messages" with the (rare) "--messages" turning a preceding
			// --no-messages back off (last-wins). Suppresses per-file/path
			// error messages AND ignore-file load warnings, but NEVER the
			// exit code -- an error still forces exit 2 (M-block).
			long: "no-messages", negated: "messages", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoMessages = on
				return nil
			},
		},
		{
			// --no-ignore-messages / --ignore-messages: suppress ignore-file
			// load/parse warnings only (not regular file errors). Either this
			// or --no-messages silences them (probe M10/M11).
			long: "no-ignore-messages", negated: "ignore-messages", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoIgnoreMessages = on
				return nil
			},
		},
		{
			// --ignore-file PATH: repeatable value flag with NO negation of
			// its own (rg's IgnoreFile never sets name_negated) -- killing
			// it is --no-ignore-files' job. Each PATH's patterns are anchored
			// at CWD (probes B9-B11). A PATH that can't be read is a stderr
			// warning, never an exit-code change (probes G4r-G7); that I/O
			// happens later, in internal/engine.buildIgnoreSources, since
			// this parser does no I/O of its own.
			long: "ignore-file", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.IgnoreFiles = append(cfg.IgnoreFiles, val)
				return nil
			},
		},
		{
			// --ignore-file-case-insensitive / --no-ignore-file-case-
			// insensitive: applies to the per-directory tree sources only
			// (probe F2), never the explicit --ignore-file (F3) or global
			// matcher.
			long: "ignore-file-case-insensitive", negated: "no-ignore-file-case-insensitive", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.IgnoreCaseInsensitive = on
				return nil
			},
		},
		{
			long: "glob", short: 'g', kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.Globs = append(cfg.Globs, val)
				return nil
			},
		},
		{
			// --iglob has no short form and no negation of its own in rg
			// (defs.rs's IGlob never sets name_short/name_negated); '!'
			// negation is glob syntax handled downstream, same as -g.
			long: "iglob", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.IGlobs = append(cfg.IGlobs, val)
				return nil
			},
		},
		{
			long: "glob-case-insensitive", negated: "no-glob-case-insensitive", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.GlobCaseInsensitive = on
				return nil
			},
		},
		{
			// -d/--max-depth NUM: no negation in rg (defs.rs's MaxDepth
			// never sets name_negated). rg also accepts the alias
			// "--maxdepth" (defs.rs's aliases()); registered separately
			// below via this spec's aliases field.
			long: "max-depth", short: 'd', kind: kindValue, aliases: []string{"maxdepth"},
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				cfg.MaxDepth = &n
				return nil
			},
		},
		{
			long: "unrestricted", short: 'u', kind: kindSwitch,
			applySwitch: func(cfg *Config, ps *parseState, _ bool) error {
				ps.uLevel++
				if ps.uLevel > 3 {
					return fmt.Errorf("flag can only be repeated up to 3 times")
				}
				switch ps.uLevel {
				case 1:
					// -u once == --no-ignore (probe A9): the same five-flag
					// sugar, not no-ignore-files.
					cfg.NoIgnoreDot = true
					cfg.NoIgnoreExclude = true
					cfg.NoIgnoreGlobal = true
					cfg.NoIgnoreParent = true
					cfg.NoIgnoreVcs = true
				case 2:
					cfg.Hidden = true
				case 3:
					cfg.Binary = BinarySearchAndSuppress
				}
				cfg.Unrestricted = ps.uLevel
				return nil
			},
		},
		{
			long: "max-filesize", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				n, err := parseHumanSize(val)
				if err != nil {
					return err
				}
				cfg.MaxFilesize = n
				return nil
			},
		},
		{
			// -t/--type TYPE: repeatable, appended to the SAME ordered
			// TypeChanges list -T/--type-add/--type-clear also append to
			// (see TypeChange's doc) -- never resolved to a Config field of
			// its own the way -c/-g/etc. are, since -t/-T's own precedence
			// against each other depends on this cross-flag order.
			long: "type", short: 't', kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.TypeChanges = append(cfg.TypeChanges, TypeChange{Kind: TypeSelect, Arg: val})
				return nil
			},
		},
		{
			// -T/--type-not TYPE: see -t/--type above.
			long: "type-not", short: 'T', kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.TypeChanges = append(cfg.TypeChanges, TypeChange{Kind: TypeNegate, Arg: val})
				return nil
			},
		},
		{
			// --type-add TYPESPEC: "name:glob" or "name:include:list" --
			// TYPESPEC syntax is validated downstream (package filetype),
			// not here (this parser has no dependency on it -- see the top
			// doc comment).
			long: "type-add", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.TypeChanges = append(cfg.TypeChanges, TypeChange{Kind: TypeAdd, Arg: val})
				return nil
			},
		},
		{
			long: "type-clear", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.TypeChanges = append(cfg.TypeChanges, TypeChange{Kind: TypeClear, Arg: val})
				return nil
			},
		},
		{
			// --type-list has no negation (rg's own TypeList::update
			// asserts "has no negation"), same shape as --files above.
			// Sets Mode, not a TypeChanges entry -- see ModeTypes' doc.
			long: "type-list", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Mode = ModeTypes
				return nil
			},
		},

		// --- Output ---
		{
			// -n/--line-number and -N/--no-line-number are TWO SEPARATE
			// rg flags (each is its own struct with "has no automatic
			// negation" asserted in its own update fn), not one flag with
			// a negated spelling. Both just overwrite the same field, so
			// last-one-given wins regardless of order.
			long: "line-number", short: 'n', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				b := true
				cfg.LineNumbers = &b
				return nil
			},
		},
		{
			long: "no-line-number", short: 'N', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				b := false
				cfg.LineNumbers = &b
				return nil
			},
		},
		{
			// -H/--with-filename and -I/--no-filename are TWO SEPARATE rg
			// flags (each asserts "has no defined negation" in its own
			// update fn), same shape as -n/-N above: last one given wins
			// by plain overwrite. See Config.WithFilename's doc.
			long: "with-filename", short: 'H', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				b := true
				cfg.WithFilename = &b
				return nil
			},
		},
		{
			long: "no-filename", short: 'I', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				b := false
				cfg.WithFilename = &b
				return nil
			},
		},
		{
			long: "heading", negated: "no-heading", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Heading = &on
				return nil
			},
		},
		{
			// -0/--null: no negation in rg. See Config.Null's doc.
			long: "null", short: '0', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Null = true
				return nil
			},
		},
		{
			// --column/--no-column: nil (unset) lets wire.go fall back to
			// Vimgrep's value, matching rg's `column.unwrap_or(vimgrep)`.
			// See Config.Column's doc.
			long: "column", negated: "no-column", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Column = &on
				return nil
			},
		},
		{
			// --vimgrep has no negation (defs.rs's VimGrep::update asserts
			// this); repeating it is a harmless no-op, same shape as -x
			// above. See Config.Vimgrep's doc for the defaults it implies.
			long: "vimgrep", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Vimgrep = true
				return nil
			},
		},
		{
			long: "byte-offset", short: 'b', negated: "no-byte-offset", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.ByteOffset = on
				return nil
			},
		},
		{
			// -o/--only-matching has no negation (defs.rs's OnlyMatching::
			// update asserts this), same shape as -x/--vimgrep above.
			long: "only-matching", short: 'o', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.OnlyMatching = true
				return nil
			},
		},
		{
			// -M/--max-columns NUM: no negation in rg (defs.rs's
			// MaxColumns never sets name_negated) -- "-M0" is how it's
			// reset to unlimited instead. See Config.MaxColumns' doc for
			// why 0 is a safe unlimited sentinel here (unlike MaxCount/
			// MaxDepth's pointer convention).
			long: "max-columns", short: 'M', kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				cfg.MaxColumns = n
				return nil
			},
		},
		{
			long: "max-columns-preview", negated: "no-max-columns-preview", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.MaxColumnsPreview = on
				return nil
			},
		},
		{
			long: "trim", negated: "no-trim", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Trim = on
				return nil
			},
		},
		{
			// --field-match-separator has no short form and no negation
			// in rg (defs.rs's FieldMatchSeparator never sets name_short/
			// name_negated). See Config.FieldMatchSeparator's doc.
			long: "field-match-separator", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.FieldMatchSeparator = unescapeSeparator(val)
				return nil
			},
		},
		{
			long: "field-context-separator", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.FieldContextSeparator = unescapeSeparator(val)
				return nil
			},
		},
		{
			// --context-separator SEP / --no-context-separator: one flag,
			// matching rg's own defs.rs shape exactly (ContextSeparator's
			// name_negated is "no-context-separator") -- but the negated
			// spelling takes NO value, unlike every other value flag's
			// negation-that-isn't in this file (there are none; every
			// OTHER negated flag here is kindSwitch on both sides). See
			// applyNegatedSwitch's doc for why this needs its own small
			// parser extension rather than reusing the ordinary value
			// path. Plain last-write-wins between the two spellings, same
			// as any other flag pair -- both simply overwrite
			// Config.ContextSeparator. See ContextSep's doc.
			long: "context-separator", negated: "no-context-separator", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.ContextSeparator = &ContextSep{Value: unescapeSeparator(val)}
				return nil
			},
			applyNegatedSwitch: func(cfg *Config, _ *parseState) error {
				cfg.ContextSeparator = &ContextSep{Disabled: true}
				return nil
			},
		},
		{
			long: "count", short: 'c', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Mode = ModeCount
				return nil
			},
		},
		{
			long: "include-zero", negated: "no-include-zero", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.IncludeZero = on
				return nil
			},
		},
		{
			long: "files-with-matches", short: 'l', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Mode = ModeFilesWithMatches
				return nil
			},
		},
		{
			// --count-matches has no short form and no negation in rg
			// (defs.rs's CountMatches never sets name_short/name_negated).
			// Joins the same last-flag-wins Mode field every other mode
			// flag writes -- see SearchMode's doc.
			long: "count-matches", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Mode = ModeCountMatches
				return nil
			},
		},
		{
			// --files-without-match has no short form in rg (defs.rs's
			// FilesWithoutMatch never sets name_short). Verified against
			// the real rg binary: last-flag-wins against -l either order,
			// same as every other mode flag.
			long: "files-without-match", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Mode = ModeFilesWithoutMatch
				return nil
			},
		},
		{
			// --files has no short form in real rg (defs.rs's Files flag
			// never overrides name_short). No PATTERN is required with
			// --files; see ParseArgs's positional-resolution step for how
			// that's exempted.
			long: "files", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Mode = ModeFiles
				return nil
			},
		},
		{
			long: "quiet", short: 'q', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Quiet = true
				return nil
			},
		},
		{
			long: "color", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				switch val {
				case "never":
					cfg.Color = ColorNever
				case "auto":
					cfg.Color = ColorAuto
				case "always":
					cfg.Color = ColorAlways
				case "ansi":
					cfg.Color = ColorAnsi
				default:
					return fmt.Errorf("choice %q is unrecognized", val)
				}
				return nil
			},
		},
		{
			long: "after-context", short: 'A', kind: kindValue,
			applyValue: func(_ *Config, ps *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				// Transitioning out of Passthru discards whatever before/
				// both state existed before it (see parseState.passthru's
				// doc) -- only after ends up set.
				if ps.passthru {
					ps.passthru = false
					ps.before, ps.both = nil, nil
				}
				ps.after = &n
				return nil
			},
		},
		{
			long: "before-context", short: 'B', kind: kindValue,
			applyValue: func(_ *Config, ps *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				if ps.passthru {
					ps.passthru = false
					ps.after, ps.both = nil, nil
				}
				ps.before = &n
				return nil
			},
		},
		{
			long: "context", short: 'C', kind: kindValue,
			applyValue: func(_ *Config, ps *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				if ps.passthru {
					ps.passthru = false
					ps.before, ps.after = nil, nil
				}
				ps.both = &n
				return nil
			},
		},
		{
			// --passthru has no negation in rg (defs.rs's Passthru::update
			// asserts "--passthru has no negation") and accepts
			// "--passthrough" as an alias (defs.rs's aliases()). See
			// Config.PassThru's doc for its interaction with -A/-B/-C.
			long: "passthru", kind: kindSwitch, aliases: []string{"passthrough"},
			applySwitch: func(_ *Config, ps *parseState, _ bool) error {
				ps.passthru = true
				ps.before, ps.after, ps.both = nil, nil, nil
				return nil
			},
		},
		{
			long: "invert-match", short: 'v', negated: "no-invert-match", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Invert = on
				return nil
			},
		},
		{
			// --stats/--no-stats: last flag wins via the switch's on value.
			long: "stats", negated: "no-stats", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Stats = on
				return nil
			},
		},
		{
			// --line-buffered/--no-line-buffered: forces line buffering, or
			// restores auto on negation. Shares Config.Buffer with
			// --block-buffered, so the last of the two on the command line
			// wins (rg's single BufferMode value) -- see BufferMode's doc.
			long: "line-buffered", negated: "no-line-buffered", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				if on {
					cfg.Buffer = BufferLine
				} else {
					cfg.Buffer = BufferAuto
				}
				return nil
			},
		},
		{
			// --block-buffered/--no-block-buffered: forces block buffering,
			// or restores auto on negation. See --line-buffered above for the
			// shared-field last-wins semantics.
			long: "block-buffered", negated: "no-block-buffered", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				if on {
					cfg.Buffer = BufferBlock
				} else {
					cfg.Buffer = BufferAuto
				}
				return nil
			},
		},
		{
			// -m/--max-count NUM: repeatable, last one given wins (plain
			// overwrite of the same field, same as -A/-B/-C's ps.after/
			// before/both pattern, except MaxCount has no separate
			// "both sides" concept to resolve -- it just writes straight
			// to cfg). 0 is a legal value (verified against the real rg
			// binary: -m 0 searches nothing), distinct from "not given at
			// all" -- see Config.MaxCount's doc for why this is a pointer.
			long: "max-count", short: 'm', kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				cfg.MaxCount = &n
				return nil
			},
		},

		// --- Perf ---
		{
			long: "threads", short: 'j', kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				cfg.Threads = n
				return nil
			},
		},
		{
			// -a/--text always sets Binary explicitly (AsText when given,
			// Auto when negated via --no-text) -- it is not a no-op on
			// negation. This lets --no-text reset a Binary set earlier by
			// -uuu, matching rg's own Text::update.
			long: "text", short: 'a', negated: "no-text", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				if on {
					cfg.Binary = BinaryAsText
				} else {
					cfg.Binary = BinaryAuto
				}
				return nil
			},
		},
		{
			long: "mmap", negated: "no-mmap", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				if on {
					cfg.Mmap = MmapAlways
				} else {
					cfg.Mmap = MmapNever
				}
				return nil
			},
		},

		// --- Meta (rg parity: print and exit 0, no pattern required) ---
		{
			long: "help", short: 'h', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Help = true
				return nil
			},
		},
		{
			long: "version", short: 'V', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Version = true
				return nil
			},
		},
	}
}

// notImplementedFlag names a real rg flag gg deliberately does not
// implement yet, so ParseArgs can fail closed with a specific message
// instead of a bare "unrecognized flag".
type notImplementedFlag struct {
	long  string
	short byte // 0 = no short form
	label string
}

// notImplementedFlags is a curated (not exhaustive) list covering
// PLAN.md's explicit "Not in v1" line -- replace, multiline, PCRE2,
// non-UTF-8 encodings, compressed files, --sort, JSON -- plus a
// handful of flags an rg user is very likely to type that aren't named
// there: --binary (reachable in v1 only indirectly via -uuu, but not as a
// flag of its own yet). -h/--help and -V/--version
// are real flags (see buildV1Flags), not in this list, per M2's rg-parity
// fix. This is intentionally not the full ~100-flag rg surface -- see
// the M0 handoff note for why that would be scope inflation for this
// task.
var notImplementedFlags = []notImplementedFlag{
	{long: "replace", short: 'r', label: "-r/--replace"},
	{long: "multiline", short: 'U', label: "-U/--multiline"},
	{long: "multiline-dotall", label: "--multiline-dotall"},
	{long: "pcre2", short: 'P', label: "-P/--pcre2"},
	{long: "encoding", short: 'E', label: "-E/--encoding"},
	{long: "search-zip", short: 'z', label: "-z/--search-zip"},
	{long: "sort", label: "--sort"},
	{long: "sortr", label: "--sortr"},
	{long: "json", label: "--json"},
	{long: "binary", label: "--binary"},
}

// byLongName indexes v1Flags by every long spelling that should resolve
// to it (its primary name and, if present, its negated name), recording
// whether that spelling is the negated one.
type longEntry struct {
	spec   *flagSpec
	negate bool
}

var longIndex = buildLongIndex()
var shortIndex = buildShortIndex()
var notImplLongIndex = buildNotImplLongIndex()
var notImplShortIndex = buildNotImplShortIndex()

func buildLongIndex() map[string]longEntry {
	m := make(map[string]longEntry, len(v1Flags)*2)
	for _, s := range v1Flags {
		m[s.long] = longEntry{spec: s, negate: false}
		if s.negated != "" {
			m[s.negated] = longEntry{spec: s, negate: true}
		}
		for _, alias := range s.aliases {
			m[alias] = longEntry{spec: s, negate: false}
		}
	}
	return m
}

func buildShortIndex() map[byte]*flagSpec {
	m := make(map[byte]*flagSpec, len(v1Flags))
	for _, s := range v1Flags {
		if s.short != 0 {
			m[s.short] = s
		}
	}
	return m
}

func buildNotImplLongIndex() map[string]string {
	m := make(map[string]string, len(notImplementedFlags))
	for _, f := range notImplementedFlags {
		m[f.long] = f.label
	}
	return m
}

func buildNotImplShortIndex() map[byte]string {
	m := make(map[byte]string, len(notImplementedFlags))
	for _, f := range notImplementedFlags {
		if f.short != 0 {
			m[f.short] = f.label
		}
	}
	return m
}

// ParseArgs parses args (typically os.Args[1:]) into a Config, matching
// rg's flag syntax and override semantics for every flag in gg's v1
// scope. Any error is a parse failure that the caller should report
// alongside a short usage line and an exit code of 2, matching rg's own
// convention for bad flags/missing patterns.
func ParseArgs(args []string) (*Config, error) {
	cfg := &Config{
		Case:  CaseSensitive,
		Color: ColorAuto,
		Mmap:  MmapAuto,
	}
	ps := &parseState{}

	sawDashDash := false
	for i := 0; i < len(args); i++ {
		arg := args[i]

		if sawDashDash {
			ps.positionals = append(ps.positionals, arg)
			continue
		}
		if arg == "--" {
			sawDashDash = true
			continue
		}

		switch {
		case strings.HasPrefix(arg, "--"):
			consumed, err := parseLongFlag(cfg, ps, arg, args, i)
			if err != nil {
				return nil, err
			}
			i += consumed
		case len(arg) > 1 && arg[0] == '-':
			consumed, err := parseShortCluster(cfg, ps, arg, args, i)
			if err != nil {
				return nil, err
			}
			i += consumed
		default:
			// A bare "-" (stdin) and anything not starting with '-' are
			// both ordinary positionals at the parser level.
			ps.positionals = append(ps.positionals, arg)
		}
	}

	if cfg.Help || cfg.Version {
		// rg parity: `gg --help`/`gg -V` work with no pattern argument at
		// all -- skip the "at least one pattern" requirement below
		// entirely. The caller (run) checks these flags first and exits
		// before touching Patterns/Paths.
		return cfg, nil
	}

	if cfg.Mode == ModeFiles || cfg.Mode == ModeTypes {
		// --files never needs a PATTERN (verified against the real rg
		// binary: `rg --files somepath` treats "somepath" as a PATH, not
		// a pattern, and bare `rg --files` with zero positionals
		// searches "." -- same "no positionals" fallback execute()
		// already applies). Any pattern given anyway via -e is harmless
		// but unused, exactly like real rg (`rg --files -e pat` lists
		// every file, ignoring "pat" entirely) -- cfg.Patterns may or may
		// not be populated by -e above; ModeFiles callers must not read
		// it.
		//
		// --type-list needs no PATTERN or PATH at all -- it ignores every
		// positional outright (verified against the real rg binary: `rg
		// --type-list foo bar` prints the type table, `foo`/`bar` are
		// never treated as a pattern or paths) -- but populating cfg.Paths
		// here anyway is harmless (executeTypes never reads it) and keeps
		// this branch a single shared case with ModeFiles.
		cfg.Paths = ps.positionals
	} else if ps.sawPattern {
		// -e/--regexp was given at least once: per rg (Regexp's doc
		// comment: "When --file or --regexp is used, then ripgrep
		// treats all positional arguments as files or directories to
		// search"), every positional is a path, none become the pattern.
		cfg.Paths = ps.positionals
	} else {
		if len(ps.positionals) == 0 {
			return nil, fmt.Errorf("gripgrep requires at least one pattern to execute a search")
		}
		cfg.Patterns = []string{ps.positionals[0]}
		cfg.Paths = ps.positionals[1:]
	}

	if ps.passthru {
		// --passthru was the last of {--passthru, -A, -B, -C} given (or
		// the only one) -- ContextBefore/After stay 0 (search.Searcher.
		// PassThru's doc requires this), and ps.before/after/both are
		// necessarily all nil already (every write to them clears
		// ps.passthru -- see their applyValue funcs), so there is nothing
		// left to resolve.
		cfg.PassThru = true
	} else {
		cfg.ContextBefore, cfg.ContextAfter = resolveContext(ps.before, ps.after, ps.both)
	}

	return cfg, nil
}

// resolveContext mirrors rg's ContextModeLimited.get(): -A/-B always
// partially override -C's corresponding side, regardless of the order
// they were given in, because each is tracked independently until this
// final resolution step.
func resolveContext(before, after, both *int) (b, a int) {
	if both != nil {
		b, a = *both, *both
	}
	if before != nil {
		b = *before
	}
	if after != nil {
		a = *after
	}
	return b, a
}

// parseLongFlag handles one "--name", "--name=value", or "--name value"
// token starting at args[i]. It returns how many EXTRA tokens beyond
// args[i] were consumed (0 or 1), so the caller's loop can skip them.
func parseLongFlag(cfg *Config, ps *parseState, arg string, args []string, i int) (int, error) {
	body := arg[2:] // strip "--"
	name := body
	inlineVal := ""
	hasInline := false
	if eq := strings.IndexByte(body, '='); eq >= 0 {
		name = body[:eq]
		inlineVal = body[eq+1:]
		hasInline = true
	}

	if label, ok := notImplLongIndex[name]; ok {
		return 0, notImplementedError(label)
	}

	entry, ok := longIndex[name]
	if !ok {
		return 0, fmt.Errorf("unrecognized flag --%s", name)
	}
	spec := entry.spec

	if spec.kind == kindSwitch {
		if hasInline {
			return 0, fmt.Errorf("unexpected value for switch flag --%s", name)
		}
		if err := spec.applySwitch(cfg, ps, !entry.negate); err != nil {
			return 0, fmt.Errorf("error parsing flag --%s: %w", name, err)
		}
		return 0, nil
	}

	// A value flag's negated spelling with a different SHAPE than its
	// primary (e.g. --no-context-separator) -- see applyNegatedSwitch's
	// doc. Must be checked before the ordinary value-flag path below,
	// which would otherwise try to consume an argument this spelling
	// never takes.
	if entry.negate && spec.applyNegatedSwitch != nil {
		if hasInline {
			return 0, fmt.Errorf("unexpected value for switch flag --%s", name)
		}
		if err := spec.applyNegatedSwitch(cfg, ps); err != nil {
			return 0, fmt.Errorf("error parsing flag --%s: %w", name, err)
		}
		return 0, nil
	}

	// Value flag.
	if hasInline {
		if err := spec.applyValue(cfg, ps, inlineVal); err != nil {
			return 0, fmt.Errorf("error parsing flag --%s: %w", name, err)
		}
		return 0, nil
	}
	if i+1 >= len(args) {
		return 0, fmt.Errorf("missing value for flag --%s", name)
	}
	if err := spec.applyValue(cfg, ps, args[i+1]); err != nil {
		return 0, fmt.Errorf("error parsing flag --%s: %w", name, err)
	}
	return 1, nil
}

// parseShortCluster handles one "-xyz"-style token starting at args[i]:
// a run of single-byte switches, optionally ending in a value-taking
// flag that consumes either the rest of the token or the next argv
// entry. It returns how many EXTRA tokens beyond args[i] were consumed
// (0 or 1).
//
// Matches rg's own behavior exactly (verified against the real binary):
// a value-taking flag anywhere in the cluster greedily claims everything
// after it in the token as its value -- there is no way to place more
// switches after a value-taking flag in the same token.
func parseShortCluster(cfg *Config, ps *parseState, arg string, args []string, i int) (int, error) {
	body := arg[1:] // strip leading '-'
	for k := 0; k < len(body); k++ {
		c := body[k]

		if label, ok := notImplShortIndex[c]; ok {
			return 0, notImplementedError(label)
		}

		spec, ok := shortIndex[c]
		if !ok {
			return 0, fmt.Errorf("unrecognized flag -%c", c)
		}

		if spec.kind == kindSwitch {
			if err := spec.applySwitch(cfg, ps, true); err != nil {
				return 0, fmt.Errorf("error parsing flag -%c: %w", c, err)
			}
			continue
		}

		// Value flag: the remainder of this token is the value, or (if
		// nothing remains) the next argv entry is.
		rest := body[k+1:]
		if rest != "" {
			if err := spec.applyValue(cfg, ps, rest); err != nil {
				return 0, fmt.Errorf("error parsing flag -%c: %w", c, err)
			}
			return 0, nil
		}
		if i+1 >= len(args) {
			return 0, fmt.Errorf("missing value for flag -%c", c)
		}
		if err := spec.applyValue(cfg, ps, args[i+1]); err != nil {
			return 0, fmt.Errorf("error parsing flag -%c: %w", c, err)
		}
		return 1, nil
	}
	return 0, nil
}

func notImplementedError(label string) error {
	return fmt.Errorf("not yet implemented: %s (rg supports this flag; gg's v1 flag set does not yet -- see PLAN.md)", label)
}

// parseNonNegInt parses a non-negative decimal integer, matching the
// convert::usize/convert::u64 helpers in defs.rs: these parse directly
// into an unsigned type, so a leading '-' fails immediately as an
// invalid digit rather than as a distinct "negative not allowed" check
// (verified: `rg -A -5` fails with "invalid digit found in string", the
// same message as any other non-digit input, not a sign-specific one).
func parseNonNegInt(s string) (int, error) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("value is not a valid number: %w", err)
	}
	if n > math.MaxInt {
		return 0, fmt.Errorf("value is not a valid number: too large")
	}
	return int(n), nil
}

// parseHumanSize parses rg's --max-filesize format exactly: a run of
// ASCII digits, followed by an optional exact suffix of "K", "M", or
// "G" (binary multiples: 1024/1024^2/1024^3). No decimals, no lowercase
// suffixes -- matches crates/cli/src/human.rs::parse_human_readable_size.
func parseHumanSize(s string) (int64, error) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("invalid size: invalid format for size %q, which should be a non-empty sequence of digits followed by an optional 'K', 'M' or 'G' suffix", s)
	}
	value, err := strconv.ParseUint(s[:end], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size: %w", err)
	}
	suffix := s[end:]
	var mult uint64 = 1
	switch suffix {
	case "":
		mult = 1
	case "K":
		mult = 1 << 10
	case "M":
		mult = 1 << 20
	case "G":
		mult = 1 << 30
	default:
		return 0, fmt.Errorf("invalid size: invalid format for size %q, which should be a non-empty sequence of digits followed by an optional 'K', 'M' or 'G' suffix", s)
	}
	if mult != 1 && value > (1<<64-1)/mult {
		return 0, fmt.Errorf("invalid size: value too large for size %q", s)
	}
	return int64(value * mult), nil
}

// unescapeSeparator processes s exactly like rg's own separator-value
// parsing (--context-separator/--field-match-separator/--field-context-
// separator all funnel through the SAME grep_cli::unescape, which
// delegates to the bstr crate's ByteVec::unescape_bytes -- crates/cli/
// src/escape.rs). Operates on s's RUNES (Go's range over a string
// already does this), matching bstr's own char-based iteration -- never
// return early, malformed escapes fall back to literal text one rune at
// a time:
//   - `\0`, `\\`, `\r`, `\n`, `\t` map to their single-byte value.
//   - `\xZZ` (exactly two hex digits, upper or lower case) maps to that
//     byte.
//   - Any other backslash sequence -- including an incomplete `\x`
//     escape (fewer than two valid hex digits following, or none at
//     all) or a trailing lone backslash at the end of s -- is left
//     UNTOUCHED as literal text, byte for byte.
//
// Verified against rg's own unescape test table (the
// differential sweep): `\\x61` unescapes to the LITERAL text `\x61`
// (four bytes: backslash, x, 6, 1), NOT "a" -- the first `\\` consumes
// to one literal backslash before "x61" is ever reached as plain,
// non-escaped text; `\xZZ` (invalid hex digits) unescapes to itself
// unchanged (backslash, x, Z, Z), not partially processed.
//
// Always returns a non-nil slice, even for an empty input (relied on by
// every call site: a nil field-separator/context-separator VALUE means
// "flag never given", never "given as empty" -- see Config.
// FieldMatchSeparator's doc).
func unescapeSeparator(s string) []byte {
	out := make([]byte, 0, len(s))
	runes := []rune(s)
	for i := 0; i < len(runes); {
		r := runes[i]
		if r != '\\' {
			out = utf8.AppendRune(out, r)
			i++
			continue
		}
		if i+1 >= len(runes) {
			// Trailing lone backslash: literal.
			out = append(out, '\\')
			i++
			continue
		}
		switch runes[i+1] {
		case '0':
			out = append(out, 0)
			i += 2
		case '\\':
			out = append(out, '\\')
			i += 2
		case 'r':
			out = append(out, '\r')
			i += 2
		case 'n':
			out = append(out, '\n')
			i += 2
		case 't':
			out = append(out, '\t')
			i += 2
		case 'x':
			if i+3 < len(runes) && isHexDigit(runes[i+2]) && isHexDigit(runes[i+3]) {
				out = append(out, hexDigit(runes[i+2])<<4|hexDigit(runes[i+3]))
				i += 4
			} else {
				// Incomplete/invalid \x escape: literal "\x", NOT
				// consuming whatever (if anything) follows -- those
				// bytes are processed normally on the next loop
				// iteration(s), same as any other non-escape text.
				out = append(out, '\\', 'x')
				i += 2
			}
		default:
			// Any other backslash sequence unescapes as itself.
			out = append(out, '\\')
			out = utf8.AppendRune(out, runes[i+1])
			i += 2
		}
	}
	return out
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// hexDigit converts one hex-digit rune (already validated by
// isHexDigit) to its 4-bit value.
func hexDigit(r rune) byte {
	switch {
	case r >= '0' && r <= '9':
		return byte(r - '0')
	case r >= 'a' && r <= 'f':
		return byte(r-'a') + 10
	default: // 'A'-'F'
		return byte(r-'A') + 10
	}
}
