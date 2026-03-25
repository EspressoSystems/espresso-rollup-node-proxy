FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY mock-l1-beacon/go.mod mock-l1-beacon/go.sum ./
RUN go mod download
COPY mock-l1-beacon/main.go .
RUN go build -o fake-beacon .

FROM alpine:3.19
RUN apk add --no-cache curl
COPY --from=builder /app/fake-beacon /usr/local/bin/fake-beacon
ENTRYPOINT ["fake-beacon"]