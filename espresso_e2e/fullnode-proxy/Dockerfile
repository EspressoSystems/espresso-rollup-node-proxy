FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o fullnode-proxy ./espresso_e2e/fullnode-proxy/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/fullnode-proxy /usr/local/bin/fullnode-proxy
ENTRYPOINT ["fullnode-proxy"]
