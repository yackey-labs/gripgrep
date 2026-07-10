package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"runtime/pprof"
)

// usageLine is printed alongside any flag-parsing error, matching rg's
// convention of a short usage reminder on exit 2.
const usageLine = "Usage: gg [OPTIONS] PATTERN [PATH ...]"

// helpText is printed by -h/--help. gg's v1 flag surface is a subset of
// rg's (see PLAN.md's "v1 CLI scope" and flags.go's notImplementedFlags),
// so this only documents what gg actually implements rather than
// reproducing rg's full --help text.
const helpText = usageLine + `

gripgrep (gg) -- a fast recursive code search tool, rg-compatible for
the flags it implements.

PATTERN FLAGS:
    -e, --regexp <PATTERN>       add a pattern (repeatable; OR'd together)
    -F, --fixed-strings          treat PATTERN as a literal string
    -i, --ignore-case            case insensitive search
    -s, --case-sensitive         case sensitive search (default)
    -S, --smart-case             case insensitive unless PATTERN has an uppercase letter
    -w, --word-regexp            only match whole words

FILTER FLAGS:
    -.,  --hidden                search hidden files and directories
        --no-ignore               don't respect .gitignore/.ignore
    -g, --glob <GLOB>             include/exclude files matching GLOB (repeatable)
    -u, --unrestricted            reduce filtering (repeat up to 3 times)
        --max-filesize <SIZE>     skip files larger than SIZE (e.g. 10M)

OUTPUT FLAGS:
    -n, --line-number             show line numbers
    -N, --no-line-number          don't show line numbers
    -c, --count                   show match counts, not matched lines
    -l, --files-with-matches      show only the paths of matching files
    -q, --quiet                   show nothing, only set the exit code
        --color <WHEN>            auto|never|always|ansi
    -A, --after-context <N>       show N lines after each match
    -B, --before-context <N>      show N lines before each match
    -C, --context <N>             show N lines before and after each match
    -v, --invert-match            show non-matching lines

PERF FLAGS:
    -j, --threads <N>              number of search threads (0 = auto)
    -a, --text                     search binary files as if they were text
        --mmap/--no-mmap          use/don't use memory maps

    -h, --help                    print this help and exit
    -V, --version                 print version information and exit
`

// version is stamped by the release build via
// -ldflags "-X main.version=vX.Y.Z" (see .github/workflows/release.yml);
// source builds report "dev".
var version = "dev"

// versionText is printed by -V/--version.
var versionText = "gg " + version + " (gripgrep)"

// run contains all of main's logic, factored out so it can be exercised
// directly by an in-process test (see flags_test.go's TestRun*) in
// addition to the black-box subprocess tests that build and run the
// real binary. It writes results to stdout, diagnostics to stderr, and
// returns the process exit code; main just wires it to
// os.Args/os.Stdout/os.Stderr/os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := ParseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "gg: %s\n%s\n", err, usageLine)
		return 2
	}
	if cfg.Help {
		fmt.Fprint(stdout, helpText)
		return 0
	}
	if cfg.Version {
		fmt.Fprintln(stdout, versionText)
		return 0
	}

	// Runtime tuning before any search-path allocation, per PLAN.md's
	// "Runtime" design row: a higher GC target reduces collector
	// overhead on the short-lived, allocation-light hot path, backstopped
	// by a soft memory limit so a pathological search (huge files, huge
	// match counts) can't grow unbounded. A caller-set GOMEMLIMIT always
	// wins -- this is only a default.
	debug.SetGCPercent(400)
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(1 << 30) // 1GiB backstop
	}

	return execute(cfg, stdout, stderr)
}

// maybeStartCPUProfile is a hidden, env-var-gated profiling hook for the
// M3 bench/optimize loop and future PGO profile collection (see
// Makefile's pgo-collect target). It is deliberately NOT a CLI flag: the
// "1:1 rg CLI compatibility" contract (PLAN.md) means gg's flag surface
// must only ever contain flags rg itself has, and this has no rg
// equivalent. GG_CPUPROFILE is unset in every normal invocation, so it
// adds no overhead and never appears in --help.
func maybeStartCPUProfile() (stop func()) {
	path := os.Getenv("GG_CPUPROFILE")
	if path == "" {
		return func() {}
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gg: GG_CPUPROFILE: %s\n", err)
		return func() {}
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		fmt.Fprintf(os.Stderr, "gg: GG_CPUPROFILE: %s\n", err)
		f.Close()
		return func() {}
	}
	return func() {
		pprof.StopCPUProfile()
		f.Close()
	}
}

func main() {
	stop := maybeStartCPUProfile()
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
