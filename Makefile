SHELL := /bin/bash

.PHONY: all build test lint tidy check-sync

all: build test lint check-sync

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint: not installed, skipping"; fi

tidy:
	go mod tidy

check-sync:
	bash scripts/check-contracts-sync.sh
