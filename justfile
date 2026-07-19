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

# build a local release into ./dist without publishing, to preview artifacts
release-snapshot:
    goreleaser release --snapshot --clean
