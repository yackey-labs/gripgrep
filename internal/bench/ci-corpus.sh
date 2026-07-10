#!/usr/bin/env bash
# Deterministic benchmark-corpus provisioning for CI runners, mirroring
# the local headline corpora (docs/research/benchmarking.md):
#
#   $1/linux          -- BurntSushi/linux frozen fork (shallow), plus
#                        25k synthetic NUL-bearing .o files next to the
#                        first 25k .c files in sorted order: a "built
#                        tree" simulation so .gitignore pruning and
#                        binary filtering do real work, like the local
#                        benchmark-data/linux tree.
#   $1/en.sample.txt  -- OpenSubtitles EN corpus, stream-truncated to the
#                        same byte budget as the local sample
#                        (829,919,232 bytes) and trimmed back to the last
#                        complete line ("ends clean").
#
# Both tools in a CI run bench against byte-identical corpora built by
# this script; absolute counts may differ slightly from the ad hoc local
# tree, which is fine -- only same-runner-pair ratios are compared.
set -euo pipefail

DIR=${1:?usage: ci-corpus.sh <corpus-dir>}
SAMPLE_BYTES=829919232

mkdir -p "$DIR"
cd "$DIR"

if [ -f .corpus-complete ]; then
  echo "corpus already complete in $DIR"
  exit 0
fi

if [ ! -d linux ]; then
  echo "cloning linux corpus (BurntSushi/linux, frozen fork, shallow)..."
  git clone --depth 1 --quiet https://github.com/BurntSushi/linux
fi

echo "creating synthetic built-tree artifacts (25k NUL-bearing .o files)..."
# Two steps, not one pipeline: head closing a find|sort|head pipe early
# makes sort exit on SIGPIPE, which pipefail (CI's default shell opts)
# turns into a hard failure.
find linux -name '*.c' -type f | LC_ALL=C sort > .all-c-files.txt
head -25000 .all-c-files.txt | while IFS= read -r f; do
  printf 'ELF\x00\x00synthetic object for gg bench\x00' > "${f%.c}.o"
done
rm -f .all-c-files.txt

if [ ! -f en.sample.txt ]; then
  echo "downloading OpenSubtitles EN corpus (first ${SAMPLE_BYTES} bytes)..."
  # head kills the pipe once it has enough; suppress the resulting
  # SIGPIPE exit and verify the byte count instead.
  (curl -fsSL https://object.pouta.csc.fi/OPUS-OpenSubtitles/v2016/mono/en.txt.gz \
    | gunzip -c | head -c "$SAMPLE_BYTES" > en.sample.raw) || true
  actual=$(wc -c < en.sample.raw)
  if [ "$actual" -ne "$SAMPLE_BYTES" ]; then
    echo "en.sample.raw is $actual bytes, expected $SAMPLE_BYTES" >&2
    exit 1
  fi
  # Trim back to the last complete line so the sample "ends clean".
  python3 - <<'EOF'
import os
path = "en.sample.raw"
size = os.path.getsize(path)
with open(path, "rb") as f:
    f.seek(max(0, size - 1 << 20))
    tail = f.read()
cut = tail.rfind(b"\n")
if cut < 0:
    raise SystemExit("no newline in final 1MB of sample")
os.truncate(path, size - (len(tail) - cut - 1))
EOF
  mv en.sample.raw en.sample.txt
fi

touch .corpus-complete
echo "corpus ready in $DIR"
