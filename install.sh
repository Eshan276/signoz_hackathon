#!/bin/sh
# install.sh — download and install the signoz-init binary.
#
#   curl -fsSL https://raw.githubusercontent.com/Eshan276/signoz_hackathon/main/install.sh | sh
#
# Options (environment):
#   SIGNOZ_INIT_VERSION   version tag to install (default: latest release)
#   SIGNOZ_INIT_BIN_DIR   install directory (default: $HOME/.local/bin)
#
# No Go toolchain required — this pulls a prebuilt binary from GitHub Releases.

set -eu

REPO="Eshan276/signoz_hackathon"
BINARY="signoz-init"
VERSION="${SIGNOZ_INIT_VERSION:-latest}"
BIN_DIR="${SIGNOZ_INIT_BIN_DIR:-$HOME/.local/bin}"

info() { printf '\033[32m[install]\033[0m %s\n' "$1" >&2; }
err()  { printf '\033[31m[error]\033[0m %s\n'  "$1" >&2; exit 1; }

# ── Detect OS/arch and map to the release asset naming ──────────────
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) err "unsupported architecture: $arch" ;;
esac
case "$os" in
  linux|darwin) ;;
  *) err "unsupported OS: $os (Windows: download the .exe from the Releases page)" ;;
esac

asset="${BINARY}_${os}_${arch}"
info "target: ${os}/${arch}"

# ── Resolve the download URL ────────────────────────────────────────
if [ "$VERSION" = "latest" ]; then
  url="https://github.com/${REPO}/releases/latest/download/${asset}"
else
  url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
fi

# ── Download to a temp file, then move into place ───────────────────
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

info "downloading ${url}"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$url" -o "$tmp" || err "download failed — has a release been published yet?"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$tmp" "$url" || err "download failed — has a release been published yet?"
else
  err "need curl or wget to download"
fi

[ -s "$tmp" ] || err "downloaded file is empty"

mkdir -p "$BIN_DIR"
chmod +x "$tmp"
mv "$tmp" "$BIN_DIR/$BINARY"
trap - EXIT

info "installed to $BIN_DIR/$BINARY"

# ── PATH hint ───────────────────────────────────────────────────────
case ":$PATH:" in
  *":$BIN_DIR:"*) info "run: $BINARY init" ;;
  *)
    info "add $BIN_DIR to your PATH, e.g."
    info "  echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.bashrc && . ~/.bashrc"
    info "then run: $BINARY init"
    ;;
esac
