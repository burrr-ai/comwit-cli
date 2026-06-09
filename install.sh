#!/bin/sh
# comwit CLI installer — downloads the matching release binary and installs it.
#   curl -fsSL https://raw.githubusercontent.com/burrr-ai/comwit-cli/main/install.sh | sh
set -e

REPO="burrr-ai/comwit-cli"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin | linux) ;;
  *) echo "comwit: unsupported OS: $os" >&2; exit 1 ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "comwit: unsupported arch: $arch" >&2; exit 1 ;;
esac

tag="${COMWIT_VERSION:-}"
if [ -z "$tag" ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
fi
if [ -z "$tag" ]; then echo "comwit: could not determine latest release" >&2; exit 1; fi

asset="comwit_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "comwit: downloading $asset ($tag)..."
curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then sumcmd="sha256sum"; else sumcmd="shasum -a 256"; fi
( cd "$tmp" && grep " $asset\$" checksums.txt | $sumcmd -c - >/dev/null ) \
  || { echo "comwit: checksum verification failed" >&2; exit 1; }

tar -xzf "$tmp/$asset" -C "$tmp"

dir="/usr/local/bin"
if [ ! -d "$dir" ] || [ ! -w "$dir" ]; then dir="$HOME/.local/bin"; mkdir -p "$dir"; fi
install -m 0755 "$tmp/comwit" "$dir/comwit"

echo "comwit: installed to $dir/comwit"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "comwit: add $dir to your PATH" ;;
esac
"$dir/comwit" version
