# gripgrep — a Go rewrite of ripgrep

**Goal:** a ripgrep-class recursive code search tool in pure Go that matches or beats ripgrep on real-world queries, delivered as **both a reusable library and a CLI** (`gg`).

Research inputs (read these before implementing):
- [docs/research/ripgrep-internals.md](docs/research/ripgrep-internals.md) — how rg actually gets its speed, with file refs into `../ripgrep`
- [docs/research/go-performance.md](docs/research/go-performance.md) — Go-specific techniques and library landscape
- [docs/research/benchmarking.md](docs/research/benchmarking.md) — corpora, query set, pitfalls, targets

## Strategy (the three insights that matter)

1. **Stay out of the regex engine.** ~80% of rg's win is literal prefiltering: extract required literals (prefix/inner/suffix) from the pattern, scan the whole buffer with SIMD substring search (`bytes.Index`/`bytes.IndexByte` — AVX2 assembly in Go's stdlib), locate the enclosing line, and only then run the full regex on that single line. Prior Go tools (sift, pt) lost by 50x because they ran stdlib `regexp` over every line.
2. **Do less work.** Parallel gitignore-aware walk prunes whole subtrees before any I/O; NUL-quit binary detection abandons binaries at the first 64KB chunk; line numbers counted lazily (SIMD newline count) only when a match is emitted; count/quiet modes never format lines.
3. **Zero-allocation hot loop + atomic bulk output.** Reused 64KB rolling read buffer per worker; append-based formatting (no `fmt`); per-worker output buffer flushed to stdout as one locked write per file.

## Architecture — library first, CLI on top

Module: `github.com/yackey-labs/gripgrep` (pure Go, no cgo in v1; optional engine build tags later).

```
gripgrep/
├── glob/      # gitignore-style globs → one combined matcher (rg's globset)
├── walk/      # parallel work-stealing walker + ignore matcher stack (rg's ignore)
├── match/     # pattern → Matcher: literal extraction, SIMD prefilter, regexp fallback (rg's grep-regex)
├── search/    # searcher: rolling line buffer, fast/slow line paths, binary detection, context (rg's grep-searcher)
├── printer/   # Standard, Summary (count / files-with-matches), append-based formatting (rg's grep-printer)
├── cmd/gg/    # thin CLI: flag parsing + wiring only
├── internal/bench/  # bench harness scripts (hyperfine), corpus setup
└── docs/research/
```

Every non-`cmd`, non-`internal` package is public, embeddable, documented (godoc), and free of CLI concerns. `cmd/gg` contains no logic beyond flags→config.

### Core interfaces (defined in scaffold, stable across the team)

```go
// match.Matcher — compiled pattern. All methods operate on []byte, no string conversions.
type Matcher interface {
    // FindCandidate scans a whole buffer; returns the offset of a possible match
    // and whether it is Confirmed (real match) or Candidate (prefilter hit; caller
    // must Verify on the enclosing line).
    FindCandidate(buf []byte, start int) (off int, kind CandidateKind, ok bool)
    Verify(line []byte) bool          // full-regex confirm on one line
    Find(line []byte) (s, e int, ok bool) // leftmost match bounds (for color/column)
    NonMatchingLineTerm() bool        // true if pattern provably can't match across '\n'
}

// search.Sink — receives results; printer implements it. Mirrors grep-searcher's Sink.
type Sink interface {
    Matched(m *Match) (more bool, err error)   // Match: line bytes, lineno (lazy), byte offset
    Context(c *Ctx) (more bool, err error)
    Begin(path string) (search bool, err error)
    Finish(path string, stats *Stats) error
}

// walk.Visitor — called per file entry from worker goroutines (must be safe for concurrent use).
type Visitor func(e *Entry) WalkState  // WalkState: Continue | SkipDir | Quit
```

(Exact shapes may be refined in M0; after M0 they are frozen — package agents build against them.)

## Performance design decisions (from research — not optional)

| Area | Decision |
|---|---|
| Regex engine | `github.com/grafana/regexp` (speedup branch) as the *fallback only*; our literal layer runs first. Cache compiled patterns. |
| Literal extraction | Walk `regexp/syntax` AST: prefix/suffix/**inner** literals; thresholds mirroring rg (class ≤10, repeat ≤10, ≤64 literals, trim to 4 bytes for multi-literal); static byte-rarity table; give up gracefully → pure regex path. |
| Single-literal scan | `bytes.Index` on the literal; or `bytes.IndexByte` on its rarest byte + verify. |
| Case-insensitive literal (`-i`, pre-Teddy) | Prefer a **case-invariant rare byte** (digit/punct/`_`) from the literal for `IndexByte`; if none, two `IndexByte` scans (upper + lower of rarest letter), take min — still SIMD. Unicode-affected `-i` → engine path. |
| Multi-literal scan | v1: ≤~8 alternates → rarest-byte memchr + verify table; >~8 → pure-Go Aho-Corasick (`github.com/petar-dambovaliev/aho-corasick`); v2: Teddy port replaces both (the ripgrep-parity piece). |
| I/O | Rolling line buffer per worker (`sync.Pool`), **size is a tunable** — start 64KB (rg's 2016 default), sweep 128/256KB in M3; buffer doubles for oversized lines; `Fadvise(SEQUENTIAL)`; **mmap only for ≤10 explicitly named files** (never during walks), behind a heuristic + flag. |
| Intra-file parallelism | **Beat-rg avenue** — rg searches one file on one core. For regular files >~64MB, split at line boundaries into chunks searched by multiple workers, buffered + emitted in order (trivially parallel for `-c`/`-q`/`-l`). Internal to `search`, no interface change. Built in M3. |
| Walk | Work-stealing: per-worker LIFO deque + batch stealing, unit of work = one directory, files visited inline; quiescence via atomic active-worker count; threads = `min(NumCPU, 12)` initially — **rg's 12-cap is 2016-era; benchmark higher/adaptive caps in M3 (beat-rg avenue)**. If deque complexity stalls progress, fallback: batched channel (16–64 paths/msg) — measure both. Use unsorted `File.ReadDir(-1)` (never `os.ReadDir` — it sorts, pure waste); build child paths by appending into a pooled per-worker `[]byte` (no `filepath.Join` per entry); consider `unix.Openat` per-dir fd for deep trees (measure in M3). |
| Ignore stack | Immutable per-directory node chain (pointer to parent), five matcher slots per node, compiled once per dir, shared by reference to children; single combined GlobSet per ignore file, reverse-order last-match-wins. Hidden check = basename[0]=='.', no stat; file type from ReadDir (no stat on unix). |
| Binary detection | NUL scan (memchr) per 64KB chunk; Quit mode for walk-discovered files, Convert for named files. |
| Line numbers | Lazy: count `\n` via `bytes.Count` only in `[lastCounted..matchStart]` when emitting. `-N` skips entirely. |
| Output | Per-worker `[]byte` buffer, whole-file atomic flush under one mutex; `strconv.AppendUint` for numbers; no `fmt` on hot path; color work fully elided when not a TTY. `--files` mode skips matcher/searcher entirely: walk-only + one dedicated printer goroutine fed by a channel. |
| Alloc discipline | `sync.Pool` buffers; `[]byte` end-to-end (no string conversions — `unsafe.String` only where an API forces it); interface calls stay coarse (per-buffer/per-candidate, never per-byte/per-line); `Match`/`Ctx` structs reused across sink calls (documented as valid only during the call); no `defer` in per-line/per-candidate loops; `-gcflags=-m` audits on hot paths; BCE-friendly loops. |
| Runtime | `debug.SetGCPercent(400)` + `GOMEMLIMIT` backstop set in cmd/gg **before first allocation** (library leaves GC alone). |
| CLI startup | The whole linux-tree target is ~85ms — startup counts. **No cobra/viper**; stdlib `flag` or a minimal hand-rolled parser. Nothing heavy at init. |
| Build | PGO (`default.pgo` from a representative search), `GOAMD64=v3` release builds. |

## v1 CLI scope (correctness gate: output byte-identical to `rg` for these)

`gg [flags] PATTERN [PATH...]`
- Pattern: regex by default, `-F` fixed string, `-i` ignore case, `-S` smart case, `-w` word, `-e` (repeatable)
- Filtering: `.gitignore`/`.ignore` respected by default, `--hidden`, `--no-ignore`, `-g` glob, `-u/-uu/-uuu`, `--max-filesize`
- Output: `-n`/`-N` line numbers, `-c` count, `-l` files-with-matches, `-q` quiet, `--color auto|never|always`, `-A/-B/-C` context, `-v` invert
- Perf: `-j` threads, `-a` text, `--mmap/--no-mmap`
- Not in v1: replace, multiline, PCRE2, encodings other than UTF-8/ASCII, compressed files, `--sort`, JSON output, `-o`

## Milestones & agent team breakdown (Sonnet 5 agents)

**M0 — Scaffold** (1 agent, blocks everything): `git init`; go.mod; package skeletons with the frozen interfaces + doc.go; Makefile (`build`, `test`, `bench`, `pgo`); `internal/bench/setup.sh` + `bench.sh` (hyperfine, 3-query dev loop from benchmarking.md §4); CI-free golden-test harness that diffs `gg` vs `rg` output over `testdata/` — **sort-normalized** (parallel output order is nondeterministic in both tools) with exact exit-code comparison.

**M1 — parallel package agents** (5 agents, independent after M0):
- **glob**: gitignore glob syntax → single combined matcher (literal/prefix/suffix fast classes + regexp fallback per glob), candidate API, pooled scratch. Port rg's globset semantics + tests from `../ripgrep/crates/globset`.
- **walk**: work-stealing parallel walker + ignore matcher stack per design table. Correctness oracle: `rg --files` diff on real repos (incl. this workspace) and the ripgrep test corpus.
- **match**: literal extraction from `regexp/syntax`, rarity-ranked prefilters, smart case, word wrapping, `-F`/multi-pattern alternation, grafana/regexp fallback; implements `match.Matcher`.
- **search**: rolling line buffer (fill/roll/ensure-capacity), fast whole-buffer candidate path + slow per-line path, binary detection modes, lazy line counting, context tracking, invert; drives `Sink`.
- **printer**: Standard + Summary sinks, per-worker buffers, append formatting, TTY/color elision, atomic flush protocol.

Each package agent must: read the three research docs + relevant rg crate source; write table-driven unit tests (port rg's test cases where feasible); keep hot paths allocation-free (verify with `go test -bench . -benchmem`); document the public API.

**M2 — Integration** (1 agent, after M1): `cmd/gg` flags→config wiring (stdlib `flag`/hand-rolled — no cobra, per CLI-startup row); end-to-end golden tests vs `rg` (sort-normalized output, exact exit codes) across the v1 flag matrix; fix cross-package seams.

**M3 — Bench & optimize loop** (1–2 agents, iterative): run `internal/bench/bench.sh` (corpus in `/dev/shm`), correctness-gate first (`diff` vs rg), then profile (`pprof`) → fix → re-measure. Targets, on the same machine, warm cache: **within 1.25× of rg** on linux-tree literal + subtitles literal; then parity; then win. Explicit optimization queue, in order of expected payoff: (1) profile-driven allocation/hotspot fixes; (2) **intra-file parallelism** (beat rg on the 1GB single-file benchmarks); (3) walker thread cap sweep (rg's 12-cap is stale); (4) buffer size sweep 64/128/256KB; (5) `unix.Openat` per-dir fd; (6) PGO + `GOAMD64=v3`; (7) Teddy port for multi-literal parity.

**Definition of done (v1):** byte-identical output to rg on the golden matrix; ≤1.25× rg wall-clock on the 3-query dev loop; clean `go vet`; public packages documented; library usable without the CLI (example in README).
