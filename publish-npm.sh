#!/bin/sh
# Publish the npm mirror for an existing or new comwit CLI release.
# Safe to rerun: an already-published matching version is treated as success.
set -eu

NPM_BIN="${NPM:-npm}"
GO_BIN="${GO:-go}"
VERSION="${1:-}"

PACKAGE_NAME="$("$NPM_BIN" pkg get name | tr -d '"')"
PACKAGE_VERSION="$("$NPM_BIN" pkg get version | tr -d '"')"
SOURCE_VERSION="$(sed -n 's/^[[:space:]]*version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' cmd/comwit/main.go | head -1)"

if [ -n "$VERSION" ] && [ "${VERSION#v}" != "$PACKAGE_VERSION" ]; then
  echo "version mismatch: requested=${VERSION#v} package=${PACKAGE_VERSION:-missing}" >&2
  exit 1
fi
if [ "$SOURCE_VERSION" != "$PACKAGE_VERSION" ]; then
  echo "version mismatch: source=${SOURCE_VERSION:-missing} package=${PACKAGE_VERSION:-missing}" >&2
  exit 1
fi

published="$("$NPM_BIN" view "$PACKAGE_NAME@$PACKAGE_VERSION" version 2>/dev/null || true)"
if [ "$published" = "$PACKAGE_VERSION" ]; then
  echo "$PACKAGE_NAME@$PACKAGE_VERSION is already published"
  exit 0
fi

if ! "$NPM_BIN" whoami >/dev/null 2>&1; then
  echo "npm authentication required: run \`npm login\` before publishing" >&2
  exit 1
fi

GO="$GO_BIN" "$NPM_BIN" test
GO="$GO_BIN" "$NPM_BIN" pack --dry-run >/dev/null
GO="$GO_BIN" "$NPM_BIN" publish --access public

published="$("$NPM_BIN" view "$PACKAGE_NAME@$PACKAGE_VERSION" version)"
if [ "$published" != "$PACKAGE_VERSION" ]; then
  echo "npm verification failed: expected $PACKAGE_VERSION, got ${published:-missing}" >&2
  exit 1
fi
echo "published $PACKAGE_NAME@$PACKAGE_VERSION"
