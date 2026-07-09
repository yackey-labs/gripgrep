package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// This file implements gg's command-line flag parser. It has no
// dependency on any other gripgrep package (glob/walk/match/search/
// printer) -- it only produces a plain Config value. M2 is responsible
// for translating a Config into calls against those packages.
//
// Per PLAN.md's "1:1 rg compatibility is the contract" addendum, every
// flag gg implements must match ripgrep's naming, short forms, negation
// spelling, argument syntax, and override semantics exactly. Names,
// short flags, negations, and update semantics below were read directly
// from ../ripgrep/crates/core/flags/defs.rs and lowargs.rs (the
// authoritative source named in PLAN.md), then spot-checked against the
// real `rg` 14.1.1 binary on PATH for the ambiguous interactions (see
// flags_test.go doc comments for the exact probe commands and output).
//
// A flag outside gg's v1 scope must never be silently ignored or
// reinterpreted. Two buckets exist for that:
//   - notImplementedFlags: a curated set of real rg flags that are
//     particularly likely to be typed (PLAN.md's explicit "Not in v1"
//     list, plus a few natural neighbors of flags gg does implement).
//     These produce a clear "not yet implemented" error.
//   - anything else unrecognized: a generic "unrecognized flag" error.
// Both are exit 2, matching rg's own convention; gg cannot be
// byte-identical to rg for a flag it refuses in either case, so the
// distinction only changes the error message, not the exit code.

// CaseMode mirrors rg's CaseMode (lowargs.rs): last of -i/-s/-S wins,
// regardless of order, because each directly overwrites the same field.
type CaseMode int

const (
	CaseSensitive CaseMode = iota
	CaseInsensitive
	CaseSmart
)

// SearchMode mirrors the subset of rg's SearchMode that gg's v1 scope
// implements. Like rg's Mode::update, any later mode flag overwrites an
// earlier one.
type SearchMode int

const (
	ModeStandard SearchMode = iota
	ModeCount
	ModeFilesWithMatches
)

// BinaryMode mirrors rg's BinaryMode.
type BinaryMode int

const (
	// BinaryAuto is rg's default: skip-on-NUL for walk-discovered files,
	// convert-on-NUL for explicitly named files. Which of those applies
	// is decided downstream (by search.Searcher), not by this parser.
	BinaryAuto BinaryMode = iota
	// BinarySearchAndSuppress is rg's --binary behavior, reached in v1
	// scope only via the third -u/--unrestricted level (-uuu).
	BinarySearchAndSuppress
	// BinaryAsText is -a/--text: binary detection fully disabled.
	BinaryAsText
)

// ColorMode mirrors rg's --color choices exactly, including "ansi".
// PLAN.md's v1-scope line says "auto|never|always", but the later "1:1
// rg compatibility" addendum is the controlling requirement, and rg
// itself accepts "ansi" as a fourth choice (defs.rs Color::doc_choices).
type ColorMode int

const (
	ColorAuto ColorMode = iota
	ColorNever
	ColorAlways
	ColorAnsi
)

// MmapMode mirrors rg's MmapMode.
type MmapMode int

const (
	MmapAuto MmapMode = iota
	MmapAlways
	MmapNever
)

// Config is the parsed, plain-data result of ParseArgs. It has no
// dependency on any gripgrep library package.
type Config struct {
	// Patterns are OR'd together: either the single first positional
	// (when -e/--regexp was never given) or every -e/--regexp value (in
	// which case ALL positionals become Paths -- see ParseArgs).
	Patterns []string
	// Paths are the files/directories to search.
	Paths []string

	Case  CaseMode
	Fixed bool // -F/--fixed-strings
	Word  bool // -w/--word-regexp

	Hidden       bool     // --hidden
	NoIgnore     bool     // --no-ignore (collapses rg's 5 no-ignore-* sub-flags into one, matching walk.Options.NoIgnore)
	Globs        []string // -g/--glob, in the order given verbatim (leading '!' negation is glob syntax, handled by package glob, not here)
	Unrestricted int      // 0-3: the -u/-uu/-uuu level actually given, kept for diagnostics even though NoIgnore/Hidden/Binary already reflect its effect
	MaxFilesize  int64    // 0 = unlimited (rg's None)

	// LineNumbers is nil when neither -n nor -N was given: rg decides
	// the default from isatty(stdout) at runtime, which is an M2/cmd
	// concern, not this parser's.
	LineNumbers *bool
	Mode        SearchMode
	Quiet       bool // -q/--quiet; independent of Mode, matches rg (quiet suppresses output regardless of search mode)
	Color       ColorMode
	// ContextBefore/ContextAfter are already resolved from rg's
	// independently-tracked -A/-B/-C (see resolveContext); -A/-B always
	// partially override -C's corresponding side, regardless of order.
	ContextBefore int
	ContextAfter  int
	Invert        bool // -v/--invert-match

	Threads int        // -j/--threads; 0 = auto (rg's None)
	Binary  BinaryMode // resolved from -a/--text and -uuu; last one processed wins
	Mmap    MmapMode   // --mmap/--no-mmap
}

// parseState holds parser-transient data that isn't part of the final
// Config (either because it's resolved away, like context, or because
// it's only needed to decide how to resolve positionals).
type parseState struct {
	positionals []string
	sawPattern  bool // true once -e/--regexp has been seen at least once
	uLevel      int  // -u/--unrestricted repeat count so far

	// before/after/both mirror rg's ContextModeLimited exactly: each
	// -A/-B/-C write is tracked independently and only resolved into
	// Config.ContextBefore/After at the very end (see resolveContext).
	before, after, both *int
}

type flagKind int

const (
	kindSwitch flagKind = iota
	kindValue
)

type flagSpec struct {
	long    string
	short   byte   // 0 = no short form
	negated string // "" = no negation
	kind    flagKind

	// exactly one of these is set, matching kind
	applySwitch func(cfg *Config, ps *parseState, on bool) error
	applyValue  func(cfg *Config, ps *parseState, val string) error
}

// v1Flags is the registry of every flag gg actually implements in v1.
// Names/short-forms/negations are transcribed from
// ../ripgrep/crates/core/flags/defs.rs (see the doc comment at the top
// of this file).
var v1Flags = buildV1Flags()

func buildV1Flags() []*flagSpec {
	return []*flagSpec{
		// --- Pattern (PLAN.md v1 scope: regex default, -F, -i, -S, -w, -e) ---
		{
			long: "fixed-strings", short: 'F', negated: "no-fixed-strings", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Fixed = on
				return nil
			},
		},
		{
			// -i/--ignore-case. No negation of its own; -s/--case-sensitive
			// is the way back per defs.rs (each of -i/-s/-S sets the same
			// CaseMode field directly, so plain last-write-wins applies).
			long: "ignore-case", short: 'i', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Case = CaseInsensitive
				return nil
			},
		},
		{
			// -s/--case-sensitive is not in PLAN.md's literal v1 flag
			// list, but it is the direct complement of -i/-S (all three
			// write the same CaseMode field in rg) and omitting it would
			// break last-wins semantics for anyone combining -i/-S with
			// -s. Included deliberately; flagged in the M0 handoff.
			long: "case-sensitive", short: 's', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Case = CaseSensitive
				return nil
			},
		},
		{
			long: "smart-case", short: 'S', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Case = CaseSmart
				return nil
			},
		},
		{
			long: "word-regexp", short: 'w', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Word = true
				return nil
			},
		},
		{
			long: "regexp", short: 'e', kind: kindValue,
			applyValue: func(cfg *Config, ps *parseState, val string) error {
				cfg.Patterns = append(cfg.Patterns, val)
				ps.sawPattern = true
				return nil
			},
		},

		// --- Filtering ---
		{
			long: "hidden", short: '.', negated: "no-hidden", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Hidden = on
				return nil
			},
		},
		{
			// rg's primary flag name really is "no-ignore"; its negation
			// is the (rarely typed) "--ignore", used to turn a preceding
			// --no-ignore back off. Do not "fix" this spelling.
			long: "no-ignore", negated: "ignore", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.NoIgnore = on
				return nil
			},
		},
		{
			long: "glob", short: 'g', kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				cfg.Globs = append(cfg.Globs, val)
				return nil
			},
		},
		{
			long: "unrestricted", short: 'u', kind: kindSwitch,
			applySwitch: func(cfg *Config, ps *parseState, _ bool) error {
				ps.uLevel++
				if ps.uLevel > 3 {
					return fmt.Errorf("flag can only be repeated up to 3 times")
				}
				switch ps.uLevel {
				case 1:
					cfg.NoIgnore = true
				case 2:
					cfg.Hidden = true
				case 3:
					cfg.Binary = BinarySearchAndSuppress
				}
				cfg.Unrestricted = ps.uLevel
				return nil
			},
		},
		{
			long: "max-filesize", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				n, err := parseHumanSize(val)
				if err != nil {
					return err
				}
				cfg.MaxFilesize = n
				return nil
			},
		},

		// --- Output ---
		{
			// -n/--line-number and -N/--no-line-number are TWO SEPARATE
			// rg flags (each is its own struct with "has no automatic
			// negation" asserted in its own update fn), not one flag with
			// a negated spelling. Both just overwrite the same field, so
			// last-one-given wins regardless of order.
			long: "line-number", short: 'n', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				b := true
				cfg.LineNumbers = &b
				return nil
			},
		},
		{
			long: "no-line-number", short: 'N', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				b := false
				cfg.LineNumbers = &b
				return nil
			},
		},
		{
			long: "count", short: 'c', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Mode = ModeCount
				return nil
			},
		},
		{
			long: "files-with-matches", short: 'l', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Mode = ModeFilesWithMatches
				return nil
			},
		},
		{
			long: "quiet", short: 'q', kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, _ bool) error {
				cfg.Quiet = true
				return nil
			},
		},
		{
			long: "color", kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				switch val {
				case "never":
					cfg.Color = ColorNever
				case "auto":
					cfg.Color = ColorAuto
				case "always":
					cfg.Color = ColorAlways
				case "ansi":
					cfg.Color = ColorAnsi
				default:
					return fmt.Errorf("choice %q is unrecognized", val)
				}
				return nil
			},
		},
		{
			long: "after-context", short: 'A', kind: kindValue,
			applyValue: func(_ *Config, ps *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				ps.after = &n
				return nil
			},
		},
		{
			long: "before-context", short: 'B', kind: kindValue,
			applyValue: func(_ *Config, ps *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				ps.before = &n
				return nil
			},
		},
		{
			long: "context", short: 'C', kind: kindValue,
			applyValue: func(_ *Config, ps *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				ps.both = &n
				return nil
			},
		},
		{
			long: "invert-match", short: 'v', negated: "no-invert-match", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				cfg.Invert = on
				return nil
			},
		},

		// --- Perf ---
		{
			long: "threads", short: 'j', kind: kindValue,
			applyValue: func(cfg *Config, _ *parseState, val string) error {
				n, err := parseNonNegInt(val)
				if err != nil {
					return err
				}
				cfg.Threads = n
				return nil
			},
		},
		{
			// -a/--text always sets Binary explicitly (AsText when given,
			// Auto when negated via --no-text) -- it is not a no-op on
			// negation. This lets --no-text reset a Binary set earlier by
			// -uuu, matching rg's own Text::update.
			long: "text", short: 'a', negated: "no-text", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				if on {
					cfg.Binary = BinaryAsText
				} else {
					cfg.Binary = BinaryAuto
				}
				return nil
			},
		},
		{
			long: "mmap", negated: "no-mmap", kind: kindSwitch,
			applySwitch: func(cfg *Config, _ *parseState, on bool) error {
				if on {
					cfg.Mmap = MmapAlways
				} else {
					cfg.Mmap = MmapNever
				}
				return nil
			},
		},
	}
}

// notImplementedFlag names a real rg flag gg deliberately does not
// implement yet, so ParseArgs can fail closed with a specific message
// instead of a bare "unrecognized flag".
type notImplementedFlag struct {
	long  string
	short byte // 0 = no short form
	label string
}

// notImplementedFlags is a curated (not exhaustive) list covering
// PLAN.md's explicit "Not in v1" line -- replace, multiline, PCRE2,
// non-UTF-8 encodings, compressed files, --sort, JSON, -o -- plus a
// handful of flags an rg user is very likely to type that aren't named
// there: --help/--version, -L/--follow, -f/--file (the file-based
// alternative to -e), -x/--line-regexp, and --binary (reachable in v1
// only indirectly via -uuu, but not as a flag of its own yet). This is
// intentionally not the full ~100-flag rg surface -- see the M0 handoff
// note for why that would be scope inflation for this task.
var notImplementedFlags = []notImplementedFlag{
	{long: "replace", short: 'r', label: "-r/--replace"},
	{long: "multiline", short: 'U', label: "-U/--multiline"},
	{long: "multiline-dotall", label: "--multiline-dotall"},
	{long: "pcre2", short: 'P', label: "-P/--pcre2"},
	{long: "encoding", short: 'E', label: "-E/--encoding"},
	{long: "search-zip", short: 'z', label: "-z/--search-zip"},
	{long: "sort", label: "--sort"},
	{long: "sortr", label: "--sortr"},
	{long: "json", label: "--json"},
	{long: "only-matching", short: 'o', label: "-o/--only-matching"},
	{long: "binary", label: "--binary"},
	{long: "line-regexp", short: 'x', label: "-x/--line-regexp"},
	{long: "follow", short: 'L', label: "-L/--follow"},
	{long: "file", short: 'f', label: "-f/--file"},
	{long: "help", short: 'h', label: "-h/--help"},
	{long: "version", short: 'V', label: "-V/--version"},
}

// byLongName indexes v1Flags by every long spelling that should resolve
// to it (its primary name and, if present, its negated name), recording
// whether that spelling is the negated one.
type longEntry struct {
	spec   *flagSpec
	negate bool
}

var longIndex = buildLongIndex()
var shortIndex = buildShortIndex()
var notImplLongIndex = buildNotImplLongIndex()
var notImplShortIndex = buildNotImplShortIndex()

func buildLongIndex() map[string]longEntry {
	m := make(map[string]longEntry, len(v1Flags)*2)
	for _, s := range v1Flags {
		m[s.long] = longEntry{spec: s, negate: false}
		if s.negated != "" {
			m[s.negated] = longEntry{spec: s, negate: true}
		}
	}
	return m
}

func buildShortIndex() map[byte]*flagSpec {
	m := make(map[byte]*flagSpec, len(v1Flags))
	for _, s := range v1Flags {
		if s.short != 0 {
			m[s.short] = s
		}
	}
	return m
}

func buildNotImplLongIndex() map[string]string {
	m := make(map[string]string, len(notImplementedFlags))
	for _, f := range notImplementedFlags {
		m[f.long] = f.label
	}
	return m
}

func buildNotImplShortIndex() map[byte]string {
	m := make(map[byte]string, len(notImplementedFlags))
	for _, f := range notImplementedFlags {
		if f.short != 0 {
			m[f.short] = f.label
		}
	}
	return m
}

// ParseArgs parses args (typically os.Args[1:]) into a Config, matching
// rg's flag syntax and override semantics for every flag in gg's v1
// scope. Any error is a parse failure that the caller should report
// alongside a short usage line and an exit code of 2, matching rg's own
// convention for bad flags/missing patterns.
func ParseArgs(args []string) (*Config, error) {
	cfg := &Config{
		Case:  CaseSensitive,
		Color: ColorAuto,
		Mmap:  MmapAuto,
	}
	ps := &parseState{}

	sawDashDash := false
	for i := 0; i < len(args); i++ {
		arg := args[i]

		if sawDashDash {
			ps.positionals = append(ps.positionals, arg)
			continue
		}
		if arg == "--" {
			sawDashDash = true
			continue
		}

		switch {
		case strings.HasPrefix(arg, "--"):
			consumed, err := parseLongFlag(cfg, ps, arg, args, i)
			if err != nil {
				return nil, err
			}
			i += consumed
		case len(arg) > 1 && arg[0] == '-':
			consumed, err := parseShortCluster(cfg, ps, arg, args, i)
			if err != nil {
				return nil, err
			}
			i += consumed
		default:
			// A bare "-" (stdin) and anything not starting with '-' are
			// both ordinary positionals at the parser level.
			ps.positionals = append(ps.positionals, arg)
		}
	}

	if ps.sawPattern {
		// -e/--regexp was given at least once: per rg (Regexp's doc
		// comment: "When --file or --regexp is used, then ripgrep
		// treats all positional arguments as files or directories to
		// search"), every positional is a path, none become the pattern.
		cfg.Paths = ps.positionals
	} else {
		if len(ps.positionals) == 0 {
			return nil, fmt.Errorf("gripgrep requires at least one pattern to execute a search")
		}
		cfg.Patterns = []string{ps.positionals[0]}
		cfg.Paths = ps.positionals[1:]
	}

	cfg.ContextBefore, cfg.ContextAfter = resolveContext(ps.before, ps.after, ps.both)

	return cfg, nil
}

// resolveContext mirrors rg's ContextModeLimited.get(): -A/-B always
// partially override -C's corresponding side, regardless of the order
// they were given in, because each is tracked independently until this
// final resolution step.
func resolveContext(before, after, both *int) (b, a int) {
	if both != nil {
		b, a = *both, *both
	}
	if before != nil {
		b = *before
	}
	if after != nil {
		a = *after
	}
	return b, a
}

// parseLongFlag handles one "--name", "--name=value", or "--name value"
// token starting at args[i]. It returns how many EXTRA tokens beyond
// args[i] were consumed (0 or 1), so the caller's loop can skip them.
func parseLongFlag(cfg *Config, ps *parseState, arg string, args []string, i int) (int, error) {
	body := arg[2:] // strip "--"
	name := body
	inlineVal := ""
	hasInline := false
	if eq := strings.IndexByte(body, '='); eq >= 0 {
		name = body[:eq]
		inlineVal = body[eq+1:]
		hasInline = true
	}

	if label, ok := notImplLongIndex[name]; ok {
		return 0, notImplementedError(label)
	}

	entry, ok := longIndex[name]
	if !ok {
		return 0, fmt.Errorf("unrecognized flag --%s", name)
	}
	spec := entry.spec

	if spec.kind == kindSwitch {
		if hasInline {
			return 0, fmt.Errorf("unexpected value for switch flag --%s", name)
		}
		if err := spec.applySwitch(cfg, ps, !entry.negate); err != nil {
			return 0, fmt.Errorf("error parsing flag --%s: %w", name, err)
		}
		return 0, nil
	}

	// Value flag.
	if hasInline {
		if err := spec.applyValue(cfg, ps, inlineVal); err != nil {
			return 0, fmt.Errorf("error parsing flag --%s: %w", name, err)
		}
		return 0, nil
	}
	if i+1 >= len(args) {
		return 0, fmt.Errorf("missing value for flag --%s", name)
	}
	if err := spec.applyValue(cfg, ps, args[i+1]); err != nil {
		return 0, fmt.Errorf("error parsing flag --%s: %w", name, err)
	}
	return 1, nil
}

// parseShortCluster handles one "-xyz"-style token starting at args[i]:
// a run of single-byte switches, optionally ending in a value-taking
// flag that consumes either the rest of the token or the next argv
// entry. It returns how many EXTRA tokens beyond args[i] were consumed
// (0 or 1).
//
// Matches rg's own behavior exactly (verified against the real binary):
// a value-taking flag anywhere in the cluster greedily claims everything
// after it in the token as its value -- there is no way to place more
// switches after a value-taking flag in the same token.
func parseShortCluster(cfg *Config, ps *parseState, arg string, args []string, i int) (int, error) {
	body := arg[1:] // strip leading '-'
	for k := 0; k < len(body); k++ {
		c := body[k]

		if label, ok := notImplShortIndex[c]; ok {
			return 0, notImplementedError(label)
		}

		spec, ok := shortIndex[c]
		if !ok {
			return 0, fmt.Errorf("unrecognized flag -%c", c)
		}

		if spec.kind == kindSwitch {
			if err := spec.applySwitch(cfg, ps, true); err != nil {
				return 0, fmt.Errorf("error parsing flag -%c: %w", c, err)
			}
			continue
		}

		// Value flag: the remainder of this token is the value, or (if
		// nothing remains) the next argv entry is.
		rest := body[k+1:]
		if rest != "" {
			if err := spec.applyValue(cfg, ps, rest); err != nil {
				return 0, fmt.Errorf("error parsing flag -%c: %w", c, err)
			}
			return 0, nil
		}
		if i+1 >= len(args) {
			return 0, fmt.Errorf("missing value for flag -%c", c)
		}
		if err := spec.applyValue(cfg, ps, args[i+1]); err != nil {
			return 0, fmt.Errorf("error parsing flag -%c: %w", c, err)
		}
		return 1, nil
	}
	return 0, nil
}

func notImplementedError(label string) error {
	return fmt.Errorf("not yet implemented: %s (rg supports this flag; gg's v1 flag set does not yet -- see PLAN.md)", label)
}

// parseNonNegInt parses a non-negative decimal integer, matching the
// convert::usize/convert::u64 helpers in defs.rs: these parse directly
// into an unsigned type, so a leading '-' fails immediately as an
// invalid digit rather than as a distinct "negative not allowed" check
// (verified: `rg -A -5` fails with "invalid digit found in string", the
// same message as any other non-digit input, not a sign-specific one).
func parseNonNegInt(s string) (int, error) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("value is not a valid number: %w", err)
	}
	if n > math.MaxInt {
		return 0, fmt.Errorf("value is not a valid number: too large")
	}
	return int(n), nil
}

// parseHumanSize parses rg's --max-filesize format exactly: a run of
// ASCII digits, followed by an optional exact suffix of "K", "M", or
// "G" (binary multiples: 1024/1024^2/1024^3). No decimals, no lowercase
// suffixes -- matches crates/cli/src/human.rs::parse_human_readable_size.
func parseHumanSize(s string) (int64, error) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("invalid size: invalid format for size %q, which should be a non-empty sequence of digits followed by an optional 'K', 'M' or 'G' suffix", s)
	}
	value, err := strconv.ParseUint(s[:end], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size: %w", err)
	}
	suffix := s[end:]
	var mult uint64 = 1
	switch suffix {
	case "":
		mult = 1
	case "K":
		mult = 1 << 10
	case "M":
		mult = 1 << 20
	case "G":
		mult = 1 << 30
	default:
		return 0, fmt.Errorf("invalid size: invalid format for size %q, which should be a non-empty sequence of digits followed by an optional 'K', 'M' or 'G' suffix", s)
	}
	if mult != 1 && value > (1<<64-1)/mult {
		return 0, fmt.Errorf("invalid size: value too large for size %q", s)
	}
	return int64(value * mult), nil
}
