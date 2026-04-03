FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY p2p-attacker/go.mod p2p-attacker/go.sum ./
RUN go mod download
COPY p2p-attacker/*.go ./
RUN go build -o p2p-attacker .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/p2p-attacker /usr/local/bin/p2p-attacker
ENTRYPOINT ["p2p-attacker"]
