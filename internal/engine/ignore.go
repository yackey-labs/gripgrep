package engine

import (
	"fmt"
	"io"

	"github.com/yackey-labs/gripgrep/glob"
	"github.com/yackey-labs/gripgrep/walk"
)

// buildIgnoreSources resolves the two per-walk ignore matchers that live
// outside the per-directory tree chain -- the explicit --ignore-file set
// and the global gitignore -- applying the sub-flags that kill each, and
// wiring the compiled sets into walk.Options fields.
//
// It is the engine (not walk) that owns this because both matchers need
// stderr: a --ignore-file that can't be read is warned about, one line per
// bad file, and -- per rg (probes G4r-G7) -- these load errors NEVER
// change the exit code, so they are written straight to stderr here
// without ever touching the Result.AnyError signal the walk's own
// per-file errors feed. walk supplies the resolution logic (LoadExplicit
// Ignore / LoadGlobalIgnore); the flag gating and reporting live here.
func buildIgnoreSources(cfg Config, stderr io.Writer) (explicit, global *glob.Set) {
	// --no-ignore-files kills every --ignore-file, even ones that appear
	// after it on the command line (probe B6): the flag is position-
	// independent, so it's a single bool here, not an ordered interaction.
	if !cfg.NoIgnoreFiles && len(cfg.IgnoreFiles) > 0 {
		set, errs := walk.LoadExplicitIgnore(cfg.IgnoreFiles)
		// These are ignore-file LOAD warnings, silenced by EITHER
		// --no-ignore-messages OR --no-messages -- mirroring rg's
		// ignore_message! guard (messages() && ignore_messages()): either
		// flag being off suppresses them. Unlike regular file errors, they
		// never touch the exit code (Result.AnyError) at all, with or
		// without the flags (probes G4r-G7 / M10/M11).
		if !cfg.NoMessages && !cfg.NoIgnoreMessages {
			for _, e := range errs {
				// e.Err is an *os.PathError whose text already names the
				// path; printing it verbatim keeps this to the "one line per
				// bad file" rg contract without duplicating the path.
				fmt.Fprintf(stderr, "gg: %s\n", e.Err)
			}
		}
		explicit = set
	}
	// The global matcher is killed by --no-ignore-global OR --no-ignore-vcs
	// (probes E2/E3). Its per-entry anyGit gate (only inside a repo, unless
	// --no-require-git) lives in walk.ignoreCtx.matched, not here.
	// --ignore-file-case-insensitive DOES fold the global matcher (probe
	// F5), unlike the explicit one above.
	if !cfg.NoIgnoreVcs && !cfg.NoIgnoreGlobal {
		global = walk.LoadGlobalIgnore(cfg.IgnoreCaseInsensitive)
	}
	return explicit, global
}

// ignoreWalkOptions fills the ignore-related fields of a walk.Options from
// cfg plus the already-resolved explicit/global matchers, so Run and Files
// stay identical on this axis (both compose with the full ignore cluster).
func ignoreWalkOptions(cfg Config, explicit, global *glob.Set) walk.Options {
	return walk.Options{
		NoIgnoreDot:           cfg.NoIgnoreDot,
		NoIgnoreExclude:       cfg.NoIgnoreExclude,
		NoIgnoreParent:        cfg.NoIgnoreParent,
		NoIgnoreVcs:           cfg.NoIgnoreVcs,
		NoRequireGit:          cfg.NoRequireGit,
		IgnoreCaseInsensitive: cfg.IgnoreCaseInsensitive,
		ExplicitIgnore:        explicit,
		GlobalIgnore:          global,
	}
}
