FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev linux-headers

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -trimpath -o espresso-rollup-node-proxy .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 1000 proxyuser
WORKDIR /home/proxyuser
COPY --from=builder /app/espresso-rollup-node-proxy /usr/local/bin/
USER proxyuser
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1
ENTRYPOINT ["espresso-rollup-node-proxy"]
