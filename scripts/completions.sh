#!/usr/bin/env bash
# Generate shell completions into ./completions — consumed by the GoReleaser
# archives and the Nix package.
set -euo pipefail

cd "$(dirname "$0")/.."
rm -rf completions
mkdir -p completions
for sh in bash zsh fish; do
    go run ./cmd/ccswitch completions "$sh" > "completions/ccswitch.$sh"
done
