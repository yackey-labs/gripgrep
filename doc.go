// Package gripgrep is a fast, pure-Go recursive code search library --
// the same engine that powers the gg CLI (cmd/gg), with no dependency on
// the CLI itself. Every top-level function mirrors gg's/rg's own default
// behavior (recursive, gitignore-aware, binary-file filtering, parallel):
//
//	matches, err := gripgrep.Search("TODO", ".")
//
// For CLI-flag-equivalent control, build an Options value (its zero
// value is exactly the CLI defaults above) and call the same verb as a
// method:
//
//	opts := gripgrep.Options{IgnoreCase: true, Context: 2}
//	matches, err := opts.Search("todo", "./internal")
//
// See Options, Match, and SearchStream for the full surface. Every value
// this package returns (Match.Path, Match.Line, the strings in
// FilesWithMatch/Files, the keys of CountMatches) is an independent copy
// -- safe to retain indefinitely, store in a map, or hand to another
// goroutine -- unlike the lower-level walk/search packages this is built
// on, whose types deliberately expose zero-copy views valid only for the
// duration of a callback (see internal/engine's doc and PLAN.md's alloc
// discipline row). Copying at this boundary is the facade's whole job:
// it trades the engine's zero-allocation hot path for a stupid-easy,
// memory-safe API, which is exactly what a library caller (as opposed to
// gg's own hot loop) wants.
//
// # Choosing a verb
//
//	I want...                              call                   CLI equivalent
//	matches (line/path/lineno/context)     Search / SearchStream  gg PATTERN
//	just the paths that matched            FilesWithMatch         gg -l PATTERN
//	a match count per file                 CountMatches           gg -c PATTERN
//	the walked file list, no matching      Files                  gg --files
//
// Every verb above has a package-level function (CLI defaults) and a
// same-name Options method (CLI-flag-equivalent control), except Files,
// which has no Options variant -- see its own doc comment for why.
//
// See docs/library.md in the module root for the full guide: an
// Options-to-flags reference table, the Match struct's context/early-stop
// semantics, the streaming concurrency contract, the error model, and
// this package's versioning policy.
package gripgrep
