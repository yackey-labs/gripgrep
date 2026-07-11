// Command extract reads ripgrep's crates/ignore/src/default_types.rs from a
// real rg checkout and writes filetype/default_types.go: the checked-in Go
// transcription of rg's DEFAULT_TYPES table (the file-type system).
// This mirrors internal/parity/extract's pattern (a small hand-rolled
// scanner tuned to one very regular Rust source shape, run once and its
// output committed) but is deliberately a separate command/package: it
// produces a public filetype/ table, not internal/parity's own JSON
// inventory, and internal/parity/source is explicitly off limits for this
// round.
//
// Usage:
//
//	go run ./internal/typetable/extract <rg-checkout-root> <output-go-path>
//
// default_types.rs is Rust, not Go, so this scans for the DEFAULT_TYPES
// const's body and pulls out alternating (&[names...], &[globs...]) tuples
// via a simple bracket-content regex -- NOT go/ast, which only applies to
// Go source. The source has exactly one shape throughout (a flat list of
// string literals inside two `&[...]` per entry, no nested brackets), so
// this is deliberately not a general Rust parser.
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "extract:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: extract <rg-checkout-root> <output-go-path>")
	}
	root, outPath := os.Args[1], os.Args[2]

	srcPath := root + "/crates/ignore/src/default_types.rs"
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", srcPath, err)
	}

	entries, err := parseDefaultTypes(string(src))
	if err != nil {
		return fmt.Errorf("parsing %s: %w", srcPath, err)
	}
	if len(entries) < 200 {
		// DEFAULT_TYPES had 217 entries as of rg 15.1.0 -- a drastically
		// smaller count almost certainly means the regex below stopped
		// matching partway through (e.g. the source shape changed), not
		// that rg actually shrank its type table.
		return fmt.Errorf("parsed only %d entries, expected 200+ -- source shape may have changed", len(entries))
	}

	out, err := render(entries)
	if err != nil {
		return fmt.Errorf("rendering: %w", err)
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	fmt.Fprintf(os.Stderr, "extract: wrote %d entries to %s\n", len(entries), outPath)
	return nil
}

type entry struct {
	names []string
	globs []string
}

var (
	constStartRE = regexp.MustCompile(`(?s)pub\(crate\) const DEFAULT_TYPES:[^=]*=\s*&\[(.*?)\n\];`)
	stringRE     = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// parseDefaultTypes extracts every (&[names...], &[globs...]) tuple from
// the DEFAULT_TYPES const body. Names-lists and globs-lists alternate
// strictly (comma-separated string literals, possibly wrapped across
// lines), so pairing up findBrackets' matches two-at-a-time is exact
// without needing to track the outer `(...)` tuple boundaries at all --
// but finding each `&[...]` span itself must be string-literal-aware
// (findBrackets, not a naive `[^\]]*` regex): several default globs are
// themselves bracket character classes, e.g. `"*.[chH]"`, so a glob list
// like `["*.[chH]", "*.cats"]` contains a `]` that does NOT close the
// list.
func parseDefaultTypes(src string) ([]entry, error) {
	m := constStartRE.FindStringSubmatch(src)
	if m == nil {
		return nil, fmt.Errorf("DEFAULT_TYPES const body not found")
	}
	body := m[1]

	brackets, err := findBrackets(body)
	if err != nil {
		return nil, err
	}
	if len(brackets)%2 != 0 {
		return nil, fmt.Errorf("odd number of &[...] groups (%d) -- names/globs lists didn't pair up", len(brackets))
	}

	entries := make([]entry, 0, len(brackets)/2)
	for i := 0; i < len(brackets); i += 2 {
		names := extractStrings(brackets[i])
		globs := extractStrings(brackets[i+1])
		if len(names) == 0 || len(globs) == 0 {
			return nil, fmt.Errorf("entry %d: empty names (%d) or globs (%d)", i/2, len(names), len(globs))
		}
		entries = append(entries, entry{names: names, globs: globs})
	}
	return entries, nil
}

// findBrackets returns the raw content of every top-level `&[...]` span in
// src, tracking whether the scanner is inside a Rust string literal (and
// respecting `\"` escapes within one) so a `]` inside a glob string like
// `"*.[chH]"` is never mistaken for the list's own closing bracket.
func findBrackets(src string) ([]string, error) {
	var out []string
	i := 0
	for {
		rel := strings.Index(src[i:], "&[")
		if rel < 0 {
			break
		}
		start := i + rel
		contentStart := start + len("&[")
		inString := false
		j := contentStart
		depth := 1
		for ; j < len(src); j++ {
			c := src[j]
			switch {
			case inString:
				if c == '\\' {
					j++ // skip the escaped character
				} else if c == '"' {
					inString = false
				}
			case c == '"':
				inString = true
			case c == '[':
				depth++
			case c == ']':
				depth--
				if depth == 0 {
					goto closed
				}
			}
		}
		return nil, fmt.Errorf("unterminated &[ starting at byte %d", start)
	closed:
		out = append(out, src[contentStart:j])
		i = j + 1
	}
	return out, nil
}

func extractStrings(s string) []string {
	matches := stringRE.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, mm := range matches {
		out = append(out, mm[1])
	}
	return out
}

const header = `// Code generated by internal/typetable/extract from ripgrep's
// crates/ignore/src/default_types.rs (the file-type system). DO NOT
// EDIT BY HAND -- regenerate with:
//
//	go run ./internal/typetable/extract <rg-checkout-root> filetype/default_types.go
//
// See internal/typetable/extract/main.go's doc comment for the extraction
// approach. Entry order here is source order (rg's own lexicographic-by-
// first-name sort per default_types.rs's own doc comment and test); output
// sorting for --type-list happens at Definitions() time instead, since
// --type-add/--type-clear mutate the table at runtime -- see filetype's
// doc.
package filetype

// defaultTypeEntry is one DEFAULT_TYPES row: one or more alias names
// sharing the exact same glob list (e.g. "bat"/"batch" both map to
// *.bat) -- mirrors default_types.rs's own (&[&str], &[&str]) shape
// exactly, including the double-nesting, so AddDefaults can iterate it
// the same way rg's own add_defaults() does (crates/ignore/src/types.rs):
// for each name, for each glob, register one (name, glob) pair.
type defaultTypeEntry struct {
	names []string
	globs []string
}

var defaultTypes = []defaultTypeEntry{
`

func render(entries []entry) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(header)
	for _, e := range entries {
		b.WriteString("\t{names: []string{")
		writeStringList(&b, e.names)
		b.WriteString("}, globs: []string{")
		writeStringList(&b, e.globs)
		b.WriteString("}},\n")
	}
	b.WriteString("}\n")

	formatted, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gofmt: %w (source follows)\n%s", err, b.String())
	}
	return formatted, nil
}

func writeStringList(b *bytes.Buffer, ss []string) {
	for i, s := range ss {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", s)
	}
}
