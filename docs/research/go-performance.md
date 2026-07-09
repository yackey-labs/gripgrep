# Go Performance Techniques for a ripgrep-class Search Tool (gripgrep)

> Research by go-perf agent, 2026-07-09.

Bottom line up front: **the regex engine is the whole ballgame.** ripgrep's speed is ~80% "stay out of the regex engine" (SIMD literal prefilters) and ~20% good I/O + parallelism + output hygiene. Go's stdlib regexp is the single biggest liability. A pure-Go gripgrep can get *close* to ripgrep if you build your own SIMD literal-prefilter layer on top of `bytes.Index`/`bytes.IndexByte` and only fall through to `regexp` on rare candidate windows. To actually *match/beat* ripgrep on regex-heavy patterns you almost certainly need a non-stdlib engine (cgo `rure`, or `go-re2`).

---

## 1. The regexp problem (the decisive issue)

Go's `regexp` is linear-time (Thompson NFA/onepass, RE2-lineage) but has **poor constant factors** and — critically — **no literal-optimization / SIMD prefilter layer** and weak Unicode handling. This is exactly why the earlier Go tools lost (see §8).

Current landscape (Go 1.26 era, mid-2026):

- **stdlib `regexp`**: No dedicated perf overhaul landed in 1.24/1.25/1.26. 1.25 only expanded `\p{name}` support in `regexp/syntax`. General runtime wins (stack-allocated slice backing stores in 1.26; Green Tea GC default in 1.26) help marginally but don't touch the core matcher. **Assume stdlib matching stays slow.**
- **`github.com/grafana/regexp`**: Drop-in fork, **all optimizations upstreamed but unmerged** (golang/go#26623), linear-time preserved, passes upstream tests. Use the **`speedup` branch** (main is unoptimized). Reported **5x–300x** on filter-style workloads (the Loki simplification: byte-comparison instead of full match). Free, zero-risk win as your stdlib replacement — but it does *not* add a real literal/SIMD prefilter engine, so it's incremental, not transformative.
- **`github.com/wasilibs/go-re2`**: RE2 compiled to WASM (runs on wazero, **no cgo**) with an optional cgo mode (`-tags re2_cgo`, needs libre2). Drop-in API. Concrete benches from its README:
  - Compile is **2–5x slower** than stdlib (onepass 4µs→24µs wasm/7.8µs cgo; hard 66µs→272µs wasm/94µs cgo) → cache compiled patterns.
  - Match, easy pattern / 1MB: stdlib **240µs** vs wasm **43µs** vs cgo **155ns**.
  - Match, hard alternation (Hard1) / 1MB: stdlib **214ms** vs wasm **6.3ms** vs cgo **1.9ms**.
  - **Crossover ~1KB**: below that, stdlib wins (WASM/FFI per-call overhead dominates); above it, re2 pulls far ahead. cgo ≈ 30% faster than WASM.
- **`github.com/BurntSushi/rure-go`**: cgo bindings to **Rust's regex crate — literally ripgrep's engine** (finite automata + SIMD + literal opts). Same algorithmic guarantees. Caveat: **cgo call overhead sets a ceiling** — you need biggish haystacks and must iterate matches inside C (`FindAll` on ~0.5MB) to amortize. Best raw ceiling of any option, worst deployment story (cgo + Rust build).
- **`coregex` / golang/go#76818**: A pure-Go engine (lazy DFA + caching, AVX2/SSSE3 prefilters, Teddy, reverse-suffix/inner literal search) claiming **3–192x vs stdlib**. Proposal to upstream was **closed "not planned"** — stays external, v0.8.x, only battle-tested via GoAWK. Interesting as a *reference implementation to steal ideas from*, risky as a dependency.

**Recommendation:** Ship pure-Go with **grafana/regexp (speedup branch)** as the fallback matcher, and **build your own literal-prefilter layer** (§2) so the engine rarely runs. Offer an **optional cgo/wasm fast path**: a `-tags rure` build using `rure-go` for regex-heavy workloads (best ceiling), with `go-re2` (cgo) as the no-Rust-toolchain alternative. Cache all compiled patterns.

## 2. SIMD literal prefiltering — where you actually win in pure Go

This is the highest-leverage pure-Go work. Go's `bytes` primitives are hand-written AVX2 assembly and extremely fast:
- `bytes.IndexByte` / `bytes.Index` / `bytes.Count`: AVX2, ~memory-bandwidth-bound (tens of GB/s). `bytes.Index` uses a tuned substring search (Rabin-Karp fallback, but SIMD for short needles).

Strategy (mirrors ripgrep's core trick):
1. **Extract literals** from the pattern (prefix/suffix/**inner**). E.g. `\w+foo\d+` → search for `foo`. Go's `regexp/syntax` gives you the parsed AST — walk it to pull required literal substrings. This is the piece stdlib *doesn't* do for you and you must build.
2. **Rare-byte `memchr`**: don't scan for the first byte — pick the **statistically rarest** byte in the literal and `bytes.IndexByte` on it (ripgrep beat sift **0.325s vs 16.4s** on Russian text purely from smarter byte choice). Keep a static byte-frequency table.
3. On a candidate hit, run the **full regex only on that window/line**. If the pattern is a plain literal, you skip the engine entirely.
4. **Multiple literals** (alternations, case-insensitive expansions): implement a **Teddy-style SIMD multi-substring matcher** (16 bytes/iter, ripgrep generates ~24 prefix alternates for case-insensitive `Sherlock Holmes`, ~10x over grep). Teddy is the single most valuable algorithm to port; there's no pure-Go library, so this is real work but it's what separates "fast" from "ripgrep-fast." Alternatively `ahocorasick`-style for many literals, but Teddy wins for small sets.

Net: with a good literal layer, matcher choice matters much less because the engine runs on <1% of bytes.

## 3. I/O

- **mmap in Go is a trap for the general case.** `syscall.Mmap` / `golang.org/x/exp/mmap` cause GC/scheduler stalls (the runtime can't preempt during page faults) and a big grep **evicts the page cache** hurting the whole system (valyala, "mmap in Go considered harmful"). ripgrep confirms empirically: **mmap ~25% faster for one big file**, but **buffered `read` is ~4.8x faster across a repo of many small files** (per-map setup/teardown cost compounds).
- **Recommendation:** default to **large buffered reads** (`Read` into a reused 64–256KB buffer; handle line-straddling-buffer + oversized-line edge cases yourself). Use **mmap only when searching a single large file** (opt-in heuristic on file size + file count), and gate it behind a flag since it can regress in low-memory/networked-FS environments.
- **Readahead:** `unix.Fadvise(fd, 0, 0, FADV_SEQUENTIAL)` (and `FADV_WILLNEED`) via `golang.org/x/sys/unix` gives a cheap sequential-scan boost.
- **io_uring:** Not in stdlib (proposal golang/go#57701 open, no landing). Third-party (`iceber/iouring-go`, `godzie44/go-uring`) exist but are immature and fight Go's runtime/netpoller. **Skip for v1** — buffered reads + a worker pool saturate disk/CPU already; io_uring's win is deep queue depth on many-file random I/O, marginal for sequential scans. Revisit only if profiling shows I/O-wait dominating.

## 4. Allocation control

- **`sync.Pool`** for per-file read buffers and per-worker output buffers — the biggest single allocation win; reuse across files.
- **Avoid `[]byte`↔`string` copies**: use `unsafe.String(&b[0], len(b))` / `unsafe.Slice` on hot paths (matching, output) where you control the buffer lifetime. `regexp` has `*Bytes` method variants (`FindIndex`, `Match`) — use them, never convert to `string`.
- **Escape-analysis discipline**: keep match/scan hot loops free of interface boxing and closures that capture loop-locals; pass `[]byte` not `io.Reader` into the inner loop; `go build -gcflags=-m` to verify no surprise heap escapes.
- **GC tuning for a short-lived CLI**: this is a batch process that exits — you *want* to trade memory for fewer collections. Set **`GOGC=400`–`800`** or `debug.SetGCPercent`, and/or a `GOMEMLIMIT` ceiling as a safety valve. Go 1.26's **Green Tea GC (now default)** lowers collection overhead for free. For a pure throughput CLI some tools even set `GOGC=off` with a `GOMEMLIMIT` backstop.
- **Arenas: dead end.** `GOEXPERIMENT=arenas` is **on hold indefinitely, may be removed**, not production-safe. Don't design around it. `sync.Pool` + preallocation is the supported path.

## 5. Parallelism

- **Worker pool, not goroutine-per-file.** A bounded pool sized to `GOMAXPROCS` (default = NumCPU; leave it) avoids scheduler thrash and unbounded memory when a dir has 100k files.
- **Directory traversal**: ripgrep uses a **Chase-Lev lock-free work-stealing deque**. In Go, the pragmatic equivalent is a shared **buffered channel of work items** fed by parallel directory walkers (the `ignore`/gitignore walk itself parallelizes and is often the bottleneck on huge trees). A true work-stealing deque (per-worker local deque + steal) reduces contention further if the channel becomes hot — measure first.
- **Channel overhead**: per-item channel send/recv is ~50–100ns; on millions of tiny files that adds up. **Batch** — send slices of paths (e.g. 16–64 per message), or hand each worker a subtree. This is the difference between the channel being negligible vs. a top profile entry.
- Respect `.gitignore` during the walk (ripgrep parses 178 ignore files in the Linux kernel tree) — filtering *before* opening files is a huge win (binary/hidden/ignored files never get read).

## 6. Output

- **Never use `fmt` on the hot path** — reflection + allocation. Build lines with **`append`** into a `[]byte` (`strconv.AppendInt` for line numbers, `append` for path/text).
- **Per-worker output buffer**, flushed to a single shared `*bufio.Writer` (or raw `os.Stdout`) **under one mutex** in coarse chunks — this preserves per-file output atomicity and avoids interleaving without serializing the search.
- Large stdout buffer (`bufio.NewWriterSize`, 64–256KB); one `Flush` at end (or per-file). When piped (not a TTY), buffer aggressively; ripgrep detects TTY to switch line-buffering/coloring.
- Detect TTY to skip ANSI color assembly when redirected.

## 7. Build-level

- **PGO**: real and free — **2–14%** typical (Datadog ~up to 14% CPU; commonly 5–10%). Add a hidden `-cpuprofile` flag, collect a profile over a representative search, commit `default.pgo`, `go build` auto-uses it. Worthwhile for a shipped binary; inlines your hot match/scan paths.
- **`GOAMD64=v3`** (AVX2 baseline, ~2013+ CPUs) — lets the compiler use AVX2 broadly; ship a v3 binary + a v1 fallback. `bytes.*` assembly already uses AVX2 at runtime regardless, but v3 helps *your* scalar loops autovectorize.
- **Bounds-check elimination**: hoist `_ = b[len-1]` style hints, slice once (`b = b[:n]`) before tight loops, index with a variable the compiler can prove in-range. Verify with `go build -gcflags=-d=ssa/check_bce/debug=1`. In byte-scan inner loops this is 10–20%.
- `-ldflags="-s -w"` for size only (no perf); `-gcflags=-B` disables bounds checks globally — **don't** (unsafe), do targeted BCE instead.

## 8. Prior art — why Go greps lost, what to steal

- **sift / the platinum searcher (pt) / ag**: all rode **Go's stdlib regexp with no literal/SIMD prefilter and no Unicode-aware DFA**. On the Russian UTF-8 literal benchmark ripgrep was **50x faster than sift** (0.325s vs 16.4s) — almost entirely the missing rare-byte `memchr` + literal layer, not I/O. They *did* parallelize traversal + respect gitignore, so those aren't differentiators. **Lesson: the win is the matcher's literal/SIMD front-end, which none of them built.**
- **ripgrep's stack to emulate**: literal extraction (prefix/suffix/inner) → rare-byte `memchr` → Teddy multi-pattern SIMD → lazy DFA with UTF-8 baked into the FSM (Unicode `\w` adds ~0.3x overhead vs GNU grep's 5x) → work-stealing parallelism → adaptive mmap/buffered I/O.
- **zoekt** (Google's trigram-indexed code search): different problem (builds a trigram index for *repeated* queries) — **not** applicable to gripgrep's single-shot grep model, but if you ever add a persistent-index mode, its **trigram posting-list** design is the reference. For grep-on-demand, ignore.
- Modern Go text-search worth a look: **`grafana/regexp`** (matcher), **`wasilibs/go-re2`** (engine), and Go's own `bytes`/`internal/bytealg` assembly (read it for how they structure AVX2 scans).

---

## Recommended build

**Pure-Go default (the product):** buffered reads + fadvise; worker pool with batched path channels + parallel gitignore walk; **literal-prefilter layer you build** (literal extraction → rare-byte `bytes.IndexByte` → port Teddy for multi-literal) with **grafana/regexp (speedup branch)** as the fallback matcher on candidate windows only; `append`-based per-worker output flushed under one lock; `GOGC` high + `GOMEMLIMIT` backstop; PGO + `GOAMD64=v3` + targeted BCE. mmap only for single large files, flag-gated.

**Optional faster paths (build tags):** `-tags rure` → `rure-go` (ripgrep's own Rust engine, best ceiling, needs Rust/cgo); `-tags re2_cgo` → `go-re2` cgo (no Rust, needs libre2); default WASM `go-re2` for a cgo-free "fast regex" build. All three only pay off on regex-heavy/large-haystack workloads — with a strong literal layer most real queries never reach the engine anyway.

**Priority order for effort:** (1) literal-prefilter + rare-byte memchr — biggest win, pure Go; (2) Teddy port — the ripgrep-parity piece; (3) buffered-read/worker-pool I/O; (4) output + allocation hygiene; (5) grafana/regexp swap (trivial); (6) PGO/build flags; (7) optional cgo engine paths.

Sources: [ripgrep design (burntsushi)](https://burntsushi.net/ripgrep/) · [go-re2](https://github.com/wasilibs/go-re2) · [grafana/regexp](https://github.com/grafana/regexp) · [rure-go](https://github.com/BurntSushi/rure-go) · [coregex proposal #76818](https://github.com/golang/go/issues/76818) · [regexp perf issue #26623](https://github.com/golang/go/issues/26623) · [Go PGO / Datadog 14%](https://www.datadoghq.com/blog/datadog-pgo-go/) · [mmap in Go considered harmful](https://valyala.medium.com/mmap-in-go-considered-harmful-d92a25cb161d) · [io_uring proposal #57701](https://github.com/golang/go/issues/57701) · [arenas proposal #51317](https://github.com/golang/go/issues/51317) · [Go 1.26 release notes](https://go.dev/doc/go1.26)
