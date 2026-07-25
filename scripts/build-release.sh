#!/usr/bin/env bash
# build-release.sh — cross-compile signoz-init for every supported platform.
#
#   scripts/build-release.sh v0.1.0
#
# Produces dist/signoz-init_<os>_<arch> binaries whose names match what
# install.sh downloads. Upload the dist/ contents as assets on a GitHub Release
# tagged with the same version.
set -euo pipefail

VERSION="${1:-dev}"
PKG="./cmd/signoz-init"
OUT="dist"

# Asset names here MUST match the `asset=` pattern in install.sh.
PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"

rm -rf "$OUT"
mkdir -p "$OUT"

for platform in $PLATFORMS; do
  os="${platform%/*}"
  arch="${platform#*/}"
  name="signoz-init_${os}_${arch}"
  [ "$os" = "windows" ] && name="${name}.exe"

  echo "building $name"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o "$OUT/$name" "$PKG"
done

echo
echo "built $(ls -1 "$OUT" | wc -l) binaries in $OUT/:"
ls -lh "$OUT"
echo
echo "Next: create a GitHub Release tagged ${VERSION} and upload these as assets."
echo "  gh release create ${VERSION} $OUT/* --title ${VERSION} --generate-notes"
