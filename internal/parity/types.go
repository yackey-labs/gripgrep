// Package parity holds the checked-in rg flag inventory and the logic to
// regenerate docs/rg-parity.md from it plus gg's own flag table
// (cmd/gg/flags.go). See internal/parity/extract (re-extracts rg-flags.json
// from an rg checkout) and internal/parity/gen (runs the generator, `make
// parity-doc`). internal/parity/parity_test.go is the drift check: it runs
// the same generation in-memory and fails if the result differs from what's
// committed in docs/rg-parity.md.
package parity

// RgCategory mirrors ripgrep's crates/core/flags/mod.rs Category enum
// (variant names, not its as_str() kebab-case form).
type RgCategory string

const (
	CategoryInput          RgCategory = "Input"
	CategorySearch         RgCategory = "Search"
	CategoryFilter         RgCategory = "Filter"
	CategoryOutput         RgCategory = "Output"
	CategoryOutputModes    RgCategory = "OutputModes"
	CategoryLogging        RgCategory = "Logging"
	CategoryOtherBehaviors RgCategory = "OtherBehaviors"
)

// categoryTitles gives the doc heading text for each category, in the
// display order used by docs/rg-parity.md's "Flag-by-flag" section.
var categoryOrder = []RgCategory{
	CategoryInput,
	CategorySearch,
	CategoryFilter,
	CategoryOutput,
	CategoryOutputModes,
	CategoryLogging,
	CategoryOtherBehaviors,
}

var categoryTitles = map[RgCategory]string{
	CategoryInput:          "Input",
	CategorySearch:         "Search",
	CategoryFilter:         "Filtering",
	CategoryOutput:         "Output",
	CategoryOutputModes:    "Output modes",
	CategoryLogging:        "Logging",
	CategoryOtherBehaviors: "Other behaviors",
}

// RgFlag is one logical rg flag (one `impl Flag for X` block in defs.rs):
// its long name, optional short byte, optional negated long spelling,
// alias long spellings, doc category, and one-line doc summary
// (doc_short()).
type RgFlag struct {
	Long     string     `json:"long"`
	Short    string     `json:"short,omitempty"` // single-char string, "" = no short form
	Negated  string     `json:"negated,omitempty"`
	Aliases  []string   `json:"aliases,omitempty"`
	Category RgCategory `json:"category"`
	DocShort string     `json:"docShort"`
}

// Pin identifies the exact rg source snapshot rg-flags.json was extracted
// from, echoed into docs/rg-parity.md's "What is being compared" table.
type Pin struct {
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Version string `json:"version"`
}

// Inventory is the full checked-in rg-flags.json payload.
type Inventory struct {
	Pin   Pin      `json:"pin"`
	Flags []RgFlag `json:"flags"`
}
