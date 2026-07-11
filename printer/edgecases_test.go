package printer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yackey-labs/gripgrep/search"
)

// These edge cases are lead-mandated (PLAN.md's "Test coverage
// requirements": "Printer: paths containing ':', non-UTF-8 path bytes,
// multi-MB single output line"). Each was verified empirically against
// real rg first (see the comments below): rg writes path bytes raw and
// unescaped in every case, including a literal ':' that makes the
// "path:line:text" format ambiguous to parse back — that's rg's actual
// behavior, and Standard must match it exactly rather than "fixing" it.

// TestStandard_PathContainingColon mirrors `rg -n -H hello
// "weird:name/f.txt"`, verified to produce
// "weird:name/f.txt:1:hello" with the path's own ':' left completely
// unescaped, ambiguous parse or not.
func TestStandard_PathContainingColon(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)

	path := "weird:name/f.txt"
	p.Begin(path)
	p.Matched(&search.Match{Line: []byte("hello"), LineNumber: 1, HasLineNumber: true})
	p.Finish(path, &search.Stats{Matched: true})

	want := "weird:name/f.txt:1:hello\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStandard_NonUTF8PathBytes mirrors `rg -n -H hello` on a path
// containing raw invalid-UTF-8 bytes (0xFF 0xFE), verified to come out
// byte-for-byte unchanged (rg is byte-oriented, not UTF-8-validating,
// for paths just as for content). Go strings can hold arbitrary bytes,
// so this only requires that Standard never validates or mangles path.
func TestStandard_NonUTF8PathBytes(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)

	path := "nonutf8-\xff\xfe-dir/f.txt"
	p.Begin(path)
	p.Matched(&search.Match{Line: []byte("hello"), LineNumber: 1, HasLineNumber: true})
	p.Finish(path, &search.Stats{Matched: true})

	want := "nonutf8-\xff\xfe-dir/f.txt:1:hello\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStandard_NonUTF8PathBytes_Count and _FilesWithMatches extend the
// same non-UTF-8-path guarantee to the summary sinks, since they format
// the path independently of Standard.
func TestStandard_NonUTF8PathBytes_Count(t *testing.T) {
	dest, out := newTestDest()
	p := NewCount(dest)

	path := "nonutf8-\xff\xfe-dir/f.txt"
	p.Begin(path)
	p.Matched(&search.Match{Line: []byte("hello")})
	p.Finish(path, &search.Stats{Matched: true})

	want := "nonutf8-\xff\xfe-dir/f.txt:1\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStandard_NonUTF8PathBytes_FilesWithMatches(t *testing.T) {
	dest, out := newTestDest()
	p := NewFilesWithMatches(dest)

	path := "nonutf8-\xff\xfe-dir/f.txt"
	p.Begin(path)
	p.Matched(&search.Match{Line: []byte("hello")})
	p.Finish(path, &search.Stats{Matched: true})

	want := "nonutf8-\xff\xfe-dir/f.txt\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestStandard_MultiMBSingleLine verifies a single output line many
// megabytes long (e.g. a minified JS/JSON blob rg users actually hit)
// round-trips through the buffer, its pool-growth path, and Finish's
// single Write byte-for-byte, with no truncation or corruption. This
// also exercises resetBuf's "release oversized buffer back through the
// bounded pool policy" path on the *next* Begin.
func TestStandard_MultiMBSingleLine(t *testing.T) {
	dest, out := newTestDest()
	p := NewStandard(dest)

	const size = 5 * 1024 * 1024 // 5MB
	line := bytes.Repeat([]byte("a"), size)
	// Make it findable/checkable without comparing 5MB of 'a's blindly:
	copy(line[size/2:], []byte("NEEDLE"))

	p.Begin("huge.txt")
	if _, err := p.Matched(&search.Match{Line: line, LineNumber: 1, HasLineNumber: true}); err != nil {
		t.Fatal(err)
	}
	if err := p.Finish("huge.txt", &search.Stats{Matched: true}); err != nil {
		t.Fatal(err)
	}

	want := "huge.txt:1:" + string(line) + "\n"
	got := out.String()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
	if got != want {
		t.Error("multi-MB line output corrupted (byte mismatch)")
	}

	// The buffer that just handled a 5MB line is far above
	// maxPooledCap; per resetBuf's design the shrink happens on the
	// *next* Begin (Finish only writes, it never resizes), so it's
	// still oversized here...
	if cap(p.buf) <= maxPooledCap {
		t.Fatalf("test invariant broken: expected buf still oversized (cap=%d) right after Finish", cap(p.buf))
	}
	p.Begin("small.txt")
	// ...and must be released by the time Begin returns.
	if cap(p.buf) > maxPooledCap {
		t.Errorf("Begin did not release the oversized buffer: cap=%d, want <= %d", cap(p.buf), maxPooledCap)
	}

	// Sanity: the huge buffer still round-trips through a fresh small
	// file correctly afterward (no leftover state).
	dest2, out2 := newTestDest()
	p.dest = dest2
	p.Matched(&search.Match{Line: []byte("tiny"), LineNumber: 1, HasLineNumber: true})
	p.Finish("small.txt", &search.Stats{Matched: true})
	if got := out2.String(); got != "small.txt:1:tiny\n" {
		t.Errorf("post-huge-line state corrupted: got %q", got)
	}
}

// TestCount_MultiMBSingleLine mirrors the same guarantee for -c: a
// single huge matched line must not blow up the (tiny, count-only)
// buffer or corrupt the eventual "path:count" write.
func TestCount_MultiMBSingleLine(t *testing.T) {
	dest, out := newTestDest()
	p := NewCount(dest)

	const size = 3 * 1024 * 1024
	line := bytes.Repeat([]byte("b"), size)

	p.Begin("huge.txt")
	for i := 0; i < 3; i++ {
		if _, err := p.Matched(&search.Match{Line: line}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Finish("huge.txt", &search.Stats{Matched: true}); err != nil {
		t.Fatal(err)
	}

	want := "huge.txt:3\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q (huge lines must never be formatted by Count)", got, want)
	}
}

// TestPathPrinter_NonUTF8AndColonPaths mirrors --files over paths with
// both a ':' and invalid UTF-8 bytes.
func TestPathPrinter_NonUTF8AndColonPaths(t *testing.T) {
	dest, out := newTestDest()
	pp := NewPathPrinter(dest, false, false)

	paths := []string{"weird:name/f.txt", "nonutf8-\xff\xfe-dir/f.txt"}
	for _, p := range paths {
		pp.Paths() <- p
	}
	close(pp.Paths())
	pp.Wait()

	got := out.String()
	for _, p := range paths {
		if !strings.Contains(got, p+"\n") {
			t.Errorf("expected output to contain %q, got %q", p+"\n", got)
		}
	}
}
