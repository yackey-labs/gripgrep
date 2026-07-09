package glob

import "errors"

// tokenKind identifies the kind of a parsed glob token. The token set and
// its semantics mirror ripgrep's globset::glob::Token exactly (see
// ../ripgrep/crates/globset/src/glob.rs), with one deliberate narrowing:
// this parser always operates as if literal_separator=true (a bare `*` or
// `?` never crosses `/`), since that is the only mode gitignore-style
// matching uses. allow_unclosed_class is always true, matching
// GitignoreBuilder's default (an unclosed `[` is a literal `[`, not a
// parse error).
type tokenKind uint8

const (
	tLiteral             tokenKind = iota
	tAny                           // ?
	tZeroOrMore                    // *
	tRecursivePrefix               // **/ at the start of a pattern
	tRecursiveSuffix               // /** at the end of a pattern
	tRecursiveZeroOrMore           // /**/ in the middle of a pattern
	tClass                         // [...] or [!...]
	tAlternates                    // {a,b,c}
)

type token struct {
	kind    tokenKind
	lit     rune      // valid when kind == tLiteral
	negated bool      // valid when kind == tClass
	ranges  [][2]rune // valid when kind == tClass
	alts    [][]token // valid when kind == tAlternates
}

var (
	errUnopenedAlternates = errors.New("glob: unopened alternate group ('}' without matching '{')")
	errUnclosedAlternates = errors.New("glob: unclosed alternate group ('{' without matching '}')")
	errDanglingEscape     = errors.New("glob: dangling '\\' at end of pattern")
)

// isSeparator reports whether r is a path separator. gripgrep normalizes
// all paths to '/' internally (see PLAN.md's walk design), so unlike
// std::path::is_separator this is never platform-dependent.
func isSeparator(r rune) bool {
	return r == '/'
}

// parser mirrors ripgrep's globset::glob::Parser. It consumes a glob
// pattern rune-by-rune and produces a flat token sequence (nested only
// inside tAlternates).
type parser struct {
	src []rune
	pos int // index of the next rune to be returned by bump

	prev     rune
	havePrev bool
	cur      rune
	haveCur  bool

	alternatesStack []int
	branches        [][]token

	foundUnclosedClass bool
}

func newParser(pattern string) *parser {
	return &parser{src: []rune(pattern), branches: [][]token{{}}}
}

// parseGlob parses a single glob pattern (already stripped of any
// gitignore-specific `!`/`/`-anchoring decoration) into its token
// sequence.
func parseGlob(pattern string) ([]token, error) {
	p := newParser(pattern)
	if err := p.parse(); err != nil {
		return nil, err
	}
	if len(p.branches) > 1 {
		return nil, errUnclosedAlternates
	}
	return p.branches[0], nil
}

func (p *parser) bump() (rune, bool) {
	p.prev, p.havePrev = p.cur, p.haveCur
	if p.pos < len(p.src) {
		p.cur, p.haveCur = p.src[p.pos], true
		p.pos++
	} else {
		p.cur, p.haveCur = 0, false
	}
	return p.cur, p.haveCur
}

func (p *parser) peek() (rune, bool) {
	if p.pos < len(p.src) {
		return p.src[p.pos], true
	}
	return 0, false
}

func (p *parser) parse() error {
	for {
		c, ok := p.bump()
		if !ok {
			return nil
		}
		var err error
		switch {
		case c == '?':
			err = p.pushToken(token{kind: tAny})
		case c == '*':
			err = p.parseStar()
		case c == '[' && !p.foundUnclosedClass:
			err = p.parseClass()
		case c == '{':
			err = p.pushAlternate()
		case c == '}':
			err = p.popAlternate()
		case c == ',':
			err = p.parseComma()
		case c == '\\':
			err = p.parseBackslash()
		default:
			err = p.pushToken(token{kind: tLiteral, lit: c})
		}
		if err != nil {
			return err
		}
	}
}

func (p *parser) pushAlternate() error {
	p.alternatesStack = append(p.alternatesStack, len(p.branches))
	p.branches = append(p.branches, []token{})
	return nil
}

func (p *parser) popAlternate() error {
	if len(p.alternatesStack) == 0 {
		return errUnopenedAlternates
	}
	start := p.alternatesStack[len(p.alternatesStack)-1]
	p.alternatesStack = p.alternatesStack[:len(p.alternatesStack)-1]

	alts := make([][]token, len(p.branches)-start)
	copy(alts, p.branches[start:])
	p.branches = p.branches[:start]
	return p.pushToken(token{kind: tAlternates, alts: alts})
}

func (p *parser) pushToken(t token) error {
	if len(p.branches) == 0 {
		return errUnopenedAlternates
	}
	last := len(p.branches) - 1
	p.branches[last] = append(p.branches[last], t)
	return nil
}

func (p *parser) popToken() (token, error) {
	if len(p.branches) == 0 {
		return token{}, errUnopenedAlternates
	}
	last := len(p.branches) - 1
	n := len(p.branches[last])
	t := p.branches[last][n-1]
	p.branches[last] = p.branches[last][:n-1]
	return t, nil
}

func (p *parser) haveTokens() (bool, error) {
	if len(p.branches) == 0 {
		return false, errUnopenedAlternates
	}
	return len(p.branches[len(p.branches)-1]) > 0, nil
}

func (p *parser) parseComma() error {
	if len(p.alternatesStack) == 0 {
		return p.pushToken(token{kind: tLiteral, lit: ','})
	}
	p.branches = append(p.branches, []token{})
	return nil
}

func (p *parser) parseBackslash() error {
	// backslash_escape is always true for gitignore-style patterns.
	c, ok := p.bump()
	if !ok {
		return errDanglingEscape
	}
	return p.pushToken(token{kind: tLiteral, lit: c})
}

func (p *parser) parseStar() error {
	prevHad, prev := p.havePrev, p.prev
	if nc, ok := p.peek(); !(ok && nc == '*') {
		return p.pushToken(token{kind: tZeroOrMore})
	}
	if c2, ok2 := p.bump(); !ok2 || c2 != '*' {
		panic("glob: parser invariant violated: expected second '*'")
	}

	have, err := p.haveTokens()
	if err != nil {
		return err
	}
	if !have {
		// Rust: `!self.peek().map_or(true, is_separator)` — a missing
		// next char counts as a boundary (map_or's default is true),
		// so only a present, non-separator char takes the "not a
		// recursive prefix" branch below.
		pc, pok := p.peek()
		if pok && !isSeparator(pc) {
			if err := p.pushToken(token{kind: tZeroOrMore}); err != nil {
				return err
			}
			return p.pushToken(token{kind: tZeroOrMore})
		}
		if err := p.pushToken(token{kind: tRecursivePrefix}); err != nil {
			return err
		}
		if bc, bok := p.bump(); bok && !isSeparator(bc) {
			panic("glob: parser invariant violated: expected separator or EOF")
		}
		return nil
	}

	prevIsSep := prevHad && isSeparator(prev)
	if !prevIsSep {
		prevIsCommaOrBrace := prevHad && (prev == ',' || prev == '{')
		if len(p.branches) <= 1 || !prevIsCommaOrBrace {
			if err := p.pushToken(token{kind: tZeroOrMore}); err != nil {
				return err
			}
			return p.pushToken(token{kind: tZeroOrMore})
		}
	}

	pc, pok := p.peek()
	var isSuffix bool
	switch {
	case !pok:
		if _, bok := p.bump(); bok {
			panic("glob: parser invariant violated: expected EOF")
		}
		isSuffix = true
	case (pc == ',' || pc == '}') && len(p.branches) >= 2:
		isSuffix = true
	case pok && isSeparator(pc):
		if bc, bok := p.bump(); !bok || !isSeparator(bc) {
			panic("glob: parser invariant violated: expected separator")
		}
		isSuffix = false
	default:
		if err := p.pushToken(token{kind: tZeroOrMore}); err != nil {
			return err
		}
		return p.pushToken(token{kind: tZeroOrMore})
	}

	last, err := p.popToken()
	if err != nil {
		return err
	}
	switch last.kind {
	case tRecursivePrefix:
		return p.pushToken(token{kind: tRecursivePrefix})
	case tRecursiveSuffix:
		return p.pushToken(token{kind: tRecursiveSuffix})
	default:
		if isSuffix {
			return p.pushToken(token{kind: tRecursiveSuffix})
		}
		return p.pushToken(token{kind: tRecursiveZeroOrMore})
	}
}

func (p *parser) parseClass() error {
	savedPos := p.pos
	savedPrev, savedHavePrev := p.prev, p.havePrev
	savedCur, savedHaveCur := p.cur, p.haveCur

	var ranges [][2]rune
	negated := false
	if nc, ok := p.peek(); ok && (nc == '!' || nc == '^') {
		p.bump()
		negated = true
	}

	first := true
	inRange := false
loop:
	for {
		c, ok := p.bump()
		if !ok {
			// allow_unclosed_class is always true: roll back and treat
			// the opening '[' as a literal.
			p.pos = savedPos
			p.prev, p.havePrev = savedPrev, savedHavePrev
			p.cur, p.haveCur = savedCur, savedHaveCur
			p.foundUnclosedClass = true
			return p.pushToken(token{kind: tLiteral, lit: '['})
		}
		switch c {
		case ']':
			if first {
				ranges = append(ranges, [2]rune{']', ']'})
			} else {
				break loop
			}
		case '-':
			switch {
			case first:
				ranges = append(ranges, [2]rune{'-', '-'})
			case inRange:
				if err := addToLastRange(&ranges[len(ranges)-1], '-'); err != nil {
					return err
				}
				inRange = false
			default:
				inRange = true
			}
		default:
			if inRange {
				if err := addToLastRange(&ranges[len(ranges)-1], c); err != nil {
					return err
				}
			} else {
				ranges = append(ranges, [2]rune{c, c})
			}
			inRange = false
		}
		first = false
	}
	if inRange {
		// The class ended with a trailing '-'; treat it as a literal.
		ranges = append(ranges, [2]rune{'-', '-'})
	}
	return p.pushToken(token{kind: tClass, negated: negated, ranges: ranges})
}

func addToLastRange(r *[2]rune, add rune) error {
	r[1] = add
	if r[1] < r[0] {
		return &rangeError{lo: r[0], hi: r[1]}
	}
	return nil
}

type rangeError struct {
	lo, hi rune
}

func (e *rangeError) Error() string {
	return "glob: invalid character range " + string(e.lo) + "-" + string(e.hi)
}
