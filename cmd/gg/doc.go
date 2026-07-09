// Command gg is the gripgrep CLI: a thin wrapper that parses flags into
// config for the walk/match/search/printer packages and wires them
// together. It contains no search logic of its own — see PLAN.md's v1
// CLI scope for the target flag matrix.
//
// Flag parsing: use the stdlib flag package or a minimal hand-rolled
// parser only. Do NOT pull in cobra/viper (or any similarly heavy CLI
// framework) — per PLAN.md's "CLI startup" design decision, rg's whole
// linux-tree benchmark target is ~85ms, so process startup cost and
// init-time work are part of the performance budget, not just the hot
// search loop.
package main
