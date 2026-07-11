package filetype

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// ChangeKind identifies which of -t/-T/--type-add/--type-clear one Change
// represents -- mirrors rg's TypeChange enum (crates/core/flags/lowargs.rs).
type ChangeKind uint8

const (
	// Select is -t/--type NAME.
	Select ChangeKind = iota
	// Negate is -T/--type-not NAME.
	Negate
	// Add is --type-add TYPESPEC.
	Add
	// Clear is --type-clear NAME.
	Clear
)

// Change is one -t/-T/--type-add/--type-clear operation, in the exact
// order it appeared on the command line. Order matters throughout this
// package: see Builder's doc.
type Change struct {
	Kind ChangeKind
	// Arg is the type NAME for Select/Negate/Clear, or the raw TYPESPEC
	// string for Add (e.g. "foo:*.foo" or "foo:include:cpp,py").
	Arg string
}

// ErrInvalidDefinition is returned by Apply for a malformed --type-add
// TYPESPEC: not "name:glob" or "name:include:list", an empty name/glob, a
// type name that isn't all Unicode letters/numbers (or is the reserved
// name "all"), or an include directive naming a type that doesn't exist
// yet. rg's own error text is identical across all of these (verified
// against the real rg 15.1.0 binary: every case above prints "invalid
// definition (format is type:glob, e.g., html:*.html)"), so gg matches it
// with one sentinel rather than rg's own finer-grained (but
// identically-worded) Error enum.
var ErrInvalidDefinition = errors.New("invalid definition (format is type:glob, e.g., html:*.html)")

// errUnrecognizedFileType matches rg's exact wording (verified against the
// real binary): "unrecognized file type: NAME". Returned by Build when a
// Select/Negate names a type that doesn't exist in the final table.
func errUnrecognizedFileType(name string) error {
	return fmt.Errorf("unrecognized file type: %s", name)
}

// selection is one resolved Select/Negate entry: by the time it lands
// here, an "all" Change has already been expanded to one selection per
// type name current at that point (see Select/Negate's doc) -- Build
// looks up globs by name later, using the table's FINAL state (see
// Build's doc for why that lookup is deliberately deferred).
type selection struct {
	name   string
	negate bool
}

// Builder mirrors ripgrep's TypesBuilder (crates/ignore/src/types.rs)
// closely enough to reproduce its exact order-dependent behavior:
//
//   - AddDefaults/Add/Clear mutate a live name->globs table as they're
//     called, exactly like TypesBuilder's own `types: HashMap<...>`.
//   - Select("all")/Negate("all") expand to one selection per name
//     CURRENTLY in that table, snapshotted at the moment they're called
//     -- not at Build time. A --type-clear or --type-add AFTER "-t all"
//     does not retroactively add or remove it from that expansion (rg's
//     own select(): "if name == all { for name in self.types.keys() ...
//     }", executed immediately, not deferred).
//   - A plain Select/Negate(name) is NOT validated when it's called --
//     only its NAME is recorded. The glob lookup happens at Build, using
//     the table's FINAL state after every Change has been applied (rg's
//     own build(): `self.types.get(selection.name())`, called once, after
//     the whole CLI has been processed) -- so `--type-clear rust -t rust`
//     errors (rust no longer exists at Build time) but `--type-clear rust
//     --type-add 'rust:*.rs2' -t rust` succeeds, using ONLY the rebuilt
//     glob (verified against the real rg binary, round #35's probes).
//
// Since every Add/Clear/Select/Negate must therefore be applied ONE AT A
// TIME, in exact CLI order (never batch-sorted or reordered), callers
// must use Apply (or ApplyChange in a loop) rather than calling
// AddDefaults/Select/etc. out of order.
type Builder struct {
	types      map[string][]string // name -> globs, mutated in place
	selections []selection
}

// NewBuilder returns an empty Builder (no type definitions at all --
// callers that want rg's default table must call AddDefaults first, as rg
// itself always does before applying any CLI Change: crates/core/flags/
// hiargs.rs's types() calls `builder.add_defaults()` unconditionally
// before iterating type_changes).
func NewBuilder() *Builder {
	return &Builder{types: make(map[string][]string, len(defaultTypes)*2)}
}

// AddDefaults registers rg's built-in type table (filetype/default_types.go,
// extracted from ripgrep's own crates/ignore/src/default_types.rs -- see
// that file's doc). Mirrors TypesBuilder::add_defaults() exactly: for each
// entry's name (some entries define more than one alias for the same glob
// list, e.g. bat/batch), for each glob, register one (name, glob) pair.
func (b *Builder) AddDefaults() {
	for _, e := range defaultTypes {
		for _, name := range e.names {
			b.types[name] = append(b.types[name], e.globs...)
		}
	}
}

// isValidTypeName reports whether name is legal for Add/AddDef: non-empty,
// every rune a Unicode letter or number, and not the reserved name "all"
// (rg: "name can be arbitrary ... If name is all ... then an error is
// returned").
func isValidTypeName(name string) bool {
	if name == "" || name == "all" {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// add registers one (name, glob) pair, mutating the live table in place --
// mirrors TypesBuilder::add.
func (b *Builder) add(name, glob string) error {
	if !isValidTypeName(name) {
		return ErrInvalidDefinition
	}
	b.types[name] = append(b.types[name], glob)
	return nil
}

// addDef parses and applies one --type-add TYPESPEC, mirroring
// TypesBuilder::add_def's two accepted shapes: "name:glob" (a root
// definition) and "name:include:type1,type2,..." (composes existing
// types' globs into a new one, evaluated against the table's state AT
// THIS POINT, not deferred -- an include naming a not-yet-defined type
// fails immediately, exactly like rg: verified against the real binary).
func (b *Builder) addDef(spec string) error {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 2:
		name, g := parts[0], parts[1]
		if name == "" || g == "" {
			return ErrInvalidDefinition
		}
		return b.add(name, g)
	case 3:
		name, kw, list := parts[0], parts[1], parts[2]
		if name == "" || kw != "include" || list == "" {
			return ErrInvalidDefinition
		}
		included := strings.Split(list, ",")
		for _, t := range included {
			if _, ok := b.types[t]; !ok {
				return ErrInvalidDefinition
			}
		}
		for _, t := range included {
			for _, g := range b.types[t] {
				if err := b.add(name, g); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return ErrInvalidDefinition
	}
}

// selectOrNegate implements Select/Negate's shared "all" expansion (see
// Builder's doc): sorted for determinism (rg's own HashMap iteration
// order is unspecified there too, but harmless -- every name expanded
// this way shares the SAME polarity, so their relative order among
// themselves never changes a Match outcome, only their position relative
// to some OTHER, separately-given Change, which this preserves since the
// whole expansion happens contiguously at this Change's CLI position).
func (b *Builder) selectOrNegate(name string, negate bool) {
	if name == "all" {
		names := make([]string, 0, len(b.types))
		for n := range b.types {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			b.selections = append(b.selections, selection{name: n, negate: negate})
		}
		return
	}
	b.selections = append(b.selections, selection{name: name, negate: negate})
}

// Select records one -t/--type NAME (or, for NAME == "all", every type
// name currently defined -- see Builder's doc). Never errors: an unknown
// name is only detected at Build.
func (b *Builder) Select(name string) { b.selectOrNegate(name, false) }

// Negate records one -T/--type-not NAME. See Select's doc.
func (b *Builder) Negate(name string) { b.selectOrNegate(name, true) }

// Clear removes name's definition from the table (a no-op if it doesn't
// exist, matching HashMap::remove -- rg's TypesBuilder::clear never
// errors either). Globs can be re-added under the same name afterward via
// Add/AddDef.
func (b *Builder) Clear(name string) { delete(b.types, name) }

// ApplyChange applies one Change, dispatching to Select/Negate/addDef/
// Clear. Must be called in exact CLI order -- see Builder's doc.
func (b *Builder) ApplyChange(c Change) error {
	switch c.Kind {
	case Select:
		b.Select(c.Arg)
	case Negate:
		b.Negate(c.Arg)
	case Add:
		return b.addDef(c.Arg)
	case Clear:
		b.Clear(c.Arg)
	}
	return nil
}

// Apply applies every Change in changes, in order, stopping at (and
// returning) the first error.
func (b *Builder) Apply(changes []Change) error {
	for _, c := range changes {
		if err := b.ApplyChange(c); err != nil {
			return err
		}
	}
	return nil
}

// Def is one file-type definition as reported by Definitions: a name and
// its globs, both used only for display (--type-list), never for
// matching -- Build compiles the matcher separately, from the same
// underlying table.
type Def struct {
	Name  string
	Globs []string
}

// Definitions returns every type currently in the table (reflecting every
// AddDefaults/Add/Clear applied so far, but NOT filtered by any Select/
// Negate -- rg's own definitions() reads self.types, never
// self.selections), sorted by name, with each Def's Globs sorted
// lexicographically. Sorting happens here rather than being baked into
// the table, because --type-add/--type-clear mutate the table at
// runtime -- matches rg's own TypesBuilder::definitions(), which re-sorts
// on every call for the same reason.
func (b *Builder) Definitions() []Def {
	defs := make([]Def, 0, len(b.types))
	for name, globs := range b.types {
		gs := append([]string(nil), globs...)
		sort.Strings(gs)
		defs = append(defs, Def{Name: name, Globs: gs})
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}
