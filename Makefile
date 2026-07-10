GOAMD64 ?= v3
PGO_PROFILE := cmd/gg/default.pgo
PGO_TREE := benchmark-data/linux
PGO_TEXT := /dev/shm/gg-bench/en.sample.txt

.PHONY: build build-release test vet cover bench bench-e2e pgo-collect parity-doc clean

# Portable baseline: default GOAMD64, no explicit -pgo flag. `go build`
# still auto-detects and uses $(PGO_PROFILE) if present (that's Go's
# default -pgo=auto behavior for any main package sitting next to a
# default.pgo) -- PGO is not opt-in, only GOAMD64=v3 is (see
# build-release). Measured M3 #26: PGO alone is worth +1.2% to +7.7%
# across the benchmark mix on this box.
build:
	go build -o gg ./cmd/gg

# Release build: GOAMD64=v3 (this box's hardware baseline supports AVX2)
# on top of the same PGO profile. Shipped as a distinct binary
# (gg-release) so it never silently clobbers the portable `gg` from
# `make build`. Measured M3 #26: GOAMD64=v3 was a wash on its own on this
# box (no consistent delta either direction across 5 rows) -- the hot
# loops (bytes.IndexByte/Index) are hand-written AVX2 assembly that
# already dispatches on runtime CPU-feature detection regardless of the
# GOAMD64 build level, so v3 only touches the smaller slice of
# compiler-generated code around them. Shipped anyway: it costs nothing,
# is the conventional release flavor for AVX2-capable deploy targets, and
# may pay off more once Teddy/SWAR (PLAN.md's M3 queue) adds
# vectorizable Go code of our own rather than relying solely on stdlib
# asm.
build-release:
	GOAMD64=$(GOAMD64) go build -pgo=$(PGO_PROFILE) -o gg-release ./cmd/gg

# -race is mandatory, not optional: walk's work-stealing deque/quiescence
# and printer's per-worker buffer flush are exactly the kind of real
# concurrency -race exists to catch. Per PLAN.md's "Test coverage
# requirements" (Race coverage), this is the one true `make test`.
test:
	go test -race ./...

vet:
	go vet ./...

# Per-package coverage report, run with -race for consistency with
# `test`. PLAN.md's "Test coverage requirements" sets a floor of ≥80%
# line coverage per package (func-level breakdown below the floor line;
# the named edge-case tests in PLAN.md outrank the raw number).
cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

# Per-package Go benchmarks (go test -bench), not the hyperfine e2e loop.
bench:
	go test -bench=. -benchmem ./...

# hyperfine-driven correctness gate + timing loop against rg, per
# docs/research/benchmarking.md §4. Requires internal/bench/setup.sh to
# have been run once (corpus lives in /dev/shm, not in-repo).
bench-e2e: build
	./internal/bench/bench.sh

# Refreshes $(PGO_PROFILE) from a representative query mix (M3 #26):
# walk-only, tree literal search, single-file literal/multi-literal/
# case-insensitive, and a glob-filtered tree search -- spanning the walk,
# glob, match, and search packages rather than over-fitting one path.
# Requires the benchmark corpora to already exist ($(PGO_TREE) --
# internal/bench/setup.sh's linux clone or this repo's benchmark-data/
# fixture -- and $(PGO_TEXT), the OpenSubtitles corpus); this target
# does not provision them. Uses cmd/gg's hidden GG_CPUPROFILE hook, then
# merges with `go tool pprof -proto`.
pgo-collect: build
	GG_CPUPROFILE=/tmp/gg-pgo-1.prof ./gg -n PM_RESUME $(PGO_TREE) >/dev/null
	GG_CPUPROFILE=/tmp/gg-pgo-2.prof ./gg --files $(PGO_TREE) >/dev/null
	GG_CPUPROFILE=/tmp/gg-pgo-3.prof ./gg -n "Sherlock Holmes" $(PGO_TEXT) >/dev/null
	GG_CPUPROFILE=/tmp/gg-pgo-4.prof ./gg -n "Sherlock|Watson" $(PGO_TEXT) >/dev/null
	GG_CPUPROFILE=/tmp/gg-pgo-5.prof ./gg -n -i "sherlock holmes" $(PGO_TEXT) >/dev/null
	GG_CPUPROFILE=/tmp/gg-pgo-6.prof ./gg -n -g '*.c' PM_RESUME $(PGO_TREE) >/dev/null
	go tool pprof -proto -output=$(PGO_PROFILE) /tmp/gg-pgo-*.prof
	rm -f /tmp/gg-pgo-*.prof
	@echo "refreshed $(PGO_PROFILE) -- 'make build'/'make build-release' pick it up automatically"

# Regenerates docs/rg-parity.md's generated regions (the flag-by-flag
# tables and the "Score: N of M" line) from the checked-in rg flag
# inventory (internal/parity/rg-flags.json) and cmd/gg/flags.go. No rg
# checkout needed -- that's only required to re-extract the inventory
# itself, via internal/parity/extract, when the rg pin moves (see
# docs/rg-parity.md's "Regenerating this document").
parity-doc:
	go run ./internal/parity/gen

clean:
	rm -f gg gg-release
