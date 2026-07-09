# gripgrep

`gripgrep` is a ripgrep-class recursive code search tool written in pure
Go. It aims to match or beat `rg` on real-world queries and ships as both
a reusable library (`glob`, `walk`, `match`, `search`, `printer`) and a
CLI, `gg`. See [PLAN.md](PLAN.md) for the full architecture, performance
design decisions, and milestone breakdown.

**Status:** M0 scaffold. Package interfaces are frozen; implementations
are stubs (`// TODO(M1-<pkg>)`) that compile and return zero values or
`ErrNotImplemented`. `cmd/gg` does not search yet.

## Library usage

The core packages compose a search pipeline: `walk` finds files,
`match` compiles a pattern, `search` drives the matcher over each file's
bytes, and a `search.Sink` (e.g. `printer.Standard`) reports results.

```go
package main

import (
	"fmt"
	"os"

	"github.com/yackey-labs/gripgrep/match"
	"github.com/yackey-labs/gripgrep/printer"
	"github.com/yackey-labs/gripgrep/search"
	"github.com/yackey-labs/gripgrep/walk"
)

func main() {
	m, err := match.New(match.Config{
		Patterns: []string{"TODO"},
		CaseMode: match.CaseSmart,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // expected for now: match.ErrNotImplemented
	}

	s := search.New(search.Searcher{
		Matcher:     m,
		LineNumbers: true,
	})
	sink := printer.NewStandard(os.Stdout)

	err = walk.Walk([]string{"."}, walk.Options{}, func(e *walk.Entry) walk.WalkState {
		if e.Type != walk.TypeFile {
			return walk.Continue
		}
		f, ferr := os.Open(e.Path)
		if ferr != nil {
			return walk.Continue
		}
		defer f.Close()
		_ = s.Search(e.Path, f, sink)
		return walk.Continue
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // expected for now: walk.ErrNotImplemented
	}
}
```

This compiles against the frozen M0 interfaces today; once M1 lands real
implementations, the same code performs a real search.

## CLI usage (target, v1)

```
gg [flags] PATTERN [PATH...]
```

See PLAN.md's "v1 CLI scope" for the full flag matrix
(`-F -i -S -w -e`, `--hidden --no-ignore -g -u/-uu/-uuu --max-filesize`,
`-n -c -l -q --color -A/-B/-C -v`, `-j -a --mmap/--no-mmap`).

## Dev workflow

```
make build      # go build -o gg ./cmd/gg
make test       # go test ./...
make vet        # go vet ./...
make bench      # go test -bench=. -benchmem ./...  (per-package Go benchmarks)
make bench-e2e  # hyperfine correctness gate + timing vs rg (needs internal/bench/setup.sh once)
make pgo        # GOAMD64=v3 build using cmd/gg/default.pgo, once one is collected
```

Golden end-to-end tests (`gg` vs `rg` byte-for-byte, over `testdata/corpus`)
live in `e2e_test.go`, gated behind the `e2e` build tag and currently
`t.Skip`'d pending M2:

```
go test -tags e2e -v .
```

## Docs

- [PLAN.md](PLAN.md) — architecture, performance design decisions, milestones
- [docs/research/ripgrep-internals.md](docs/research/ripgrep-internals.md)
- [docs/research/go-performance.md](docs/research/go-performance.md)
- [docs/research/benchmarking.md](docs/research/benchmarking.md)
