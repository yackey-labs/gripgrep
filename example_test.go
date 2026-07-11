package gripgrep_test

import (
	"fmt"
	"sort"

	"github.com/yackey-labs/gripgrep"
)

// ExampleSearch runs the default, CLI-equivalent search against a single
// named file (rather than a directory) so its output is deterministic --
// gripgrep, like the gg CLI it's built on, searches multiple files in
// parallel, so a directory example's line order isn't reproducible
// across runs.
func ExampleSearch() {
	matches, err := gripgrep.Search("hello", "testdata/corpus/a/b/foo.txt")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, m := range matches {
		fmt.Println(m.Line)
	}
	// Output:
	// hello world
}

// ExampleOptions_Search shows CLI-flag-equivalent control via Options --
// here, -i/--ignore-case.
func ExampleOptions_Search() {
	opts := gripgrep.Options{IgnoreCase: true}
	matches, err := opts.Search("CAT", "testdata/corpus/a/b/foo.txt")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, m := range matches {
		fmt.Println(m.LineNumber, m.Line)
	}
	// Output:
	// 3 the cat sat on the mat
	// 4 CATERPILLAR should not match a whole-word search for "cat"
}

// ExampleSearchStream shows the streaming form: fn is called once per
// match as it's found, and returning false stops the search early. A
// single named file keeps delivery order deterministic for the example
// (see ExampleSearch's doc); SearchStream itself searches every path
// concurrently.
func ExampleSearchStream() {
	err := gripgrep.SearchStream("cat", []string{"testdata/corpus/a/b/foo.txt"}, func(m gripgrep.Match) bool {
		fmt.Println(m.Line)
		return true
	})
	if err != nil {
		fmt.Println("error:", err)
	}
	// Output:
	// the cat sat on the mat
	// CATERPILLAR should not match a whole-word search for "cat"
}

// ExampleFilesWithMatch lists paths containing at least one match, like
// gg -l. testdata/corpus/a has exactly one file matching "hello", so the
// result is deterministic without needing to sort (unlike a directory
// with multiple matching files -- see ExampleFiles below).
func ExampleFilesWithMatch() {
	files, err := gripgrep.FilesWithMatch("hello", "testdata/corpus/a")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, f := range files {
		fmt.Println(f)
	}
	// Output:
	// testdata/corpus/a/b/foo.txt
}

// ExampleCountMatches returns a map from path to match count, like gg -c.
// testdata/corpus/a has exactly one file matching "hello", so the single
// map entry prints deterministically.
func ExampleCountMatches() {
	counts, err := gripgrep.CountMatches("hello", "testdata/corpus/a")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for path, n := range counts {
		fmt.Println(path, n)
	}
	// Output:
	// testdata/corpus/a/b/foo.txt 1
}

// ExampleFiles lists every file that would be searched under a root,
// like gg --files, without matching anything. Files walks in parallel
// like every other verb in this package (see ExampleSearch's doc), so
// its result order is nondeterministic across runs -- this example sorts
// before printing to keep the Output block reproducible, which is the
// pattern to follow for any multi-file FilesWithMatch/Files/CountMatches
// use where order matters to the caller.
func ExampleFiles() {
	files, err := gripgrep.Files("testdata/facade/globs")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	sort.Strings(files)
	for _, f := range files {
		fmt.Println(f)
	}
	// Output:
	// testdata/facade/globs/File.TXT
	// testdata/facade/globs/file.txt
	// testdata/facade/globs/other.md
}

// ExampleOptions_Search_globs shows filtering the walked file set with
// -g/--glob before matching: only notes.txt (not readme.md) is searched.
func ExampleOptions_Search_globs() {
	opts := gripgrep.Options{Globs: []string{"*.txt"}}
	matches, err := opts.Search("needle", "testdata/facade/examples")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, m := range matches {
		fmt.Println(m.Path, m.Line)
	}
	// Output:
	// testdata/facade/examples/notes.txt needle in notes
}

// ExampleOptions_Search_maxCount shows -m/--max-count: only the first 2
// of the fixture's 5 matching lines are returned.
func ExampleOptions_Search_maxCount() {
	opts := gripgrep.Options{MaxCount: 2}
	matches, err := opts.Search("alpha", "testdata/facade/maxcount.txt")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, m := range matches {
		fmt.Println(m.Line)
	}
	// Output:
	// alpha one
	// alpha two
}

// ExampleOptions_Search_lineRegexp shows -x/--line-regexp: only the line
// that is exactly "cat" matches, not "cats", " cat", or "category".
func ExampleOptions_Search_lineRegexp() {
	opts := gripgrep.Options{LineRegexp: true}
	matches, err := opts.Search("cat", "testdata/facade/lineregexp.txt")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, m := range matches {
		fmt.Println(m.Line)
	}
	// Output:
	// cat
}

// ExampleOptions_Search_types shows -t/--type filtering: of the fixture's
// three files (one each of Go/Python/plain-text, all containing
// "needle"), only the Go one is searched.
func ExampleOptions_Search_types() {
	opts := gripgrep.Options{Types: []string{"go"}}
	matches, err := opts.Search("needle", "testdata/facade/types")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, m := range matches {
		fmt.Println(m.Path, m.Line)
	}
	// Output:
	// testdata/facade/types/main.go // needle marks the line this fixture's Types/TypesNot tests search for.
	// testdata/facade/types/main.go var needle = "found"
}

// ExampleOptions_Search_columnAndByteOffset shows Match.Column (1-based
// byte column of the first match on the line) and Match.ByteOffset
// (absolute byte offset of the line's start), mirroring the CLI's
// --column/-b. A single named file keeps result order deterministic
// (see ExampleSearch's doc).
func ExampleOptions_Search_columnAndByteOffset() {
	matches, err := gripgrep.Search("needle", "testdata/facade/column.txt")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, m := range matches {
		fmt.Println(m.LineNumber, m.Column, m.ByteOffset, m.Line)
	}
	// Output:
	// 1 4 0 xx needle one
	// 2 1 14 needle two
	// 3 4 25    needle three
}
