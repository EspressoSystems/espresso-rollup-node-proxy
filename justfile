default:
    @just --list

build:
    go build ./...

test *args:
    go test -skip TestOPE2E ./... {{ args }}

e2e *args:
    go test -timeout 15m ./espresso_e2e/... {{ args }}

fmt:
    gofmt -w .

lint:
    golangci-lint run ./...
