package glob

// Set is a compiled collection of gitignore-style glob patterns, queried
// as one combined unit per path.
//
// TODO(M1-glob): compiled matcher state — literal/prefix/suffix fast
// classes plus a regexp fallback per glob, pooled scratch slices for
// matches-into-buffer style queries.
type Set struct {
	_ struct{} // TODO(M1-glob): remove once fields exist
}

// Builder accumulates glob patterns before compiling them into a Set.
// The zero value is ready to use.
type Builder struct {
	// TODO(M1-glob): patterns []string plus per-pattern whitelist flag,
	// source-file/line for error messages, etc.
}

// Add registers one gitignore-style pattern. A pattern prefixed with '!'
// is a whitelist (re-include) entry, matching gitignore syntax. Add
// never fails outright; a malformed pattern is recorded and surfaced by
// Build. Add returns the Builder to allow chaining.
//
// TODO(M1-glob): actually record the pattern; parse '!' prefix and
// directory-only trailing '/'.
func (b *Builder) Add(pattern string) *Builder {
	return b
}

// Build compiles all patterns added via Add into a single Set.
//
// TODO(M1-glob): real compilation. The M0 stub returns an empty, always-
// non-matching Set and a nil error.
func (b *Builder) Build() (*Set, error) {
	return &Set{}, nil
}

// MatchResult is the outcome of matching one path against a Set.
type MatchResult uint8

const (
	// NoMatch means no pattern in the Set matched this path.
	NoMatch MatchResult = iota
	// Ignored means the winning match (last match, per gitignore
	// last-match-wins precedence) was an ordinary (non-whitelist)
	// pattern: the path should be excluded.
	Ignored
	// Whitelisted means the winning match was a '!'-prefixed pattern:
	// the path should be included even though an earlier/outer pattern
	// matched it.
	Whitelisted
)

// Match reports how path matches the compiled Set.
//
// path is the byte slice being tested and is never converted to string
// on this hot path — Match is called once per walked entry. isDir lets
// directory-only patterns (trailing '/' in the source pattern) match
// correctly. Match must not allocate in steady state.
//
// TODO(M1-glob): real last-match-wins evaluation over compiled globs.
// The M0 stub always returns NoMatch.
func (s *Set) Match(path []byte, isDir bool) MatchResult {
	return NoMatch
}
