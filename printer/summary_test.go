package printer

import (
	"testing"

	"github.com/yackey-labs/gripgrep/search"
)

// TestCount_BasicAndOwnTally mirrors `rg -c hello a/b/foo.txt`: "path:N"
// where N is Count's own tally of Matched calls, not stats.MatchCount
// (deliberately passed a different, wrong value to prove Count ignores
// it).
func TestCount_BasicAndOwnTally(t *testing.T) {
	dest, out := newTestDest()
	p := NewCount(dest)

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine3), LineNumber: 3, HasLineNumber: true})
	p.Matched(&search.Match{Line: []byte(fooLine4), LineNumber: 4, HasLineNumber: true})
	if err := p.Finish("a/b/foo.txt", &search.Stats{Matched: true, MatchCount: 999}); err != nil {
		t.Fatal(err)
	}

	want := "a/b/foo.txt:2\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestCount_ZeroMatchesNoOutput verifies a non-matching file is
// silently skipped: no "path:0" line, matching rg's -c behavior.
func TestCount_ZeroMatchesNoOutput(t *testing.T) {
	dest, out := newTestDest()
	p := NewCount(dest)

	p.Begin("a/b/foo.txt")
	p.Finish("a/b/foo.txt", &search.Stats{Matched: false})

	if out.Len() != 0 {
		t.Errorf("expected no output, got %q", out.String())
	}
}

// TestCount_Color mirrors `rg -c --color=always hello a/b/foo.txt`:
// only the path is colored, the count is plain.
func TestCount_Color(t *testing.T) {
	dest, out := newTestDest()
	p := NewCount(dest)
	p.Color = true

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine1)})
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "\x1b[0m\x1b[35ma/b/foo.txt\x1b[0m:1\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestCount_ShowPathFalse mirrors `rg -c hello a/b/foo.txt` when
// a/b/foo.txt is the single explicit file named on the command line: rg
// prints a bare count with no path prefix at all (verified against the
// real rg binary -- unlike Standard, Count has no Heading mode, so
// ShowPath=false must suppress the "path:" prefix entirely, not just
// switch to a heading line).
func TestCount_ShowPathFalse(t *testing.T) {
	dest, out := newTestDest()
	p := NewCount(dest)
	p.ShowPath = false

	p.Begin("a/b/foo.txt")
	p.Matched(&search.Match{Line: []byte(fooLine1)})
	p.Matched(&search.Match{Line: []byte(fooLine3)})
	if err := p.Finish("a/b/foo.txt", &search.Stats{Matched: true}); err != nil {
		t.Fatal(err)
	}

	want := "2\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFilesWithMatches_AbortsEarly mirrors `rg -l hello a/b/foo.txt`:
// just "path", and Matched must return more=false after the first hit.
func TestFilesWithMatches_AbortsEarly(t *testing.T) {
	dest, out := newTestDest()
	p := NewFilesWithMatches(dest)

	p.Begin("a/b/foo.txt")
	more, err := p.Matched(&search.Match{Line: []byte(fooLine1)})
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Error("expected more=false to abort the file after the first match")
	}
	p.Finish("a/b/foo.txt", &search.Stats{Matched: true})

	want := "a/b/foo.txt\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFilesWithMatches_ZeroMatchesNoOutput verifies non-matching files
// produce nothing.
func TestFilesWithMatches_ZeroMatchesNoOutput(t *testing.T) {
	dest, out := newTestDest()
	p := NewFilesWithMatches(dest)

	p.Begin("a/b/foo.txt")
	p.Finish("a/b/foo.txt", &search.Stats{Matched: false})

	if out.Len() != 0 {
		t.Errorf("expected no output, got %q", out.String())
	}
}

// TestQuiet_FoundAndAbortsEarly mirrors -q: no output ever, Matched
// returns more=false immediately, and Found() reflects the match.
func TestQuiet_FoundAndAbortsEarly(t *testing.T) {
	p := NewQuiet()

	if p.Found() {
		t.Fatal("Found should be false before any match")
	}

	canSearch, err := p.Begin("a/b/foo.txt")
	if err != nil || !canSearch {
		t.Fatalf("Begin should allow searching before any match found: search=%v err=%v", canSearch, err)
	}

	more, err := p.Matched(&search.Match{Line: []byte(fooLine1)})
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Error("expected more=false immediately on first match")
	}
	if !p.Found() {
		t.Error("expected Found() true after a match")
	}

	if err := p.Finish("a/b/foo.txt", &search.Stats{Matched: true}); err != nil {
		t.Fatal(err)
	}

	// Begin on a subsequent file should now decline to search at all.
	if canSearch, _ := p.Begin("other.txt"); canSearch {
		t.Error("expected Begin to return search=false once a match has been found")
	}
}
