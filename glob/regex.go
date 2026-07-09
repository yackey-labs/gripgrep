package glob

import (
	"github.com/grafana/regexp"
	"strings"
)

// tokensToRegex translates a token sequence into a Go regexp pattern with
// equivalent matching semantics, mirroring Tokens::to_regex_with in
// ../ripgrep/crates/globset/src/glob.rs under literal_separator=true,
// case_insensitive=false.
//
// Two deliberate deviations from upstream, both a consequence of Go's
// regexp (RE2) being rune-oriented rather than byte-oriented:
//   - Upstream escapes non-ASCII literals as raw `\xHH` bytes under a
//     `(?-u)` byte-mode regex. We instead emit the rune itself (quoted),
//     which RE2 matches as UTF-8. This is equivalent for valid UTF-8
//     paths but means a path containing invalid UTF-8 won't necessarily
//     behave like globset's byte-level matching would. Given gripgrep
//     targets UTF-8/ASCII source trees (PLAN.md v1 scope), this is an
//     accepted gap, not a bug.
//   - No `(?-u)`/`(?i)` prefix is needed or emitted since Go's regexp has
//     no separate unicode-mode toggle and this package never exposes a
//     case-insensitive builder option (case_insensitive is unused by
//     gitignore parsing).
func tokensToRegex(tokens []token) string {
	var sb strings.Builder
	sb.WriteByte('^')
	if len(tokens) == 1 && tokens[0].kind == tRecursivePrefix {
		sb.WriteString(".*$")
		return sb.String()
	}
	writeTokens(&sb, tokens)
	sb.WriteByte('$')
	return sb.String()
}

func writeTokens(sb *strings.Builder, tokens []token) {
	for _, t := range tokens {
		switch t.kind {
		case tLiteral:
			sb.WriteString(escapeRune(t.lit))
		case tAny:
			sb.WriteString("[^/]")
		case tZeroOrMore:
			sb.WriteString("[^/]*")
		case tRecursivePrefix:
			sb.WriteString("(?:/?|.*/)")
		case tRecursiveSuffix:
			sb.WriteString("/.*")
		case tRecursiveZeroOrMore:
			sb.WriteString("(?:/|/.*/)")
		case tClass:
			sb.WriteByte('[')
			if t.negated {
				sb.WriteByte('^')
			}
			for _, r := range t.ranges {
				if r[0] == r[1] {
					sb.WriteString(escapeRune(r[0]))
				} else {
					sb.WriteString(escapeRune(r[0]))
					sb.WriteByte('-')
					sb.WriteString(escapeRune(r[1]))
				}
			}
			sb.WriteByte(']')
		case tAlternates:
			var parts []string
			for _, alt := range t.alts {
				var asb strings.Builder
				writeTokens(&asb, alt)
				if asb.Len() > 0 {
					parts = append(parts, asb.String())
				}
			}
			if len(parts) > 0 {
				sb.WriteString("(?:")
				sb.WriteString(strings.Join(parts, "|"))
				sb.WriteByte(')')
			}
		}
	}
}

func escapeRune(r rune) string {
	return regexp.QuoteMeta(string(r))
}
