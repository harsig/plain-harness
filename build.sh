#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
version="$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)"
cd "$root/src"

go vet ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$root/bin/plain-harness" .
echo "built $root/bin/plain-harness $version"
