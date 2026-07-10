package glob

import (
	"fmt"
	"github.com/grafana/regexp"
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
	kindSuffix
	kindPrefix
	kindContains
	kindBetween
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
	literal     string // valid for kindLiteral / kindBasename / kindExt / kindSuffix / kindPrefix / kindContains / kindBetween (prefix half)
	literal2    string // valid for kindBetween only (suffix half)

	re *regexp.Regexp // valid for kindRegex
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
// The returned slice normally holds exactly one compiledPattern; it is
// empty (with a nil error) for a line that gitignore syntax defines as
// producing no pattern at all (a comment or a blank line), and can hold
// more than one when the pattern contains a small character class that
// expandClasses turns into several literal variants, every one of which
// classifies into a fast (non-regex) class -- see expandClasses' doc.
// All returned patterns share this line's index/isWhitelist/isOnlyDir,
// since they came from the same gitignore line and must resolve
// last-match-wins precedence together.
func compileLine(index int, raw string) (cps []compiledPattern, err error) {
	line := raw
	if strings.HasPrefix(line, "#") {
		return nil, nil
	}
	if !strings.HasSuffix(line, `\ `) {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	if line == "" {
		return nil, nil
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
		return nil, fmt.Errorf("invalid glob %q (from %q): %w", actual, raw, perr)
	}

	base := compiledPattern{index: index, isWhitelist: isWhitelist, isOnlyDir: isOnlyDir}

	if variants, ok := expandClasses(toks); ok {
		expanded := make([]compiledPattern, 0, len(variants))
		allFast := true
		for _, vtoks := range variants {
			lit, lit2, kind, kok := classifyFast(vtoks)
			if !kok {
				allFast = false
				break
			}
			cp := base
			cp.kind, cp.literal, cp.literal2 = kind, lit, lit2
			expanded = append(expanded, cp)
		}
		if allFast {
			return expanded, nil
		}
		// At least one expanded variant still needs regex (the class
		// wasn't the pattern's only wildcard) -- expanding would trade
		// one regex for several without removing the regex fallback
		// requirement at all, a pure regression, so fall through and
		// compile the original, unexpanded pattern below instead.
	}

	if lit, lit2, kind, ok := classifyFast(toks); ok {
		cp := base
		cp.kind, cp.literal, cp.literal2 = kind, lit, lit2
		return []compiledPattern{cp}, nil
	}

	reSrc := tokensToRegex(toks)
	re, rerr := regexp.Compile(reSrc)
	if rerr != nil {
		return nil, fmt.Errorf("compile regex %q (from glob %q): %w", reSrc, raw, rerr)
	}
	cp := base
	cp.kind, cp.re = kindRegex, re
	return []compiledPattern{cp}, nil
}
