package engine

import (
	"fmt"
	"strings"

	"github.com/yackey-labs/gripgrep/filetype"
	"github.com/yackey-labs/gripgrep/glob"
	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/search"
)

// BuildMatcher compiles cfg's pattern set into a match.Matcher. Exported
// because both cmd/gg (which also feeds the same matcher into its
// printer.Standard sink for color spans) and the root facade need the
// matcher instance itself, not just what Run does with it internally.
func BuildMatcher(cfg Config) (match.Matcher, error) {
	return match.New(match.Config{
		Patterns:   cfg.Patterns,
		CaseMode:   convertCaseMode(cfg.Case),
		Word:       cfg.Word,
		Fixed:      cfg.Fixed,
		LineRegexp: cfg.LineRegexp,
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

// NewSearcher builds a *search.Searcher from cfg and a pre-built matcher.
// Exported for the same reason as BuildMatcher: cmd/gg's WorkerFactory
// closures (see Run's doc) need to construct one per pooled worker, as
// does the facade's own.
func NewSearcher(cfg Config, matcher match.Matcher) *search.Searcher {
	return search.New(search.Searcher{
		Matcher:         matcher,
		Invert:          cfg.Invert,
		LineNumbers:     cfg.LineNumbers,
		BeforeContext:   cfg.BeforeContext,
		AfterContext:    cfg.AfterContext,
		PassThru:        cfg.PassThru,
		MaxCount:        cfg.MaxCount,
		ParallelWorkers: resolveParallelWorkers(cfg.Threads),
	})
}

// buildGlobs compiles -g/--glob and --iglob patterns into a single
// walk.Options.Globs override set, flipping polarity per
// walk.Options.GlobsRequireMatch's doc: a plain pattern is really an
// *include* filter in rg (only search files matching some -g/--iglob),
// so it becomes a '!'-prefixed (whitelist) entry in the underlying
// gitignore-style glob.Set; a '!'-prefixed pattern is an ordinary
// exclude, so its leading '!' is stripped to become a plain (ignore)
// entry. requireMatch is true iff at least one plain pattern was given
// across EITHER globs or iglobs, which is exactly when the "anything
// matching none of them is excluded" semantics apply (see
// walk.Options.GlobsRequireMatch's doc) -- verified against the real rg
// binary: a lone `--iglob '!pat'` with no positive pattern anywhere does
// NOT turn into an include filter, but `-g 'pat'` (positive) followed by
// `--iglob '!other'` does, and the iglob exclusion still applies on top.
//
// globs are added to the Builder before iglobs, always, regardless of
// their actual relative order on the command line -- see Config.IGlobs'
// doc for why (matches rg's own hiargs.rs::globs() unconditionally). Every
// glob pattern is added case-sensitively unless ci is set (--glob-case-
// insensitive); every iglob pattern is always added via Builder.AddCI.
func buildGlobs(globs, iglobs []string, ci bool) (*glob.Set, bool, error) {
	if len(globs) == 0 && len(iglobs) == 0 {
		return nil, false, nil
	}
	var b glob.Builder
	requireMatch := false
	addOne := func(p string, caseInsensitive bool) {
		stripped, negated := strings.CutPrefix(p, "!")
		add := b.Add
		if caseInsensitive {
			add = b.AddCI
		}
		if negated {
			// rg's "-g '!x'" is an ordinary exclude: strip the leading
			// '!' to become a plain (ignore) entry in gitignore terms.
			add(stripped)
		} else {
			// rg's "-g 'x'" (no leading '!') is really an *include*
			// filter: give it gitignore's OWN '!' (whitelist) so it
			// participates in GlobsRequireMatch's exclusion below.
			requireMatch = true
			add("!" + p)
		}
	}
	for _, p := range globs {
		addOne(p, ci)
	}
	for _, p := range iglobs {
		addOne(p, true)
	}
	set, err := b.Build()
	if err != nil {
		return nil, false, fmt.Errorf("invalid glob: %w", err)
	}
	return set, requireMatch, nil
}

// buildTypes compiles cfg's -t/-T/--type-add/--type-clear changes (round
// #35) into a *filetype.Matcher, applied over rg's default type table.
// Fast-paths to (nil, nil) when changes is empty -- the common case,
// which must cost nothing beyond this one length check (see
// walk.Options.Types' doc). Errors here (an unrecognized -t/-T name, a
// malformed --type-add TYPESPEC) are surfaced BEFORE buildGlobs' own
// errors at every call site in this package, matching rg's own
// construction order (crates/core/flags/hiargs.rs builds HiArgs.types
// before HiArgs.globs) -- verified against the real rg binary: a bad -t
// alongside a bad -g reports the type error, the probes.
func buildTypes(changes []filetype.Change) (*filetype.Matcher, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	b := filetype.NewBuilder()
	b.AddDefaults()
	if err := b.Apply(changes); err != nil {
		return nil, err
	}
	return b.Build()
}

// ResolvePaths splits a possibly-empty cfg.Paths into the two forms Run/
// Files and their callers need, which must differ when no PATH argument
// was given at all:
//
//   - statPaths always has at least one real, stat-able entry ("."
//     substituted for empty) -- for a caller's own showPath-style
//     heuristics and mmapEligible, which call os.Stat and need something
//     valid to call it on.
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
func ResolvePaths(cfgPaths []string) (statPaths, walkRoots []string) {
	if len(cfgPaths) == 0 {
		return []string{"."}, []string{""}
	}
	norm := normalizeSeparators(cfgPaths)
	return norm, norm
}
