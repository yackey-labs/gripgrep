package main

import (
	"fmt"
	"io"

	"github.com/yackey-labs/gripgrep/filetype"
)

// executeTypes implements --type-list: print every currently-defined file
// type (rg's built-in table plus any --type-add/--type-clear given) and
// exit, without ever touching the matcher, the walk, or the patterns/
// paths parsed elsewhere in cfg. -q/--quiet does NOT suppress this output
// (verified against the real rg binary: `rg -q --type-list` still prints
// the full table) -- unlike every search mode, --type-list has nothing to
// search, so there is no "confirmed find" for -q's early-exit contract to
// apply to.
//
// The Builder is still Built (not just AddDefaults+Apply'd) even though
// executeTypes only reads its Definitions afterward: Build is what
// surfaces an unrecognized -t/-T name or a malformed --type-add TYPESPEC,
// and rg keeps validating those even under --type-list (verified: `rg -t
// bogus --type-list` still exits 2 with "unrecognized file type: bogus"
// -- rg constructs its Types matcher unconditionally, before ever
// dispatching on Mode).
func executeTypes(cfg *Config, stdout, stderr io.Writer) int {
	b := filetype.NewBuilder()
	b.AddDefaults()
	if err := b.Apply(convertTypeChanges(cfg.TypeChanges)); err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}
	if _, err := b.Build(); err != nil {
		fmt.Fprintf(stderr, "gg: %s\n", err)
		return 2
	}

	defs := b.Definitions()
	for _, d := range defs {
		fmt.Fprint(stdout, d.Name, ": ")
		for i, g := range d.Globs {
			if i > 0 {
				fmt.Fprint(stdout, ", ")
			}
			fmt.Fprint(stdout, g)
		}
		fmt.Fprint(stdout, "\n")
	}

	// Matches rg's own exit code exactly (crates/core/main.rs's types()):
	// 1 only if the table ended up completely empty (every type
	// --type-clear'd with nothing added back), 0 otherwise -- never
	// "matched vs not", since --type-list doesn't search anything.
	if len(defs) == 0 {
		return 1
	}
	return 0
}
