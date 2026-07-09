// Package glob compiles gitignore-style glob patterns into a single
// combined matcher: rg's "globset" equivalent. A Builder accumulates
// patterns (including '!'-prefixed whitelist/re-include patterns); Build
// compiles them into a Set that a walker can query per path with
// last-match-wins, reverse-order semantics matching gitignore's
// precedence rules.
package glob
