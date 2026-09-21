#!/usr/bin/env bash
# Runs unit tests for the template compilation caching changes.
# Usage: ./test.sh
set -euo pipefail

cd "$(dirname "$0")"

echo "==> go vet"
go vet ./models/... ./internal/manager/...

echo "==> go build"
go build ./...

echo "==> unit tests (with race detector)"
go test -v -race -count=1 ./models/... ./internal/manager/...

echo "==> all checks passed"
