package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/yackey-labs/gripgrep/internal/engine"
	"github.com/yackey-labs/gripgrep/printer"
)

// executeFiles implements --files: list every file that would be
// searched, without actually searching any of them (per PLAN.md's Output
// row: "--files mode skips matcher/searcher entirely: walk-only + one
// dedicated printer goroutine fed by a channel"). Dispatched from
// execute() before buildMatcher runs, since --files needs no pattern at
// all -- ParseArgs already exempts it from the "at least one pattern"
// requirement and routes every positional into cfg.Paths.
//
// The walk-only traversal itself lives in internal/engine.Files (shared
// with the root facade's Files verb -- see PLAN.md's "one engine"
// requirement); this function only wires that traversal to cmd/gg's own
// printer.PathPrinter/color/-q display concerns, composing with
// everything that shapes the walked file set (--hidden, --no-ignore,
// -g/--glob, the -u ladder, --max-filesize, -j) exactly like the search
// modes.
func executeFiles(cfg *Config, stdout, stderr io.Writer) int {
	econf := toEngineConfig(cfg)

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

	visit := func(path string) {
		if pp != nil {
			// path is a view valid only for the duration of this call
			// (see engine.FilesVisit's doc) -- the PathPrinter's channel
			// send retains it well past that, so it must be copied here,
			// not referenced.
			pp.Paths() <- strings.Clone(path)
		}
	}

	result, err := engine.Files(econf, visit, stderr)
	if err != nil {
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
		if result.Matched {
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
