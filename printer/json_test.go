package printer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yackey-labs/gripgrep/search"
)

// TestAppendJSONString_EscapingTable pins gg's string escaping to
// serde_json's exact output: the two structural escapes, the five control
// short-forms, \u00XX (lowercase) for every other control byte, and verbatim
// passthrough of the HTML-significant bytes and multi-byte UTF-8 (which Go's
// own encoding/json would escape or mangle).
func TestAppendJSONString_EscapingTable(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`a"b`, `a\"b`},
		{`a\b`, `a\\b`},
		{"a\bb", `a\bb`},
		{"a\tb", `a\tb`},
		{"a\nb", `a\nb`},
		{"a\fb", `a\fb`},
		{"a\rb", `a\rb`},
		{"a\x00b", `a\u0000b`},
		{"a\x01b", `a\u0001b`},
		{"a\x1fb", `a\u001fb`},
		{"a\x0bb", `a\u000bb`},   // vertical tab has no short form
		{"<>&/", `<>&/`},         // never HTML-escaped
		{"héllo  x", "héllo  x"}, // multi-byte + line-sep verbatim
		{"\x7f", "\x7f"},         // DEL is >= 0x20, verbatim
	}
	for _, tc := range cases {
		got := string(appendJSONString(nil, []byte(tc.in)))
		if got != tc.want {
			t.Errorf("appendJSONString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAppendData_TextVsBytes covers the per-field valid-UTF-8 decision: valid
// bytes stream as {"text":<escaped>}, invalid bytes as {"bytes":<standard
// base64 with padding>}.
func TestAppendData_TextVsBytes(t *testing.T) {
	if got := string(appendData(nil, []byte("needle\n"))); got != `{"text":"needle\n"}` {
		t.Errorf("valid utf8: got %s", got)
	}
	// The J13 fixture's first line bytes ("needle \xff\xfe tail\n") base64.
	if got := string(appendData(nil, []byte("needle \xff\xfe tail\n"))); got != `{"bytes":"bmVlZGxlIP/+IHRhaWwK"}` {
		t.Errorf("invalid utf8: got %s", got)
	}
	// The J14 fixture's path bytes ("bad\xffname.txt") base64.
	if got := string(appendData(nil, []byte("bad\xffname.txt"))); got != `{"bytes":"YmFk/25hbWUudHh0"}` {
		t.Errorf("invalid utf8 path: got %s", got)
	}
}

// newTestJSON builds a JSON sink writing into buf, with lit as the submatch
// matcher, plus its accumulator.
func newTestJSON(buf *bytes.Buffer, lit string) (*JSON, *JSONAccumulator) {
	acc := NewJSONAccumulator()
	j := NewJSON(NewDest(buf), acc)
	j.Matcher = &literalMatcher{lit: []byte(lit)}
	return j, acc
}

func feedOne(t *testing.T, j *JSON, path string, m *search.Match, st *search.Stats) {
	t.Helper()
	if _, err := j.Begin(path); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Matched(m); err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(path, st); err != nil {
		t.Fatal(err)
	}
}

// TestJSON_FieldOrderAndShape drives one match end to end and pins the exact
// message shapes and their contractual (struct-order) field sequence.
func TestJSON_FieldOrderAndShape(t *testing.T) {
	var buf bytes.Buffer
	j, _ := newTestJSON(&buf, "needle")
	feedOne(t, j, "a.txt",
		&search.Match{Line: []byte("alpha needle one\n"), LineNumber: 1, HasLineNumber: true, Offset: 0},
		&search.Stats{Matched: true, BytesSearched: 52})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected begin+match+end, got %d lines:\n%s", len(lines), buf.String())
	}
	if lines[0] != `{"type":"begin","data":{"path":{"text":"a.txt"}}}` {
		t.Errorf("begin: %s", lines[0])
	}
	wantMatch := `{"type":"match","data":{"path":{"text":"a.txt"},"lines":{"text":"alpha needle one\n"},"line_number":1,"absolute_offset":0,"submatches":[{"match":{"text":"needle"},"start":6,"end":12}]}}`
	if lines[1] != wantMatch {
		t.Errorf("match:\n got %s\nwant %s", lines[1], wantMatch)
	}
	// end: path, binary_offset, stats{elapsed,searches,searches_with_match,
	// bytes_searched,bytes_printed,matched_lines,matches} -- elapsed value is
	// nondeterministic, so check the prefix up to it and the tail after it.
	if !strings.HasPrefix(lines[2], `{"type":"end","data":{"path":{"text":"a.txt"},"binary_offset":null,"stats":{"elapsed":{"secs":`) {
		t.Errorf("end prefix/field-order: %s", lines[2])
	}
	if !strings.HasSuffix(lines[2], `,"searches":1,"searches_with_match":1,"bytes_searched":52,"bytes_printed":236,"matched_lines":1,"matches":1}}}`) {
		t.Errorf("end tail/field-order: %s", lines[2])
	}
}

// TestJSON_BytesPrintedSelfAccounting pins the self-referential bytes_printed:
// it is the byte length of begin+match messages only (each with its '\n'), and
// the end message -- appended after the count is taken -- never counts toward
// it. Here begin(49) + match(187) = 236.
func TestJSON_BytesPrintedSelfAccounting(t *testing.T) {
	var buf bytes.Buffer
	j, acc := newTestJSON(&buf, "needle")
	feedOne(t, j, "a.txt",
		&search.Match{Line: []byte("alpha needle one\n"), LineNumber: 1, HasLineNumber: true, Offset: 0},
		&search.Stats{Matched: true, BytesSearched: 52})

	full := buf.String()
	endIdx := strings.Index(full, `{"type":"end"`)
	beforeEnd := int64(endIdx) // bytes of begin+match, incl their newlines
	if got := acc.bytesPrinted.Load(); got != beforeEnd {
		t.Errorf("summary bytes_printed = %d, want %d (begin+match only, end excluded)", got, beforeEnd)
	}
	if beforeEnd != 236 {
		t.Errorf("begin+match byte length = %d, want 236", beforeEnd)
	}
	// The end message itself carries the same figure in its own stats.
	if !strings.Contains(full, `"bytes_printed":236,`) {
		t.Errorf("end stats missing bytes_printed:236:\n%s", full)
	}
}

// TestJSON_SubmatchPopTrailingEmpty covers rg's record_matches rule: an empty
// pattern reports one submatch per position from 0 up to (not including) the
// line length -- the trailing empty match at the very end of the bytes is
// popped. For "alpha needle one\n" (17 bytes) that is 17 submatches (0..16),
// and matches counts 17 too.
func TestJSON_SubmatchPopTrailingEmpty(t *testing.T) {
	var buf bytes.Buffer
	j, acc := newTestJSON(&buf, "") // empty literal matches every position
	feedOne(t, j, "a.txt",
		&search.Match{Line: []byte("alpha needle one\n"), LineNumber: 1, HasLineNumber: true, Offset: 0},
		&search.Stats{Matched: true, BytesSearched: 52})

	match := strings.Split(buf.String(), "\n")[1]
	// Highest submatch start must be 16 (len-1), never 17 (== len).
	if strings.Contains(match, `"start":17,"end":17`) {
		t.Errorf("trailing empty match at len not popped:\n%s", match)
	}
	if !strings.Contains(match, `"start":16,"end":16`) {
		t.Errorf("expected empty submatch at 16:\n%s", match)
	}
	if got := acc.matches.Load(); got != 17 {
		t.Errorf("matches = %d, want 17 (one per position, terminator excluded)", got)
	}
	if got := acc.matchedLines.Load(); got != 1 {
		t.Errorf("matched_lines = %d, want 1", got)
	}
}

// TestJSON_BinaryClampAndOffset pins the explicit-binary end shape: binary_offset
// carries the NUL offset and bytes_searched is clamped to it (J15).
func TestJSON_BinaryClampAndOffset(t *testing.T) {
	var buf bytes.Buffer
	j, acc := newTestJSON(&buf, "needle")
	feedOne(t, j, "nul.dat",
		&search.Match{Line: []byte("needle\x00binary\n"), LineNumber: 1, HasLineNumber: true, Offset: 0},
		&search.Stats{Matched: true, BytesSearched: 14, Binary: true, BinaryOffset: 6})

	end := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")[2]
	if !strings.Contains(end, `"binary_offset":6,`) {
		t.Errorf("expected binary_offset:6:\n%s", end)
	}
	if !strings.Contains(end, `"bytes_searched":6,`) {
		t.Errorf("expected bytes_searched clamped to 6:\n%s", end)
	}
	if got := acc.bytesSearched.Load(); got != 6 {
		t.Errorf("summary bytes_searched = %d, want 6 (clamped)", got)
	}
}

// TestJSON_QuietEmitsNothingButCounts pins -q under --json: no per-file
// messages, bytes_printed stays 0, but stats still accumulate for the summary.
func TestJSON_QuietEmitsNothingButCounts(t *testing.T) {
	var buf bytes.Buffer
	j, acc := newTestJSON(&buf, "needle")
	j.Quiet = true
	feedOne(t, j, "a.txt",
		&search.Match{Line: []byte("alpha needle one\n"), LineNumber: 1, HasLineNumber: true, Offset: 0},
		&search.Stats{Matched: true, BytesSearched: 52})

	if buf.Len() != 0 {
		t.Errorf("quiet emitted output:\n%s", buf.String())
	}
	if got := acc.bytesPrinted.Load(); got != 0 {
		t.Errorf("quiet bytes_printed = %d, want 0", got)
	}
	if acc.matchedLines.Load() != 1 || acc.matches.Load() != 1 || acc.searches.Load() != 1 || acc.searchesWithMatch.Load() != 1 {
		t.Errorf("quiet stats not accumulated: matched_lines=%d matches=%d searches=%d searches_with_match=%d",
			acc.matchedLines.Load(), acc.matches.Load(), acc.searches.Load(), acc.searchesWithMatch.Load())
	}
}
