default:
    @just --list

build:
    go build ./... ./streamer/op/...

test *args:
    go test ./... ./streamer/op/... {{ args }}

fmt:
    gofmt -w .

lint:
    golangci-lint run ./... ./streamer/op/...

test
