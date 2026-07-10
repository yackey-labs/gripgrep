# SDK (library facade) layout plan

Owner: lead. Status: **approved design, execution queued as round #33**
(after the round #31/#32 flag work lands, so the new flags fold into
`Options` in one pass). The facade shipped in v0.2.0 (round #30); this
plan is about making it *incredibly easy to adopt* and *fully
documented*, and about giving `Options` a growth policy so flag rounds
never have to re-debate what belongs in the library.

## Design principles (the part that doesn't change)

1. **Options controls what matches and where we look. Match carries
   the data. Formatting is the caller's job.** CLI flags that select
   or filter (case, word, fixed, globs, ignore rules, max-count,
   max-depth, line-regexp, follow, types) surface as `Options` fields.
   CLI flags that *decorate output* (`-H/-I`, `--heading`, `--color`,
   `--vimgrep`, separators, `--trim`, `--max-columns`) never do —
   their *information content* surfaces as `Match` fields instead
   (e.g. round #34's `--column`/`-b` work should add `Match.Column`
   and `Match.ByteOffset`, additive and backward-compatible, rather
   than any formatting knob).
2. **Zero value = CLI defaults.** Every new `Options` field must mean
   "CLI default" at its zero value. Where the CLI distinguishes
   "explicitly 0" from "unset" (e.g. `-d 0` = roots only), the Options
   field documents that 0 means *unlimited/default* and names the
   workaround; we do not add pointer fields or Set booleans.
3. **Verbs stay flat and few.** `Search`, `SearchStream`,
   `FilesWithMatch`, `CountMatches`, `Files` — package-level function
   with CLI defaults + same-name `Options` method. New verbs need a
   real consumer, not symmetry. (Known deliberate gap: multi-pattern —
   see "Deferred".)
4. **Copy-at-the-boundary stays absolute.** Anything the facade
   returns is an independent copy (memsafety_test.go is the gate).
5. **Additions only.** The facade is on the v0.x release train with
   real external consumers; fields and verbs are added, never renamed
   or repurposed. Anything we'd want to rename instead gets a doc fix.

## Deliverables for round #33 (implementer executes, lead reviews)

### 1. `docs/library.md` — the guide page (new)

Linked from README's "Library usage" section ("full guide →") and from
`doc.go`. Sections, in order:

- **Install & one-liner quickstart** (go get, the four one-line verbs).
- **Choosing a verb** — a 5-row table: I want matches / just paths /
  counts / to stream with early stop / the file list, → verb, → CLI
  equivalent.
- **Options ↔ flags reference** — one table, generated-by-hand but
  checked against options.go in review: field, type, CLI flag, zero
  value meaning. Includes the round #31/#32 additions (below).
- **The Match struct** — anatomy, context (Before/After) semantics
  and the early-stop timing note, 1-based LineNumber.
- **Streaming** — concurrency contract (fn called from multiple
  goroutines; caller synchronizes), early stop semantics (the
  "one more match may arrive" caveat), when to prefer it (big trees,
  first-hit-wins).
- **Error model** — per-file errors are collected and folded into one
  returned error alongside partial results; pattern errors return
  immediately; nothing is silently dropped.
- **Memory safety** — the copy contract, verbatim short version.
- **Performance notes** — you get the CLI's engine: parallel walk,
  intra-file parallel search on big files, no tuning knobs needed;
  `Workers` exists but 0/auto is right.
- **Relationship to the low-level packages** — `glob`/`walk`/`match`/
  `search`/`printer` are public for zero-copy wiring; the facade is
  the supported path; link godoc.
- **Versioning** — semver via autotag; additive facade policy (#5
  above).

### 2. `Options` additions (folding in rounds #31/#32)

| Field | Type | CLI flag | Zero value |
|---|---|---|---|
| `MaxCount` | `int` | `-m` | 0 = unlimited (CLI default). The CLI's `-m 0` ("match nothing") is not expressible; callers wanting that don't call Search. |
| `LineRegexp` | `bool` | `-x` | off |
| `MaxDepth` | `int` | `-d` | 0 = unlimited. The CLI's `-d 0` (roots only) is not expressible; pass explicit file paths instead. |
| `IGlobs` | `[]string` | `--iglob` | nil |
| `GlobCaseInsensitive` | `bool` | `--glob-case-insensitive` | off |

Not surfaced (decoration, per principle 1): `-H/-I`, `--heading`,
`-f` (a CLI input mechanic; library callers hold their patterns as
values already — see Deferred/multi-pattern).

Each new field: doc comment naming the flag it mirrors (existing
style), facade tests vs the CLI's own behavior on testdata/corpus,
and an entry in the docs/library.md reference table.

### 3. pkg.go.dev polish

- `example_test.go`: add `ExampleFilesWithMatch`, `ExampleCountMatches`,
  `ExampleFiles`, `ExampleOptions_Search` variants for Globs and the
  new MaxCount/LineRegexp — all on single files/fixed fixtures so
  outputs stay deterministic (existing pattern).
- `doc.go`: add the "choosing a verb" mini-table (godoc-friendly
  plain text) + a link to docs/library.md.
- README "Library usage": add the link line; no restructure.

## Deferred (decided, not drifting)

- **Multi-pattern verbs** (`-e`×N / `-f` equivalent): the engine
  already ORs `[]string` patterns, so this is cheap *when a consumer
  asks*; the API sketch is a parallel verb set (`o.SearchPatterns(
  patterns []string, paths ...string)`), NOT an `Options.Patterns`
  field (it would fight the pattern positional arg). sprocketv2 needs
  zero of this today; don't build it speculatively.
- **Types** (`-t`): surfaces as `Options.Types/TypesNot []string` when
  round #35 lands the type system; fits principle 1 cleanly.
- **Sorted output option**: the CLI has no stable order and neither do
  we; callers sort. Revisit only if `--sort` ever lands CLI-side.
- **Context/streaming structured events** (begin/end-of-file, binary
  notices): no consumer; the `Match`-only stream stays.

## Review gates (lead, before accepting round #33)

- Every code sample in docs/library.md and README compiles and runs
  verbatim (`go test` the examples; hand-run any bare snippets).
- Options table cross-checked field-by-field against options.go.
- `go doc` output reads clean top-to-bottom; examples show on
  pkg.go.dev preview (`pkgsite` local render if available).
- memsafety/facade tests extended for each new Options field.
- No engine-behavior drift: facade tests diff against the CLI's own
  results on the same corpus.
