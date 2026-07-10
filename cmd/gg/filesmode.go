package main

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/yackey-labs/gripgrep/printer"
	"github.com/yackey-labs/gripgrep/walk"
)

// executeFiles implements --files: list every file that would be
// searched, without actually searching any of them (per PLAN.md's Output
// row: "--files mode skips matcher/searcher entirely: walk-only + one
// dedicated printer goroutine fed by a channel"). Dispatched from
// execute() before buildMatcher runs, since --files needs no pattern at
// all -- ParseArgs already exempts it from the "at least one pattern"
// requirement and routes every positional into cfg.Paths.
//
// --files composes with everything that shapes the walked file set
// (--hidden, --no-ignore, -g/--glob, the -u ladder, --max-filesize, -j)
// exactly like the search modes, via the same walk.Options construction;
// it does not use mmap, color spans, line numbers, or any other
// search-output concern, since there is no search output.
func executeFiles(cfg *Config, stdout, stderr io.Writer) int {
	globSet, globsRequireMatch, err := buildGlobs(cfg.Globs)
	if err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}

	// executeFiles never calls os.Stat on a path itself (no
	// computeShowPath/mmapEligible equivalent here), so only walkRoots
	// (the "" vs "." distinction resolvePaths exists for -- see its doc)
	// matters; the stat-able form is discarded.
	_, walkRoots := resolvePaths(cfg.Paths)

	walkOpts := walk.Options{
		Hidden:            cfg.Hidden,
		NoIgnore:          cfg.NoIgnore,
		MaxFileSize:       cfg.MaxFilesize,
		Threads:           cfg.Threads,
		Globs:             globSet,
		GlobsRequireMatch: globsRequireMatch,
	}

	isTTY := isTerminalWriter(stdout)
	color := cfg.Color == ColorAlways || cfg.Color == ColorAnsi || (cfg.Color == ColorAuto && isTTY)

	dest := printer.NewDest(stdout)
	// -q suppresses path output entirely (verified against the real rg
	// binary: `rg --files -q` prints nothing but still reports per-path
	// errors to stderr and reflects found-or-not in the exit code) -- so
	// when Quiet is set, skip starting a PathPrinter at all rather than
	// spin up its goroutine just to discard everything it would write.
	var pp *printer.PathPrinter
	if !cfg.Quiet {
		pp = printer.NewPathPrinter(dest, color)
	}

	var anyMatched, anyError atomic.Bool
	visitor := func(e *walk.Entry) walk.WalkState {
		if e.Err != nil {
			fmt.Fprintf(stderr, "gg: %s: %s\n", e.Path, e.Err)
			anyError.Store(true)
			return walk.Continue
		}
		if e.Type != walk.TypeFile {
			// Directories recurse internally; symlinks are never
			// followed in v1 (-L not implemented) and carry no listable
			// content of their own -- matches real rg's own --files
			// output, verified directly: an unfollowed symlink never
			// appears in the listing. TypeUnknown (FIFO/socket/device)
			// is excluded the same way the search path already excludes
			// it.
			return walk.Continue
		}
		anyMatched.Store(true)
		if pp != nil {
			// Path is a view valid only for the duration of this call
			// (see walk.Entry's doc) -- the PathPrinter's channel send
			// retains it well past that, so it must be copied here, not
			// referenced.
			pp.Paths() <- strings.Clone(e.Path)
		}
		return walk.Continue
	}

	if err := walk.Walk(walkRoots, walkOpts, visitor); err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}

	if pp != nil {
		close(pp.Paths())
		pp.Wait()
	}

	// Same exit-code precedence as execute()'s search modes (see its
	// comment for the real-rg verification this mirrors): under -q, a
	// confirmed find locks in exit 0 regardless of any error seen
	// elsewhere in the same walk; otherwise a per-path error overrides a
	// real find, since the listing is known-incomplete.
	if cfg.Quiet {
		if anyMatched.Load() {
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
