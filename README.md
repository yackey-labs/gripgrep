# gripgrep

`gripgrep` (`gg`) is a ripgrep-class recursive code search tool written in
**pure Go** — no cgo, no wasm, no FFI. It exists to answer one question:
can Go compete with Rust's flagship CLI on its home turf?

It ships as both a CLI (`gg`, a drop-in `rg` workalike for the flags it
supports) and a set of reusable library packages (`glob`, `walk`, `match`,
`search`, `printer`) you can embed in your own tools.

**Status: working, correct, and closing the gap with ripgrep.** Intra-file
parallelism just flipped the flagship single-file benchmark from "slower"
to a genuine win. This is a live work-in-progress and the numbers below
are the real ones.

## Where we stand vs ripgrep

Correctness first: for every flag `gg` ships, output is verified against
real `rg` — a 17-case golden end-to-end suite plus manual full-tree diffs
(literal, `-i`, `-w`, `-c`, `-l`, regex, `-g`, context, binary handling)
on the ripgrep source tree itself, byte-identical after sort-normalization.
Every layer also has a differential oracle: `glob` is fuzzed against real
`git check-ignore`, `walk` diffs against `rg --files`, `match` fuzzes
against Go's stdlib regexp, `search` against a naive oracle.

Speed, measured with hyperfine (warm cache, same box, same corpus — only
the gg:rg ratio is meaningful):

| Benchmark | gg vs rg | Trend |
|---|---|---|
| Linux kernel tree (built, ~104k files), literal, gitignore-aware | **~1.3-1.5× slower** | was 2.48× (3.74× originally); an evaluation-count census on real .gitignore patterns found gitignore glob matching (`glob.Set.Match`) at ~50% of tree CPU, nearly all in a handful of regex patterns (`cscope.*`, `*.o.*`, `#*#`, ...) evaluated on almost every file — added three new O(1)/linear fast classes (prefix, contains, prefix+suffix) that these shapes actually need, cutting glob's cumulative CPU share roughly in half |
| OpenSubtitles corpus (~830MB, 28M lines), literal (`Sherlock Holmes`), default settings | **1.64× FASTER** | was 1.18× slower; intra-file parallelism (rg searches one file on one core — a lever rg doesn't have) — the first row gg wins outright |
| Same file, `Sherlock\|Watson` (multi-literal), default settings | **~parity** (0.92×-1.08× depending on run) | was 3.25× slower; #22's rare-byte-scanner fix (2.34×) plus intra-file parallelism together close nearly all of the remaining gap |

Micro-level, the core engine is already in ripgrep's class: the literal
prefilter scans at **9.8 GB/s** (0 allocs/op), and the searcher's fast
path streams at 4.6 GB/s. mmap (explicitly-named files, matching rg's
own `<=10 paths, all regular files` policy exactly) closed most of the
single-file gap on literal queries, and intra-file parallel search
(splitting a large file into line-aligned chunks searched concurrently,
replayed back in order) turned that into an outright win. Honest caveat:
self-speedup at 4 workers on the benchmark box lands around 1.9-2.3x, not
a naive 4x — isolating mmap from the picture (reading into a plain heap
buffer instead) raises it to ~2.3x, so mmap page-fault handling is a real
but partial contributor; the rest isn't fully isolated yet. v1 of
intra-file parallelism only covers the no-context, non-invert case
(`-A`/`-B`/`-C`/`-v` fall back to the existing serial path); context
support is designed but not yet landed. Walker syscall/scheduling
overhead (blind ignore-file probes, nanosleep-based idle spin) has since
been fixed (M3 #24: real cond-var parking, ~10k blind openat probes
eliminated) and PGO has landed (M3 #26); what's left on the linux-tree
row is a residual couple of genuinely hard-to-fast-class glob patterns
and full Teddy-class SIMD multi-literal matching, which remains the path
to complete parity on the regex row.

The optimization log lives in the commit history (`git log --grep "M3
perf"`); dead ends are documented alongside wins.

## Why this is plausible at all

ripgrep's dominance is ~80% "stay out of the regex engine": extract
required literals from the pattern, scan with SIMD, and only run the
engine on candidate lines. Go's `bytes.IndexByte`/`bytes.Index` are
hand-written AVX2 assembly — the same class of primitive rg's memchr
uses. gripgrep ports ripgrep's whole literal-extraction architecture
(inner-literal trick, byte-rarity ranking, class expansion, quality
gates) on top of them, so the (much slower) Go regexp engine almost
never runs. The architecture notes in
[docs/research/](docs/research/) document exactly which ripgrep
mechanisms were ported and why.

## Install

```
go install github.com/yackey-labs/gripgrep/cmd/gg@latest
```

## CLI usage

```
gg [flags] PATTERN [PATH...]
```

1:1 rg compatibility is the contract for every implemented flag — same
names, same defaults, same output bytes, same exit codes (0 match /
1 no match / 2 error). Currently implemented: `-F -i -s -S -w -e`,
`--hidden --no-ignore -g -u/-uu/-uuu --max-filesize`, `-n/-N -c -l -q
--color -A/-B/-C -v`, `-j -a`, `--files`. rg flags that gg doesn't
implement yet fail loudly with exit 2 — never silently ignored.

## Library usage

```go
package main

import (
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
		panic(err)
	}

	dest := printer.NewDest(os.Stdout)
	sink := printer.NewStandard(dest)
	s := search.New(search.Searcher{Matcher: m, LineNumbers: true})

	_ = walk.Walk([]string{"."}, walk.Options{}, func(e *walk.Entry) walk.WalkState {
		if e.Type != walk.TypeFile {
			return walk.Continue
		}
		f, ferr := os.Open(e.Path)
		if ferr != nil {
			return walk.Continue
		}
		_ = s.Search(e.Path, f, sink)
		f.Close()
		return walk.Continue
	})
}
```

Note: `walk.Walk` calls the visitor from multiple goroutines; for
parallel use, give each worker its own `Searcher` + `Standard` sharing
one `Dest` (see `cmd/gg/wire.go` for the real wiring, including
`sync.Pool` sharing and binary-mode selection).

## Dev workflow

```
make build         # go build -o gg ./cmd/gg -- portable baseline, still gets
                   # PGO automatically if cmd/gg/default.pgo is present
make build-release # GOAMD64=v3 + PGO -> gg-release, an opt-in release flavor
make test       # go test -race ./...
make cover      # coverage report (floor: 80%/package)
make bench      # per-package Go benchmarks (-benchmem)
make bench-e2e  # hyperfine timing vs rg (run internal/bench/setup.sh once)
make pgo-collect # refresh cmd/gg/default.pgo from a representative query mix
go test -tags e2e .   # golden gg-vs-rg end-to-end suite (needs rg on PATH)
```

`cmd/gg/default.pgo` is checked in (M3 #26): measured +1.2% to +7.7% across
the benchmark mix on the reference box, no regressions. `GOAMD64=v3`
(`make build-release`) was measured as a wash on its own on that box --
the hottest loops (`bytes.IndexByte`/`Index`) are hand-written AVX2
assembly that already dispatches on runtime CPU-feature detection
regardless of the GOAMD64 build level, so v3 mainly affects the smaller
slice of compiler-generated code around them. Shipped anyway since it's
free and the conventional release flavor; `make build` stays at the
portable baseline.

## Docs

- [PLAN.md](PLAN.md) — architecture, binding performance decisions, test mandates, milestones
- [docs/research/ripgrep-internals.md](docs/research/ripgrep-internals.md) — how rg actually gets its speed
- [docs/research/go-performance.md](docs/research/go-performance.md) — the Go-specific playbook
- [docs/research/benchmarking.md](docs/research/benchmarking.md) — methodology, corpora, pitfalls

## Credit

This project would not exist without [ripgrep](https://github.com/BurntSushi/ripgrep)
by Andrew Gallant ([@BurntSushi](https://github.com/BurntSushi)) — both as
the bar to clear and as the reference implementation whose designs,
semantics, and test cases this project ports (see
[LICENSE-THIRD-PARTY](LICENSE-THIRD-PARTY)). If you need the fastest,
most battle-tested grep today, use ripgrep.

## License

MIT — see [LICENSE](LICENSE).
