FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY mock-engine/go.mod mock-engine/go.sum ./
RUN go mod download
COPY mock-engine/main.go mock-engine/interceptor.go ./
RUN go build -o mock-engine .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/mock-engine /usr/local/bin/mock-engine
ENTRYPOINT ["mock-engine"]
