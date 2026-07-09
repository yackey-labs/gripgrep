GOAMD64 ?= v3
PGO_PROFILE := cmd/gg/default.pgo

.PHONY: build test vet bench bench-e2e pgo pgo-collect clean

build:
	go build -o gg ./cmd/gg

test:
	go test ./...

vet:
	go vet ./...

# Per-package Go benchmarks (go test -bench), not the hyperfine e2e loop.
bench:
	go test -bench=. -benchmem ./...

# hyperfine-driven correctness gate + timing loop against rg, per
# docs/research/benchmarking.md §4. Requires internal/bench/setup.sh to
# have been run once (corpus lives in /dev/shm, not in-repo).
bench-e2e: build
	./internal/bench/bench.sh

# Collect a representative CPU profile for PGO. TODO(M2+): cmd/gg needs a
# hidden -cpuprofile flag before this target does anything useful; until
# then it's a documented placeholder.
pgo-collect: build
	@echo "TODO: run a representative search with -cpuprofile, then:"
	@echo "  cp cpu.prof $(PGO_PROFILE)"

# Builds with PGO (if a profile has been committed) and GOAMD64=v3.
pgo:
	GOAMD64=$(GOAMD64) go build -pgo=$(PGO_PROFILE) -o gg ./cmd/gg

clean:
	rm -f gg
