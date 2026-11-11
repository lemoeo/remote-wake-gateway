#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ "${RWG_SKIP_WEB:-0}" != 1 ]; then
  (cd web && npm ci --no-audit --no-fund && npm run build)
fi
mkdir -p bin
target_os="${GOOS:-$(go env GOOS)}"
output=bin/remotewake-gateway
if [ "$target_os" = windows ]; then
  output="$output.exe"
fi
build_flags='-s -w'
if [ -n "${RWG_VERSION:-}" ]; then
  build_version=$(sh scripts/release.sh --check-version "v${RWG_VERSION#v}")
  build_flags="$build_flags -X main.version=$build_version"
fi
CGO_ENABLED=0 go build -trimpath -ldflags="$build_flags" -o "$output" ./cmd/remotewake-gateway
