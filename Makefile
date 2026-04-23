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

.PHONY: release install

release: test lint check-sync
	@if [ "$${CI:-}" != "true" ] && [ "$${ALLOW_LOCAL_RELEASE:-0}" != "1" ]; then \
		echo "local publish is disabled; use the guarded GitHub release workflow or rerun with ALLOW_LOCAL_RELEASE=1"; \
		exit 1; \
	fi
	goreleaser release --clean

install:
	bash scripts/install.sh
