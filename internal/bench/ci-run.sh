#!/usr/bin/env bash
# Run the four headline benchmark rows for ONE tool on this runner and
# write hyperfine JSON + match counts into an output dir. rg and gg run
# on separate fresh runners (so neither's cache/thermal state taints the
# other); ci-report.py later pairs the two legs per platform.
#
# usage: ci-run.sh <tool-binary> <corpus-dir> <out-dir>
set -euo pipefail

BIN=${1:?tool binary}
CORPUS=${2:?corpus dir}
OUT=${3:?output dir}
mkdir -p "$OUT"

run_row() {
  local name=$1; shift
  # Match-count sanity value first (also warms the cache): the report
  # flags cross-tool divergence, since the two legs can't byte-diff
  # across runners.
  local count
  count=$("$BIN" "$@" | wc -l | tr -d '[:space:]') || true
  echo "{\"row\": \"$name\", \"lines\": $count}" > "$OUT/$name.count.json"
  hyperfine --warmup 3 -m 15 -N --export-json "$OUT/$name.json" \
    "$(printf '%q ' "$BIN" "$@")"
}

run_row tree_literal -n PM_RESUME "$CORPUS/linux"
run_row files_walk --files "$CORPUS/linux"
run_row big_literal -n "Sherlock Holmes" "$CORPUS/en.sample.txt"
run_row multi_literal -n "Sherlock|Watson" "$CORPUS/en.sample.txt"

"$BIN" --version | head -1 > "$OUT/version.txt" || true
echo "bench rows complete -> $OUT"
