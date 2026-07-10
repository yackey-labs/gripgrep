package gripgrep_test

import (
	"fmt"

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
