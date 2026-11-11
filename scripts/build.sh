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
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$output" ./cmd/remotewake-gateway
