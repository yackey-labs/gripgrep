# ripgrep performance internals — technical report

> Research by rg-internals agent + three deep-read sub-agents, 2026-07-09.
> All file refs are under the ripgrep checkout at `../ripgrep/`.

## 1. Search strategy selection (mmap vs buffered)

Dispatch is per-file in `crates/core/search.rs` `SearchWorker::search`: stdin/preprocessor/decompress → `search_reader`; everything else → `search_path`, which is the fast path (enables mmap). `Searcher::search_file_maybe_path` (`crates/searcher/src/searcher/mod.rs:678`) then picks:

1. **mmap** if `MmapChoice::open` returns Some (`search_slice` on the mapped bytes).
2. else **multi-line heap read** (only when multiline is on): pre-allocates `file.len()+1` and `read_to_end` (mod.rs:940).
3. else **incremental line buffer** (`ReadByLine`, glue.rs:38).

**mmap policy** — default is `Never` (mmap.rs:21-25). Core opts in via `unsafe MmapChoice::auto()` but only when `low.mmap == Auto && paths.len() <= 10 && all paths are files` (`crates/core/flags/hiargs.rs:234-245`). So directory walks essentially never mmap; mmap is for a handful of explicitly-named files. macOS disables mmap unconditionally (mmap.rs:73). Rationale (mod.rs:476 docs): managing many maps in a big walk is *slower* than plain reads; maps only win on a single already-cached large file.

**Buffer sizing** — `DEFAULT_BUFFER_CAPACITY = 64 KB` (`line_buffer.rs:6`). Transcoding scratch buffer is 8 KB (mod.rs:333). Reads happen in ≤64KB `rdr.read(free_buffer)` chunks (line_buffer.rs:418).

**Line rolling for long lines** (`line_buffer.rs`): the buffer holds `[pos..last_lineterm]` = complete lines only; bytes after the last `\n` are a partial line. `roll()` (line_buffer.rs:480) `copy_within`s the unconsumed tail to offset 0, then `fill()` reads more into the free space. If a single line exceeds the buffer, `ensure_capacity` (line_buffer.rs:499) **doubles** the buffer until the line fits. With `--heap-limit` set, it errors past the cap. `fill()` finds the last terminator with `rfind_byte` (memchr, line_buffer.rs:465).

## 2. Literal optimizations & the fast line-confirm path

**Two line-search paths** in `crates/searcher/src/searcher/core.rs`:
- **Fast** (`match_by_line_fast` core.rs:385 → `find_by_line_fast` core.rs:476): runs the matcher's SIMD-accelerated literal scan over the *whole buffer at once* via `matcher.find_candidate_line`, then `lines::locate` expands the hit byte to its enclosing line boundaries. Avoids iterating line-by-line — jumps straight to candidate bytes.
- **Slow** (`match_by_line_slow` core.rs:330): `LineStep` iterator walks every line, calling `shortest_match` on each. Used for passthru, inverted-with-context edge cases, or when the regex needs line-terminator semantics the fast matcher can't guarantee.

`is_line_by_line_fast` (core.rs:673) gates this: fast path requires the matcher's `non_matching_bytes()` to contain the line terminator (regex provably can't match across `\n`), so a whole-buffer literal scan is sound.

**`find_candidate_line` → `LineMatchKind`** (core.rs:488):
- `Confirmed(i)`: no separate prefilter regex exists — the real regex ran, hit is genuine, report directly.
- `Candidate(i)`: literal prefilter hit; full regex must confirm on just that one line (core.rs:511).

`-F/--fixed-strings` and bare literals bypass parsing entirely: `is_fixed_strings` (config.rs:102) constructs the HIR directly as an alternation of `Hir::literal`, so pure-literal search is memmem/Teddy with no regex engine at all.

### 2a. grep-regex literal extraction detail (`crates/regex/src/literal.rs`)

- **Core idea**: ripgrep searches line-by-line, so it can pluck a literal from *anywhere* in the regex, run a fast vectorized search (memmem/Teddy) for it, find the enclosing line, then run the full regex only on candidate lines. Motivating example: `\s+(Sherlock|[A-Z]atso[a-z]|Moriarty)\s+`.
- **`InnerLiterals::new`** (literal.rs:54-92) declines extraction when: no line terminator configured; `re.is_accelerated()` and no Unicode word boundary (trust the engine's own prefilter); pattern is a pure literal alternation (engine handles it).
- **Extractor thresholds** (literal.rs:140-147): `limit_class=10` (max chars in a class before it becomes "anything"), `limit_repeat=10` (max unrolled repetitions), `limit_literal_len=100` (bytes kept per literal), `limit_total=64` (max literals in a Seq). Concat → cross-product (short-circuits once inexact); alternation → union; repetition tracks exact/inexact (`a*`→`[inexact(a),exact("")]`); classes expanded only if ≤10 chars.
- **Union culling to Teddy width** (literal.rs:398-430): when a union would exceed 64 literals, trim every literal to **first 4 bytes** (Teddy searches literals up to length 4), dedup; only if still over-limit give up.
- **Quality gates**: `is_good`: reject "poisonous" literals; if min literal len ≤1 require ≤3 literals; else require min≥2 && ≤64 literals. `is_really_good`: min≥3 && ≤8 literals (short-circuits concat traversal). `is_poisonous`: empty literal, or a single byte with `rank() >= 250` (very common bytes like space). A static byte-frequency rank table drives this.
- The surviving literal Seq becomes an alternation compiled as a **separate** prefilter regex (`fast_line_regex`) that degenerates to memmem/Teddy.
- **Smart case** (config.rs:84-92): AST walk tracks `any_literal` and `any_uppercase` (literal uppercase only — `\pL` doesn't count); enable case-insensitive iff `any_literal && !any_uppercase`.
- **non_matching_bytes** (non_matching.rs): ByteSet complement of every byte the regex can match — used to prove the regex can't match across `\n` (enables fast path).
- **Word mode**: wraps HIR in half word-boundary looks (one side non-word), not `\b…\b`.
- **Engine config**: `utf8_empty(false)` (byte-oriented), size_limit=100MiB, hybrid (lazy DFA) cache 1000MiB, onepass 10MiB, fully-compiled DFA capped 1MiB/1000 states — memory traded for throughput.

## 3. Parallel directory traversal (`crates/ignore`)

**Work-stealing** via `crossbeam_deque` — no channels, no mutex-guarded queue:

- One `Stack` per worker: thread-local `Deque<Message>` (LIFO) + shared slice of all workers' `Stealer`s (walk.rs:1522-1561).
- **LIFO = depth-first, deliberate**: keeps live paths + gitignore matcher count low; breadth-first on wide trees "is disastrous (e.g. all of crates.io)."
- **Pop/steal** (walk.rs:1570-1587): pop local deque, else steal — iterate other stealers starting at `index+1` wrapping (fairness), `steal_batch_and_pop` (steals a **batch** into local deque, amortizing contention).
- **Unit of work = one directory** (`Work { dent, ignore, root_device }`); non-dir files are visited immediately in `run_one`, never enqueued.
- **Quiescence** (walk.rs:1811-1893): `AtomicUsize active_workers` init to `threads`. Idle worker decrements; if count hits 0, everyone's idle with empty deques → broadcast `Message::Quit` (cascading — each quitting worker re-pushes a Quit). Otherwise sleep 1ms and retry. `AtomicBool quit_now` for immediate abort (`-q`, `-l` early exit).
- **Default threads**: `available_parallelism().min(12)` (walk.rs:1437-1443). Uses scoped threads, no global pool.
- Initial roots distributed round-robin across per-thread deques.

**Matcher stack** (`dir.rs`): `Ignore` = cheap `Arc<IgnoreInner>` handle; nodes form an **immutable singly-linked list, one per directory level**, built at descent (`add_child`). Each node holds five compiled matchers (custom-ignore, `.ignore`, `.gitignore`, `.git/info/exclude`, global). Parsing happens **once per directory**; the Arc node is refcount-cloned into every child Work — a `.gitignore` is never re-parsed per file. Matching walks innermost→outermost, first non-None per matcher kind wins; precedence: custom > .ignore > .gitignore > exclude > global. Parents above the search root are cached in a `RwLock<HashMap<OsString, Weak<IgnoreInner>>>` shared across roots. Git matchers only consulted if a `.git` dir was seen (`require_git` gating).

**gitignore matching** (`gitignore.rs`): all patterns from one file compile into a **single combined `GlobSet`**; `matches_candidate_into` returns all matching glob indices in one pass, then iterate in **reverse** (highest index first) for last-match-wins; `!` whitelist flags checked on the winner. Scratch `Vec<usize>` drawn from a pool. `globset` picks literal/prefix/suffix/regex strategies per glob.

**Fast rejections**: ignored directories pruned before enqueue (whole subtree never descended). Hidden check is basename first-byte == `.` — no stat. On Unix, file type comes from `readdir` (no `stat` per entry). Symlinks not followed by default. `--max-filesize` gates non-dirs. Parallel walker is always **unsorted**; `--sort` implies the serial walker.

## 4. Binary detection & early bailout

NUL byte is the heuristic. Three modes: **Quit(NUL)** — default for walk-discovered files: first NUL → treat as EOF, stop searching. **Convert(NUL)** — for explicitly-named files (never quit early on a named file) and `--binary`. **None** — `--text`.

Where detection runs: **buffered path** — every 64KB chunk scanned for NUL as read (memchr). **mmap path** — only first 64KB scanned up front, plus each matching/context line. `replace_bytes` has a NUL-run fast path (byte-scan consecutive NULs instead of restarting memchr).

## 5. Printer / output buffering & interleaving

- Parallel search: one shared `termcolor::BufferWriter`; each worker owns a private in-memory `Buffer` (growable `Vec<u8>`). Per file: clear buffer → accumulate the whole file's output → `bufwtr.print(buffer)` flushes as **one locked write** to stdout. No line-tearing, lock held only for the bulk copy. Zero contention during formatting.
- `--files` mode: dedicated printer thread fed by mpsc channel instead.
- **Sorted vs unsorted**: default unsorted (completion order). Any `--sort` forces `threads=1`; non-path sort keys collect-then-sort the whole file list (with a stat per file). `--sort path` ascending is free (walker pre-sorts).
- **Color short-circuit**: `ColorChoice::Auto` collapses to `Never` when stdout isn't a TTY; `write_colored_line` then jumps straight to `write_line`, skipping all per-match location work.
- **Fast path** (`standard.rs:928-960`): the per-match vector is only populated when config needs it (color, column, --only-matching, --vimgrep, replace). Plain `path:line:text` keeps it **empty → `sink_fast`**: write prelude, write whole line verbatim, no per-match regex re-scan.
- **Formatting**: custom `DecimalFormatter` (u64→ASCII, util.rs:406-445) bypasses `std::fmt`; many small `write_all`s into the private buffer (no writev needed since flush is bulk). Path bytes converted once per file and amortized across matches; hyperlinks lazy via OnceCell.
- **Count mode** (`-c`): Summary printer; `matched` just increments — no formatting at all. Plain count (no stats, not multiline) doesn't even find individual matches within a line. `-q`/`-l` abort the file after first match (`quit_early`), and `quit_after_match` aborts the entire parallel walk.

## 6. Line counting

Lazy and incremental: `count_lines` (core.rs:661) only counts the slice `[last_line_counted..upto]` when a line actually needs reporting — no-match files pay ~nothing. The count is `memchr_iter(b'\n', slice).count()` — SIMD newline counting. Byte offsets tracked separately so `--byte-offset` is free.

## 7. Other perf-critical tricks

- **memchr everywhere**: line boundaries, NUL scan, buffer roll, context location — no hand-rolled byte loops on hot paths.
- **CRLF**: terminator requirement is `\n` only, so the fast path stays valid; `$` made CRLF-aware in the regex instead.
- **`without_terminator`**: strip trailing terminator before matching so `(?m)^$` doesn't spuriously match past the newline — cheap suffix compare.
- **Adjacent-match coalescing** (multiline): overlapping line matches merged into one sink call.
- **Byte-oriented matching**: `utf8(false)` on the HIR — no UTF-8 validation on haystacks; Unicode classes compiled into byte-level automata.
- **`stop_on_nonmatch`**: for sorted input, bail a file once a non-match follows a match.

## Highest-leverage items for the Go rewrite

1. SIMD memchr/memmem for both line splitting and literal prefilter — the single biggest lever.
2. The extract-inner-literal → scan-whole-buffer → locate-line → confirm pipeline (don't iterate line-by-line for literal-bearing patterns).
3. 64KB rolling read buffer; mmap only for ≤10 explicit files.
4. Work-stealing walker (unit = directory, LIFO/depth-first) with an immutable Arc-linked per-directory gitignore matcher stack; per-worker output buffers flushed as one atomic write per file.
5. NUL-quit binary detection on the buffer.
6. Lazy memchr-based line counting only when a line is actually emitted.
7. Append-based decimal formatting; per-match work elided when output is piped/plain.
