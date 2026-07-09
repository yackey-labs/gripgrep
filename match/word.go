package match

import (
	"unicode"
	"unicode/utf8"
)

// Word-boundary handling for -w.
//
// ripgrep implements -w not as `\b...\b` but by wrapping the pattern's
// HIR in a pair of *half* word-boundary looks: WordStartHalf before, and
// WordEndHalf after (crates/regex/src/config.rs, into_word). A half
// boundary only constrains its own side: WordStartHalf requires that the
// byte immediately before the match is either the start of text/line or
// a non-word character (it does not care what the match itself starts
// with); WordEndHalf is the mirror image for the byte immediately after.
//
// This is deliberately *not* the same as Go's (or Rust's) plain `\b`,
// which fires on any word/non-word transition regardless of which side
// the word character is on. The difference matters: for the pattern
// `-foo` against the haystack "x-foo", `\b` succeeds at the position
// between 'x' and '-' (a word/non-word transition exists there), so
// `\b(?:-foo)\b` would incorrectly match. rg's half-boundary check
// instead asks "is the character before the match position a non-word
// character (or the start of text)?" -- 'x' is a word character, so it
// correctly rejects this as an embedded match.
//
// Go's regexp/syntax has no equivalent asymmetric look-around op (its
// only boundary primitives are the symmetric \b/\B), so rather than
// mis-wrap the pattern tree, -w is implemented here as a post-match
// acceptance check: run the underlying matcher unmodified, then confirm
// the half-boundary condition on the surrounding bytes before treating a
// hit as real. This is applied uniformly across all three matching
// strategies (pure literal, prefiltered regex, engine-everywhere) at
// their confirmation points.

// isWordByte reports whether the ASCII byte b is a word character
// (letter, digit, or underscore) per the same definition Go's own \w /
// \b use.
func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}

// isWordRune reports whether r is a word character under rg's
// Unicode-aware word-boundary definition (letter, digit, or
// underscore). Used for the byte immediately outside an ASCII fast
// path, i.e. when decoding a multi-byte UTF-8 rune bordering a match.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// acceptWordBoundary reports whether the match at buf[s:e] satisfies
// rg's half-word-boundary check on both sides: the rune ending at s
// (i.e. immediately before the match) must be absent (s==0, or a line
// start) or non-word, and the rune starting at e (immediately after the
// match) must be absent (e==len(buf)) or non-word.
//
// buf is the full context available to the caller (a whole search
// buffer or a single line) -- '\n' bytes naturally act as boundaries
// too, since they are never word characters, so this is safe to call
// with either.
func acceptWordBoundary(buf []byte, s, e int) bool {
	return acceptWordStart(buf, s) && acceptWordEnd(buf, e)
}

func acceptWordStart(buf []byte, s int) bool {
	if s <= 0 {
		return true
	}
	b := buf[s-1]
	if b < utf8.RuneSelf {
		return !isWordByte(b)
	}
	r, _ := utf8.DecodeLastRune(buf[:s])
	return !isWordRune(r)
}

func acceptWordEnd(buf []byte, e int) bool {
	if e >= len(buf) {
		return true
	}
	b := buf[e]
	if b < utf8.RuneSelf {
		return !isWordByte(b)
	}
	r, _ := utf8.DecodeRune(buf[e:])
	return !isWordRune(r)
}
