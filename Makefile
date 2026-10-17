GO ?= go

# Load .env (secrets + LLM endpoints) when present and export it to every
# command so `make run` / `make eval` reach the real services.
ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: run build test vet lint tidy eval

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

# eval runs the offline evaluation system (v1.6 FR-33): replays
# eval/dataset samples through the full review pipeline and writes
# eval/metrics.json + eval/report.md.
eval:
	$(GO) run ./cmd/eval -config configs/config.yaml -dataset eval/dataset -out eval
