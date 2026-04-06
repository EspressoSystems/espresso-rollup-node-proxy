FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev linux-headers

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o espresso-rollup-node-proxy .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/espresso-rollup-node-proxy /usr/local/bin/
ENTRYPOINT ["espresso-rollup-node-proxy"]
