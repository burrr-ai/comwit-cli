#!/bin/sh
# Build per-OS/arch binaries and publish a GitHub Release.
#   ./release.sh v0.1.0
set -e

VERSION="${1:?usage: ./release.sh vX.Y.Z}"
ver="${VERSION#v}"
REPO="burrr-ai/comwit-cli"

rm -rf dist && mkdir -p dist
for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  os="${platform%_*}"; arch="${platform#*_}"
  echo "building $platform..."
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=$ver" -o dist/comwit ./cmd/comwit
  tar -C dist -czf "dist/comwit_${platform}.tar.gz" comwit
  rm dist/comwit
done

( cd dist && shasum -a 256 *.tar.gz > checksums.txt )

gh release create "$VERSION" dist/*.tar.gz dist/checksums.txt \
  --repo "$REPO" --title "$VERSION" \
  --notes "comwit CLI $VERSION — install: \`curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | sh\`"
echo "released $VERSION"
