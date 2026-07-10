#!/bin/sh
# gripgrep installer / updater — downloads the latest gg release binary.
#
#   curl -fsSL https://raw.githubusercontent.com/yackey-labs/gripgrep/main/install.sh | sh
#
# Re-running updates in place: the new binary is downloaded to a temp
# file in the install dir, checksum-verified, then atomically renamed
# over the old one (same filesystem, so mv is atomic and any running gg
# keeps its old inode). The installer also drops a `gg-update` helper
# next to gg that re-runs this script.
#
# Options via environment:
#   GG_INSTALL_DIR   install directory (default: ~/.local/bin)
#   GG_VERSION       specific tag to install (default: latest release)
#   GG_FORCE=1       reinstall even if the same version is present
set -eu

REPO="yackey-labs/gripgrep"
INSTALL_DIR="${GG_INSTALL_DIR:-$HOME/.local/bin}"
RAW_URL="https://raw.githubusercontent.com/$REPO/main/install.sh"

OS=$(uname -s)
ARCH=$(uname -m)

case "$OS" in
  Linux) GOOS=linux ;;
  Darwin) GOOS=darwin ;;
  *)
    echo "unsupported OS: $OS (Linux and macOS here; Windows uses install.ps1)" >&2
    exit 1 ;;
esac

case "$ARCH" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

if [ -z "${GG_VERSION:-}" ]; then
  GG_VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4)
  if [ -z "$GG_VERSION" ]; then
    echo "could not determine the latest release — is one published yet?" >&2
    echo "https://github.com/$REPO/releases" >&2
    exit 1
  fi
fi

if [ "${GG_FORCE:-0}" != 1 ] && [ -x "$INSTALL_DIR/gg" ] \
  && "$INSTALL_DIR/gg" --version 2>/dev/null | grep -qF "$GG_VERSION"; then
  echo "gg $GG_VERSION is already installed at $INSTALL_DIR/gg (GG_FORCE=1 to reinstall)"
  exit 0
fi

NAME="gg-$GG_VERSION-$GOOS-$GOARCH"
URL="https://github.com/$REPO/releases/download/$GG_VERSION/$NAME.tar.gz"
SUMS_URL="https://github.com/$REPO/releases/download/$GG_VERSION/SHA256SUMS"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "downloading gg $GG_VERSION ($GOOS/$GOARCH)..."
curl -fsSL -o "$TMP/$NAME.tar.gz" "$URL"

if curl -fsSL -o "$TMP/SHA256SUMS" "$SUMS_URL" 2>/dev/null; then
  SUM_TOOL="sha256sum"
  command -v sha256sum >/dev/null || SUM_TOOL="shasum -a 256"  # macOS
  (cd "$TMP" && grep " $NAME.tar.gz\$" SHA256SUMS | $SUM_TOOL -c - >/dev/null) \
    || { echo "checksum verification FAILED" >&2; exit 1; }
  echo "checksum OK"
fi

tar -C "$TMP" -xzf "$TMP/$NAME.tar.gz"
mkdir -p "$INSTALL_DIR"

# Stage inside the install dir so the final rename is same-filesystem
# (atomic); only then does the old binary go away.
STAGE="$INSTALL_DIR/.gg.new.$$"
cp "$TMP/$NAME/gg" "$STAGE"
chmod 0755 "$STAGE"
mv -f "$STAGE" "$INSTALL_DIR/gg"

# The update command: re-runs this installer.
cat > "$INSTALL_DIR/gg-update" <<EOF
#!/bin/sh
# Updates gg by re-running the gripgrep installer.
exec sh -c 'curl -fsSL $RAW_URL | sh'
EOF
chmod 0755 "$INSTALL_DIR/gg-update"

echo "installed: $INSTALL_DIR/gg (update any time with gg-update)"
"$INSTALL_DIR/gg" --version

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: $INSTALL_DIR is not on your PATH — add it, e.g.:"
     echo "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
