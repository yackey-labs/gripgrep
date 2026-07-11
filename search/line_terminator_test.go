package search

import (
	"bytes"
	"strings"
	"testing"
)

// TestWithoutTerminator covers the match-window terminator strip for all
// three modes: default '\n', CRLF ('\r\n' and the '\r'-strip boundaries),
// and null-data '\x00'. The stripped window is what the matcher sees, so
// getting these boundaries wrong is what makes `$`/`-x`/`.` diverge from rg.
func TestWithoutTerminator(t *testing.T) {
	cases := []struct {
		name string
		in   string
		term byte
		crlf bool
		want string
	}{
		{"lf_terminated", "foo\n", '\n', false, "foo"},
		{"lf_no_terminator", "foo", '\n', false, "foo"},
		{"lf_empty", "", '\n', false, ""},
		{"lf_bare_terminator", "\n", '\n', false, ""},

		{"crlf_terminated", "foo\r\n", '\n', true, "foo"},
		{"crlf_lone_lf", "foo\n", '\n', true, "foo"},
		{"crlf_no_terminator", "foo", '\n', true, "foo"},
		{"crlf_lone_cr_at_eof_stays", "foo\r", '\n', true, "foo\r"},
		{"crlf_interior_cr_stays", "a\rb\r\n", '\n', true, "a\rb"},
		{"crlf_empty_crlf", "\r\n", '\n', true, ""},
		{"crlf_bare_lf", "\n", '\n', true, ""},

		{"null_terminated", "rec\x00", 0, false, "rec"},
		{"null_no_terminator", "rec", 0, false, "rec"},
		{"null_interior_newline_stays", "a\nb\x00", 0, false, "a\nb"},
		{"null_empty_record", "\x00", 0, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := withoutTerminator([]byte(c.in), c.term, c.crlf)
			if string(got) != c.want {
				t.Errorf("withoutTerminator(%q, %q, crlf=%v) = %q, want %q",
					c.in, c.term, c.crlf, got, c.want)
			}
		})
	}
}

// nullSearcher builds a NullData searcher over a fake literal matcher on
// the slow path (so record windows flow through Verify record by record).
func nullSearcher(lit string) *Searcher {
	return New(Searcher{
		Matcher: &fakeMatcher{literal: []byte(lit)},
		// BinaryNone mirrors what the engine forces under --null-data (a NUL
		// is the terminator, not a binary marker) -- without it SearchBytes'
		// upfront BinaryQuit NUL check would discard the whole slice.
		BinaryMode:  BinaryNone,
		NullData:    true,
		LineNumbers: true,
	})
}

// TestNullRecordSplitting_SearchBytes exercises the '\x00' record splitter
// over the in-memory slice path: empty records still count as records, a
// record with no final terminator is still delivered, and an interior '\n'
// never ends a record.
func TestNullRecordSplitting_SearchBytes(t *testing.T) {
	cases := []struct {
		name       string
		data       string
		lit        string
		wantLines  []string
		wantLineNo []int64
	}{
		{
			name:       "basic_records",
			data:       "rec1 needle\x00rec2\x00needle again\x00",
			lit:        "needle",
			wantLines:  []string{"rec1 needle\x00", "needle again\x00"},
			wantLineNo: []int64{1, 3},
		},
		{
			name:       "empty_records_count",
			data:       "a\x00\x00needle\x00",
			lit:        "needle",
			wantLines:  []string{"needle\x00"},
			wantLineNo: []int64{3},
		},
		{
			name:       "no_final_terminator",
			data:       "no final term needle",
			lit:        "needle",
			wantLines:  []string{"no final term needle"},
			wantLineNo: []int64{1},
		},
		{
			name:       "interior_newline_in_record",
			data:       "text with\nnewline needle\x00second\x00",
			lit:        "newline needle",
			wantLines:  []string{"text with\nnewline needle\x00"},
			wantLineNo: []int64{1},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := newRecordingSink()
			s := nullSearcher(c.lit)
			if err := s.SearchBytes("t", []byte(c.data), sink); err != nil {
				t.Fatal(err)
			}
			assertMatches(t, sink, c.wantLines, c.wantLineNo)
		})
	}
}

// TestNullRecordSplitting_Streaming runs the same splitter through the
// rolling-buffer io.Reader path (Search, not SearchBytes) AND forces a
// single record larger than the rolling buffer's initial capacity, so the
// grow-on-'\x00' behavior (a record that fits no '\x00' in the first read)
// is actually exercised -- a big explicit file may be mmap'd to SearchBytes
// and skip this path entirely.
func TestNullRecordSplitting_Streaming(t *testing.T) {
	big := strings.Repeat("p", DefaultBufferSize*3) + " needle\x00tail rec\x00"
	sink := newRecordingSink()
	s := nullSearcher("needle")
	if err := s.Search("t", bytes.NewReader([]byte(big)), sink); err != nil {
		t.Fatal(err)
	}
	wantLine := strings.Repeat("p", DefaultBufferSize*3) + " needle\x00"
	assertMatches(t, sink, []string{wantLine}, []int64{1})
}

func assertMatches(t *testing.T, sink *recordingSink, wantLines []string, wantLineNo []int64) {
	t.Helper()
	got := sink.matchLines()
	if len(got) != len(wantLines) {
		t.Fatalf("got %d matches %q, want %d %q", len(got), got, len(wantLines), wantLines)
	}
	for i, w := range wantLines {
		if got[i] != w {
			t.Errorf("match %d line = %q, want %q", i, got[i], w)
		}
	}
	var gotNo []int64
	for _, e := range sink.events {
		if e.kind == "match" {
			gotNo = append(gotNo, e.lineNumber)
		}
	}
	for i, w := range wantLineNo {
		if gotNo[i] != w {
			t.Errorf("match %d line number = %d, want %d", i, gotNo[i], w)
		}
	}
}
