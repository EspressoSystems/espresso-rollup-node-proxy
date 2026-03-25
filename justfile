default:
    @just --list

build:
    go build ./... ./streamer/op/...

test *args:
    go test ./... ./streamer/op/... {{ args }}

e2e *args:
    go test -timeout 15m ./espresso_e2e/... {{ args }}

fmt:
    gofmt -w .

lint:
    golangci-lint run ./... ./streamer/op/...
