# Using gripgrep as a library

The root `gripgrep` package is the `gg` CLI's own search engine, exported
as a Go API with no dependency on the CLI binary itself. This page is the
full guide; `doc.go`'s package comment and the README's "Library usage"
section are shorter pointers into it.

## Install & one-liner quickstart

```sh
go get github.com/yackey-labs/gripgrep
```

Four package-level verbs cover the common cases, each taking CLI defaults
(recursive, gitignore-aware, case-sensitive, binary-file filtering, auto
worker count) and a pattern plus zero or more paths (`"."` if none given):

```go
import "github.com/yackey-labs/gripgrep"

matches, err := gripgrep.Search("TODO", ".")        // []Match: Path, LineNumber, Line, ...
files, err   := gripgrep.FilesWithMatch("TODO", ".") // like gg -l
counts, err  := gripgrep.CountMatches("TODO", ".")   // like gg -c, map[path]count
all, err     := gripgrep.Files("src", "docs")        // like gg --files, no pattern
```

## Choosing a verb

| I want... | call | CLI equivalent |
|---|---|---|
| the matches themselves (line, path, line number, context) | `Search` / `Options.Search` | `gg PATTERN` |
| just the paths that matched | `FilesWithMatch` / `Options.FilesWithMatch` | `gg -l PATTERN` |
| a match count per file | `CountMatches` / `Options.CountMatches` | `gg -c PATTERN` |
| to stream results and stop early on the first hit(s) | `SearchStream` / `Options.SearchStream` | `gg PATTERN` (piped through `head`, roughly) |
| the file list a search would walk, without matching | `Files` | `gg --files` |

Every verb has a package-level function (CLI defaults) and a same-name
`Options` method (CLI-flag-equivalent control), except `Files`, which has
no `Options` variant -- see its doc comment for why.

## Options ↔ flags reference

`Options`'s zero value is exactly the CLI's own defaults. Every field
below is additive; new rounds only ever add fields, never rename or
repurpose one (see "Versioning").

| Field | Type | CLI flag | Zero value means |
|---|---|---|---|
| `IgnoreCase` | `bool` | `-i`/`--ignore-case` | off (case-sensitive) |
| `SmartCase` | `bool` | `-S`/`--smart-case` | off; wins over `IgnoreCase` if both are set |
| `Word` | `bool` | `-w`/`--word-regexp` | off; see `LineRegexp` for the tie-break if both are set |
| `FixedStrings` | `bool` | `-F`/`--fixed-strings` | off (pattern is a regex) |
| `LineRegexp` | `bool` | `-x`/`--line-regexp` | off; wins over `Word` if both are set |
| `Hidden` | `bool` | `--hidden` | off (hidden files/dirs skipped) |
| `NoIgnore` | `bool` | `--no-ignore` | off (gitignore rules applied) |
| `Globs` | `[]string` | `-g`/`--glob`, repeatable | nil (no glob filter); leading `!` negates |
| `IGlobs` | `[]string` | `--iglob`, repeatable | nil; always case-insensitive regardless of `GlobCaseInsensitive` |
| `GlobCaseInsensitive` | `bool` | `--glob-case-insensitive` | off; makes `Globs` (not `IGlobs`) case-insensitive |
| `Context` | `int` | `-C`/`--context` | 0 (no context lines) |
| `Before` | `int` | `-B`/`--before-context` | 0; overrides `Context`'s leading side when non-zero |
| `After` | `int` | `-A`/`--after-context` | 0; overrides `Context`'s trailing side when non-zero |
| `InvertMatch` | `bool` | `-v`/`--invert-match` | off |
| `MaxCount` | `int` | `-m`/`--max-count` | 0 = unlimited; the CLI's `-m 0` ("match nothing") is not expressible |
| `MaxDepth` | `int` | `-d`/`--max-depth` | 0 = unlimited; the CLI's `-d 0` (roots only) is not expressible -- pass explicit file paths instead |
| `MaxFilesize` | `int64` | `--max-filesize` | 0 = unlimited |
| `Workers` | `int` | `-j`/`--threads` | 0 = auto |

Not surfaced, by design (see the SDK plan's design principles: `Options`
controls *what matches and where we look*; output decoration doesn't
belong here): `-H`/`-I`, `--heading`, `--color`, `--vimgrep`, `--trim`,
`--max-columns`, and `-f` (a CLI input mechanic -- library callers already
hold their patterns as Go values).

## The Match struct

```go
type Match struct {
	Path       string   // relative to the search root, like the CLI's own output paths
	LineNumber int       // 1-based
	Line       string    // the matched line, no trailing newline
	Before     []string  // leading context lines, oldest first; nil if none requested
	After      []string  // trailing context lines, file order; nil if none requested
}
```

`Before`/`After` are populated from `Options.Before`/`After` (or
`Context` for both sides at once). `After` in particular isn't known
until the searcher has read that many further lines past the match --
under `SearchStream`, that means a match's `After` context is filled in
by the time `fn` is called for it, but if you're using early stop
(returning `false` from `fn`), an in-flight file may still deliver one
more match with incomplete-looking `After` context before the stop is
observed. Under `Search` (which collects everything before returning),
this is never visible -- every returned `Match` is fully populated.

## Streaming

`SearchStream`'s `fn` is called once per match, from multiple goroutines
concurrently -- one per file being searched in parallel, exactly like
every other verb in this package. If `fn` has any side effect beyond
consuming the delivered `Match` (appending to a slice, incrementing a
counter), it must synchronize that itself; see `gripgrep.go`'s
`matchCollector` for the pattern `Search` itself uses internally.

Returning `false` from `fn` stops the search as soon as practical: the
current file's remaining search aborts immediately, and no further file's
search is started. Because other files may already be searching
concurrently on other goroutines, one more match may still arrive from
one of them after you've returned `false` -- this is an unavoidable
consequence of the parallel walk, not a bug to work around. Prefer
streaming over `Search` when the tree is large and you expect to stop
early (first-hit-wins), or when you don't want to hold every match in
memory at once.

## Error model

Per-file errors (permission denied, a file that disappeared between
readdir and open) are collected across the whole walk and folded into one
returned `error` alongside whatever partial results were gathered --
nothing is silently dropped. Pattern errors (an invalid regex) return
immediately, before any file is touched. A non-nil `error` doesn't
necessarily mean zero results: check both.

## Memory safety

Every value this package returns -- `Match.Path`, `Match.Line`,
`Match.Before`/`After`, the strings in `FilesWithMatch`/`Files`, the keys
of `CountMatches` -- is an independent copy, safe to retain indefinitely,
store in a map, or hand to another goroutine. This is unlike the
lower-level `walk`/`search` packages this facade is built on, whose types
deliberately expose zero-copy views valid only for the duration of a
callback. Copying at this boundary is the facade's whole job: it trades
the engine's zero-allocation hot path for a memory-safe API, which is
what a library caller (as opposed to `gg`'s own hot loop) wants.

## Performance notes

You get the same engine `gg` uses: parallel directory walk, and
intra-file parallel search on large files, with no tuning knobs required.
`Options.Workers` exists (mirroring `-j`/`--threads`) but the default (0 =
auto) is the right choice for almost every caller; only set it if you
have a specific reason to pin the worker count.

## Relationship to the low-level packages

`glob`, `walk`, `match`, `search`, and `printer` remain public packages
of their own, for callers who want the zero-copy hot path and are willing
to do their own wiring (their types are the same ones `internal/engine`
composes to build this facade). The root `gripgrep` package is the
supported, easy path for everyone else. See each package's own godoc for
its API.

## Versioning

Releases are tagged via the repo's autotag workflow and follow semver.
The facade is on the v0.x release train with real external consumers:
fields and verbs are only ever added, never renamed or repurposed (see
the SDK plan's design principles) -- code written against an older
`Options` continues to compile and behave the same way against a newer
one.
