#!/usr/bin/env bash
# Pre-push gate: the same checks CI runs, in the order that fails fastest.
# Usage: scripts/check.sh [--quick]   (--quick skips the race test run)
set -euo pipefail
cd "$(dirname "$0")/.."
BIN="$(go env GOPATH)/bin"

step() { printf '\n== %s\n' "$*"; }

step "gofumpt (changed .go files)"
changed=$( { git diff --name-only HEAD; git ls-files --others --exclude-standard; } | grep '\.go$' | sort -u || true)
if [ -n "$changed" ]; then
  bad=$(echo "$changed" | xargs -r "$BIN/gofumpt" -l 2>/dev/null || true)
  if [ -n "$bad" ]; then echo "needs gofumpt -w:"; echo "$bad"; exit 1; fi
fi
echo ok

step "go build"
go build ./... 

step "log capitalization"
./scripts/check_log_capitalization.sh

step "golangci-lint (v2.15.0, as in CI)"
"$BIN/golangci-lint" run --config=.golangci.yml --timeout=10m ./... 2>&1 | tail -n 40

if [ "${1:-}" != "--quick" ]; then
  step "go test -race"
  out=$(mktemp); trap 'rm -f "$out"' EXIT
  if ! go test -race -failfast ./... >"$out" 2>&1; then
    grep -vE '^(ok|\?) ' "$out" | tail -n 40; echo "tests FAILED"; exit 1
  fi
  echo ok
fi
step "all checks passed"
