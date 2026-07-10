// Command extract reads ripgrep's crates/core/flags/defs.rs from a real rg
// checkout and writes internal/parity/rg-flags.json: the checked-in flag
// inventory that internal/parity/gen (and the drift test) build from
// without ever needing an rg checkout again.
//
// Usage:
//
//	go run ./internal/parity/extract <rg-checkout-root> <output-json-path>
//
// The rg checkout must be a real git clone (its HEAD commit/date and
// Cargo.toml version become the "pin" stamped into the JSON and, from
// there, into docs/rg-parity.md's "What is being compared" table).
//
// defs.rs is Rust, not Go, so this is a small hand-rolled scanner tuned to
// its very regular shape (one `impl Flag for X { ... }` block per flag,
// each method body a single expression) -- NOT go/ast, which only applies
// to the Go source this package also reads (cmd/gg/flags.go, parsed
// elsewhere in package parity).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/yackey-labs/gripgrep/internal/parity"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "extract:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: extract <rg-checkout-root> <output-json-path>")
	}
	root, outPath := os.Args[1], os.Args[2]

	defsPath := root + "/crates/core/flags/defs.rs"
	src, err := os.ReadFile(defsPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", defsPath, err)
	}

	flags, err := extractFlags(string(src))
	if err != nil {
		return fmt.Errorf("extracting flags: %w", err)
	}

	pin, err := derivePin(root)
	if err != nil {
		return fmt.Errorf("deriving pin: %w", err)
	}

	inv := parity.Inventory{Pin: pin, Flags: flags}
	out, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "extracted %d flags (pin %s / %s) -> %s\n", len(flags), pin.Commit, pin.Version, outPath)
	return nil
}

func derivePin(root string) (parity.Pin, error) {
	commit, err := gitOut(root, "rev-parse", "--short=12", "HEAD")
	if err != nil {
		return parity.Pin{}, err
	}
	date, err := gitOut(root, "log", "-1", "--format=%ad", "--date=format:%Y-%m-%d")
	if err != nil {
		return parity.Pin{}, err
	}
	cargoToml, err := os.ReadFile(root + "/Cargo.toml")
	if err != nil {
		return parity.Pin{}, err
	}
	m := regexp.MustCompile(`(?m)^version = "([^"]+)"`).FindStringSubmatch(string(cargoToml))
	if m == nil {
		return parity.Pin{}, fmt.Errorf("no top-level version in %s/Cargo.toml", root)
	}
	return parity.Pin{Commit: commit, Date: date, Version: m[1]}, nil
}

func gitOut(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	return strings.TrimSpace(string(out)), nil
}

var implBlockRe = regexp.MustCompile(`impl Flag for (\w+) \{`)

// extractFlags scans src for every `impl Flag for X { ... }` block (brace-
// matched, since method bodies contain their own braces) and pulls the
// fields parity.RgFlag needs out of each one.
func extractFlags(src string) ([]parity.RgFlag, error) {
	var flags []parity.RgFlag
	for _, loc := range implBlockRe.FindAllStringSubmatchIndex(src, -1) {
		structName := src[loc[2]:loc[3]]
		openBrace := loc[1] - 1 // index of the "{" that closed the match
		block, err := braceBody(src, openBrace)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", structName, err)
		}

		long, ok := reFirst(block, `fn name_long\(&self\) -> &'static str \{\s*"([^"]*)"\s*\}`)
		if !ok {
			return nil, fmt.Errorf("%s: no name_long", structName)
		}
		short, _ := reFirst(block, `fn name_short\(&self\) -> Option<u8> \{\s*Some\(b'(.)'\)`)
		negated, _ := reFirst(block, `fn name_negated\(&self\) -> Option<&'static str> \{\s*Some\("([^"]*)"\)`)
		category, ok := reFirst(block, `fn doc_category\(&self\) -> Category \{\s*Category::(\w+)`)
		if !ok {
			return nil, fmt.Errorf("%s: no doc_category", structName)
		}
		docShort, err := extractDocShort(block)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", structName, err)
		}
		aliases := extractAliases(block)

		flags = append(flags, parity.RgFlag{
			Long:     long,
			Short:    short,
			Negated:  negated,
			Aliases:  aliases,
			Category: parity.RgCategory(category),
			DocShort: docShort,
		})
	}
	return flags, nil
}

// braceBody returns the text strictly between the '{' at src[open] and its
// matching '}', tracking nested braces (and skipping braces inside string
// literals, which defs.rs's doc_long bodies contain via \fB...\fP-style
// groff, not braces -- but update() bodies do have real nested braces, so
// this can't just find the next '}').
func braceBody(src string, open int) (string, error) {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : i], nil
			}
		}
	}
	return "", fmt.Errorf("unbalanced braces from offset %d", open)
}

// extractDocShort pulls the doc_short() method's single string literal,
// which is spelled as "...", r"...", or (once, for a summary containing an
// embedded quote) r#"...#"#.
func extractDocShort(block string) (string, error) {
	m := regexp.MustCompile(`fn doc_short\(&self\) -> &'static str \{\s*\n\s*(.*?)\n\s*\}`).FindStringSubmatch(block)
	if m == nil {
		return "", fmt.Errorf("no doc_short")
	}
	lit := strings.TrimSpace(m[1])
	switch {
	case strings.HasPrefix(lit, `r#"`) && strings.HasSuffix(lit, `"#`):
		return strings.TrimSuffix(strings.TrimPrefix(lit, `r#"`), `"#`), nil
	case strings.HasPrefix(lit, `r"`) && strings.HasSuffix(lit, `"`):
		return strings.TrimSuffix(strings.TrimPrefix(lit, `r"`), `"`), nil
	case strings.HasPrefix(lit, `"`) && strings.HasSuffix(lit, `"`):
		return strings.TrimSuffix(strings.TrimPrefix(lit, `"`), `"`), nil
	default:
		return "", fmt.Errorf("unrecognized doc_short literal form: %q", lit)
	}
}

func extractAliases(block string) []string {
	m := regexp.MustCompile(`fn aliases\(&self\) -> &'static \[&'static str\] \{\s*&\[(.*?)\]`).FindStringSubmatch(block)
	if m == nil {
		return nil
	}
	var out []string
	for _, s := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, s[1])
	}
	return out
}

func reFirst(s, pattern string) (string, bool) {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1], true
}
