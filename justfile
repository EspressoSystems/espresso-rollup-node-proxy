default:
    @just --list

build:
    go build ./...

test *args:
    go test -skip 'TestOPE2E|TestNitroE2E' ./... {{ args }}

e2e *args:
    go test -timeout 15m ./espresso_e2e/... {{ args }}

e2e-op *args:
    go test -timeout 15m -run TestOPE2E ./espresso_e2e/... {{ args }}

e2e-nitro *args:
    go test -timeout 15m -run TestNitroE2E ./espresso_e2e/... {{ args }}

fmt:
    gofmt -w .

lint:
    golangci-lint run ./...
