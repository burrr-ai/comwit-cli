#!/bin/sh
# Build per-OS/arch binaries and publish a GitHub Release.
#   ./release.sh v0.1.0
set -e

VERSION="${1:?usage: ./release.sh vX.Y.Z}"
ver="${VERSION#v}"
REPO="burrr-ai/comwit-cli"
GO_BIN="${GO:-go}"

SOURCE_VERSION="$(sed -n 's/^[[:space:]]*version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' cmd/comwit/main.go | head -1)"
if [ "$SOURCE_VERSION" != "$ver" ]; then
  echo "version mismatch: release=$ver source=${SOURCE_VERSION:-missing}" >&2
  exit 1
fi

CACHE_ROOT=""
if [ -z "${GOCACHE:-}" ]; then
  CACHE_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/comwit-release-go-cache.XXXXXX")"
  trap 'rm -rf "$CACHE_ROOT"' EXIT
fi

rm -rf dist && mkdir -p dist
for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  os="${platform%_*}"; arch="${platform#*_}"
  echo "building $platform..."
  if [ -n "$CACHE_ROOT" ]; then
    target_cache="$CACHE_ROOT/$platform"
    mkdir -p "$target_cache"
    GOCACHE="$target_cache" GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
      "$GO_BIN" build -trimpath -ldflags "-s -w" -o dist/comwit ./cmd/comwit
    rm -rf "$target_cache"
  else
    GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
      "$GO_BIN" build -trimpath -ldflags "-s -w" -o dist/comwit ./cmd/comwit
  fi
  tar -C dist -czf "dist/comwit_${platform}.tar.gz" comwit
  rm dist/comwit
done

( cd dist && shasum -a 256 *.tar.gz > checksums.txt )

gh release create "$VERSION" dist/*.tar.gz dist/checksums.txt \
  --repo "$REPO" --title "$VERSION" \
  --notes "comwit CLI $VERSION — install: \`curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | sh\`"
echo "released $VERSION"
