#!/usr/bin/env bash
# Builds release binaries for every target into dist/. Called by semantic-release with the new version.
set -euo pipefail

version="${1:?usage: build-release.sh <version>}"
name="plain-harness"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)

rm -rf "$root/dist"
mkdir -p "$root/dist"
cd "$root/src"
for target in "${targets[@]}"; do
  os="${target%/*}" arch="${target#*/}"
  out="$root/dist/${name}_${version}_${os}_${arch}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$out" .
  echo "built $out"
done
(cd "$root/dist" && sha256sum "${name}"_* > checksums.txt)
