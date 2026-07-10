# gripgrep

`gripgrep` (`gg`) is a ripgrep-class recursive code search tool written in
**pure Go** — no cgo, no wasm, no FFI. It exists to answer one question:
can Go compete with Rust's flagship CLI on its home turf?

It ships as both a CLI (`gg`, a drop-in `rg` workalike for the flags it
supports) and a set of reusable library packages (`glob`, `walk`, `match`,
`search`, `printer`) you can embed in your own tools.

**Status: working, correct, and winning most rounds.** gg now beats rg
outright on large single files (both literal and multi-literal queries)
and on pure directory traversal (~2× faster), and has pulled
many-small-files tree search — historically its worst row — to
statistical parity. As of 2026-07-10 it builds and runs on **Linux,
macOS, and Windows** (per-OS build-tagged tty/mmap/rawfile/symlink
layers; everything else was portable Go all along). This is a live
work-in-progress and the numbers below are the real ones.

## Where we stand vs ripgrep

Correctness first: for every flag `gg` ships, output is verified against
real `rg` — a 17-case golden end-to-end suite plus manual full-tree diffs
(literal, `-i`, `-w`, `-c`, `-l`, regex, `-g`, context, binary handling)
on the ripgrep source tree itself, byte-identical after sort-normalization.
Every layer also has a differential oracle: `glob` is fuzzed against real
`git check-ignore`, `walk` diffs against `rg --files`, `match` fuzzes
against Go's stdlib regexp, `search` against a naive oracle.

Speed, measured with hyperfine (warm cache, same box, same corpus — only
the gg:rg ratio is meaningful). Reference conditions for the numbers
below: i7-6820HQ (4C/8T, Skylake), Fedora Linux, otherwise-idle machine
(1-min load < 0.35), hyperfine `--warmup 3 -m 15 -N`, rg 14.1.1, gg
built with `make build` (PGO active), 2026-07-10:

| Benchmark | gg vs rg | Trend |
|---|---|---|
| Linux kernel tree (built, ~104k files), literal, gitignore-aware | **~parity, edge to gg** (four consecutive quiet-box runs with gg's mean in front, 1.03–1.08×, still within σ — latest 514ms gg vs 529ms rg) | was 3.74× originally, 2.48× mid-M3, 1.41× at M3 close; #27 killed the last gitignore regex-fallback evals (341,876 → 54 via path-between + basename-chain fast classes) and the per-file 3-byte BOM probe read; #28 removed the per-file confirm-EOF read too (short read on a regular file already proves EOF) — strace: gg now issues 259k syscalls on this row to rg's 464k, and both its user and system CPU are below rg's |
| Same tree, `--files` (pure walk + gitignore, no search) | **1.98× FASTER** (± 0.32; 93ms vs 184ms) | was 3.07× slower when first measured, ~parity at M3 close; #27's glob fast classes took the remaining regex evals out of ignore matching and the walk now beats rg outright |
| OpenSubtitles corpus (~830MB, 28M lines), literal (`Sherlock Holmes`), default settings | **1.65× FASTER** (110ms vs 181ms) | was 1.61× slower at first measurement; mmap + intra-file parallelism (rg searches one file on one core — a lever rg doesn't have) — the first row gg won outright |
| Same file, `Sherlock\|Watson` (multi-literal), default settings | **1.10× FASTER** (266ms vs 293ms) | was 3.25× slower; the rare-byte scanner fix took it to 2.34×, intra-file parallelism to ~parity, and PGO tipped it into a win — all without a Teddy port |

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
eliminated) and PGO has landed (M3 #26). With the walk itself now at
parity (`--files` row) and both single-file rows won outright, the
remaining tree-row gap is per-file search overhead (open/read/setup cost
across ~79k small files) — the current optimization frontier. The tree
gap has closed from 3.74× to 1.41× through profile-driven work, and every
number above is reproducible from this repo.

### macOS and Windows (hosted CI runners)

The table above is the authoritative one, measured on a controlled Linux
box. macOS and Windows numbers come from the on-demand benchmark
workflow (`.github/workflows/bench.yml`) instead: each tool on its own
fresh hosted runner, same hyperfine settings, identically-built corpora
(Windows checks out the same frozen kernel fork via sparse-checkout,
minus its 3 reserved-DOS-name files). Hosted hardware varies run to run,
so these are ranges across benchmark runs on 2026-07-10, indicative
rather than authoritative:

| Benchmark | macOS (arm64) | Windows (x64) |
|---|---|---|
| Linux kernel tree, literal, gitignore-aware | 1.6–4× **slower** | **~1.1× faster** |
| Same tree, `--files` (pure walk, no search) | **1.1–1.4× faster** | **~3.4× faster** |
| OpenSubtitles ~830MB, literal | **1.5–2.2× faster** | **~1.9× faster** |
| Same file, `Sherlock\|Watson` | **1.1–1.3× faster** | **~1.5× faster** |

Windows sweeps all four rows. The one honest red is macOS tree search:
the per-file open path's poller-bypass trick (`rawfile_unix.go`) was
profiled and tuned on Linux, and the win doesn't transfer — that row
hasn't had a single optimization pass on macOS yet.

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
