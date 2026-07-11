package walk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestComparePathComponentWise pins the component-wise ordering rg's --sort
// path uses: the '/' boundary is a component break, NOT byte 0x2F, so a
// directory sorts against a sibling file by name. A regression to a plain
// byte-wise strings.Compare would flip every one of these pairs.
func TestComparePathComponentWise(t *testing.T) {
	cases := []struct {
		a, b string
		want int // sign of comparePath(a,b)
	}{
		// dir "a" (a/deep.txt) sorts before file "a+.txt": component "a" <
		// "a+.txt"; byte-wise "a/" > "a+" since '/'(0x2F) > '+'(0x2B).
		{"a/deep.txt", "a+.txt", -1},
		// dir "b" (b/m.txt) sorts before file "b.txt": component "b" <
		// "b.txt"; byte-wise "b/" > "b." since '/'(0x2F) > '.'(0x2E).
		{"b/m.txt", "b.txt", -1},
		{"b/m.txt", "ba.txt", -1},
		{"b.txt", "ba.txt", -1},
		// nested components compare left to right.
		{"sub/a.txt", "sub/b.txt", -1},
		{"sub/charlie.txt", "zz/delta.txt", -1},
		// "./"-prefixed group sorts before a bare "sub" (component "." <
		// "sub").
		{"./alpha.txt", "sub/charlie.txt", -1},
		// identical paths compare equal.
		{"x/y.txt", "x/y.txt", 0},
		// reflexive antisymmetry sanity.
		{"a+.txt", "a/deep.txt", 1},
	}
	for _, c := range cases {
		got := comparePath(c.a, c.b)
		if sign(got) != c.want {
			t.Errorf("comparePath(%q,%q) = %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

// TestSortByTimeStableTiesBothDirections verifies rg's tie contract: a
// stable sort with a REVERSED COMPARATOR means files with equal timestamps
// keep their discovery order in BOTH ascending and descending sorts (never
// a sort-then-reverse, which would flip the tie).
func TestSortByTimeStableTiesBothDirections(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	// Discovery order: e0, e1 tie at base; older sorts first, newer last.
	build := func() []sortItem {
		return []sortItem{
			{path: "older", modtime: base.Add(-time.Hour), hasTime: true},
			{path: "e0", modtime: base, hasTime: true},   // tie, discovered first
			{path: "e1", modtime: base, hasTime: true},   // tie, discovered second
			{path: "newer", modtime: base.Add(time.Hour), hasTime: true},
		}
	}

	asc := build()
	sortByTime(asc, false)
	if got := paths(asc); !eq(got, []string{"older", "e0", "e1", "newer"}) {
		t.Errorf("asc = %v, want [older e0 e1 newer] (ties keep discovery order)", got)
	}

	desc := build()
	sortByTime(desc, true)
	// newer first, but the e0/e1 tie STILL keeps discovery order (e0 before
	// e1) -- the reversed comparator does not flip equal keys.
	if got := paths(desc); !eq(got, []string{"newer", "e0", "e1", "older"}) {
		t.Errorf("desc = %v, want [newer e0 e1 older] (ties NOT reversed)", got)
	}
}

// TestCompareTimeMissingSortsLast pins rg's rule that a file whose stat
// failed (hasTime false) sorts after one with a time, ascending.
func TestCompareTimeMissingSortsLast(t *testing.T) {
	have := sortItem{path: "have", modtime: time.Unix(1, 0), hasTime: true}
	missing := sortItem{path: "missing"}
	if compareTime(have, missing) >= 0 {
		t.Errorf("have should sort before missing (ascending)")
	}
	if compareTime(missing, have) <= 0 {
		t.Errorf("missing should sort after have (ascending)")
	}
	if compareTime(missing, missing) != 0 {
		t.Errorf("two missing times compare equal")
	}
}

// TestWalkSortedPathPerRoot verifies that ascending --sort path is applied
// PER ROOT in argument order (rg's during-traversal sort_by_file_name): each
// root's whole subtree is emitted, internally sorted, before the next root's
// begins -- so a later-sorting root argument still comes first if it was
// named first.
func TestWalkSortedPathPerRoot(t *testing.T) {
	root := buildTree(t, map[string]string{
		"zebra/z.txt": "z",
		"apple/a.txt": "a",
	})
	// Roots given as [zebra, apple]: per-root asc-path keeps that argument
	// order, NOT a global sort (which would put apple/ first).
	got := collectSorted(t, []string{filepath.Join(root, "zebra"), filepath.Join(root, "apple")},
		SortConfig{Kind: SortPath})
	want := []string{filepath.Join(root, "zebra", "z.txt"), filepath.Join(root, "apple", "a.txt")}
	if !eq(got, want) {
		t.Errorf("per-root asc path = %v, want %v", got, want)
	}
}

// TestWalkSortedModifiedGlobal verifies that time-keyed sorts collect ALL
// roots into one buffer and sort globally (rg's collect-then-sort), so files
// from different roots interleave by timestamp rather than staying grouped
// by root.
func TestWalkSortedModifiedGlobal(t *testing.T) {
	root := buildTree(t, map[string]string{
		"r1/new.txt": "n",
		"r2/old.txt": "o",
	})
	newer := filepath.Join(root, "r1", "new.txt")
	older := filepath.Join(root, "r2", "old.txt")
	if err := os.Chtimes(older, time.Unix(1000, 0), time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, time.Unix(2000, 0), time.Unix(2000, 0)); err != nil {
		t.Fatal(err)
	}
	// Roots [r1, r2] but ascending modified must emit older (r2) first,
	// proving a global sort across roots rather than per-root grouping.
	got := collectSorted(t, []string{filepath.Join(root, "r1"), filepath.Join(root, "r2")},
		SortConfig{Kind: SortModified})
	if !eq(got, []string{older, newer}) {
		t.Errorf("global modified = %v, want [%s %s]", got, older, newer)
	}
}

// collectSorted runs a sorted Walk over roots and returns the visited file
// paths in emission order.
func collectSorted(t *testing.T, roots []string, sc SortConfig) []string {
	t.Helper()
	var got []string
	err := Walk(roots, Options{NoIgnoreDot: true, NoIgnoreVcs: true, Hidden: true, Sort: sc},
		func(e *Entry) WalkState {
			if e.Err == nil && e.Type == TypeFile {
				got = append(got, e.Path)
			}
			return Continue
		})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return got
}

func paths(items []sortItem) []string {
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].path
	}
	return out
}

func eq(a, b []string) bool {
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
