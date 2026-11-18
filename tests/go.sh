#!/bin/sh
# Every Go test of jackstay (docs/design/implementation.md): vet, the tests with the race detector, and a build.
# Prints "Ran N tests" for jackstay.json's evidence pattern.
set -e
cd "$(dirname "$0")/.."
go vet ./...
out=$(mktemp)
trap 'rm -f "$out"' EXIT
status=0
go test -count=1 -race -v ./... > "$out" 2>&1 || status=$?
grep -E '^(--- FAIL|FAIL|ok|panic:)' "$out" || true
if [ "$status" -ne 0 ]; then
  grep -B2 -A20 -E '^--- FAIL' "$out" | head -200
  echo "FAILED: go test"
  exit "$status"
fi
go build -trimpath -o .build/jst ./cmd/jst
echo "Ran $(grep -c -E '^    --- PASS|^--- PASS' "$out") tests"
