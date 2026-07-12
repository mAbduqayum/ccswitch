default:
    @just --list

run *ARGS:
    go run ./cmd/ccswitch {{ARGS}}

build:
    go build -o ccswitch ./cmd/ccswitch

test:
    go test -race ./...

cover:
    go test -race -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out

fmt:
    gofumpt -w .

lint:
    golangci-lint run

tidy:
    go mod tidy

check:
    just lint
    just test

# tag and push a release; no arg bumps the patch of the latest tag — CI runs GoReleaser
release VERSION="":
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{VERSION}}"
    if [ -z "$version" ]; then
        latest=$(git tag --sort=-v:refname | head -n1)
        if [ -z "$latest" ]; then
            version="v0.1.0"
        else
            IFS=. read -r major minor patch <<< "${latest#v}"
            version="v${major}.${minor}.$((patch + 1))"
        fi
    fi
    git tag -a "$version" -m "$version"
    git push origin "$version"
    echo "pushed $version"

# build a local release into ./dist without publishing, to preview artifacts
release-snapshot:
    goreleaser release --snapshot --clean
