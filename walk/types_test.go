package walk

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/yackey-labs/gripgrep/filetype"
	"github.com/yackey-labs/gripgrep/glob"
)

// buildTypesMatcher applies changes over rg's default type table and
// returns the compiled Matcher, failing the test on any error.
func buildTypesMatcher(t *testing.T, changes ...filetype.Change) *filetype.Matcher {
	t.Helper()
	b := filetype.NewBuilder()
	b.AddDefaults()
	if err := b.Apply(changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	m, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

func selectChange(name string) filetype.Change {
	return filetype.Change{Kind: filetype.Select, Arg: name}
}

func negateChange(name string) filetype.Change {
	return filetype.Change{Kind: filetype.Negate, Arg: name}
}

func TestTypesSelectFiltersByExtension(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.rs"), "a")
	writeFile(t, filepath.Join(root, "main.c"), "b")
	writeFile(t, filepath.Join(root, "note.txt"), "c")

	m := buildTypesMatcher(t, selectChange("rust"))
	got := visitFiles(t, root, Options{Types: m})
	want := []string{"main.rs"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTypesNegateAloneKeepsUntyped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.rs"), "a")
	writeFile(t, filepath.Join(root, "main.c"), "b")
	writeFile(t, filepath.Join(root, "note.xyz"), "c") // no default type

	m := buildTypesMatcher(t, negateChange("rust"))
	got := visitFiles(t, root, Options{Types: m})
	want := []string{"main.c", "note.xyz"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("-T rust alone: got %v, want %v (untyped files must pass through)", got, want)
	}
}

// TestTypesDoesNotPruneDirs mirrors TestGlobsRequireMatchDoesNotPruneDirs:
// -t rust must never stop descent into a directory just because the
// directory's own name isn't a .rs file -- files at every depth must
// still get their own chance to match.
func TestTypesDoesNotPruneDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "top.rs"), "a")
	writeFile(t, filepath.Join(root, "sub", "nested.rs"), "b")
	writeFile(t, filepath.Join(root, "sub", "deeper", "more.rs"), "c")
	writeFile(t, filepath.Join(root, "sub", "skip.c"), "d")

	m := buildTypesMatcher(t, selectChange("rust"))
	got := visitFiles(t, root, Options{Types: m})
	want := []string{"more.rs", "nested.rs", "top.rs"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (subdirectories must never be pruned by -t)", got, want)
	}
}

// TestTypesLowerPrecedenceThanGlobs verifies round #35's precedence
// probes against the real rg binary: -g/--iglob decides outright
// (Ignored or Whitelisted) before Types is ever consulted; Types only
// applies to paths -g left undecided.
func TestTypesLowerPrecedenceThanGlobs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.rs"), "a")
	writeFile(t, filepath.Join(root, "main.c"), "b")
	writeFile(t, filepath.Join(root, "note.txt"), "c")

	// -t rust -g '!*.rs': the glob excludes every .rs file outright
	// (Types never even consulted for them); remaining files (.c, .txt)
	// fall through to Types, which excludes them (not rust). Net: empty.
	var b glob.Builder
	b.Add("*.rs") // plain pattern = Ignored verdict, see classify's doc
	set, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	m := buildTypesMatcher(t, selectChange("rust"))
	got := visitFiles(t, root, Options{Globs: set, Types: m})
	if len(got) != 0 {
		t.Errorf("-g '*.rs' -t rust: got %v, want none (glob excludes .rs outright; type excludes the rest)", got)
	}
}

func TestTypesGlobOverridesTypeRestriction(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.rs"), "a")
	writeFile(t, filepath.Join(root, "main.c"), "b")
	writeFile(t, filepath.Join(root, "note.txt"), "c")

	// -g '*.c' -t rust: a POSITIVE override. main.c matches the override
	// outright (Whitelisted, short-circuits Types entirely) even though
	// it isn't the rust type. main.rs matches no override glob, and with
	// a positive override present, Set.Match/GlobsRequireMatch excludes
	// it before Types is ever consulted (round #35 probe).
	var b glob.Builder
	b.Add("!*.c") // flipped-polarity whitelist, matching cmd/gg's own -g CLI encoding
	set, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	m := buildTypesMatcher(t, selectChange("rust"))
	got := visitFiles(t, root, Options{Globs: set, GlobsRequireMatch: true, Types: m})
	want := []string{"main.c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestTypesWhitelistOverridesHidden verifies round #35's probe against
// the real rg binary: a Types Whitelist verdict overrides the
// hidden-file rule exactly like a Globs/ignore-stack whitelist already
// does (rg's sh type includes dotfiles like .bashrc; `rg -t sh` shows
// them without --hidden).
func TestTypesWhitelistOverridesHidden(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".bashrc"), "a")
	writeFile(t, filepath.Join(root, "visible.sh"), "b")

	m := buildTypesMatcher(t, selectChange("sh"))
	got := visitFiles(t, root, Options{Types: m})
	want := []string{".bashrc", "visible.sh"}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (-t sh's Whitelist must override the hidden-file rule)", got, want)
	}
}

func TestTypesNilOptionIsInert(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.rs"), "a")
	writeFile(t, filepath.Join(root, "main.c"), "b")

	got := visitFiles(t, root, Options{})
	want := []string{"main.c", "main.rs"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (nil Types must not filter anything)", got, want)
	}
}
