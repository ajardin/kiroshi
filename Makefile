BINARY  := kiroshi
PKG     := github.com/ajardin/kiroshi
BIN_DIR := bin

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X '$(PKG)/internal/version.version=$(VERSION)' \
	-X '$(PKG)/internal/version.commit=$(COMMIT)' \
	-X '$(PKG)/internal/version.date=$(DATE)'

GO       ?= go
GOFLAGS  ?=

.PHONY: all build test cover lint fmt tidy run clean help

all: lint test build ## lint, test, build

build: ## compile the binary into ./bin
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)

test: ## run tests with race detector
	$(GO) test -race -count=1 ./...

cover: ## run tests with coverage report
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out

lint: ## run golangci-lint
	golangci-lint run ./...

fmt: ## format code (gofmt + goimports via golangci-lint)
	golangci-lint fmt

tidy: ## tidy and verify go.mod
	$(GO) mod tidy
	$(GO) mod verify

run: ## run the binary via `go run`
	$(GO) run ./cmd/$(BINARY)

clean: ## remove build artifacts
	rm -rf $(BIN_DIR) coverage.out

help: ## show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "\033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
