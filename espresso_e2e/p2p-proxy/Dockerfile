FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY p2p-proxy/go.mod p2p-proxy/go.sum ./
RUN go mod download
COPY p2p-proxy/main.go ./
RUN go build -o p2p-proxy .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/p2p-proxy /usr/local/bin/p2p-proxy
ENTRYPOINT ["p2p-proxy"]
