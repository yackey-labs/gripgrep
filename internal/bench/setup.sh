#!/usr/bin/env bash
# One-time provisioning of the benchmark corpus, per
# docs/research/benchmarking.md §4. Corpus lives in tmpfs (/dev/shm) so
# timing measures CPU/algorithm, not disk.
set -euo pipefail

DIR=${GG_BENCH_DIR:-/dev/shm/gg-bench}
mkdir -p "$DIR"
cd "$DIR"

if [ ! -d linux ]; then
  echo "cloning linux corpus (BurntSushi/linux, frozen fork, shallow)..."
  git clone --depth 1 https://github.com/BurntSushi/linux
else
  echo "linux corpus already present, skipping clone"
fi

if [ ! -f en.txt ]; then
  echo "downloading OpenSubtitles EN corpus..."
  curl -LO https://object.pouta.csc.fi/OPUS-OpenSubtitles/v2016/mono/en.txt.gz
  gunzip en.txt.gz
else
  echo "en.txt already present, skipping download"
fi

echo "bench corpus ready in $DIR"
echo "  linux/  -> many small files (tree walk + gitignore)"
echo "  en.txt  -> single large file (raw scan throughput)"
