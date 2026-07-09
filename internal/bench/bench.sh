#!/usr/bin/env bash
# Minimal 3-query dev loop, per docs/research/benchmarking.md §4.
#
# Correctness gate first: compares `wc -l` of gg vs rg output before
# trusting any timing number. Tolerates gg not being functional yet
# (M0/M1) -- reports a mismatch instead of hard-failing, and hyperfine is
# run with --ignore-failure so a non-zero gg exit doesn't abort the whole
# script.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIR=${GG_BENCH_DIR:-/dev/shm/gg-bench}
GG_BIN=${GG_BIN:-"$ROOT/gg"}

if [ ! -d "$DIR" ]; then
  echo "bench corpus not found at $DIR -- run internal/bench/setup.sh first" >&2
  exit 1
fi

if [ ! -x "$GG_BIN" ]; then
  echo "building gg..."
  (cd "$ROOT" && go build -o gg ./cmd/gg)
fi

if ! command -v hyperfine >/dev/null; then
  echo "hyperfine not found on PATH -- install it (cargo install hyperfine, or your package manager)" >&2
  exit 1
fi

if ! command -v rg >/dev/null; then
  echo "rg (ripgrep) not found on PATH" >&2
  exit 1
fi

cd "$DIR"

correctness_gate() {
  local desc="$1" rg_cmd="$2" gg_cmd="$3"
  local rg_n gg_n
  rg_n=$(eval "$rg_cmd" 2>/dev/null | wc -l || true)
  gg_n=$(eval "$gg_cmd" 2>/dev/null | wc -l || true)
  if [ "$rg_n" != "$gg_n" ]; then
    echo "[correctness] $desc: MISMATCH rg=$rg_n gg=$gg_n (expected until M2 lands cmd/gg)" >&2
  else
    echo "[correctness] $desc: OK ($rg_n lines)"
  fi
}

echo "=== correctness gate ==="
correctness_gate "linux_literal"        "rg -n PM_RESUME linux"              "'$GG_BIN' -n PM_RESUME linux"
correctness_gate "subtitles_en_literal" "rg -n 'Sherlock Holmes' en.txt"      "'$GG_BIN' -n 'Sherlock Holmes' en.txt"
correctness_gate "linux_regex_literal"  "rg -n '[A-Z]+_RESUME' linux"         "'$GG_BIN' -n '[A-Z]+_RESUME' linux"

echo
echo "=== timing (hyperfine -w 3 -r 10) ==="
hyperfine -w 3 -r 10 --ignore-failure \
  "rg -n PM_RESUME linux" "$GG_BIN -n PM_RESUME linux"

hyperfine -w 3 -r 10 --ignore-failure \
  "rg -n 'Sherlock Holmes' en.txt" "$GG_BIN -n 'Sherlock Holmes' en.txt"

hyperfine -w 3 -r 10 --ignore-failure \
  "rg -n '[A-Z]+_RESUME' linux" "$GG_BIN -n '[A-Z]+_RESUME' linux"

echo
echo "Run the full 20-query matrix (docs/research/benchmarking.md §1) only pre-release."
