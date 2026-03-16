default:
    @just --list

build:
    go build ./...

test *args:
    go test ./... {{ args }}

fmt:
    gofmt -w .

lint:
    golangci-lint run ./...
