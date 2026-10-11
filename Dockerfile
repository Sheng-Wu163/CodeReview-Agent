# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/codereviewagent ./cmd/server

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app

WORKDIR /app
COPY --from=builder /bin/codereviewagent /app/codereviewagent
COPY --from=builder /app/templates /app/templates

EXPOSE 8080
ENTRYPOINT ["/app/codereviewagent"]
