// Command gen regenerates docs/rg-parity.md's GENERATED REGIONS (the
// flag-by-flag category tables and the "Score: N of M" line) from the
// checked-in rg flag inventory (internal/parity/rg-flags.json) and gg's
// own flag tables (cmd/gg/flags.go). It needs no rg checkout and no rg
// binary. Run via `make parity-doc` or `go run ./internal/parity/gen`.
package main

import (
	"fmt"
	"os"

	"github.com/yackey-labs/gripgrep/internal/parity"
)

const (
	flagsGoPath = "cmd/gg/flags.go"
	docPath     = "docs/rg-parity.md"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run() error {
	inv, err := parity.LoadInventory()
	if err != nil {
		return err
	}
	implemented, notImplemented, err := parity.ParseGGFlags(flagsGoPath)
	if err != nil {
		return err
	}
	result, err := parity.Generate(inv, implemented, notImplemented)
	if err != nil {
		return err
	}

	doc, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	updated, err := parity.Splice(string(doc), result)
	if err != nil {
		return fmt.Errorf("%s: %w", docPath, err)
	}
	if err := os.WriteFile(docPath, []byte(updated), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "regenerated %s: %d of %d rg flags implemented\n", docPath, result.Implemented, result.Total)
	return nil
}
