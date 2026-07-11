package filetype

import (
	"errors"
	"sort"
	"testing"

	"github.com/yackey-labs/gripgrep/glob"
)

func TestAddDefaultsSelectAllCompiles(t *testing.T) {
	// The "matcher-level oracle" the design review called for:
	// every default glob must actually COMPILE in package glob's engine,
	// not just round-trip as a string through Definitions(). Selecting
	// "all" builds one matcher out of every default type's every glob.
	b := NewBuilder()
	b.AddDefaults()
	b.Select("all")
	m, err := b.Build()
	if err != nil {
		t.Fatalf("Build() with -t all over every default type: %v", err)
	}
	if m == nil {
		t.Fatal("Build() returned a nil Matcher for a non-empty selection")
	}
}

func TestDefinitionsSortedNameAndGlobs(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	defs := b.Definitions()
	if len(defs) != 218 {
		t.Fatalf("got %d definitions, want 218 (verified byte-identical against `rg --type-list`)", len(defs))
	}
	if !sort.SliceIsSorted(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name }) {
		t.Error("Definitions() not sorted by name")
	}
	for _, d := range defs {
		if !sort.StringsAreSorted(d.Globs) {
			t.Errorf("type %q: globs not sorted: %v", d.Name, d.Globs)
		}
	}
}

func TestTypeAddExtendsExisting(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	if err := b.ApplyChange(Change{Kind: Add, Arg: "rust:*.rs2"}); err != nil {
		t.Fatal(err)
	}
	defs := b.Definitions()
	var rust *Def
	for i := range defs {
		if defs[i].Name == "rust" {
			rust = &defs[i]
		}
	}
	if rust == nil {
		t.Fatal("rust type missing")
	}
	want := []string{"*.rs", "*.rs2"}
	if !equalStrings(rust.Globs, want) {
		t.Errorf("rust globs = %v, want %v (type-add should EXTEND, not replace)", rust.Globs, want)
	}
}

func TestTypeAddIncludeDirective(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	if err := b.ApplyChange(Change{Kind: Add, Arg: "src:include:rust,c"}); err != nil {
		t.Fatal(err)
	}
	defs := b.Definitions()
	var src *Def
	for i := range defs {
		if defs[i].Name == "src" {
			src = &defs[i]
		}
	}
	if src == nil {
		t.Fatal("src type missing")
	}
	// rust: *.rs; c: *.[chH], *.[chH].in, *.cats -- composed and sorted.
	want := []string{"*.[chH]", "*.[chH].in", "*.cats", "*.rs"}
	if !equalStrings(src.Globs, want) {
		t.Errorf("src globs = %v, want %v", src.Globs, want)
	}
}

func TestTypeAddIncludeUnknownTypeErrors(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	err := b.ApplyChange(Change{Kind: Add, Arg: "combo:include:rust,bogus"})
	if !errors.Is(err, ErrInvalidDefinition) {
		t.Errorf("err = %v, want ErrInvalidDefinition", err)
	}
}

func TestTypeAddMalformedSpec(t *testing.T) {
	cases := []string{"foo", "foo:bar:baz", "", ":glob", "name:"}
	for _, spec := range cases {
		b := NewBuilder()
		b.AddDefaults()
		err := b.ApplyChange(Change{Kind: Add, Arg: spec})
		if !errors.Is(err, ErrInvalidDefinition) {
			t.Errorf("spec %q: err = %v, want ErrInvalidDefinition", spec, err)
		}
	}
}

func TestTypeAddReservedOrInvalidName(t *testing.T) {
	cases := []string{"all:*.foo", "fo-o:*.foo", "fo o:*.foo"}
	for _, spec := range cases {
		b := NewBuilder()
		err := b.ApplyChange(Change{Kind: Add, Arg: spec})
		if !errors.Is(err, ErrInvalidDefinition) {
			t.Errorf("spec %q: err = %v, want ErrInvalidDefinition", spec, err)
		}
	}
}

func TestSelectUnknownTypeErrorsAtBuild(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	b.Select("bogus")
	_, err := b.Build()
	if err == nil || err.Error() != "unrecognized file type: bogus" {
		t.Errorf("err = %v, want %q", err, "unrecognized file type: bogus")
	}
}

func TestClearThenSelectErrors(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	if err := b.ApplyChange(Change{Kind: Clear, Arg: "rust"}); err != nil {
		t.Fatal(err)
	}
	b.Select("rust")
	_, err := b.Build()
	if err == nil || err.Error() != "unrecognized file type: rust" {
		t.Errorf("err = %v, want unrecognized file type error (probed on the real rg binary: `rg --type-clear rust -t rust` errors)", err)
	}
}

func TestClearThenAddRebuilds(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	if err := b.ApplyChange(Change{Kind: Clear, Arg: "rust"}); err != nil {
		t.Fatal(err)
	}
	if err := b.ApplyChange(Change{Kind: Add, Arg: "rust:*.rs2"}); err != nil {
		t.Fatal(err)
	}
	b.Select("rust")
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	// Only the rebuilt glob applies -- *.rs (the original default) must
	// NOT match anymore (probed on the real rg binary: `rg --type-clear rust --type-add
	// 'rust:*.rs2' --type-list` shows only "rust: *.rs2").
	if m.Match([]byte("main.rs")) != glob.NoMatch {
		t.Error("main.rs matched after --type-clear rust: old glob should be gone")
	}
	if m.Match([]byte("main.rs2")) != glob.Whitelisted {
		t.Error("main.rs2 did not match the rebuilt rust type")
	}
}

func TestSelectNegateSameTypeLastWins(t *testing.T) {
	// Round #35 probes against the real rg binary: `-t rust -T rust`
	// excludes .rs files (negate given last wins); `-T rust -t rust`
	// includes them (select given last wins).
	b := NewBuilder()
	b.AddDefaults()
	b.Select("rust")
	b.Negate("rust")
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Match([]byte("main.rs")); got != glob.Ignored {
		t.Errorf("-t rust -T rust: main.rs = %v, want Ignored", got)
	}

	b2 := NewBuilder()
	b2.AddDefaults()
	b2.Negate("rust")
	b2.Select("rust")
	m2, err := b2.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := m2.Match([]byte("main.rs")); got != glob.Whitelisted {
		t.Errorf("-T rust -t rust: main.rs = %v, want Whitelisted", got)
	}
}

func TestNegateAloneKeepsUntyped(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	b.Negate("rust")
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if m.RequireMatch() {
		t.Error("-T alone must not set RequireMatch (untyped files must pass through)")
	}
	if got := m.Match([]byte("note.xyz")); got != glob.NoMatch {
		t.Errorf("untyped file under -T rust = %v, want NoMatch (kept)", got)
	}
	if got := m.Match([]byte("main.rs")); got != glob.Ignored {
		t.Errorf("main.rs under -T rust = %v, want Ignored", got)
	}
}

func TestSelectAllThenNegateOne(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	b.Select("all")
	b.Negate("rust")
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !m.RequireMatch() {
		t.Error("-t all must set RequireMatch")
	}
	if got := m.Match([]byte("main.rs")); got != glob.Ignored {
		t.Errorf("main.rs under `-t all -T rust` = %v, want Ignored (negate given after all)", got)
	}
	if got := m.Match([]byte("main.c")); got != glob.Whitelisted {
		t.Errorf("main.c under `-t all -T rust` = %v, want Whitelisted", got)
	}
}

func TestNoSelectionsBuildsNilMatcher(t *testing.T) {
	b := NewBuilder()
	b.AddDefaults()
	if err := b.ApplyChange(Change{Kind: Add, Arg: "foo:*.foo"}); err != nil {
		t.Fatal(err)
	}
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Error("Build() with zero Select/Negate must return a nil Matcher (inert, per rg's Types::is_empty gating on selections)")
	}
}

func TestNilMatcherIsInert(t *testing.T) {
	var m *Matcher
	if m.Match([]byte("anything.rs")) != glob.NoMatch {
		t.Error("nil Matcher.Match must always return NoMatch")
	}
	if m.RequireMatch() {
		t.Error("nil Matcher.RequireMatch must always return false")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
