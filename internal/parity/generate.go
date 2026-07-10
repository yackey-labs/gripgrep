package parity

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed rg-flags.json
var rgFlagsJSON []byte

// LoadInventory decodes the checked-in rg flag inventory (rg-flags.json),
// extracted once from an rg checkout by internal/parity/extract. Every
// downstream consumer (the generator, the drift test) uses this -- none of
// them need an rg checkout.
func LoadInventory() (Inventory, error) {
	var inv Inventory
	if err := json.Unmarshal(rgFlagsJSON, &inv); err != nil {
		return Inventory{}, fmt.Errorf("parsing embedded rg-flags.json: %w", err)
	}
	return inv, nil
}

// status is a flag's parity state, matching docs/rg-parity.md's legend.
type status int

const (
	statusImplemented status = iota // ✅
	statusRejected                  // ⚠️ recognized, rejected with a targeted error
	statusUnknown                   // ❌ not implemented, generic unknown-flag error
)

func (s status) symbol() string {
	switch s {
	case statusImplemented:
		return "✅"
	case statusRejected:
		return "⚠️"
	default:
		return "❌"
	}
}

// Result is the output of Generate: the regenerated markdown for each
// GENERATED region, plus the score, ready to splice into docs/rg-parity.md
// between its marker comments.
type Result struct {
	TablesMarkdown string
	ScoreLine      string
	Implemented    int
	Total          int
}

// Generate builds the parity doc's generated regions from the rg
// inventory and gg's own flag tables, and performs the mandatory
// consistency assertions (fail loudly, per the design):
//   - no overlap between gg's implemented and notImplemented lists
//   - every gg flag (implemented or notImplemented) exists in the rg
//     inventory -- gg must have ZERO flags rg lacks
//   - the score numerator equals the number of ✅ rows emitted, and the
//     denominator equals the inventory size
func Generate(inv Inventory, implemented []GGFlag, notImplemented []GGNotImplemented) (Result, error) {
	rgLongs := make(map[string]RgFlag, len(inv.Flags))
	for _, f := range inv.Flags {
		rgLongs[f.Long] = f
	}

	implByLong := make(map[string]GGFlag, len(implemented))
	for _, f := range implemented {
		if _, dup := implByLong[f.Long]; dup {
			return Result{}, fmt.Errorf("cmd/gg/flags.go: duplicate implemented flag --%s", f.Long)
		}
		implByLong[f.Long] = f
		if _, ok := rgLongs[f.Long]; !ok {
			return Result{}, fmt.Errorf("cmd/gg/flags.go implements --%s, which rg's inventory (rg-flags.json) has no record of -- gg must have zero flags rg lacks", f.Long)
		}
	}

	notImplByLong := make(map[string]GGNotImplemented, len(notImplemented))
	for _, f := range notImplemented {
		if _, dup := notImplByLong[f.Long]; dup {
			return Result{}, fmt.Errorf("cmd/gg/flags.go: duplicate notImplementedFlags entry --%s", f.Long)
		}
		notImplByLong[f.Long] = f
		if _, ok := rgLongs[f.Long]; !ok {
			return Result{}, fmt.Errorf("cmd/gg/flags.go's notImplementedFlags claims --%s, which rg's inventory (rg-flags.json) has no record of", f.Long)
		}
		if _, overlap := implByLong[f.Long]; overlap {
			return Result{}, fmt.Errorf("cmd/gg/flags.go: --%s is in both v1Flags (implemented) and notImplementedFlags -- these must not overlap", f.Long)
		}
	}

	byCategory := make(map[RgCategory][]RgFlag, len(categoryOrder))
	for _, f := range inv.Flags {
		byCategory[f.Category] = append(byCategory[f.Category], f)
	}

	var b strings.Builder
	checkCount := 0
	for _, cat := range categoryOrder {
		flags := byCategory[cat]
		sort.Slice(flags, func(i, j int) bool { return flags[i].Long < flags[j].Long })

		fmt.Fprintf(&b, "### %s\n\n", categoryTitles[cat])
		b.WriteString("| Flag | Short | gg | Summary |\n|---|---|---|---|\n")
		for _, f := range flags {
			st := statusUnknown
			switch {
			case containsImpl(implByLong, f.Long):
				st = statusImplemented
			case containsNotImpl(notImplByLong, f.Long):
				st = statusRejected
			}
			if st == statusImplemented {
				checkCount++
			}

			flagCell := "`--" + f.Long + "`"
			if f.Negated != "" {
				flagCell += " (+`--" + f.Negated + "`)"
			}
			shortCell := ""
			if f.Short != "" {
				shortCell = "`-" + f.Short + "`"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", flagCell, shortCell, st.symbol(), f.DocShort)
		}
		b.WriteString("\n")
	}
	tables := strings.TrimRight(b.String(), "\n")

	total := len(inv.Flags)
	if checkCount != len(implByLong) {
		return Result{}, fmt.Errorf("internal inconsistency: %d ✅ rows emitted but %d flags marked implemented", checkCount, len(implByLong))
	}
	scoreLine := fmt.Sprintf("**Score: %d of %d rg flags implemented.**", checkCount, total)

	return Result{
		TablesMarkdown: tables,
		ScoreLine:      scoreLine,
		Implemented:    checkCount,
		Total:          total,
	}, nil
}

func containsImpl(m map[string]GGFlag, long string) bool {
	_, ok := m[long]
	return ok
}

func containsNotImpl(m map[string]GGNotImplemented, long string) bool {
	_, ok := m[long]
	return ok
}
