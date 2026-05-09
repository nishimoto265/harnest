SHELL := /bin/bash

.PHONY: all build build-cli test integration-test lint tidy size-report check-sync script-test

all: build test lint check-sync script-test

build:
	go build ./...

build-cli:
	mkdir -p bin
	go build -o bin/harnest ./cmd/auto-improve

test:
	go test ./...

integration-test:
	AUTO_IMPROVE_INTEGRATION=1 go test ./cmd/auto-improve -count=1

lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint: not installed, skipping"; fi

tidy:
	go mod tidy

size-report:
	bash scripts/size-report.sh

check-sync:
	bash scripts/check-contracts-sync.sh

script-test:
	bash -n scripts/install.sh scripts/install-launchd.sh scripts/launchd-common.sh scripts/install-migration-test.sh scripts/check-contracts-sync.sh scripts/check-contracts-sync_test.sh scripts/render-workflow-config.sh scripts/size-report.sh
	bash scripts/install-migration-test.sh
	bash scripts/check-contracts-sync_test.sh

.PHONY: release install

release: test integration-test lint check-sync
	@if [ "$${CI:-}" != "true" ] && [ "$${ALLOW_LOCAL_RELEASE:-0}" != "1" ]; then \
		echo "local publish is disabled; use the guarded GitHub release workflow or rerun with ALLOW_LOCAL_RELEASE=1"; \
		exit 1; \
	fi
	goreleaser release --clean

install:
	bash scripts/install.sh
