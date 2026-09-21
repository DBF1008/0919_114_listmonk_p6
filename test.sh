#!/usr/bin/env bash
#
# test.sh runs all unit tests for the template-compilation cache and the
# manager link-tracking concurrency optimizations.
#
# Usage:
#   ./test.sh            # normal run, verbose
#   ./test.sh -race      # run with the race detector
#   ./test.sh -bench     # also run benchmarks
#
set -euo pipefail

cd "$(dirname "$0")"

# Use a writable build cache when the default is outside the sandbox.
export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/listmonk-gocache}"

PKGS=(./models/... ./internal/manager/...)

RACE=0
BENCH=0
for arg in "$@"; do
	case "$arg" in
		-race)  RACE=1 ;;
		-bench) BENCH=1 ;;
		*) echo "unknown flag: $arg" >&2; exit 2 ;;
	esac
done

ARGS=(-count=1 -v)
[ "$RACE" -eq 1 ] && ARGS+=( -race )

echo ">> go vet ${PKGS[*]}"
go vet "${PKGS[@]}"

echo ">> go test ${ARGS[*]} ${PKGS[*]}"
go test "${ARGS[@]}" "${PKGS[@]}"

if [ "$BENCH" -eq 1 ]; then
	echo ">> go test -bench=. -benchmem ${PKGS[*]}"
	go test -bench=. -benchmem -run='^$' "${PKGS[@]}"
fi

echo ">> all tests passed"
