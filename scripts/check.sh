#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
go test -race ./...
go vet ./...
(cd web && npm ci --no-audit --no-fund && npm test && npm run build)
node --test scripts/release.test.mjs
