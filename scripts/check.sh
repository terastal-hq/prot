#!/usr/bin/env bash
# Run the same Go checks the release workflow gates on: formatting, vet and the race-enabled test suite.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

unformatted="$(gofmt -l cmd internal completions)"
if [ -n "$unformatted" ]; then
    echo "gofmt needed on:" >&2
    echo "$unformatted" >&2
    exit 1
fi

go vet ./...
go test -race -count=1 ./...
echo "✓ all checks passed"
