#!/usr/bin/env bash
# Build one architecture's release archive for the native installer.
set -euo pipefail
cd "$(dirname "$0")/.."

arch="${1:-amd64}"
case "$arch" in
  amd64|arm64) ;;
  *) echo "Usage: bash scripts/build-native-release.sh [amd64|arm64]" >&2; exit 2 ;;
esac
command -v docker >/dev/null || { echo "Docker with buildx is required." >&2; exit 1; }
docker buildx version >/dev/null
mkdir -p release
output="$(mktemp -d)"
trap 'rm -rf "$output"' EXIT
# The Alpine-built binary must run on glibc-based distributions too.
docker buildx build --platform "linux/$arch" --build-arg 'XUI_LDFLAGS=-linkmode external -extldflags=-static -w -s' --target native-release --output "type=local,dest=$output" .
test -s "$output/x-ui/x-ui"
test -s "$output/x-ui/bin/xray-linux-$arch"
archive="release/x-ui-linux-$arch.tar.gz"
tar -C "$output" -czf "$archive" x-ui
(cd release && sha256sum "x-ui-linux-$arch.tar.gz" > "x-ui-linux-$arch.tar.gz.sha256")
echo "Created $archive and its SHA-256 checksum."
