# Local/release build surface for harness CLI.
# `make build` refreshes the repo-root binary operators invoke (./harness / ./harness.exe)
# so it stays in command parity with `go run ./cmd/harness`.
BINARY_NAME=harness
CMD_PATH=./cmd/harness
# Keep default aligned with cmd/harness/main.go; releases override via goreleaser tags.
VERSION?=0.3.1
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE?=$(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)
LDFLAGS=-ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

# go build appends .exe on Windows when the -o name has no extension.
ifeq ($(OS),Windows_NT)
  ROOT_BIN=$(BINARY_NAME).exe
  BIN_OUT=bin/$(BINARY_NAME).exe
else
  ROOT_BIN=$(BINARY_NAME)
  BIN_OUT=bin/$(BINARY_NAME)
endif

.PHONY: all build test lint clean release-dry-run check-binary

all: test build

# Build into bin/ and copy to repo root so .\harness.exe / ./harness match source.
build:
	@mkdir -p bin
	go build $(LDFLAGS) -o $(BIN_OUT) $(CMD_PATH)
	cp $(BIN_OUT) $(ROOT_BIN)
	@echo "Built $(ROOT_BIN) version=$(VERSION) commit=$(COMMIT) built=$(DATE)"
	@echo "Repo-root binary refreshed; prefer this over a stale harness.exe."

# Fail closed if a repo-root binary exists but lacks conversation commands
# (signals a stale binary vs current ./cmd/harness).
check-binary:
	@if [ ! -f "$(ROOT_BIN)" ]; then \
		echo "error: $(ROOT_BIN) missing; run 'make build' (or go build -o $(ROOT_BIN) $(CMD_PATH))"; \
		exit 1; \
	fi
	@./$(ROOT_BIN) commands --help >/dev/null 2>&1 || { \
		echo "error: $(ROOT_BIN) is stale (unknown command: commands). Rebuild with 'make build'."; \
		echo "  go run $(CMD_PATH) already exposes commands/sessions; the on-disk binary does not."; \
		exit 1; \
	}
	@./$(ROOT_BIN) sessions --help >/dev/null 2>&1 || { \
		echo "error: $(ROOT_BIN) is stale (unknown command: sessions). Rebuild with 'make build'."; \
		exit 1; \
	}
	@echo "$(ROOT_BIN) has commands/sessions parity check OK ($$(./$(ROOT_BIN) --version 2>/dev/null || true))"

test:
	go test -v -race ./...

test-short:
	go test -v -short ./...

clean:
	rm -rf bin/ dist/ *.exe coverage.txt $(BINARY_NAME)

lint:
	go vet ./...

release-dry-run:
	goreleaser build --snapshot --clean --single-target

cross-compile:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe $(CMD_PATH)
	GOOS=windows GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-arm64.exe $(CMD_PATH)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 $(CMD_PATH)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 $(CMD_PATH)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 $(CMD_PATH)
