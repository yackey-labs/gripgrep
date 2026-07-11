// Package filetype implements rg's file-type system: -t/--type,
// -T/--type-not, --type-add, --type-clear, and --type-list.
// It mirrors ripgrep's crates/ignore/src/types.rs (TypesBuilder/Types)
// closely enough that Builder's Apply/Build sequencing reproduces the same
// observable behavior, including its more surprising corners -- see
// Builder's doc for the "all" expansion-order nuance and Build's doc for
// the last-match-wins precedence within a single compiled matcher.
package filetype
