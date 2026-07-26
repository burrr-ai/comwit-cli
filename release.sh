#!/bin/sh
# Publish one synchronized GitHub + npm release.
#   ./release.sh vX.Y.Z
set -eu

VERSION="${1:?usage: ./release.sh vX.Y.Z}"
ver="${VERSION#v}"
REPO="burrr-ai/comwit-cli"
GO_BIN="${GO:-go}"
NPM_BIN="${NPM:-npm}"

SOURCE_VERSION="$(sed -n 's/^[[:space:]]*version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' cmd/comwit/main.go | head -1)"
if [ "$SOURCE_VERSION" != "$ver" ]; then
  echo "version mismatch: release=$ver source=${SOURCE_VERSION:-missing}" >&2
  exit 1
fi

PACKAGE_VERSION="$("$NPM_BIN" pkg get version | tr -d '"')"
if [ "$PACKAGE_VERSION" != "$ver" ]; then
  echo "version mismatch: release=$ver package=${PACKAGE_VERSION:-missing}" >&2
  exit 1
fi

if [ -n "$(git status --porcelain)" ]; then
  echo "release requires a clean worktree" >&2
  exit 1
fi
current_sha="$(git rev-parse HEAD)"

# Do not create a GitHub Release unless npm publication is possible. A version
# already present on npm is safe for an idempotent retry.
if ! "$NPM_BIN" view "comwit-cli@$ver" version >/dev/null 2>&1; then
  if ! "$NPM_BIN" whoami >/dev/null 2>&1; then
    echo "npm authentication required: run \`npm login\` before releasing" >&2
    exit 1
  fi
fi

GO="$GO_BIN" "$NPM_BIN" test

if gh release view "$VERSION" --repo "$REPO" >/dev/null 2>&1; then
  release_sha="$(gh api "repos/$REPO/git/ref/tags/$VERSION" --jq '.object.sha')"
  if [ "$release_sha" != "$current_sha" ]; then
    echo "existing release $VERSION points to $release_sha, current commit is $current_sha" >&2
    exit 1
  fi
  echo "GitHub Release $VERSION already exists at this commit; skipping asset rebuild"
  GO="$GO_BIN" NPM="$NPM_BIN" ./publish-npm.sh "$VERSION"
  echo "released $VERSION on GitHub and npm"
  exit 0
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
  --repo "$REPO" --target "$current_sha" --title "$VERSION" \
  --notes "comwit CLI $VERSION — install: \`curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | sh\`"

GO="$GO_BIN" NPM="$NPM_BIN" ./publish-npm.sh "$VERSION"
echo "released $VERSION on GitHub and npm"
