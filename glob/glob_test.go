package glob

import (
	"fmt"
	"testing"
)

// buildSet compiles patterns (one per Builder.Add call, in order) into a
// Set, failing the test on any compile error.
func buildSet(t *testing.T, patterns ...string) *Set {
	t.Helper()
	var b Builder
	for _, p := range patterns {
		b.Add(p)
	}
	s, err := b.Build()
	if err != nil {
		t.Fatalf("Build(%q): %v", patterns, err)
	}
	return s
}

func assertIgnored(t *testing.T, patterns []string, path string, isDir bool) {
	t.Helper()
	s := buildSet(t, patterns...)
	got := s.Match([]byte(path), isDir)
	if got != Ignored {
		t.Errorf("patterns %q: Match(%q, isDir=%v) = %v, want Ignored", patterns, path, isDir, got)
	}
}

func assertNotIgnored(t *testing.T, patterns []string, path string, isDir bool) {
	t.Helper()
	s := buildSet(t, patterns...)
	got := s.Match([]byte(path), isDir)
	if got == Ignored {
		t.Errorf("patterns %q: Match(%q, isDir=%v) = Ignored, want NoMatch or Whitelisted", patterns, path, isDir)
	}
}

// The following are ported from the `ignored!`/`not_ignored!` table in
// ../ripgrep/crates/ignore/src/gitignore.rs. Cases that exercise
// Gitignore's root-relative path stripping (passing a path like
// "./foo/bar" or a root other than the gitignore's own directory) are
// intentionally excluded: this package's Match contract takes a path
// already relative to the ignore file's directory (see glob.go's Match
// doc comment) — normalizing a caller's path against a root is the
// walk package's job, not glob's.
func TestGitignoreSemanticsIgnored(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		path     string
		isDir    bool
	}{
		{"ig1", []string{"months"}, "months", false},
		{"ig2", []string{"*.lock"}, "Cargo.lock", false},
		{"ig3", []string{"*.rs"}, "src/main.rs", false},
		{"ig4", []string{"src/*.rs"}, "src/main.rs", false},
		{"ig5", []string{"/*.c"}, "cat-file.c", false},
		{"ig6", []string{"/src/*.rs"}, "src/main.rs", false},
		{"ig7", []string{"!src/main.rs", "*.rs"}, "src/main.rs", false},
		{"ig8", []string{"foo/"}, "foo", true},
		{"ig9", []string{"**/foo"}, "foo", false},
		{"ig10", []string{"**/foo"}, "src/foo", false},
		{"ig11", []string{"**/foo/**"}, "src/foo/bar", false},
		{"ig12", []string{"**/foo/**"}, "wat/src/foo/bar/baz", false},
		{"ig13", []string{"**/foo/bar"}, "foo/bar", false},
		{"ig14", []string{"**/foo/bar"}, "src/foo/bar", false},
		{"ig15", []string{"abc/**"}, "abc/x", false},
		{"ig16", []string{"abc/**"}, "abc/x/y", false},
		{"ig17", []string{"abc/**"}, "abc/x/y/z", false},
		{"ig18", []string{"a/**/b"}, "a/b", false},
		{"ig19", []string{"a/**/b"}, "a/x/b", false},
		{"ig20", []string{"a/**/b"}, "a/x/y/b", false},
		{"ig21", []string{`\!xy`}, "!xy", false},
		{"ig22", []string{`\#foo`}, "#foo", false},
		{"ig23", []string{"foo"}, "./foo", false},
		{"ig24", []string{"target"}, "grep/target", false},
		{"ig25", []string{"Cargo.lock"}, "./tabwriter-bin/Cargo.lock", false},
		{"ig27", []string{"foo/"}, "xyz/foo", true},
		{"ig29", []string{"node_modules/ "}, "node_modules", true},
		{"ig30", []string{"**/"}, "foo/bar", true},
		{"ig31", []string{"path1/*"}, "path1/foo", false},
		{"ig32", []string{".a/b"}, ".a/b", false},
		{"ig38", []string{`\[`}, "[", false},
		{"ig39", []string{`\?`}, "?", false},
		{"ig40", []string{`\*`}, "*", false},
		{"ig41", []string{`\a`}, "a", false},
		{"ig42", []string{"s*.rs"}, "sfoo.rs", false},
		{"ig43", []string{"**"}, "foo.rs", false},
		{"ig44", []string{"**/**/*"}, "a/foo.rs", false},
		{"cs1", []string{"*.html"}, "foo.html", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertIgnored(t, c.patterns, c.path, c.isDir)
		})
	}
}

func TestGitignoreSemanticsNotIgnored(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		path     string
		isDir    bool
	}{
		{"ignot1", []string{"amonths"}, "months", false},
		{"ignot2", []string{"monthsa"}, "months", false},
		{"ignot3", []string{"/src/*.rs"}, "src/grep/src/main.rs", false},
		{"ignot4", []string{"/*.c"}, "mozilla-sha1/sha1.c", false},
		{"ignot6", []string{"*.rs", "!src/main.rs"}, "src/main.rs", false},
		{"ignot7", []string{"foo/"}, "foo", false},
		{"ignot8", []string{"**/foo/**"}, "wat/src/afoo/bar/baz", false},
		{"ignot9", []string{"**/foo/**"}, "wat/src/fooa/bar/baz", false},
		{"ignot10", []string{"**/foo/bar"}, "foo/src/bar", false},
		{"ignot11", []string{"#foo"}, "#foo", false},
		{"ignot12", []string{"", "", ""}, "foo", false},
		{"ignot13", []string{"foo/**"}, "foo", true},
		{"ignot15", []string{"!/bar"}, "foo/bar", false},
		{"ignot16", []string{"*", "!**/"}, "foo", true},
		{"ignot17", []string{"src/*.rs"}, "src/grep/src/main.rs", false},
		{"ignot18", []string{"path1/*"}, "path2/path1/foo", false},
		{"ignot19", []string{"s*.rs"}, "src/foo.rs", false},
		{"cs2", []string{"*.html"}, "foo.HTML", false},
		{"cs3", []string{"*.html"}, "foo.htm", false},
		{"cs4", []string{"*.html"}, "foo.HTM", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertNotIgnored(t, c.patterns, c.path, c.isDir)
		})
	}
}

// TestLastMatchWins directly exercises the precedence rule Match relies
// on: the highest Builder.Add-order index that matches wins, regardless
// of which fast class (literal/basename/ext/regex) it or the patterns
// before/after it fall into.
func TestLastMatchWins(t *testing.T) {
	s := buildSet(t, "*.rs", "!foo.rs", "foo.*")
	// index0 *.rs (ignore, ext) < index1 !foo.rs (whitelist, literal) <
	// index2 foo.* (ignore, regex fallback: no recursive-prefix ext
	// shape) — index2 should win.
	if got := s.Match([]byte("foo.rs"), false); got != Ignored {
		t.Errorf("Match(foo.rs) = %v, want Ignored (foo.* is the last, highest-index match)", got)
	}
	if got := s.Match([]byte("bar.rs"), false); got != Ignored {
		t.Errorf("Match(bar.rs) = %v, want Ignored (via *.rs)", got)
	}
}

func TestWhitelistOverridesIgnore(t *testing.T) {
	s := buildSet(t, "*.rs", "!keep.rs")
	if got := s.Match([]byte("keep.rs"), false); got != Whitelisted {
		t.Errorf("Match(keep.rs) = %v, want Whitelisted", got)
	}
	if got := s.Match([]byte("drop.rs"), false); got != Ignored {
		t.Errorf("Match(drop.rs) = %v, want Ignored", got)
	}
}

func TestEmptySet(t *testing.T) {
	s := buildSet(t)
	if got := s.Match([]byte("anything"), false); got != NoMatch {
		t.Errorf("Match on empty Set = %v, want NoMatch", got)
	}
}

func TestDirOnlyRequiresIsDir(t *testing.T) {
	s := buildSet(t, "build/")
	if got := s.Match([]byte("build"), false); got != NoMatch {
		t.Errorf("Match(build, isDir=false) = %v, want NoMatch (dir-only pattern)", got)
	}
	if got := s.Match([]byte("build"), true); got != Ignored {
		t.Errorf("Match(build, isDir=true) = %v, want Ignored", got)
	}
}

// TestDirOnlyAndFileBothLiteral covers the case the advisor flagged: a
// basename key ("build") holding both a dir-only entry and a plain-file
// entry must not have one silently shadow the other in the map bucket.
func TestDirOnlyAndFileBothLiteral(t *testing.T) {
	s := buildSet(t, "build/", "!build")
	// "build/" (index0, ignore, dir-only) and "!build" (index1,
	// whitelist, matches files and dirs) share the basename key "build".
	if got := s.Match([]byte("build"), false); got != Whitelisted {
		t.Errorf("Match(build, isDir=false) = %v, want Whitelisted (dir-only entry shouldn't apply)", got)
	}
	if got := s.Match([]byte("build"), true); got != Whitelisted {
		t.Errorf("Match(build, isDir=true) = %v, want Whitelisted (index1 beats index0)", got)
	}
}

func TestBuilderChaining(t *testing.T) {
	var b Builder
	b.Add("*.log").Add("!keep.log")
	s, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Match([]byte("keep.log"), false); got != Whitelisted {
		t.Errorf("Match(keep.log) = %v, want Whitelisted", got)
	}
}

func TestInvalidPatternError(t *testing.T) {
	var b Builder
	b.Add("[z-a]") // invalid range
	if _, err := b.Build(); err == nil {
		t.Error("Build with an invalid character range should return an error")
	}
}

// TestBasenameAndExtension exercises the two map-dispatched fast
// classes together against a path that must fall through both without
// a false positive.
func TestBasenameAndExtension(t *testing.T) {
	s := buildSet(t, "*.o", "node_modules")
	if got := s.Match([]byte("main.o"), false); got != Ignored {
		t.Errorf("Match(main.o) = %v, want Ignored", got)
	}
	if got := s.Match([]byte("src/deep/nested/main.o"), false); got != Ignored {
		t.Errorf("Match(src/deep/nested/main.o) = %v, want Ignored (ext matches at any depth)", got)
	}
	if got := s.Match([]byte("a/b/node_modules"), true); got != Ignored {
		t.Errorf("Match(a/b/node_modules) = %v, want Ignored (basename matches at any depth)", got)
	}
	if got := s.Match([]byte("main.oo"), false); got != NoMatch {
		t.Errorf("Match(main.oo) = %v, want NoMatch", got)
	}
}

// --- Benchmarks -------------------------------------------------------
//
// realisticGitignore mimics a non-trivial, real-world root .gitignore:
// mostly basename/extension literals with a handful of anchored and
// wildcard regex-fallback entries, in roughly the proportions a real
// project has (see the advisor note: the no-match path, dominated by
// map lookups plus a short regex-fallback scan, is the one that matters
// at walk scale).
var realisticGitignore = []string{
	"*.o", "*.a", "*.so", "*.dll", "*.exe", "*.class", "*.pyc", "*.log",
	"*.tmp", "*.bak", "*.swp",
	"target", "node_modules", "dist", "build", ".DS_Store", "__pycache__",
	".idea", ".vscode", "vendor",
	"/config/local.yml",
	"src/**/*.generated.go",
	"/build/**",
	"!important.log",
	"docs/*.pdf",
}

func benchSet(b *testing.B) *Set {
	var bld Builder
	for _, p := range realisticGitignore {
		bld.Add(p)
	}
	s, err := bld.Build()
	if err != nil {
		b.Fatal(err)
	}
	return s
}

func BenchmarkMatchLiteralHit(b *testing.B) {
	s := benchSet(b)
	path := []byte("node_modules")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Match(path, true)
	}
}

func BenchmarkMatchExtensionHit(b *testing.B) {
	s := benchSet(b)
	path := []byte("src/main.pyc")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Match(path, false)
	}
}

func BenchmarkMatchNoMatchShallow(b *testing.B) {
	s := benchSet(b)
	path := []byte("main.go")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Match(path, false)
	}
}

// BenchmarkMatchNoMatchDeep is the case the advisor called out as the
// one that dominates real walks: a path several directories deep that
// matches nothing, forced through every fast-class map lookup plus the
// full regex-fallback scan on every call.
func BenchmarkMatchNoMatchDeep(b *testing.B) {
	s := benchSet(b)
	path := []byte("internal/pkg/service/handlers/http/middleware/logging.go")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Match(path, false)
	}
}

func BenchmarkMatchRegexFallbackHit(b *testing.B) {
	s := benchSet(b)
	path := []byte("src/api/types.generated.go")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Match(path, false)
	}
}

func BenchmarkMatchWhitelistOverride(b *testing.B) {
	s := benchSet(b)
	path := []byte("important.log")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Match(path, false)
	}
}

func BenchmarkBuild(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var bld Builder
		for _, p := range realisticGitignore {
			bld.Add(p)
		}
		if _, err := bld.Build(); err != nil {
			b.Fatal(err)
		}
	}
}

func ExampleBuilder() {
	var b Builder
	b.Add("*.log")
	b.Add("!important.log")
	set, err := b.Build()
	if err != nil {
		panic(err)
	}
	fmt.Println(set.Match([]byte("debug.log"), false))
	fmt.Println(set.Match([]byte("important.log"), false))
	fmt.Println(set.Match([]byte("main.go"), false))
	// Output:
	// 1
	// 2
	// 0
}
