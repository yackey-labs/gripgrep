package glob

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// patternKind identifies which fast class (or the regex fallback) a
// compiled pattern was classified into.
type patternKind uint8

const (
	kindLiteral patternKind = iota
	kindBasename
	kindExt
	kindRegex
)

// compiledPattern is the result of compiling one gitignore-style pattern
// line: its classification plus everything Match needs to test it and to
// resolve last-match-wins / directory-only precedence.
type compiledPattern struct {
	index       int
	isWhitelist bool
	isOnlyDir   bool
	kind        patternKind
	literal     string         // valid for kindLiteral / kindBasename / kindExt
	re          *regexp.Regexp // valid for kindRegex
}

// compileLine applies gitignore's line-level rewriting rules (comment and
// blank-line skipping, `!`/`\!`/`\#` handling, leading/interior `/`
// anchoring, trailing `/` directory-only marking, the `/**` -> `/**/*`
// fixup) and then parses and classifies the resulting glob, mirroring
// GitignoreBuilder::add_line in
// ../ripgrep/crates/ignore/src/gitignore.rs. index is this pattern's
// position in Builder-Add order, which is exactly the precedence gitignore's
// last-match-wins rule needs.
//
// ok is false (with a nil error) for a line that gitignore syntax defines
// as producing no pattern at all: a comment (`#...`) or a blank line.
func compileLine(index int, raw string) (cp compiledPattern, ok bool, err error) {
	line := raw
	if strings.HasPrefix(line, "#") {
		return compiledPattern{}, false, nil
	}
	if !strings.HasSuffix(line, `\ `) {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	if line == "" {
		return compiledPattern{}, false, nil
	}

	isWhitelist := false
	isOnlyDir := false
	isAbsolute := false

	if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
		line = line[1:]
		isAbsolute = strings.HasPrefix(line, "/")
	} else {
		if strings.HasPrefix(line, "!") {
			isWhitelist = true
			line = line[1:]
		}
		if strings.HasPrefix(line, "/") {
			// A leading '/' anchors the pattern to the ignore file's
			// directory: strip it and ban `**/`-prefixing below, which
			// (since literal_separator is always on) is what makes `/`
			// interior to the pattern rather than a free-floating
			// wildcard boundary.
			line = line[1:]
			isAbsolute = true
		}
	}
	if strings.HasSuffix(line, "/") {
		isOnlyDir = true
		line = line[:len(line)-1]
		// If the trailing slash was itself escaped, drop the escape too.
		// See https://github.com/BurntSushi/ripgrep/issues/2236.
		if strings.HasSuffix(line, `\`) {
			line = line[:len(line)-1]
		}
	}

	actual := line
	hasDoubleStarPrefix := strings.HasPrefix(actual, "**/") || actual == "**"
	if !isAbsolute && !strings.ContainsRune(line, '/') {
		// No anchoring and no interior slash: this pattern is meant to
		// match a basename at any depth, so give it a recursive prefix
		// (unless it already has an equivalent one).
		if !hasDoubleStarPrefix {
			actual = "**/" + actual
		}
	}
	if strings.HasSuffix(actual, "/**") {
		// `dir/**` should ignore dir's contents but not dir itself;
		// forcing at least one further path segment achieves that.
		actual += "/*"
	}

	toks, perr := parseGlob(actual)
	if perr != nil {
		return compiledPattern{}, false, fmt.Errorf("invalid glob %q (from %q): %w", actual, raw, perr)
	}

	cp = compiledPattern{index: index, isWhitelist: isWhitelist, isOnlyDir: isOnlyDir}
	if lit, ok := basenameLiteralOf(toks); ok {
		cp.kind, cp.literal = kindBasename, lit
		return cp, true, nil
	}
	if lit, ok := literalOf(toks); ok {
		cp.kind, cp.literal = kindLiteral, lit
		return cp, true, nil
	}
	if ext, ok := extOfTokens(toks); ok {
		cp.kind, cp.literal = kindExt, ext
		return cp, true, nil
	}

	reSrc := tokensToRegex(toks)
	re, rerr := regexp.Compile(reSrc)
	if rerr != nil {
		return compiledPattern{}, false, fmt.Errorf("compile regex %q (from glob %q): %w", reSrc, raw, rerr)
	}
	cp.kind, cp.re = kindRegex, re
	return cp, true, nil
}
