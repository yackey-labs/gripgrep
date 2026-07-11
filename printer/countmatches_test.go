package printer

import (
	"testing"

	"github.com/yackey-labs/gripgrep/match"
)

// TestCountMatches covers the occurrence counter that feeds --stats' "N
// matches" line: it must count every non-overlapping occurrence on a line
// (so a line agrees with what --count-matches reports), return 0 on a line
// the pattern does not occur on (the -v case), and return 0 for a nil
// matcher.
func TestCountMatches(t *testing.T) {
	m, err := match.New(match.Config{Patterns: []string{"needle"}, Fixed: true})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		line string
		want int
	}{
		{"single", "needle one", 1},
		{"three", "needle needle needle", 3},
		{"none", "no match here", 0},
		{"substring_boundary", "aneedleb needle", 2},
		{"empty_line", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountMatches(m, []byte(tc.line), false, false); got != tc.want {
				t.Errorf("CountMatches(%q) = %d, want %d", tc.line, got, tc.want)
			}
		})
	}

	if got := CountMatches(nil, []byte("needle"), false, false); got != 0 {
		t.Errorf("CountMatches(nil, ...) = %d, want 0", got)
	}
}

// TestCountMatchesEmptyPatternStripsTerminator pins that the trailing '\n'
// is excluded from the empty-pattern occurrence count, so a line of N
// characters yields N+1 positions (one per char plus one before the end),
// not N+2 -- matching rg's --stats/--count-matches count exactly. A CRLF
// '\r' is ordinary content and stays, so "ab\r\n" yields 4 positions.
func TestCountMatchesEmptyPatternStripsTerminator(t *testing.T) {
	m, err := match.New(match.Config{Patterns: []string{""}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		line string
		want int
	}{
		{"needle one\n", 11}, // 10 chars + 1
		{"hay\n", 4},         // 3 chars + 1
		{"", 1},              // empty line: one position
		{"ab", 3},            // no terminator: 2 chars + 1
		{"ab\r\n", 4},        // '\r' stays as content: 3 chars + 1
	}
	for _, tc := range cases {
		if got := CountMatches(m, []byte(tc.line), false, false); got != tc.want {
			t.Errorf("CountMatches(%q) = %d, want %d", tc.line, got, tc.want)
		}
	}
}

// TestCountMatchesTerminatorWindow pins that an anchored pattern's --stats
// occurrence re-scan strips the SAME terminator the searcher's match window
// stripped: under --crlf the '\r\n' (so `foo$` re-matches "foo"), under
// --null-data the '\x00' (so `needle$` re-matches the record content).
// Without it the count undercounts to 0 -- the X5 regression the round-45
// fix closes (verified against the real rg binary: `--crlf --stats -c
// 'foo$'` reports "1 matches").
func TestCountMatchesTerminatorWindow(t *testing.T) {
	crlfM, err := match.New(match.Config{Patterns: []string{"foo$"}})
	if err != nil {
		t.Fatal(err)
	}
	nulM, err := match.New(match.Config{Patterns: []string{"needle$"}, MultiLine: true})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name           string
		matcher        match.Matcher
		line           string
		crlf, nullData bool
		want           int
	}{
		{"crlf_anchored_matches", crlfM, "foo\r\n", true, false, 1},
		{"crlf_off_anchored_misses", crlfM, "foo\r\n", false, false, 0},
		{"crlf_lf_line_matches", crlfM, "foo\n", true, false, 1},
		{"null_anchored_matches", nulM, "rec1 needle\x00", false, true, 1},
		{"null_anchored_interior_newline", nulM, "x\nneedle\x00", false, true, 1},
		{"null_off_anchored_misses", nulM, "rec1 needle\x00", false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountMatches(tc.matcher, []byte(tc.line), tc.crlf, tc.nullData); got != tc.want {
				t.Errorf("CountMatches(%q, crlf=%v, null=%v) = %d, want %d",
					tc.line, tc.crlf, tc.nullData, got, tc.want)
			}
		})
	}
}
