package filetype

import (
	"strings"

	"github.com/yackey-labs/gripgrep/glob"
)

// Matcher is a compiled, ready-to-query file-type filter (the result of
// Build). A nil *Matcher is the zero-cost "no -t/-T active" case: Match
// always returns glob.NoMatch and RequireMatch always returns false, so a
// caller need not special-case "no Types option was set" separately from
// "Types was set but produced an inert matcher" -- see walk.Options.Types'
// doc for how callers thread that through the walk's hot path.
type Matcher struct {
	set          *glob.Set
	requireMatch bool
}

// Build compiles b's accumulated selections into a Matcher, resolving
// each selection's glob list from the table's FINAL state (see Builder's
// doc for why that resolution is deliberately deferred to here rather
// than done at Select/Negate time). Returns (nil, nil) if there are no
// selections at all -- e.g. only --type-add/--type-clear were given, with
// no -t/-T -- since rg's own Types::is_empty() (selections.is_empty(),
// NOT types.is_empty()) gates its whole matched() branch the same way
// (crates/ignore/src/dir.rs: `if !self.inner.types.is_empty() { ... }`),
// making an all-Add/Clear, no-Select/Negate matcher permanently inert.
//
// Build reuses package glob's existing gitignore-style Builder/Set rather
// than a bespoke matcher: encoding each Select glob as a '!'-prefixed
// (whitelist) pattern and each Negate glob as a plain (ignore) pattern,
// in Select/Negate CLI order, reproduces rg's own Types::matched() last-
// glob-wins-by-add-order precedence exactly (crates/ignore/src/types.rs:
// "the highest precedent match is the last one" over ALL globs from every
// selection flattened into one ordered set, not per-selection) --
// verified against the real rg binary for the two cases that matter most
// (round #35 probes): `-t rust -T rust` (negate given last -> excluded)
// and `-T rust -t rust` (select given last -> included).
//
// Every glob is escaped via escapeGlobSpecials first: rg's own Types
// matcher treats a type glob as a literal fnmatch pattern with no
// leading-character meaning of its own (crates/ignore/src/types.rs's
// add(): raw GlobBuilder, not gitignore syntax), unlike package glob's
// Builder, which is gitignore-flavored and treats a leading '!' as its
// own whitelist toggle and a leading '#' as a comment. This only matters
// for --type-add's user-supplied glob -- every default-type glob (round
// #35's audit of filetype/default_types.go) already avoids both
// characters as a first byte.
//
// Known, deliberate gap: a --type-add glob containing a literal '/' is
// matched by package glob as a gitignore-anchored (full relative path)
// pattern, whereas real rg's Types matcher only ever tests a bare file
// name (crates/ignore/src/types.rs's matched(): `file_name(path)`) and so
// would never match such a glob at all. No default-type glob contains
// '/' (round #35's audit), and this divergence only changes behavior for
// a hand-written --type-add spec that itself embeds a path separator --
// treated as out-of-scope debt for this round rather than blocking it.
func (b *Builder) Build() (*Matcher, error) {
	if len(b.selections) == 0 {
		return nil, nil
	}
	hasSelected := false
	for _, s := range b.selections {
		if !s.negate {
			hasSelected = true
			break
		}
	}

	var gb glob.Builder
	for _, s := range b.selections {
		globs, ok := b.types[s.name]
		if !ok {
			return nil, errUnrecognizedFileType(s.name)
		}
		for _, g := range globs {
			g = escapeGlobSpecials(g)
			if s.negate {
				gb.Add(g)
			} else {
				gb.Add("!" + g)
			}
		}
	}
	set, err := gb.Build()
	if err != nil {
		return nil, err
	}
	return &Matcher{set: set, requireMatch: hasSelected}, nil
}

// escapeGlobSpecials neutralizes package glob's gitignore-syntax leading-
// character meanings ('!' whitelist, '#' comment) for a raw type glob --
// see Build's doc.
func escapeGlobSpecials(g string) string {
	if strings.HasPrefix(g, "!") || strings.HasPrefix(g, "#") {
		return `\` + g
	}
	return g
}

// Match reports how path (root-relative, matching walk.Entry.Path's
// convention) matches m's compiled type globs. Callers must never call
// this for a directory -- rg's own Types::matched() never applies to
// directories at all (it returns Match::None unconditionally when
// is_dir), which is why this signature takes no isDir parameter: the
// walk-side call site is expected to gate on file-only itself (see
// walk/worker.go's classify).
func (m *Matcher) Match(path []byte) glob.MatchResult {
	if m == nil {
		return glob.NoMatch
	}
	return m.set.Match(path, false)
}

// RequireMatch reports whether m has at least one Select (non-negated)
// entry -- when true, a path matching NONE of m's globs must be treated
// as excluded, not merely "no verdict" (rg: "if at least one file type is
// selected and path doesn't match, then the path is also considered
// ignored" -- crates/ignore/src/types.rs's Types::matched doc). A nil
// Matcher always reports false.
func (m *Matcher) RequireMatch() bool {
	return m != nil && m.requireMatch
}
