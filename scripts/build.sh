#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ "${RWG_SKIP_WEB:-0}" != 1 ]; then
  (cd web && npm ci --no-audit --no-fund && npm run build)
fi
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/remotewake-gateway ./cmd/remotewake-gateway
