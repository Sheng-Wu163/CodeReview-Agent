GO ?= go

.PHONY: run build test vet lint tidy

run:
	$(GO) run ./cmd/server

build:
	$(GO) build ./...

test:
	$(GO) test ./... -race

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

tidy:
	$(GO) mod tidy
