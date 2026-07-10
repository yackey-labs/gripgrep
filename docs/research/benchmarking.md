# Benchmarking gripgrep against ripgrep — methodology guide

> Research by bench-research agent, 2026-07-09.
> Sources: ripgrep `benchsuite/benchsuite` (canonical runner), `benchsuite/runs/2022-12-16-archlinux-duff/` (rg 13.0.0 on i9-12900K), and https://burntsushi.net/ripgrep/.

## 1. Canonical corpora and query set

**Two deliberately opposite corpora** — "many small files" vs "a few large files." They stress different paths (traversal + parallelism + gitignore vs raw single-stream scan throughput).

**Corpus A — Linux kernel tree** (many small files):
- Shallow clone of `https://github.com/BurntSushi/linux` — a *frozen fork* so the corpus never changes (blog commit `d0acc7`).
- Critically, the tree is **built** (`make defconfig && make -j$(nproc)`) so it's full of build artifacts (`.o`, `vmlinux`). This makes ripgrep's `.gitignore`/binary filtering matter — an unfair-by-design edge over plain `grep -r`.

**Corpus B — OpenSubtitles2016 monolingual** (single huge files):
- English: `https://object.pouta.csc.fi/OPUS-OpenSubtitles/v2016/mono/en.txt.gz`, decompressed then **sampled to first 55,000,000 lines** (`en.sample.txt`, ~1 GB) to match the Russian size.
- Russian: `https://object.pouta.csc.fi/OPUS-OpenSubtitles/v2016/mono/ru.txt.gz` → `ru.txt` (~1.6 GB). Russian = UTF-8/Unicode stress case; English = ASCII case.

**Query set** (each has EN + RU, often ASCII vs Unicode variants):

| Category | Pattern | Isolates |
|---|---|---|
| Literal (default) | `PM_RESUME` | default incl. smart filtering (unfair on purpose) |
| Literal (fair) | `PM_RESUME`, `-n` forced everywhere | pure literal scan |
| Literal case-insensitive | `PM_RESUME -i` / `Sherlock Holmes -i` | case folding |
| Regex w/ literal suffix | `[A-Z]+_RESUME` | literal extraction from regex |
| Whole-word | `-w PM_RESUME` | word-boundary handling |
| Alternation of literals | `ERR_SYS\|PME_TURN_OFF\|LINK_REQ_RST\|CFG_BME_EVT` | multi-literal (Aho-Corasick / Teddy) |
| Unicode category | `\p{Greek}` | Unicode class support |
| Unicode `\w` | `\wAh` | Unicode-aware word char |
| No-literal regex | `\w{5}\s+\w{5}\s+\w{5}\s+\w{5}\s+\w{5}` | pure DFA path, no literal shortcut |
| Surrounding words | `\w+\s+Holmes\s+\w+` | inner-literal + regex around it |

## 2. How benchsuite measures

- **Warm cache always.** Commands are warmed so the corpus is in the OS page cache — it measures CPU/algorithm, not disk. The 2022 run put the corpus on **`--dir /dev/shm`** (tmpfs) to remove disk I/O entirely.
- **Samples:** script default `warmup_count=1, count=3` (mean ± stdev). Blog's original EC2 run used 3 warmup / 10 samples. Use ≥3, report stdev.
- **No hyperfine** — it's a plain Python `time.time()` around `subprocess.run`. **For your own harness, use hyperfine instead** (outlier detection, warmup, shell-spawn accounting) — it's the ecosystem standard now.
- **Output discarded** — stdout → `/dev/null`, except when counting result lines (piped + counted). stderr → `/dev/null`.
- **Line-number fairness:** `-n` is forced on every tool so nobody is penalized/advantaged.
- **Locale set explicitly per tool:** `LC_ALL=C` for ASCII grep, `LC_ALL=en_US.UTF-8` for Unicode grep. Biggest confound for grep.

**Flags for apples-to-apples:** force `-n` identically; force consistent binary handling (`grep -I`, `-a` on the Russian file which some tools misdetect as binary); **mmap on/off is a first-class variable** (rg is benched `--mmap` and `--no-mmap`); benchsuite does NOT pass `-j`, so Corpus A runs multi-core (multi-file parallelism) and Corpus B is single-core (rg 13 doesn't parallelize within one file) — pin `-j1` to isolate single-thread scan speed. For a like-for-like literal scan vs grep, disable smart filtering (`rg --no-ignore -uuu`) so rg searches the same bytes.

## 3. Pitfalls

1. **Cold vs warm cache** — cold runs measure disk and dwarf everything else. Warm up or use tmpfs.
2. **Locale** — GNU grep on the Russian casei query: **4.767 s** with `LC_ALL=en_US.UTF-8` vs **0.506 s** with `LC_ALL=C`. Control it for every command.
3. **stdout destination** — pipe-to-counter vs `/dev/null` vs TTY all differ wildly. Redirect to `/dev/null` consistently; never bench into a terminal.
4. **CPU freq scaling / turbo / thermal** — set governor to `performance`; 0.08 s runs are very sensitive.
5. **Output ordering cost** — ripgrep buffers and sorts per-file output when parallel. A tool that just races threads to stdout looks faster but is doing different (incorrect) work. Compare only tools with equivalent deterministic output.
6. **Result-count divergence is the correctness gate** — benchsuite prints `lines: N` per tool; always assert gg and rg return the same count before trusting any timing.
7. **Binary/UTF-8 detection** differences silently change how many bytes get scanned.

## 4. Minimal local harness (<2 min dev loop)

Setup (one-time):
```bash
#!/usr/bin/env bash
set -euo pipefail
DIR=/dev/shm/gg-bench; mkdir -p "$DIR"; cd "$DIR"
[ -d linux ] || git clone --depth 1 https://github.com/BurntSushi/linux
[ -f en.txt ] || { curl -LO https://object.pouta.csc.fi/OPUS-OpenSubtitles/v2016/mono/en.txt.gz && gunzip en.txt.gz; }
```
Run (same hyperfine call per tool so warmup/stats match):
```bash
cd /dev/shm/gg-bench
hyperfine -w 3 -r 10 'rg -n PM_RESUME linux' 'gg -n PM_RESUME linux'          # many small files, multi-thread
hyperfine -w 3 -r 10 'rg -n "Sherlock Holmes" en.txt' 'gg -n "Sherlock Holmes" en.txt'  # single large file
LC_ALL=C hyperfine -w 3 -r 10 'rg -n "[A-Z]+_RESUME" linux' 'gg -n "[A-Z]+_RESUME" linux'  # regex+literal
```
Keep the loop to ~3 queries (literal-tree, literal-bigfile, regex-tree); run the full 20-query matrix only pre-release. Always `diff <(rg ...) <(gg ...)` or compare `wc -l` first as a correctness gate.

## 5. Realistic targets (the bar)

From the 2022 reference run (i9-12900K, 128 GB, rg 13.0.0, corpus in tmpfs):

| Benchmark | ripgrep | Nearest competitor |
|---|---|---|
| `linux_literal` (tree, `-n`) | **0.085 s** | git grep 0.211, ugrep 0.189, GNU grep 0.996 |
| `linux_literal_casei` | **0.088 s** | ugrep 0.174, git grep 0.214 |
| `subtitles_en_literal` (1 GB, no `-n`) | **0.123 s** (mmap) | ugrep 0.185, GNU grep 0.572 |
| same, with `-n` | 0.189 s | ugrep 0.185 (ties rg) |
| `subtitles_ru_literal` (1.6 GB, Unicode) | **0.133 s** (mmap) | GNU grep 0.510, ugrep 0.680 |
| `subtitles_ru_literal_casei` | **0.268 s** | grep(ASCII) 0.506, grep(Unicode) 4.767 |

Absolute seconds are hardware-dependent — only the rg-vs-gg ratio on the same box matters, so always re-baseline rg locally.

**Two load-bearing design insights the numbers reveal:**
1. **mmap is corpus-dependent, not universal.** On the tree, rg default (`read()` into a reusable buffer) is **0.085 s** but `rg --mmap` is **0.322 s** — mmap per tiny file loses to `read()` due to per-file page-fault/syscall overhead. On the single 1 GB file, mmap wins (0.123 vs 0.176). So: read-into-buffer for many small files, mmap for large files, chosen adaptively.
2. **Tree wins come from doing less work** — parallel walk + gitignore/binary filtering (rg scans far fewer bytes than `grep -r`) + SIMD literal prefilter. Single-file wins come from SIMD substring search + lazy line counting (don't count line numbers until a match is found). In Go: lean on `bytes.IndexByte`/`bytes.Index` (already SIMD assembly) for the memchr/substring core, keep allocations near zero in the hot loop, and get parallel traversal + ignore-matching + buffer reuse right. Matching rg on the single-file literal is achievable; matching the 0.085 s tree literal (~40k files walked, filtered, and searched across cores in 85 ms) is the hard bar.

## 6. Parallel-I/O dead ends (round #29, 2026-07-10) — do not re-explore without new evidence

Intra-file parallel search (#18) leaves a measured ~22% gap at `-j4` on
the 830MB tmpfs corpus between the production shared-mmap path (~94.7ms)
and a zero-fault-cost ceiling (heap buffer with the copy excluded from
timing: ~73.4ms). Round #29 tried three mechanisms to capture it; all
were falsified with root causes. Nothing landed. Prototypes were fully
tested/green before rejection.

1. **Per-worker `pread(2)` into pooled buffers** (`SearchBytesFD`
   prototype): the mechanism *worked* — minor faults fell 13,120 → 882
   at `-j4` — and still lost outright (325ms vs 151ms). mmap search is
   one pass over memory; any copy-based approach is two, and a
   standalone diagnostic put the copy alone at a ~200ms floor for 830MB
   on the reference box, **independent of thread count** (1 thread ≈ 4
   threads — likely a VM memory-channel ceiling; the box is qemu). The
   copy floor alone exceeds the entire mmap-parallel search time, so no
   copy-based approach can win here. Corollary: the earlier "2.3×
   self-speedup on a heap buffer" figure never counted the copy cost —
   don't cite it as recoverable headroom.
2. **Per-worker sub-range mmaps** (own VMA per worker): reproducible
   24–34% *loss*. `mmap()` itself takes the process `mmap_lock` in
   write mode to insert each VMA, so concurrent per-worker maps
   serialize on a worse lock than the read-side fault contention they
   were meant to avoid.
3. **Worker-local `madvise`** (`MADV_WILLNEED` and `MADV_POPULATE_READ`
   on each worker's own range): noise centered on zero across three
   runs (−22%…+4%); never approached the +10% land bar. The kernel's
   own fault-around clustering on the shared mapping appears to already
   provide the equivalent batching. (`MAP_POPULATE` at map time was
   separately measured worse back in M3 — it populates serially.)

Also mis-specified once during this round, worth remembering: comparing
*self-speedup ratios* between mmap and heap paths is invalid — mmap's
`-j1` baseline carries the serial fault-in cost, inflating its ratio.
Compare absolute `-jN` wall times under one harness instead.

Untried and deliberately declined: THP/`MAP_HUGETLB`-backed mappings
(would cut fault count ~512×) — shmem THP depends on kernel config
(`shmem_enabled` is commonly `never`), so a win wouldn't generalize to
user machines.
