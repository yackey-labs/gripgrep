package parity

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot locates the module root from this test file's own path, so the
// test works regardless of the working directory `go test` is invoked
// from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// this file is internal/parity/parity_test.go
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// TestDocMatchesGenerated is the drift check: it regenerates
// docs/rg-parity.md's GENERATED regions in-memory from the checked-in rg
// inventory (rg-flags.json, embedded) and cmd/gg/flags.go, and fails if
// the result differs from what's actually committed. Needs no rg checkout
// and no rg binary.
func TestDocMatchesGenerated(t *testing.T) {
	root := repoRoot(t)

	inv, err := LoadInventory()
	if err != nil {
		t.Fatalf("LoadInventory: %v", err)
	}
	implemented, notImplemented, err := ParseGGFlags(filepath.Join(root, "cmd", "gg", "flags.go"))
	if err != nil {
		t.Fatalf("ParseGGFlags: %v", err)
	}
	result, err := Generate(inv, implemented, notImplemented)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	docPath := filepath.Join(root, "docs", "rg-parity.md")
	committed, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("reading %s: %v", docPath, err)
	}

	regenerated, err := Splice(string(committed), result)
	if err != nil {
		t.Fatalf("Splice: %v", err)
	}

	if regenerated != string(committed) {
		t.Errorf("docs/rg-parity.md is out of date with cmd/gg/flags.go and internal/parity/rg-flags.json.\n"+
			"Run `make parity-doc` and commit the result.\n"+
			"implemented=%d notImplemented=%d total=%d",
			len(implemented), len(notImplemented), result.Total)
	}
}

// TestGenerateCatchesOverlap is a forced-failure demonstration that
// Generate's overlap assertion actually fires: an rg flag can't be both
// implemented and notImplemented in cmd/gg/flags.go.
func TestGenerateCatchesOverlap(t *testing.T) {
	inv := Inventory{Flags: []RgFlag{{Long: "glob", Category: CategoryFilter, DocShort: "x"}}}
	implemented := []GGFlag{{Long: "glob"}}
	notImplemented := []GGNotImplemented{{Long: "glob", Label: "--glob"}}

	if _, err := Generate(inv, implemented, notImplemented); err == nil {
		t.Fatal("expected an error for an rg flag in both implemented and notImplemented, got nil")
	}
}

// TestGenerateCatchesUnknownGGFlag is a forced-failure demonstration of
// the "gg must have zero flags rg lacks" assertion.
func TestGenerateCatchesUnknownGGFlag(t *testing.T) {
	inv := Inventory{Flags: []RgFlag{{Long: "glob", Category: CategoryFilter, DocShort: "x"}}}
	implemented := []GGFlag{{Long: "glob"}, {Long: "not-a-real-rg-flag"}}

	if _, err := Generate(inv, implemented, nil); err == nil {
		t.Fatal("expected an error for a gg flag rg's inventory doesn't have, got nil")
	}
}
