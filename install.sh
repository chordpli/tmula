#!/bin/sh
# tmula installer — downloads a prebuilt single binary (with the web UI baked in)
# for your OS/arch, or builds from source if Go + Node are available.
#
#   curl -fsSL https://raw.githubusercontent.com/chordpli/tmula/main/install.sh | sh
#
# Env overrides:
#   TMULA_INSTALL_DIR  install location (default: ~/.local/bin)
#   TMULA_VERSION      release tag to install (default: latest)
set -eu

REPO="chordpli/tmula"
BIN="tmula"
INSTALL_DIR="${TMULA_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# --- detect platform --------------------------------------------------------
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin | linux) ;;
  *) os="" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  arm64 | aarch64) arch="arm64" ;;
  *) arch="" ;;
esac

# --- resolve release tag ----------------------------------------------------
tag="${TMULA_VERSION:-}"
if [ -z "$tag" ]; then
  tag="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)"
fi

mkdir -p "$INSTALL_DIR"

install_prebuilt() {
  [ -n "$os" ] && [ -n "$arch" ] && [ -n "$tag" ] || return 1
  asset="${BIN}_${os}_${arch}"
  url="https://github.com/$REPO/releases/download/$tag/$asset"
  tmp="$(mktemp)"
  say "downloading $asset ($tag)..."
  curl -fsSL -o "$tmp" "$url" 2>/dev/null || { rm -f "$tmp"; return 1; }
  chmod +x "$tmp"
  mv "$tmp" "$INSTALL_DIR/$BIN"
  say "installed $BIN $tag -> $INSTALL_DIR/$BIN"
}

build_from_source() {
  command -v go >/dev/null 2>&1 || return 1
  command -v npm >/dev/null 2>&1 || return 1
  command -v git >/dev/null 2>&1 || return 1
  say "no prebuilt binary for ${os:-?}/${arch:-?}; building from source (Go + Node)..."
  tmp="$(mktemp -d)"
  git clone --depth 1 "https://github.com/$REPO" "$tmp" >/dev/null 2>&1 || { rm -rf "$tmp"; return 1; }
  ( cd "$tmp" && make embed ) || { rm -rf "$tmp"; return 1; }
  cp "$tmp/bin/$BIN" "$INSTALL_DIR/$BIN"
  rm -rf "$tmp"
  say "built and installed $BIN -> $INSTALL_DIR/$BIN"
}

if ! install_prebuilt; then
  build_from_source || die "no published release for your platform, and Go+Node+git are not all available to build from source.
See https://github.com/$REPO for manual install (make web)."
fi

# --- PATH hint + next steps -------------------------------------------------
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say ""; say "add $INSTALL_DIR to your PATH:"; say "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

say ""
say "done. try it:"
say "  $BIN --role local --addr :8080      # web console -> http://localhost:8080"
say "  $BIN run --target http://localhost:9000 --get / --users 20   # quick CLI run"
