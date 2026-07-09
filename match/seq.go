package match

import (
	"regexp/syntax"
	"sort"
	"unicode"
	"unicode/utf8"
)

// This file ports ripgrep's literal-extraction machinery to Go, operating
// on regexp/syntax.Regexp trees instead of Rust's regex-syntax Hir.
//
// Two source files are being ported and merged into one:
//   - regex-syntax's src/hir/literal.rs: the Seq/Literal/PreferenceTrie
//     arithmetic (exact/inexact tracking, cross product, union, dedup,
//     preference-order minimization, the elaborate optimize-by-preference
//     heuristics).
//   - ripgrep's own crates/regex/src/literal.rs: the InnerLiterals
//     extractor, which wraps the above in a "prefix" flag + choose()
//     short-circuit so that literals can be pulled from *anywhere* in a
//     concatenation (not just a true prefix) -- this is the "inner literal"
//     trick.
//
// A Go OpLiteral node bundles a whole run of same-flags characters into
// one node; when FoldCase is set, Go's parser (unlike Rust's translator)
// does NOT pre-expand it into a class-of-alternatives -- it stays a single
// literal with a fold flag. Character classes, by contrast, ARE always
// pre-expanded by Go's parser. So literal extraction here must do its own
// per-rune fold-orbit expansion (via unicode.SimpleFold) when it
// encounters a folded OpLiteral, cross-producting each rune position's
// orbit exactly like the fold-expanded classes Rust's translator would
// have produced.

// literalItem is a single candidate literal: some bytes plus whether it
// is "exact" (reaches a match state, and can still be extended) or
// "inexact" (truncated or otherwise can't be grown further).
type literalItem struct {
	bytes []byte
	exact bool
}

func exactLit(b []byte) literalItem {
	return literalItem{bytes: append([]byte(nil), b...), exact: true}
}
func inexactLit(b []byte) literalItem {
	return literalItem{bytes: append([]byte(nil), b...), exact: false}
}

func (l *literalItem) makeInexact() { l.exact = false }

func (l *literalItem) extend(other literalItem) {
	if !l.exact {
		return
	}
	l.bytes = append(l.bytes, other.bytes...)
}

func (l *literalItem) keepFirstBytes(n int) {
	if n >= len(l.bytes) {
		return
	}
	l.makeInexact()
	l.bytes = l.bytes[:n]
}

func (l *literalItem) isPoisonous() bool {
	return len(l.bytes) == 0 || (len(l.bytes) == 1 && isPoisonousByte(l.bytes[0]))
}

func (l literalItem) equalBytes(o literalItem) bool {
	if len(l.bytes) != len(o.bytes) {
		return false
	}
	for i := range l.bytes {
		if l.bytes[i] != o.bytes[i] {
			return false
		}
	}
	return true
}

// seq is a sequence of literals, mirroring regex_syntax::hir::literal::Seq.
// When infinite is true, the sequence represents "any literal" and lits is
// ignored. Otherwise lits (possibly empty) is the finite set of members.
type seq struct {
	lits     []literalItem
	infinite bool
}

func emptySeq() seq                  { return seq{lits: []literalItem{}} }
func infiniteSeq() seq               { return seq{infinite: true} }
func singletonSeq(l literalItem) seq { return seq{lits: []literalItem{l}} }

func (s *seq) isFinite() bool { return !s.infinite }

func (s *seq) length() (int, bool) {
	if s.infinite {
		return 0, false
	}
	return len(s.lits), true
}

func (s *seq) isEmpty() bool {
	n, ok := s.length()
	return ok && n == 0
}

func (s *seq) isExact() bool {
	if s.infinite {
		return false
	}
	for _, l := range s.lits {
		if !l.exact {
			return false
		}
	}
	return true
}

func (s *seq) isInexact() bool {
	if s.infinite {
		return true
	}
	for _, l := range s.lits {
		if l.exact {
			return false
		}
	}
	return true
}

func (s *seq) makeInexact() {
	if s.infinite {
		return
	}
	for i := range s.lits {
		s.lits[i].makeInexact()
	}
}

func (s *seq) makeInfinite() {
	s.infinite = true
	s.lits = nil
}

func (s *seq) push(l literalItem) {
	if s.infinite {
		return
	}
	if n := len(s.lits); n > 0 && s.lits[n-1].equalBytes(l) {
		return
	}
	s.lits = append(s.lits, l)
}

func (s *seq) minLiteralLen() (int, bool) {
	if s.infinite || len(s.lits) == 0 {
		return 0, false
	}
	min := len(s.lits[0].bytes)
	for _, l := range s.lits[1:] {
		if len(l.bytes) < min {
			min = len(l.bytes)
		}
	}
	return min, true
}

func (s *seq) maxLiteralLen() (int, bool) {
	if s.infinite || len(s.lits) == 0 {
		return 0, false
	}
	max := len(s.lits[0].bytes)
	for _, l := range s.lits[1:] {
		if len(l.bytes) > max {
			max = len(l.bytes)
		}
	}
	return max, true
}

func (s *seq) maxUnionLen(o *seq) (int, bool) {
	n1, ok1 := s.length()
	n2, ok2 := o.length()
	if !ok1 || !ok2 {
		return 0, false
	}
	return n1 + n2, true
}

func (s *seq) maxCrossLen(o *seq) (int, bool) {
	n1, ok1 := s.length()
	n2, ok2 := o.length()
	if !ok1 || !ok2 {
		return 0, false
	}
	return n1 * n2, true
}

// crossForward computes the cross product of s (as prefixes) with other
// (appended as suffixes), mirroring Seq::cross_forward. other is drained.
func (s *seq) crossForward(other *seq) {
	if other.infinite {
		if min, ok := s.minLiteralLen(); ok && min == 0 {
			s.makeInfinite()
		} else {
			s.makeInexact()
		}
		return
	}
	if s.infinite {
		other.lits = nil
		return
	}
	lits2 := other.lits
	newLits := make([]literalItem, 0, len(s.lits)*len(lits2))
	for _, selfLit := range s.lits {
		if !selfLit.exact {
			newLits = append(newLits, selfLit)
			continue
		}
		for _, otherLit := range lits2 {
			nl := exactLit(selfLit.bytes)
			nl.extend(otherLit)
			if !otherLit.exact {
				nl.makeInexact()
			}
			newLits = append(newLits, nl)
		}
	}
	s.lits = newLits
	other.lits = nil
	s.dedup()
}

// union unions other into s, mirroring Seq::union. other is drained.
func (s *seq) union(other *seq) {
	if other.infinite {
		s.makeInfinite()
		return
	}
	lits2 := other.lits
	other.lits = nil
	if s.infinite {
		return
	}
	s.lits = append(s.lits, lits2...)
	s.dedup()
}

// dedup removes adjacent equivalent literals, preferring inexact when
// exactness disagrees (mirrors Seq::dedup).
func (s *seq) dedup() {
	if s.infinite || len(s.lits) == 0 {
		return
	}
	out := s.lits[:1]
	for _, cur := range s.lits[1:] {
		last := &out[len(out)-1]
		if last.equalBytes(cur) {
			if last.exact != cur.exact {
				last.makeInexact()
			}
			continue
		}
		out = append(out, cur)
	}
	s.lits = out
}

func (s *seq) keepFirstBytes(n int) {
	if s.infinite {
		return
	}
	for i := range s.lits {
		s.lits[i].keepFirstBytes(n)
	}
}

func (s *seq) longestCommonPrefix() ([]byte, bool) {
	if s.infinite || len(s.lits) == 0 {
		return nil, false
	}
	base := s.lits[0].bytes
	prefixLen := len(base)
	for _, l := range s.lits[1:] {
		n := 0
		for n < prefixLen && n < len(l.bytes) && base[n] == l.bytes[n] {
			n++
		}
		prefixLen = n
	}
	return base[:prefixLen], true
}

// minimizeByPreference runs PreferenceTrie minimization, dropping literals
// that can never match in a leftmost-first search because a shorter
// preference-order literal already covers them.
func (s *seq) minimizeByPreference(keepExact bool) {
	if s.infinite {
		return
	}
	preferenceTrieMinimize(&s.lits, keepExact)
}

// preferenceTrieMinimize ports PreferenceTrie::minimize.
func preferenceTrieMinimize(lits *[]literalItem, keepExact bool) {
	type state struct {
		trans []struct {
			b    byte
			next int
		}
	}
	var states []state
	matches := []int{} // 0 = no match; else literal index + 1
	createState := func() int {
		states = append(states, state{})
		matches = append(matches, 0)
		return len(states) - 1
	}
	root := func() int {
		if len(states) != 0 {
			return 0
		}
		return createState()
	}
	insert := func(bytes []byte) (int, bool) {
		prev := root()
		if matches[prev] != 0 {
			return matches[prev], false
		}
		for _, b := range bytes {
			found := -1
			for i, t := range states[prev].trans {
				if t.b == b {
					found = i
					break
				}
			}
			if found >= 0 {
				prev = states[prev].trans[found].next
				if matches[prev] != 0 {
					return matches[prev], false
				}
				continue
			}
			next := createState()
			states[prev].trans = append(states[prev].trans, struct {
				b    byte
				next int
			}{b, next})
			prev = next
		}
		return prev, true
	}
	nextLiteralIndex := 1
	var kept []literalItem
	var makeInexactIdx []int
	for _, lit := range *lits {
		prevOrIdx, ok := insert(lit.bytes)
		if ok {
			// prevOrIdx is the state id of the match; record its literal index.
			matches[prevOrIdx] = nextLiteralIndex
			nextLiteralIndex++
			kept = append(kept, lit)
		} else {
			if !keepExact {
				makeInexactIdx = append(makeInexactIdx, prevOrIdx-1)
			}
		}
	}
	for _, i := range makeInexactIdx {
		if i >= 0 && i < len(kept) {
			kept[i].makeInexact()
		}
	}
	*lits = kept
}

// optimizeForPrefixByPreference ports Seq::optimize_by_preference(prefix=true).
func (s *seq) optimizeForPrefixByPreference() {
	origLen, ok := s.length()
	if !ok {
		return
	}
	if min, ok := s.minLiteralLen(); ok && min == 0 {
		s.makeInfinite()
		return
	}
	s.minimizeByPreference(true)

	fix, hasFix := s.longestCommonPrefix()
	if hasFix {
		if origLen > 1 && len(fix) >= 1 && len(fix) <= 3 && rank(fix[0]) < 200 {
			s.keepFirstBytes(1)
			s.dedup()
			return
		}
		isFast := s.isExact()
		if n, ok := s.length(); !ok || n > 16 {
			isFast = false
		}
		useFix := len(fix) > 4 || (len(fix) > 1 && !isFast)
		if useFix {
			s.keepFirstBytes(len(fix))
			s.dedup()
		}
	}

	var exact *seq
	if s.isExact() {
		cp := s.clone()
		exact = &cp
	}

	attempts := []struct{ keep, limit int }{
		{5, 10}, {4, 10}, {3, 64}, {2, 64}, {1, 10},
	}
	for _, a := range attempts {
		n, ok := s.length()
		if !ok {
			break
		}
		if n <= a.limit {
			break
		}
		s.keepFirstBytes(a.keep)
		s.minimizeByPreference(true)
	}

	if !s.infinite {
		for _, l := range s.lits {
			if l.isPoisonous() {
				s.makeInfinite()
				break
			}
		}
	}

	if exact != nil {
		if !s.isFinite() {
			*s = *exact
			return
		}
		if min, ok := s.minLiteralLen(); !ok || min <= 2 {
			*s = *exact
			return
		}
		if n, ok := s.length(); !ok || n > 64 {
			*s = *exact
			return
		}
	}
}

func (s *seq) clone() seq {
	cp := seq{infinite: s.infinite}
	if !s.infinite {
		cp.lits = make([]literalItem, len(s.lits))
		for i, l := range s.lits {
			cp.lits[i] = literalItem{bytes: append([]byte(nil), l.bytes...), exact: l.exact}
		}
	}
	return cp
}

// tSeq wraps seq with the "prefix" tag from ripgrep's own literal.rs
// TSeq, used to gate whether a sequence may be crossed as a suffix of
// another (make_not_prefix marks a sequence that resulted from a choose()
// as unsuitable for further prefix-style crossing).
type tSeq struct {
	s      seq
	prefix bool
}

func tSeqEmpty() tSeq                  { return tSeq{s: emptySeq(), prefix: true} }
func tSeqInfinite() tSeq               { return tSeq{s: infiniteSeq(), prefix: true} }
func tSeqSingleton(l literalItem) tSeq { return tSeq{s: singletonSeq(l), prefix: true} }

func (t *tSeq) makeNotPrefix() { t.prefix = false }

func (t *tSeq) hasPoisonousLiteral() bool {
	if t.s.infinite {
		return false
	}
	for _, l := range t.s.lits {
		if l.isPoisonous() {
			return true
		}
	}
	return false
}

// isGood mirrors TSeq::is_good.
func (t *tSeq) isGood() bool {
	if t.hasPoisonousLiteral() {
		return false
	}
	min, ok := t.s.minLiteralLen()
	if !ok {
		return false
	}
	n, ok := t.s.length()
	if !ok {
		return false
	}
	if min <= 1 {
		return n <= 3
	}
	return min >= 2 && n <= 64
}

// isReallyGood mirrors TSeq::is_really_good.
func (t *tSeq) isReallyGood() bool {
	if t.hasPoisonousLiteral() {
		return false
	}
	min, ok := t.s.minLiteralLen()
	if !ok {
		return false
	}
	n, ok := t.s.length()
	if !ok {
		return false
	}
	return min >= 3 && n <= 8
}

// choose mirrors TSeq::choose: picks whichever of the two sequences looks
// like the better prefilter candidate, marking the result inexact since
// picking one means discarding the other's continuation.
func (t tSeq) choose(o tSeq) tSeq {
	t.s.makeInexact()
	o.s.makeInexact()

	if !t.s.isFinite() {
		return o
	}
	if !o.s.isFinite() {
		return t
	}
	if t.hasPoisonousLiteral() {
		return o
	}
	if o.hasPoisonousLiteral() {
		return t
	}
	min1, ok1 := t.s.minLiteralLen()
	if !ok1 {
		return o
	}
	min2, ok2 := o.s.minLiteralLen()
	if !ok2 {
		return t
	}
	if min1 < min2 {
		return o
	} else if min2 < min1 {
		return t
	}
	len1, _ := t.s.length()
	len2, _ := o.s.length()
	if len1 < len2 {
		return o
	} else if len2 < len1 {
		return t
	}
	return t
}

// --- Extractor -------------------------------------------------------

// extractLimits mirror ripgrep's InnerLiterals::Extractor thresholds
// (crates/regex/src/literal.rs), NOT the more permissive regex-syntax
// defaults.
const (
	limitClass      = 10
	limitRepeat     = 10
	limitLiteralLen = 100
	limitTotal      = 64
)

type extractor struct{}

// extractUntagged mirrors Extractor::extract_untagged: runs the extractor
// at the top level, optimizes the result for prefix preference, and
// discards it (makes it infinite) if it doesn't look "good."
func (extractor) extractUntagged(re *syntax.Regexp) seq {
	t := extractor{}.extract(re)
	t.s.optimizeForPrefixByPreference()
	if !t.isGood() {
		t.s.makeInfinite()
	}
	return t.s
}

func (e extractor) extract(re *syntax.Regexp) tSeq {
	switch re.Op {
	case syntax.OpEmptyMatch,
		syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return tSeqSingleton(exactLit(nil))
	case syntax.OpLiteral:
		return e.extractLiteral(re)
	case syntax.OpCharClass:
		return e.extractClass(re)
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		return e.extractRepetition(re)
	case syntax.OpCapture:
		return e.extract(re.Sub[0])
	case syntax.OpConcat:
		return e.extractConcat(re.Sub)
	case syntax.OpAlternate:
		return e.extractAlternation(re.Sub)
	case syntax.OpNoMatch:
		return tSeq{s: emptySeq(), prefix: true}
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return tSeqInfinite()
	default:
		return tSeqInfinite()
	}
}

func (e extractor) extractLiteral(re *syntax.Regexp) tSeq {
	fold := re.Flags&syntax.FoldCase != 0
	t := tSeqSingleton(exactLit(nil))
	for _, r := range re.Rune {
		var runeSeq tSeq
		if !fold {
			buf := make([]byte, utf8.RuneLen(r))
			utf8.EncodeRune(buf, r)
			runeSeq = tSeqSingleton(exactLit(buf))
		} else {
			orbit := foldOrbit(r)
			s := emptySeq()
			for _, fr := range orbit {
				buf := make([]byte, utf8.RuneLen(fr))
				utf8.EncodeRune(buf, fr)
				s.push(exactLit(buf))
			}
			e.enforceLiteralLen(&s)
			runeSeq = tSeq{s: s, prefix: true}
		}
		t = e.cross(t, runeSeq)
	}
	return t
}

// foldOrbit returns the sorted set of runes case-equivalent to r
// (including r itself), via unicode.SimpleFold.
func foldOrbit(r rune) []rune {
	orbit := []rune{r}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		orbit = append(orbit, f)
	}
	sort.Slice(orbit, func(i, j int) bool { return orbit[i] < orbit[j] })
	return orbit
}

func (e extractor) extractClass(re *syntax.Regexp) tSeq {
	if e.classOverLimit(re) {
		return tSeqInfinite()
	}
	s := emptySeq()
	for i := 0; i+1 < len(re.Rune); i += 2 {
		lo, hi := re.Rune[i], re.Rune[i+1]
		for r := lo; r <= hi; r++ {
			buf := make([]byte, utf8.RuneLen(r))
			utf8.EncodeRune(buf, r)
			s.push(exactLit(buf))
		}
	}
	e.enforceLiteralLen(&s)
	return tSeq{s: s, prefix: true}
}

func (e extractor) classOverLimit(re *syntax.Regexp) bool {
	count := 0
	for i := 0; i+1 < len(re.Rune); i += 2 {
		if count > limitClass {
			return true
		}
		count += int(re.Rune[i+1]-re.Rune[i]) + 1
	}
	return count > limitClass
}

func (e extractor) extractConcat(subs []*syntax.Regexp) tSeq {
	s := tSeqSingleton(exactLit(nil))
	var prev *tSeq
	for _, sub := range subs {
		if s.s.isInexact() {
			if s.s.isEmpty() {
				return s
			}
			if s.isReallyGood() {
				return s
			}
			if prev == nil {
				saved := s
				prev = &saved
			} else {
				chosen := prev.choose(s)
				prev = &chosen
			}
			s = tSeqSingleton(exactLit(nil))
			s.makeNotPrefix()
		}
		s = e.cross(s, e.extract(sub))
	}
	if prev != nil {
		return prev.choose(s)
	}
	return s
}

func (e extractor) extractAlternation(subs []*syntax.Regexp) tSeq {
	s := tSeq{s: emptySeq(), prefix: true}
	for _, sub := range subs {
		if !s.s.isFinite() {
			break
		}
		sub2 := e.extract(sub)
		s = e.union(s, &sub2)
	}
	return s
}

// extractRepetition mirrors Extractor::extract_repetition, but first
// normalizes Go's distinct Star/Plus/Quest/Repeat ops into a single
// (min, max) pair (max == -1 means unbounded) so the same four-way case
// analysis Rust performs on its unified Repetition{min,max} applies
// uniformly.
func (e extractor) extractRepetition(re *syntax.Regexp) tSeq {
	greedy := re.Flags&syntax.NonGreedy == 0

	var min, max int
	switch re.Op {
	case syntax.OpStar:
		min, max = 0, -1
	case syntax.OpPlus:
		min, max = 1, -1
	case syntax.OpQuest:
		min, max = 0, 1
	default: // syntax.OpRepeat
		min, max = re.Min, re.Max
	}
	// x{0} matches only the empty string. Rust's HIR translator const-
	// folds this away entirely before the extractor ever sees it; Go's
	// parser keeps an explicit Repeat{min:0,max:0} node, so replicate
	// that fold here rather than extracting (and unioning in) the
	// sub-pattern at all.
	if min == 0 && max == 0 {
		return tSeqSingleton(exactLit(nil))
	}

	subseq := e.extract(re.Sub[0])

	if min == 0 {
		// 'a?' retains exactness (equivalent to 'a|'); anything else
		// with min=0 (a*, a{0,n} for n!=1) does not.
		if max != 1 {
			subseq.s.makeInexact()
		}
		empty := tSeqSingleton(exactLit(nil))
		if !greedy {
			subseq, empty = empty, subseq
		}
		return e.union(subseq, &empty)
	}
	if max == -1 {
		// Catch-all for an unbounded upper bound with min > 0 (a+,
		// a{2,}, ...): rg does not build `min` concatenated copies here,
		// it just extracts the sub-pattern once and marks it inexact.
		subseq.s.makeInexact()
		return subseq
	}
	if min == max {
		return e.extractRepeatRange(subseq, min, true)
	}
	// min < max: bounded range, always inexact.
	return e.extractRepeatRange(subseq, min, false)
}

// extractRepeatRange builds up to min copies of subseq via cross product
// (capped at limitRepeat), mirroring the min==max / min<max arms of
// Extractor::extract_repetition. When exactCount is true (min==max), the
// result stays exact unless min exceeded the repeat limit; when false
// (min<max, a bounded range), the result is always made inexact.
func (e extractor) extractRepeatRange(subseq tSeq, min int, exactCount bool) tSeq {
	limit := limitRepeat
	s := tSeqSingleton(exactLit(nil))
	reps := min
	if reps > limit {
		reps = limit
	}
	for i := 0; i < reps; i++ {
		if s.s.isInexact() {
			break
		}
		s = e.cross(s, cloneTSeq(subseq))
	}
	if exactCount {
		if min > limit {
			s.s.makeInexact()
		}
	} else {
		s.s.makeInexact()
	}
	return s
}

func cloneTSeq(t tSeq) tSeq {
	return tSeq{s: t.s.clone(), prefix: t.prefix}
}

func (e extractor) enforceLiteralLen(s *seq) {
	s.keepFirstBytes(limitLiteralLen)
}

// cross mirrors ripgrep's Extractor::cross (crates/regex/src/literal.rs),
// which additionally consults the TSeq.prefix tag.
func (e extractor) cross(s1, s2 tSeq) tSeq {
	if !s2.prefix {
		return s1.choose(s2)
	}
	if n, ok := s1.s.maxCrossLen(&s2.s); ok && n > limitTotal {
		s2.s.makeInfinite()
	}
	s1.s.crossForward(&s2.s)
	e.enforceLiteralLen(&s1.s)
	return s1
}

// union mirrors Extractor::union.
func (e extractor) union(s1 tSeq, s2 *tSeq) tSeq {
	if n, ok := s1.s.maxUnionLen(&s2.s); ok && n > limitTotal {
		s1.s.keepFirstBytes(4)
		s2.s.keepFirstBytes(4)
		s1.s.dedup()
		s2.s.dedup()
		if n2, ok2 := s1.s.maxUnionLen(&s2.s); ok2 && n2 > limitTotal {
			s2.s.makeInfinite()
		}
	}
	s1.s.union(&s2.s)
	s1.prefix = s1.prefix && s2.prefix
	return s1
}

// extractInnerLiterals runs the full ported extractor over re and returns
// the resulting literal seq (possibly infinite, meaning "give up").
func extractInnerLiterals(re *syntax.Regexp) seq {
	return extractor{}.extractUntagged(re)
}
