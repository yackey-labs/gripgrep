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

case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) WINDOWS=yes ;; *) WINDOWS=no ;; esac

if [ ! -d linux ]; then
  echo "cloning linux corpus (BurntSushi/linux, frozen fork, shallow)..."
  if [ "$WINDOWS" = yes ]; then
    # Win32 can't create the tree's handful of reserved-DOS-device-name
    # files (aux.c and friends), which makes a plain checkout hard-fail
    # on NTFS. But that's ~7 files out of ~79k: clone without checkout,
    # then sparse-checkout everything EXCEPT the reserved basenames
    # (aux/con/nul/prn/com1-9/lpt1-9, any extension). Case-colliding
    # paths (xt_CONNMARK.h vs xt_connmark.h etc.) merely warn on the
    # case-insensitive filesystem and keep one of the pair. Both tools
    # on this runner search the identical slightly-reduced tree, and
    # the report only compares same-runner pairs, so absolute-count
    # drift vs the unix corpora is fine (see header note).
    git clone --depth 1 --quiet --no-checkout https://github.com/BurntSushi/linux
    (
      cd linux
      git config core.longpaths true
      { echo '/*'
        git ls-tree -r --name-only HEAD \
          | grep -iE '(^|/)(aux|con|nul|prn|com[0-9]|lpt[0-9])(\.[^/]*)?$' \
          | sed 's|^|!/|'
      } | git sparse-checkout set --no-cone --stdin
      git checkout --quiet HEAD
    )
  else
    git clone --depth 1 --quiet https://github.com/BurntSushi/linux
  fi
fi

echo "creating synthetic built-tree artifacts (25k NUL-bearing .o files)..."
# Two steps, not one pipeline: head closing a find|sort|head pipe early
# makes sort exit on SIGPIPE, which pipefail (CI's default shell opts)
# turns into a hard failure. The .o writes themselves go through one
# python process, not a 25k-iteration shell loop: byte-identical output,
# and per-file shell writes are painfully slow under msys on the Windows
# runners (the loop predates the tree existing there at all).
find linux -name '*.c' -type f | LC_ALL=C sort > .all-c-files.txt
python3 - <<'EOF'
with open(".all-c-files.txt", "rb") as fh:
    files = fh.read().splitlines()
for f in files[:25000]:
    with open(f[:-2] + b".o", "wb") as out:
        out.write(b"ELF\x00\x00synthetic object for gg bench\x00")
EOF
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
    f.seek(max(0, size - (1 << 20)))
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
