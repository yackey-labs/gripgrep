# rg ↔ gg parity

The complete inventory of ripgrep's CLI surface and gg's status against
it, generated from rg's own flag definitions (`crates/core/flags/defs.rs`)
cross-referenced with gg's `cmd/gg/flags.go`. The contract (PLAN.md): every
flag gg implements behaves 1:1 with rg — same name, same default, same
output bytes, same exit codes — and every flag gg does not implement fails
loudly with exit 2, never silently ignored. gg's flag surface contains
**zero** flags that rg doesn't have (checked mechanically each time this
document is regenerated).

## What is being compared

| | version | role |
|---|---|---|
| ripgrep flag authority | master `3a570990c4cf` (2026-07-09, version 15.1.0), `crates/core/flags/defs.rs` | source of the flag inventory below |
| ripgrep binary (golden suite + CI benchmarks) | **rg 15.1.0** (single pin: `internal/bench/rg-version.txt`, enforced by the suite itself) | every implemented flag is byte-diff-verified against this binary (17-case e2e suite + full-tree diffs) |
| gripgrep | `4243295` / pending release | the status column below |

<!-- BEGIN GENERATED: score -->**Score: 50 of 104 rg flags implemented.**<!-- END GENERATED --> The gap is
dominated by a few feature clusters (see the notes after the table):
the file-type system, PCRE2/multiline, encodings, output decoration,
and replacement.

## Flag-by-flag

Status: ✅ implemented, 1:1 verified · ⚠️ recognized, rejected with a
targeted "not implemented" error (exit 2) · ❌ not implemented (generic
unknown-flag error, exit 2)

<!-- BEGIN GENERATED: tables -->

### Input

| Flag | Short | gg | Summary |
|---|---|---|---|
| `--file` | `-f` | ✅ | Search for patterns from the given file. |
| `--pre` (+`--no-pre`) |  | ❌ | Search output of COMMAND for each PATH. |
| `--pre-glob` |  | ❌ | Include or exclude files from a preprocessor. |
| `--regexp` | `-e` | ✅ | A pattern to search for. |
| `--search-zip` (+`--no-search-zip`) | `-z` | ⚠️ | Search in compressed files. |

### Search

| Flag | Short | gg | Summary |
|---|---|---|---|
| `--auto-hybrid-regex` (+`--no-auto-hybrid-regex`) |  | ❌ | (DEPRECATED) Use PCRE2 if appropriate. |
| `--case-sensitive` | `-s` | ✅ | Search case sensitively (default). |
| `--crlf` (+`--no-crlf`) |  | ❌ | Use CRLF line terminators (nice for Windows). |
| `--dfa-size-limit` |  | ❌ | The upper size limit of the regex DFA. |
| `--encoding` (+`--no-encoding`) | `-E` | ⚠️ | Specify the text encoding of files to search. |
| `--engine` |  | ❌ | Specify which regex engine to use. |
| `--fixed-strings` (+`--no-fixed-strings`) | `-F` | ✅ | Treat all patterns as literals. |
| `--ignore-case` | `-i` | ✅ | Case insensitive search. |
| `--invert-match` (+`--no-invert-match`) | `-v` | ✅ | Invert matching. |
| `--line-regexp` | `-x` | ✅ | Show matches surrounded by line boundaries. |
| `--max-count` | `-m` | ✅ | Limit the number of matching lines. |
| `--mmap` (+`--no-mmap`) |  | ✅ | Search with memory maps when possible. |
| `--multiline` (+`--no-multiline`) | `-U` | ⚠️ | Enable searching across multiple lines. |
| `--multiline-dotall` (+`--no-multiline-dotall`) |  | ⚠️ | Make '.' match line terminators. |
| `--no-pcre2-unicode` (+`--pcre2-unicode`) |  | ❌ | (DEPRECATED) Disable Unicode mode for PCRE2. |
| `--no-unicode` (+`--unicode`) |  | ❌ | Disable Unicode mode. |
| `--null-data` |  | ❌ | Use NUL as a line terminator. |
| `--pcre2` (+`--no-pcre2`) | `-P` | ⚠️ | Enable PCRE2 matching. |
| `--regex-size-limit` |  | ❌ | The size limit of the compiled regex. |
| `--smart-case` | `-S` | ✅ | Smart case search. |
| `--stop-on-nonmatch` |  | ❌ | Stop searching after a non-match. |
| `--text` (+`--no-text`) | `-a` | ✅ | Search binary files as if they were text. |
| `--threads` | `-j` | ✅ | Set the approximate number of threads to use. |
| `--word-regexp` | `-w` | ✅ | Show matches surrounded by word boundaries. |

### Filtering

| Flag | Short | gg | Summary |
|---|---|---|---|
| `--binary` (+`--no-binary`) |  | ⚠️ | Search binary files. |
| `--follow` (+`--no-follow`) | `-L` | ⚠️ | Follow symbolic links. |
| `--glob` | `-g` | ✅ | Include or exclude file paths. |
| `--glob-case-insensitive` (+`--no-glob-case-insensitive`) |  | ✅ | Process all glob patterns case insensitively. |
| `--hidden` (+`--no-hidden`) | `-.` | ✅ | Search hidden files and directories. |
| `--iglob` |  | ✅ | Include/exclude paths case insensitively. |
| `--ignore-file` |  | ❌ | Specify additional ignore files. |
| `--ignore-file-case-insensitive` (+`--no-ignore-file-case-insensitive`) |  | ❌ | Process ignore files case insensitively. |
| `--max-depth` | `-d` | ✅ | Descend at most NUM directories. |
| `--max-filesize` |  | ✅ | Ignore files larger than NUM in size. |
| `--no-ignore` (+`--ignore`) |  | ✅ | Don't use ignore files. |
| `--no-ignore-dot` (+`--ignore-dot`) |  | ❌ | Don't use .ignore or .rgignore files. |
| `--no-ignore-exclude` (+`--ignore-exclude`) |  | ❌ | Don't use local exclusion files. |
| `--no-ignore-files` (+`--ignore-files`) |  | ❌ | Don't use --ignore-file arguments. |
| `--no-ignore-global` (+`--ignore-global`) |  | ❌ | Don't use global ignore files. |
| `--no-ignore-parent` (+`--ignore-parent`) |  | ❌ | Don't use ignore files in parent directories. |
| `--no-ignore-vcs` (+`--ignore-vcs`) |  | ❌ | Don't use ignore files from source control. |
| `--no-require-git` (+`--require-git`) |  | ❌ | Use .gitignore outside of git repositories. |
| `--one-file-system` (+`--no-one-file-system`) |  | ❌ | Skip directories on other file systems. |
| `--type` | `-t` | ✅ | Only search files matching TYPE. |
| `--type-add` |  | ✅ | Add a new glob for a file type. |
| `--type-clear` |  | ✅ | Clear globs for a file type. |
| `--type-not` | `-T` | ✅ | Do not search files matching TYPE. |
| `--unrestricted` | `-u` | ✅ | Reduce the level of "smart" filtering. |

### Output

| Flag | Short | gg | Summary |
|---|---|---|---|
| `--after-context` | `-A` | ✅ | Show NUM lines after each match. |
| `--before-context` | `-B` | ✅ | Show NUM lines before each match. |
| `--block-buffered` (+`--no-block-buffered`) |  | ❌ | Force block buffering. |
| `--byte-offset` (+`--no-byte-offset`) | `-b` | ✅ | Print the byte offset for each matching line. |
| `--color` |  | ✅ | When to use color. |
| `--colors` |  | ❌ | Configure color settings and styles. |
| `--column` (+`--no-column`) |  | ✅ | Show column numbers. |
| `--context` | `-C` | ✅ | Show NUM lines before and after each match. |
| `--context-separator` (+`--no-context-separator`) |  | ❌ | Set the separator for contextual chunks. |
| `--field-context-separator` |  | ❌ | Set the field context separator. |
| `--field-match-separator` |  | ❌ | Set the field match separator. |
| `--heading` (+`--no-heading`) |  | ✅ | Print matches grouped by each file. |
| `--help` | `-h` | ✅ | Show help output. |
| `--hostname-bin` |  | ❌ | Run a program to get this system's hostname. |
| `--hyperlink-format` |  | ❌ | Set the format of hyperlinks. |
| `--include-zero` (+`--no-include-zero`) |  | ❌ | Include zero matches in summary output. |
| `--line-buffered` (+`--no-line-buffered`) |  | ❌ | Force line buffering. |
| `--line-number` | `-n` | ✅ | Show line numbers. |
| `--max-columns` | `-M` | ✅ | Omit lines longer than this limit. |
| `--max-columns-preview` (+`--no-max-columns-preview`) |  | ✅ | Show preview for lines exceeding the limit. |
| `--no-filename` | `-I` | ✅ | Never print the path with each matching line. |
| `--no-line-number` | `-N` | ✅ | Suppress line numbers. |
| `--null` | `-0` | ❌ | Print a NUL byte after file paths. |
| `--only-matching` | `-o` | ✅ | Print only matched parts of a line. |
| `--passthru` |  | ❌ | Print both matching and non-matching lines. |
| `--path-separator` |  | ❌ | Set the path separator for printing paths. |
| `--pretty` | `-p` | ❌ | Alias for colors, headings and line numbers. |
| `--quiet` | `-q` | ✅ | Do not print anything to stdout. |
| `--replace` | `-r` | ⚠️ | Replace matches with the given text. |
| `--sort` |  | ⚠️ | Sort results in ascending order. |
| `--sort-files` (+`--no-sort-files`) |  | ❌ | (DEPRECATED) Sort results by file path. |
| `--sortr` |  | ⚠️ | Sort results in descending order. |
| `--trim` (+`--no-trim`) |  | ✅ | Trim prefix whitespace from matches. |
| `--vimgrep` |  | ✅ | Print results in a vim compatible format. |
| `--with-filename` | `-H` | ✅ | Print the file path with each matching line. |

### Output modes

| Flag | Short | gg | Summary |
|---|---|---|---|
| `--count` | `-c` | ✅ | Show count of matching lines for each file. |
| `--count-matches` |  | ✅ | Show count of every match for each file. |
| `--files-with-matches` | `-l` | ✅ | Print the paths with at least one match. |
| `--files-without-match` |  | ✅ | Print the paths that contain zero matches. |
| `--json` (+`--no-json`) |  | ⚠️ | Show search results in a JSON Lines format. |

### Logging

| Flag | Short | gg | Summary |
|---|---|---|---|
| `--debug` |  | ❌ | Show debug messages. |
| `--no-ignore-messages` (+`--ignore-messages`) |  | ❌ | Suppress gitignore parse error messages. |
| `--no-messages` (+`--messages`) |  | ❌ | Suppress some error messages. |
| `--stats` (+`--no-stats`) |  | ❌ | Print statistics about the search. |
| `--trace` |  | ❌ | Show trace messages. |

### Other behaviors

| Flag | Short | gg | Summary |
|---|---|---|---|
| `--files` |  | ✅ | Print each file that would be searched. |
| `--generate` |  | ❌ | Generate man pages and completion scripts. |
| `--no-config` |  | ❌ | Never read configuration files. |
| `--pcre2-version` |  | ❌ | Print the version of PCRE2 that ripgrep uses. |
| `--type-list` |  | ✅ | Show all supported file types. |
| `--version` | `-V` | ✅ | Print ripgrep's version. |

<!-- END GENERATED -->

## Feature parity beyond flags

Implemented and verified (differential oracles, see README):

- **Exit codes**: 0 match / 1 no match / 2 error, including rg's exact
  error-overrides-match and `-q`-locks-0 precedences.
- **gitignore semantics**: `.gitignore` + `.ignore` per directory with
  gitignore precedence and last-match-wins, whitelist (`!`) and
  dir-only patterns; the glob engine is fuzzed against real
  `git check-ignore`. (`.rgignore`, global gitignore /
  `core.excludesFile`, and `--ignore-file`: not yet.)
- **Binary detection**: rg's exact NUL semantics for both
  walk-discovered files (whole-read-chunk discard + warning) and
  explicit files (suppress + `binary file matches` line), incl. the
  mmap path's first-64KB probe behavior.
- **UTF-8 BOM stripping** with rg-identical offsets/line numbers.
- **mmap policy**: explicitly-named files, matching rg's
  `<=10 paths, all regular files` rule exactly.
- **Parallel directory traversal**; `--files` output verified against
  `rg --files`.
- **Intra-file parallel search** on large files — a gg-only capability
  (rg searches one file on one core); output stays byte-identical to
  serial (fuzz-verified), and structural cases (`-A/-B/-C`, `-v`) fall
  back to the serial path.
- **Library facade** (`import "github.com/yackey-labs/gripgrep"`) —
  no rg equivalent; the CLI without the CLI.

Not implemented (the honest list, matching the table above):

- **File-type system** (`-t/-T/--type-*`): the largest single cluster.
- **PCRE2** (`-P/--engine`) and **multiline** (`-U`): honest "maybe
  never" — gg's regex engine is RE2-class (a grafana fork of Go
  `regexp`), so look-around and backreferences have no home.
- **Encodings** (`-E/--encoding`): gg searches bytes/UTF-8 (+BOM strip)
  only; no transcoding.
- **Replacement** (`-r/--replace`), **JSON output** (`--json`),
  **sorting** (`--sort/--sortr`), **preprocessors** (`--pre`),
  **compressed search** (`-z`).
- **Output decoration**: `--column`, `--vimgrep`,
  `-o/--only-matching`, `--passthru`, separators/`--field-*` knobs,
  hyperlinks, `--stats`.
- **Config file** (`RIPGREP_CONFIG_PATH`): gg reads no config;
  flags only.
- Assorted small filters/limits (`-L/--follow`, `--one-file-system`,
  `--ignore-file`, ...): individually cheap; several are queued as
  the "compat tier" roadmap item.

## Regenerating this document

This file is generated — don't hand-edit the tables between the
`<!-- BEGIN GENERATED -->`/`<!-- END GENERATED -->` markers (the "Score:
N of M" line and the "Flag-by-flag" tables); everything else is
hand-written prose and survives regeneration untouched.

The pipeline is two steps, split so that regenerating the doc never
needs an rg checkout:

1. **Extract** (only when the rg pin moves): `go run ./internal/parity/extract <rg-checkout-root> internal/parity/rg-flags.json`
   reads rg's `crates/core/flags/defs.rs` (`impl Flag` blocks:
   long/short/negated/aliases/category/`doc_short`) and writes the
   checked-in inventory, stamped with the checkout's commit/date and
   `Cargo.toml` version — update the "ripgrep flag authority" row in
   "What is being compared" to match.
2. **Generate**: `make parity-doc` (or `go run ./internal/parity/gen`)
   reads that inventory plus gg's `cmd/gg/flags.go` (parsed with
   `go/ast`: the implemented `v1Flags` table and the curated
   `notImplementedFlags` list), asserts the two gg lists don't overlap
   and that gg has zero flags rg lacks, and rewrites the generated
   regions in place.

`go test ./internal/parity` is the drift check: it runs the same
generation in-memory and fails if the result differs from what's
committed here, or if either assertion above fires. It needs no rg
checkout and runs in ordinary CI.
