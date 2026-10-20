default:
    @just --list

build:
    go build ./...

test *args:
    go test -skip 'TestOPE2E|TestNitroE2E|TestCAS' ./... {{ args }}

e2e *args:
    go test -timeout 15m ./espresso_e2e/... {{ args }}

e2e-op *args:
    go test -timeout 15m -run TestOPE2E ./espresso_e2e/... {{ args }}

e2e-nitro *args:
    go test -timeout 15m -run TestNitroE2E ./espresso_e2e/... {{ args }}

generate-abi:
    abigen --abi verifier/nitro/abi/sources/bridge_abi.json --pkg nitroabi --type Bridge --out verifier/nitro/abi/bridge_contract_gen.go
    abigen --abi verifier/nitro/abi/sources/inbox_abi.json  --pkg nitroabi --type Inbox  --out verifier/nitro/abi/inbox_contract_gen.go

fmt:
    gofmt -w .

lint:
    golangci-lint run ./...
