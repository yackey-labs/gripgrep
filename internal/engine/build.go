package engine

import (
	"fmt"
	"strings"

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
		ParallelWorkers: resolveParallelWorkers(cfg.Threads),
	})
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
